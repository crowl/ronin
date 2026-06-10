package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
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

type ConversationConfig struct {
	CWD          string
	Assistant    llm.Assistant
	Compactor    Compactor
	Tools        []Tool
	SystemPrompt string
	MaxTurns     int
	Now          func() time.Time
	SessionStore session.Store
	Session      session.Session
}

const defaultMaxTurns = 512

func NewConversation(cfg ConversationConfig) (*Conversation, error) {
	if cfg.Assistant == nil {
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

	sess := cfg.Session
	sess.Messages = append([]llm.Message(nil), cfg.Session.Messages...)

	var contextUsage llm.Usage
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		assistantMessage, ok := sess.Messages[i].(llm.AssistantMessage)
		if !ok {
			continue
		}
		contextUsage = assistantMessage.Usage
		break
	}

	return &Conversation{
		cwd:          cfg.CWD,
		systemPrompt: cfg.SystemPrompt,
		maxTurns:     maxTurns,
		now:          now,
		assistant:    cfg.Assistant,
		contextUsage: contextUsage,
		toolDefs:     toolDefs,
		toolByName:   toolByName,
		compactor:    cfg.Compactor,
		sessionStore: cfg.SessionStore,
		session:      sess,
	}, nil
}

type Conversation struct {
	cwd          string
	systemPrompt string
	maxTurns     int
	now          func() time.Time

	assistant    llm.Assistant
	contextUsage llm.Usage

	toolDefs   []llm.Tool
	toolByName map[string]Tool

	compactor Compactor

	sessionStore session.Store
	session      session.Session
}

func (c *Conversation) CWD() string {
	return c.cwd
}

func (c *Conversation) Messages() []llm.Message {
	return append([]llm.Message(nil), c.session.Messages...)
}

func (c *Conversation) ToolCallTitle(name string, arguments []byte) string {
	t, ok := c.toolByName[name]
	if !ok {
		return name
	}
	if titleProvider, ok := t.(ToolCallTitleProvider); ok {
		if title, err := titleProvider.CallTitle(arguments); err == nil {
			return title
		}
	}
	return t.Name()
}

func (c *Conversation) Model() llm.Model {
	return c.assistant.Model()
}

func (c *Conversation) ReasoningLevel() llm.ReasoningLevel {
	return c.assistant.ReasoningLevel()
}

func (c *Conversation) ContextUsage() llm.Usage {
	return c.contextUsage
}

func (c *Conversation) SwitchModel(model llm.Model) error {
	newAssistant, err := llm.LoadAssistant(model, c.assistant.ReasoningLevel())
	if err != nil {
		return fmt.Errorf("select llm: %w", err)
	}

	updatedSession := c.session
	updatedSession.Model = config.Model{Provider: model.Provider, Name: model.Name}
	updatedSession.UpdatedAt = c.now()

	if c.sessionStore != nil && c.session.ID != "" {
		if err := c.sessionStore.Save(updatedSession); err != nil {
			return fmt.Errorf("save session model: %w", err)
		}
	}

	c.assistant = newAssistant
	c.session = updatedSession
	return nil
}

func (c *Conversation) SwitchReasoningLevel(lvl llm.ReasoningLevel) error {
	prevLevel := c.assistant.ReasoningLevel()
	if err := c.assistant.SetReasoningLevel(lvl); err != nil {
		return fmt.Errorf("set reasoning level: %w", err)
	}

	updatedSession := c.session
	updatedSession.ReasoningLevel = string(lvl)
	updatedSession.UpdatedAt = c.now()

	if c.sessionStore != nil && c.session.ID != "" {
		if err := c.sessionStore.Save(updatedSession); err != nil {
			rollbackErr := c.assistant.SetReasoningLevel(prevLevel)
			if rollbackErr != nil {
				return errors.Join(
					fmt.Errorf("save session reasoning level: %w", err),
					fmt.Errorf("rollback reasoning level: %w", rollbackErr),
				)
			}
			return fmt.Errorf("save session reasoning level: %w", err)
		}
	}

	c.session = updatedSession
	return nil
}

func (c *Conversation) CompactConversation(ctx context.Context) error {
	if c.compactor == nil {
		return fmt.Errorf("compactor is not configured")
	}
	messages, err := c.compactor.Compact(ctx, append([]llm.Message(nil), c.session.Messages...))
	if err != nil {
		return err
	}

	c.session.Messages = append([]llm.Message(nil), messages...)
	if c.sessionStore != nil && c.session.ID != "" {
		c.session.UpdatedAt = c.now()
		if err := c.sessionStore.Save(c.session); err != nil {
			return err
		}
	}
	return nil
}

func (c *Conversation) NewConversation() error {
	if c.sessionStore != nil && c.session.ID != "" {
		model := c.assistant.Model()
		reasoningLevel := c.assistant.ReasoningLevel()
		metadata := session.Metadata{
			Model: config.Model{
				Provider: model.Provider,
				Name:     model.Name,
			},
			ReasoningLevel: string(reasoningLevel),
		}
		if err := c.sessionStore.Clear(c.cwd); err != nil {
			return fmt.Errorf("clear session: %w", err)
		}
		newSess, err := c.sessionStore.Create(c.cwd, metadata)
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		c.contextUsage = llm.Usage{}
		c.session = newSess
		return nil
	}

	c.contextUsage = llm.Usage{}
	c.session.Messages = nil
	return nil
}

