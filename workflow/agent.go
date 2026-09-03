package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	lua "github.com/Shopify/go-lua"
	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/tool"
)

// AgentFunc runs one workflow agent invocation and returns assistant text or structured output.
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

// AgentRequest describes one workflow agent invocation.
type AgentRequest struct {
	Model          llm.Model
	ReasoningLevel llm.ReasoningLevel
	System         string
	Prompt         string
	Workspace      string
	ReadOnly       bool
	OutputSchema   *jsonschema.Schema
	Progress       func(AgentEvent)
}

// AgentResult describes the result of one workflow agent invocation.
type AgentResult struct {
	Text   string
	Output json.RawMessage
}

type agentRuntime struct {
	ctx       context.Context
	cancel    context.CancelFunc
	agent     AgentFunc
	emit      func(Event)
	emitMu    sync.Mutex
	jobsWG    sync.WaitGroup
	next      int
	jobs      map[int]*agentJob
	worktrees *worktreeRuntime
}

type agentJob struct {
	invocation int
	done       chan struct{}
	result     AgentResult
	err        error
	consumed   bool
}

func newAgentRuntime(ctx context.Context, agent AgentFunc, emit func(Event)) *agentRuntime {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	return &agentRuntime{
		ctx:       ctx,
		cancel:    cancel,
		agent:     agent,
		emit:      emit,
		jobs:      make(map[int]*agentJob),
		worktrees: newWorktreeRuntime(ctx),
	}
}

func runAgentFunction(rt *agentRuntime) lua.Function {
	return func(state *lua.State) int {
		req, err := parseAgentRequest(state, 1)
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		if err := rt.prepareRequest(&req); err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		invocation := rt.nextInvocation()
		result, err := rt.runAgent(invocation, req)
		if err != nil {
			panic(lua.RuntimeError(fmt.Sprintf("ronin.run_agent: %v", err)))
		}
		pushAgentResult(state, result, 0, nil)
		return 1
	}
}

func startAgentFunction(rt *agentRuntime) lua.Function {
	return func(state *lua.State) int {
		req, err := parseAgentRequest(state, 1)
		if err != nil {
			panic(lua.RuntimeError(strings.Replace(err.Error(), "ronin.run_agent", "ronin.start_agent", 1)))
		}
		if err := rt.prepareRequest(&req); err != nil {
			panic(lua.RuntimeError("ronin.start_agent: " + err.Error()))
		}
		if rt == nil || rt.agent == nil {
			panic(lua.RuntimeError("ronin.start_agent is not configured"))
		}

		invocation := rt.nextInvocation()
		job := &agentJob{invocation: invocation, done: make(chan struct{})}
		rt.jobs[invocation] = job
		rt.jobsWG.Go(func() {
			defer close(job.done)
			job.result, job.err = rt.runAgent(invocation, req)
		})

		state.PushInteger(invocation)
		return 1
	}
}

func waitAnyFunction(rt *agentRuntime) lua.Function {
	return func(state *lua.State) int {
		ids, err := readJobIDs(state, 1)
		if err != nil {
			panic(lua.RuntimeError("ronin.wait_any: " + err.Error()))
		}

		cases := make([]reflect.SelectCase, 0, len(ids)+1)
		jobs := make([]*agentJob, 0, len(ids))
		for _, id := range ids {
			job := rt.jobs[id]
			if job == nil {
				panic(lua.RuntimeError(fmt.Sprintf("ronin.wait_any: unknown job %d", id)))
			}
			if job.consumed {
				panic(lua.RuntimeError(fmt.Sprintf("ronin.wait_any: job %d was already consumed", id)))
			}
			jobs = append(jobs, job)
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(job.done)})
		}
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(rt.ctx.Done())})

		selected, _, _ := reflect.Select(cases)
		if selected == len(jobs) {
			panic(lua.RuntimeError("ronin.wait_any: " + rt.ctx.Err().Error()))
		}
		job := jobs[selected]
		job.consumed = true
		pushAgentResult(state, job.result, job.invocation, job.err)
		return 1
	}
}

func (rt *agentRuntime) prepareRequest(req *AgentRequest) error {
	if rt == nil || rt.agent == nil {
		return fmt.Errorf("ronin.run_agent is not configured")
	}
	if req.OutputSchema != nil {
		if err := jsonschema.ValidateDefinition(req.OutputSchema); err != nil {
			return fmt.Errorf("output schema is invalid: %w", err)
		}
	}
	if req.Workspace == "" {
		return nil
	}
	if !strings.HasPrefix(req.Workspace, "workspace:") {
		return fmt.Errorf("workspace must be a managed handle returned by ronin.create_worktree")
	}
	path, err := rt.worktrees.resolveWorkspace(req.Workspace)
	if err != nil {
		return err
	}
	req.Workspace = path
	return nil
}

func (rt *agentRuntime) nextInvocation() int {
	rt.next++
	return rt.next
}

