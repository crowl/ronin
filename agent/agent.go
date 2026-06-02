package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/tool"
)

type Compactor interface {
	Compact(context.Context, []llm.Message) ([]llm.Message, error)
}

type Tool interface {
	llm.Tool
	Call(ctx context.Context, rawArgs json.RawMessage) (any, error)
}

type IncrementalTool interface {
	Tool
	CallIncremental(ctx context.Context, rawArgs json.RawMessage, emit func(tool.Artifact) error) (any, error)
}

type ToolCallTitleProvider interface {
	CallTitle(rawArgs json.RawMessage) (string, error)
}

type Config struct {
	CWD          string
	LLM          llm.Assistant
	Compactor    Compactor
	ContextFiles []ContextFile
	Skills       []Skill
	Tools        []Tool
	SystemPrompt string
	SessionID    string
	MaxTurns     int
	Now          func() time.Time
	Messages     []llm.Message
}

const defaultMaxTurns = 512

func New(cfg Config) (*Agent, error) {
	if cfg.LLM == nil {
		return nil, errors.New("llm is required")
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now() }
	}
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}

	toolDefs := make([]llm.Tool, 0, len(cfg.Tools))
	toolByName := make(map[string]Tool, len(cfg.Tools))

	for i, t := range cfg.Tools {
		if t == nil {
			return nil, fmt.Errorf("tool %d is nil", i)
		}
		name := strings.TrimSpace(t.Name())
		if name == "" {
			return nil, fmt.Errorf("tool %d has empty name", i)
		}
		if _, exists := toolByName[name]; exists {
			return nil, fmt.Errorf("duplicate tool name %q", name)
		}
		toolDefs = append(toolDefs, t)
		toolByName[name] = t
	}

	return &Agent{
		cwd:          cfg.CWD,
		llm:          cfg.LLM,
		compactor:    cfg.Compactor,
		contextFiles: append([]ContextFile(nil), cfg.ContextFiles...),
		systemPrompt: cfg.SystemPrompt,
		sessionID:    cfg.SessionID,
		tools:        append([]Tool(nil), cfg.Tools...),
		skills:       append([]Skill(nil), cfg.Skills...),
		toolByName:   toolByName,
		toolDefs:     toolDefs,
		maxTurns:     maxTurns,
		messages:     append([]llm.Message(nil), cfg.Messages...),
		now:          now,
	}, nil
}

type Agent struct {
	cwd    string
	llm    llm.Assistant
	usage  llm.Usage
	now    func() time.Time
	tools  []Tool
	skills []Skill

	contextUsage llm.Usage
	toolDefs     []llm.Tool

	compactor Compactor

	contextFiles []ContextFile
	toolByName   map[string]Tool

	systemPrompt string

	sessionID string
	maxTurns  int

	messages []llm.Message
}

func (a *Agent) Messages() []llm.Message {
	return append([]llm.Message(nil), a.messages...)
}

func (a *Agent) SwitchModel(model llm.Model) error {
	newLLM, err := llm.Load(model, a.llm.ReasoningLevel())
	if err != nil {
		return fmt.Errorf("select llm: %w", err)
	}
	a.llm = newLLM
	return nil
}

func (a *Agent) SwitchReasoningLevel(lvl llm.ReasoningLevel) error {
	if err := a.llm.SetReasoningLevel(lvl); err != nil {
		return fmt.Errorf("set reasoning level: %w", err)
	}
	return nil
}

func (a *Agent) CompactConversation() error {
	if a.compactor == nil {
		return fmt.Errorf("compactor is not configured")
	}
	messages, err := a.compactor.Compact(context.Background(), append([]llm.Message(nil), a.messages...))
	if err != nil {
		return err
	}
	a.messages = append([]llm.Message(nil), messages...)
	return nil
}

func (a *Agent) NewConversation() error {
	return nil
}

// Prompt is not safe for concurrent use on the same Agent.
func (a *Agent) Prompt(ctx context.Context, prompt string) (<-chan Event, <-chan error) {
	events := make(chan Event, 32)
	errs := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errs)
		if err := a.run(ctx, prompt, events); err != nil {
			errs <- err
		}
	}()

	return events, errs
}