// Prompt is not safe for concurrent use on the same Conversation.
func (c *Conversation) Prompt(ctx context.Context, prompt string) (<-chan Event, <-chan error) {
	events := make(chan Event, 32)
	errs := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errs)
		runErr := c.run(ctx, prompt, events)

		if c.sessionStore != nil && c.session.ID != "" {
			c.session.UpdatedAt = c.now()
			if saveErr := c.sessionStore.Save(c.session); saveErr != nil {
				select {
				case events <- SessionSaveFailed{Error: saveErr}:
				case <-ctx.Done():
				}
				if runErr == nil {
					runErr = fmt.Errorf("failed to save session: %w", saveErr)
				}
			}
		}

		if runErr != nil {
			errs <- runErr
		}
	}()

	return events, errs
}

func (c *Conversation) run(ctx context.Context, prompt string, events chan<- Event) error {
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

	c.session.Messages = append(c.session.Messages, llm.UserMessage{
		Timestamp: c.now(),
		Text:      prompt,
	})

	for turn := range c.maxTurns {
		select {
		case <-ctx.Done():
			return finish(ctx.Err())
		case events <- ConversationTurnStarted{Turn: turn}:
		}

		var blocks []llm.AssistantBlock
		var toolCalls []llm.ToolCallBlock
		var usage llm.Usage

		request := llm.PredictNextRequest{
			SystemPrompt: c.systemPrompt,
			Tools:        append([]llm.Tool(nil), c.toolDefs...),
			Messages:     append([]llm.Message(nil), c.session.Messages...),
		}

		predictionEventsCh, predictionErrCh := c.assistant.PredictNext(ctx, request)

		for event := range predictionEventsCh {
			switch typedEvent := event.(type) {
			case llm.PredictionStarted:
				select {
				case <-ctx.Done():
					return finish(ctx.Err())
				case events <- AssistantMessageStarted{Message: llm.AssistantMessage{Timestamp: c.now()}}:
				}
			case llm.ThinkingDelta:
				select {
				case <-ctx.Done():
					return finish(ctx.Err())
				case events <- AssistantThinkingDeltaReceived{Text: typedEvent.Text}:
				}
			case llm.TextDelta:
				select {
				case <-ctx.Done():
					return finish(ctx.Err())
				case events <- AssistantMessageDeltaReceived{Text: typedEvent.Text}:
				}
			case llm.BlockEnded:
				blocks = append(blocks, typedEvent.Block)
				if toolCall, ok := typedEvent.Block.(llm.ToolCallBlock); ok {
					toolCalls = append(toolCalls, toolCall)
				}
			case llm.PredictionFinished:
				c.contextUsage = typedEvent.Usage
				usage = typedEvent.Usage
			}
		}

		if err := <-predictionErrCh; err != nil {
			return finish(fmt.Errorf("llm prediction error: %w", err))
		}

		msg := llm.AssistantMessage{
			Timestamp: c.now(),
			Blocks:    blocks,
			Usage:     usage,
		}

		c.session.Messages = append(c.session.Messages, msg)

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
			if err := c.executeToolCall(ctx, events, toolCall); err != nil {
				return finish(err)
			}
		}

		select {
		case <-ctx.Done():
			return finish(ctx.Err())
		case events <- ConversationTurnEnded{Turn: turn}:
		}
	}

	err := fmt.Errorf("max turns reached (%d)", c.maxTurns)
	c.session.Messages = append(c.session.Messages, llm.ErrorMessage{
		Timestamp: c.now(),
		Error:     err,
	})
	return finish(err)
}

func (c *Conversation) executeToolCall(ctx context.Context, events chan<- Event, toolCall llm.ToolCallBlock) error {
	t, ok := c.toolByName[toolCall.Name]
	if !ok {
		execErr := fmt.Errorf("tool %q not found", toolCall.Name)
		c.session.Messages = append(c.session.Messages, llm.ToolErrorMessage{
			Timestamp:  c.now(),
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

	callTitle := t.Name()

	if titleProvider, ok := t.(ToolCallTitleProvider); ok {
		var err error
		callTitle, err = titleProvider.CallTitle(toolCall.Arguments)
		if err != nil {
			return c.finishToolCall(ctx, events, t, toolCall, nil, err)
		}
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case events <- ToolExecutionStarted{
		Tool:          t,
		CallID:        toolCall.ID,
		CallArguments: toolCall.Arguments,
		CallTitle:     callTitle,
	}:
	}

	if incrementalTool, ok := t.(IncrementalTool); ok {
		toolResult, err := c.callIncrementalTool(ctx, events, incrementalTool, toolCall)
		return c.finishToolCall(ctx, events, t, toolCall, toolResult, err)
	}

	toolResult, execErr := t.Call(ctx, toolCall.Arguments)
	return c.finishToolCall(ctx, events, t, toolCall, toolResult, execErr)
}

func (c *Conversation) callIncrementalTool(ctx context.Context, events chan<- Event, incrementalTool IncrementalTool, toolCall llm.ToolCallBlock) (any, error) {
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

func (c *Conversation) finishToolCall(ctx context.Context, events chan<- Event, executedTool Tool, toolCall llm.ToolCallBlock, toolResult any, execErr error) error {
	if execErr != nil {
		c.session.Messages = append(c.session.Messages, llm.ToolErrorMessage{
			Timestamp:  c.now(),
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
		c.session.Messages = append(c.session.Messages, llm.ToolErrorMessage{
			Timestamp:  c.now(),
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

	c.session.Messages = append(c.session.Messages, llm.ToolOutputMessage{
		Timestamp:  c.now(),
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