func (rt *agentRuntime) runAgent(invocation int, req AgentRequest) (AgentResult, error) {
	rt.emitEvent(AgentStarted{Invocation: invocation, Request: req})
	var readOnlyState string
	if req.ReadOnly {
		var err error
		readOnlyState, err = rt.worktrees.fingerprint(req.Workspace)
		if err != nil {
			rt.emitEvent(AgentFinished{Invocation: invocation, Error: err.Error()})
			return AgentResult{}, fmt.Errorf("record read-only workspace state: %w", err)
		}
	}
	req.Progress = func(event AgentEvent) {
		rt.emitEvent(AgentEventReceived{Invocation: invocation, Event: event})
	}
	result, err := rt.agent(rt.ctx, req)
	if err == nil && req.OutputSchema != nil {
		if len(result.Output) == 0 {
			err = fmt.Errorf("structured agent returned no output")
		} else if validationErr := jsonschema.Validate(req.OutputSchema, result.Output); validationErr != nil {
			err = fmt.Errorf("structured agent output validation failed: %w", validationErr)
		}
	}
	if req.ReadOnly {
		after, stateErr := rt.worktrees.fingerprint(req.Workspace)
		switch {
		case stateErr != nil:
			err = errors.Join(err, fmt.Errorf("verify read-only workspace state: %w", stateErr))
		case after != readOnlyState:
			err = errors.Join(err, fmt.Errorf("read-only agent changed repository state"))
		}
	}
	if err != nil {
		rt.emitEvent(AgentFinished{Invocation: invocation, Error: err.Error()})
		return AgentResult{}, err
	}
	rt.emitEvent(AgentFinished{Invocation: invocation, Text: result.Text})
	return result, nil
}

func (rt *agentRuntime) shutdown() {
	if rt == nil {
		return
	}
	rt.cancel()
	rt.jobsWG.Wait()
}

func (rt *agentRuntime) emitEvent(event Event) {
	if rt == nil || rt.emit == nil {
		return
	}
	rt.emitMu.Lock()
	defer rt.emitMu.Unlock()
	rt.emit(event)
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
	if workspace, ok, err := readAgentStringField(state, index, "workspace", false); err != nil {
		return AgentRequest{}, err
	} else if ok {
		req.Workspace = workspace
	}
	if readOnly, ok, err := readAgentBoolField(state, index, "read_only"); err != nil {
		return AgentRequest{}, err
	} else if ok {
		req.ReadOnly = readOnly
	}
	if schemaJSON, ok, err := readAgentStringField(state, index, "output_schema", false); err != nil {
		return AgentRequest{}, err
	} else if ok {
		schema, err := jsonschema.FromRaw([]byte(schemaJSON))
		if err != nil {
			return AgentRequest{}, fmt.Errorf("invalid ronin.run_agent output_schema: %w", err)
		}
		req.OutputSchema = schema
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

func readAgentBoolField(state *lua.State, index int, name string) (bool, bool, error) {
	state.Field(index, name)
	defer state.Pop(1)
	if state.IsNil(-1) {
		return false, false, nil
	}
	if state.TypeOf(-1) != lua.TypeBoolean {
		return false, false, fmt.Errorf("ronin.run_agent %s must be a boolean", name)
	}
	return state.ToBoolean(-1), true, nil
}

func parseAgentModel(value string) (llm.Model, error) {
	provider, name, ok := strings.Cut(value, ":")
	if !ok || provider == "" || name == "" {
		return llm.Model{}, fmt.Errorf("invalid ronin.run_agent model %q: want format <provider>:<name>", value)
	}
	return llm.Model{Provider: provider, Name: name}, nil
}

func readJobIDs(state *lua.State, index int) ([]int, error) {
	if !state.IsTable(index) {
		return nil, fmt.Errorf("expects a non-empty array of job handles")
	}
	length := state.RawLength(index)
	if length == 0 {
		return nil, fmt.Errorf("expects a non-empty array of job handles")
	}
	ids := make([]int, 0, length)
	seen := make(map[int]bool, length)
	for i := 1; i <= length; i++ {
		state.RawGetInt(index, i)
		id, ok := state.ToInteger(-1)
		state.Pop(1)
		if !ok || id <= 0 {
			return nil, fmt.Errorf("job handle at index %d must be a positive integer", i)
		}
		if seen[id] {
			return nil, fmt.Errorf("job %d is repeated", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

func pushAgentResult(state *lua.State, result AgentResult, job int, runErr error) {
	state.NewTable()
	state.PushBoolean(runErr == nil)
	state.SetField(-2, "ok")
	if job > 0 {
		state.PushInteger(job)
		state.SetField(-2, "job")
	}
	state.PushString(result.Text)
	state.SetField(-2, "text")
	if runErr != nil {
		state.PushString(runErr.Error())
		state.SetField(-2, "error")
	}
	if len(result.Output) > 0 {
		var output any
		if json.Unmarshal(result.Output, &output) == nil {
			pushLuaValue(state, output)
			state.SetField(-2, "output")
		}
	}
}

func pushLuaValue(state *lua.State, value any) {
	switch value := value.(type) {
	case nil:
		state.PushNil()
	case bool:
		state.PushBoolean(value)
	case string:
		state.PushString(value)
	case float64:
		state.PushNumber(value)
	case []any:
		state.CreateTable(len(value), 0)
		for i, item := range value {
			pushLuaValue(state, item)
			state.RawSetInt(-2, i+1)
		}
	case map[string]any:
		state.CreateTable(0, len(value))
		for key, item := range value {
			pushLuaValue(state, item)
			state.SetField(-2, key)
		}
	default:
		state.PushNil()
	}
}