func (a *Agent) run(ctx context.Context, prompt string, events chan<- Event) error {
	started := false
	finished := false

	finish := func(err error) error {
		if !started || finished {
			return err
		}
		finished = true
		if err != nil {
			select {
			case <-ctx.Done():
				return err
			case events <- PromptProcessingError{Error: err}:
			}
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return err
			}
			return ctx.Err()
		case events <- PromptProcessingEnded{}:
			return err
		}
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case events <- PromptProcessingStarted{}:
		started = true
	}

	a.messages = append(a.messages, llm.UserMessage{
		Timestamp: a.now(),
		Text:      prompt,
	})

	for turn := range a.maxTurns {
		select {
		case <-ctx.Done():
			return finish(ctx.Err())
		case events <- ConversationTurnStarted{Turn: turn}:
		}

		var text strings.Builder
		var thinking strings.Builder
		var toolCalls []llm.ToolCallBlock
		var usage llm.Usage

		request := llm.PredictNextRequest{
			SystemPrompt: a.systemPrompt,
			Tools:        append([]llm.Tool(nil), a.toolDefs...),
			Messages:     append([]llm.Message(nil), a.messages...),
		}

		llmEventsCh, llmErrsCh := a.llm.PredictNext(ctx, request)

		for event := range llmEventsCh {
			switch typedEvent := event.(type) {
			case llm.PredictionStarted:
				select {
				case <-ctx.Done():
					return finish(ctx.Err())
				case events <- AssistantMessageStarted{Message: llm.AssistantMessage{Timestamp: a.now()}}:
				}
			case llm.ThinkingDelta:
				thinking.WriteString(typedEvent.Text)
				select {
				case <-ctx.Done():
					return finish(ctx.Err())
				case events <- AssistantThinkingDeltaReceived{Text: typedEvent.Text}:
				}
			case llm.TextDelta:
				text.WriteString(typedEvent.Text)
				select {
				case <-ctx.Done():
					return finish(ctx.Err())
				case events <- AssistantMessageDeltaReceived{Text: typedEvent.Text}:
				}
			case llm.ToolCallRequested:
				toolCalls = append(toolCalls, typedEvent.ToolCall)
			case llm.PredictionFinished:
				a.usage.InputTokens += typedEvent.Usage.InputTokens
				a.usage.OutputTokens += typedEvent.Usage.OutputTokens
				a.usage.CachedTokens += typedEvent.Usage.CachedTokens
				a.usage.TotalTokens += typedEvent.Usage.TotalTokens
				a.contextUsage = typedEvent.Usage
				usage = typedEvent.Usage
			}
		}

		if err := <-llmErrsCh; err != nil {
			return finish(fmt.Errorf("llm prediction error: %w", err))
		}

		blocks := make([]llm.AssistantBlock, 0, 2+len(toolCalls))
		if thinking.Len() > 0 {
			blocks = append(blocks, llm.ThinkingBlock{Text: thinking.String()})
		}
		if text.Len() > 0 {
			blocks = append(blocks, llm.TextBlock{Text: text.String()})
		}
		for _, call := range toolCalls {
			blocks = append(blocks, call)
		}

		msg := llm.AssistantMessage{
			Timestamp: a.now(),
			Blocks:    blocks,
			Usage:     usage,
		}

		a.messages = append(a.messages, msg)

		select {
		case <-ctx.Done():
			return finish(ctx.Err())
		case events <- AssistantMessageEnded{Message: msg}:
		}

		if len(toolCalls) == 0 {
			select {
			case <-ctx.Done():
				return finish(ctx.Err())
			case events <- ConversationTurnEnded{Turn: turn}:
			}
			return finish(nil)
		}

		for _, toolCall := range toolCalls {
			if err := a.executeToolCall(ctx, events, toolCall); err != nil {
				return finish(err)
			}
		}

		select {
		case <-ctx.Done():
			return finish(ctx.Err())
		case events <- ConversationTurnEnded{Turn: turn}:
		}
	}

	err := fmt.Errorf("max turns reached (%d)", a.maxTurns)
	a.messages = append(a.messages, llm.ErrorMessage{
		Timestamp: a.now(),
		Error:     err,
	})
	return finish(err)
}

