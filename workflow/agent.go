package workflow

import (
	"context"
	"fmt"
	"strings"

	lua "github.com/Shopify/go-lua"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/tool"
)

// AgentFunc runs one workflow agent invocation and returns assistant text.
type AgentFunc func(context.Context, AgentRequest) (AgentResult, error)

// AgentEvent reports progress from one workflow agent invocation.
type AgentEvent interface{ agentEvent() }

type AgentThinkingDelta struct{ Text string }
type AgentTextDelta struct{ Text string }
type AgentToolStarted struct{ ID, Title string }
type AgentToolOutput struct {
	ID       string
	Artifact tool.Artifact
}
type AgentToolFailed struct{ ID, Error string }
type AgentToolEnded struct{ ID string }

func (AgentThinkingDelta) agentEvent() {}
func (AgentTextDelta) agentEvent()     {}
func (AgentToolStarted) agentEvent()   {}
func (AgentToolOutput) agentEvent()    {}
func (AgentToolFailed) agentEvent()    {}
func (AgentToolEnded) agentEvent()     {}

// AgentRequest describes one ronin.run_agent call.
type AgentRequest struct {
	Model          llm.Model
	ReasoningLevel llm.ReasoningLevel
	System         string
	Prompt         string
	Progress       func(AgentEvent)
}

// AgentResult describes the result of one ronin.run_agent call.
type AgentResult struct {
	Text string
}

type agentRuntime struct {
	ctx   context.Context
	agent AgentFunc
	emit  func(Event)
	next  int
}

func newAgentRuntime(ctx context.Context, agent AgentFunc, emit func(Event)) *agentRuntime {
	if ctx == nil {
		ctx = context.Background()
	}
	return &agentRuntime{ctx: ctx, agent: agent, emit: emit}
}

func runAgentFunction(rt *agentRuntime) lua.Function {
	return func(state *lua.State) int {
		req, err := parseAgentRequest(state, 1)
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		if rt == nil || rt.agent == nil {
			panic(lua.RuntimeError("ronin.run_agent is not configured"))
		}
		rt.next++
		invocation := rt.next
		rt.emitEvent(AgentStarted{Invocation: invocation, Request: req})
		req.Progress = func(event AgentEvent) {
			rt.emitEvent(AgentEventReceived{Invocation: invocation, Event: event})
		}
		result, err := rt.agent(rt.ctx, req)
		if err != nil {
			rt.emitEvent(AgentFinished{Invocation: invocation, Error: err.Error()})
			panic(lua.RuntimeError(fmt.Sprintf("ronin.run_agent: %v", err)))
		}
		rt.emitEvent(AgentFinished{Invocation: invocation, Text: result.Text})

		state.NewTable()
		state.PushBoolean(true)
		state.SetField(-2, "ok")
		state.PushString(result.Text)
		state.SetField(-2, "text")
		return 1
	}
}

func (rt *agentRuntime) emitEvent(event Event) {
	if rt != nil && rt.emit != nil {
		rt.emit(event)
	}
}

func parseAgentRequest(state *lua.State, index int) (AgentRequest, error) {
	if !state.IsTable(index) {
		return AgentRequest{}, fmt.Errorf("ronin.run_agent expects a table")
	}

	prompt, _, err := readAgentStringField(state, index, "prompt", true)
	if err != nil {
		return AgentRequest{}, err
	}

	req := AgentRequest{Prompt: prompt}

	if model, ok, err := readAgentStringField(state, index, "model", false); err != nil {
		return AgentRequest{}, err
	} else if ok {
		parsedModel, err := parseAgentModel(model)
		if err != nil {
			return AgentRequest{}, err
		}
		req.Model = parsedModel
	}

	if reasoning, ok, err := readAgentStringField(state, index, "reasoning", false); err != nil {
		return AgentRequest{}, err
	} else if ok {
		lvl := llm.ReasoningLevel(reasoning)
		if !llm.IsValidReasoningLevel(lvl) {
			return AgentRequest{}, fmt.Errorf("invalid ronin.run_agent reasoning %q", reasoning)
		}
		req.ReasoningLevel = lvl
	}

	if system, ok, err := readAgentStringField(state, index, "system", false); err != nil {
		return AgentRequest{}, err
	} else if ok {
		req.System = system
	}

	return req, nil
}

func readAgentStringField(state *lua.State, index int, name string, required bool) (string, bool, error) {
	state.Field(index, name)
	defer state.Pop(1)

	if state.IsNil(-1) {
		if required {
			return "", false, fmt.Errorf("ronin.run_agent %s is required", name)
		}
		return "", false, nil
	}
	if state.TypeOf(-1) != lua.TypeString {
		return "", false, fmt.Errorf("ronin.run_agent %s must be a string", name)
	}
	value, _ := state.ToString(-1)
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", false, fmt.Errorf("ronin.run_agent %s is required", name)
	}
	return value, true, nil
}

func parseAgentModel(value string) (llm.Model, error) {
	provider, name, ok := strings.Cut(value, ":")
	if !ok || provider == "" || name == "" {
		return llm.Model{}, fmt.Errorf("invalid ronin.run_agent model %q: want format <provider>:<name>", value)
	}
	return llm.Model{Provider: provider, Name: name}, nil
}