func (a *Agent) executeToolCall(ctx context.Context, events chan<- Event, toolCall llm.ToolCallBlock) error {
	t, ok := a.toolByName[toolCall.Name]
	if !ok {
		execErr := fmt.Errorf("tool %q not found", toolCall.Name)
		a.messages = append(a.messages, llm.ToolErrorMessage{
			Timestamp:  a.now(),
			ToolCallID: toolCall.ID,
			ToolName:   toolCall.Name,
			Error:      execErr,
		})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case events <- ToolExecutionFailed{CallID: toolCall.ID, Error: execErr}:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case events <- ToolExecutionEnded{CallID: toolCall.ID}:
			return nil
		}
	}

	callTitle, err := toolCallTitle(t, toolCall.Arguments)
	if err != nil {
		return a.finishToolCall(ctx, events, t, toolCall, nil, err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case events <- ToolExecutionStarted{Tool: t, CallID: toolCall.ID, CallArguments: toolCall.Arguments, CallTitle: callTitle}:
	}

	if incrementalTool, ok := t.(IncrementalTool); ok {
		toolResult, err := a.callIncrementalTool(ctx, events, incrementalTool, toolCall)
		return a.finishToolCall(ctx, events, t, toolCall, toolResult, err)
	}

	toolResult, execErr := t.Call(ctx, toolCall.Arguments)
	return a.finishToolCall(ctx, events, t, toolCall, toolResult, execErr)
}

func toolCallTitle(t Tool, rawArgs json.RawMessage) (string, error) {
	provider, ok := t.(ToolCallTitleProvider)
	if !ok {
		return t.Name(), nil
	}
	return provider.CallTitle(rawArgs)
}

func (a *Agent) callIncrementalTool(ctx context.Context, events chan<- Event, incrementalTool IncrementalTool, toolCall llm.ToolCallBlock) (any, error) {
	emit := func(artifact tool.Artifact) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case events <- ToolExecutionOutputDeltaReceived{
			Tool:     incrementalTool,
			CallID:   toolCall.ID,
			Artifact: artifact,
		}:
			return nil
		}
	}

	return incrementalTool.CallIncremental(ctx, toolCall.Arguments, emit)
}

func (a *Agent) finishToolCall(ctx context.Context, events chan<- Event, executedTool Tool, toolCall llm.ToolCallBlock, toolResult any, execErr error) error {
	if execErr != nil {
		a.messages = append(a.messages, llm.ToolErrorMessage{
			Timestamp:  a.now(),
			ToolCallID: toolCall.ID,
			ToolName:   toolCall.Name,
			Error:      execErr,
		})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case events <- ToolExecutionFailed{Tool: executedTool, CallID: toolCall.ID, Error: execErr}:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case events <- ToolExecutionEnded{Tool: executedTool, CallID: toolCall.ID}:
			return nil
		}
	}

	toolOutputData, err := json.Marshal(toolResult)
	if err != nil {
		execErr := fmt.Errorf("marshal tool %q result: %w", toolCall.Name, err)
		a.messages = append(a.messages, llm.ToolErrorMessage{
			Timestamp:  a.now(),
			ToolCallID: toolCall.ID,
			ToolName:   toolCall.Name,
			Error:      execErr,
		})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case events <- ToolExecutionFailed{Tool: executedTool, CallID: toolCall.ID, Error: execErr}:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case events <- ToolExecutionEnded{Tool: executedTool, CallID: toolCall.ID}:
			return nil
		}
	}

	a.messages = append(a.messages, llm.ToolOutputMessage{
		Timestamp:  a.now(),
		ToolCallID: toolCall.ID,
		ToolName:   toolCall.Name,
		ToolOutput: string(toolOutputData),
	})

	artifacts := []tool.Artifact(nil)
	if result, ok := toolResult.(tool.Result); ok {
		artifacts = result.Artifacts()
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case events <- ToolExecutionResultReceived{
		Tool:      executedTool,
		CallID:    toolCall.ID,
		Artifacts: artifacts,
	}:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case events <- ToolExecutionEnded{
		Tool:   executedTool,
		CallID: toolCall.ID,
	}:
		return nil
	}
}

type State struct {
	CWD string

	Model          llm.Model
	ReasoningLevel llm.ReasoningLevel
	Usage          llm.Usage
	ContextUsage   llm.Usage
}

func (a *Agent) State() State {
	return State{
		CWD:            a.cwd,
		Model:          a.llm.Model(),
		ReasoningLevel: a.llm.ReasoningLevel(),
		Usage:          a.usage,
		ContextUsage:   a.contextUsage,
	}
}
