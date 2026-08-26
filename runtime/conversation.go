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
	CWD                           string
	ModelClient                   llm.ModelClient
	Compactor                     Compactor
	ToolOutputSummarizer          ToolOutputSummarizer
	ToolOutputSummarizationPolicy ToolOutputSummarizationPolicy
	Tools                         []Tool
	SystemPrompt                  string
	MaxTurns                      int
	Now                           func() time.Time
	SessionStore                  session.Store
	Session                       session.Session
	Messages                      []llm.Message
}

const (
	defaultMaxTurns              = 512
	metadataSaveTimeout          = 5 * time.Second
	defaultSessionTitle          = "New session"
	interruptedToolCallErrorText = "tool call interrupted before completion"
)

func NewConversation(cfg ConversationConfig) (*Conversation, error) {
	if cfg.ModelClient == nil {
		return nil, errors.New("model client is required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
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

	messages := append([]llm.Message(nil), cfg.Messages...)
	var contextUsage llm.Usage
	for i := len(messages) - 1; i >= 0; i-- {
		assistantMessage, ok := messages[i].(llm.AssistantMessage)
		if ok {
			contextUsage = assistantMessage.Usage
			break
		}
	}

	return &Conversation{
		cwd:                           cfg.CWD,
		systemPrompt:                  cfg.SystemPrompt,
		maxTurns:                      maxTurns,
		now:                           now,
		modelClient:                   cfg.ModelClient,
		contextUsage:                  contextUsage,
		toolDefs:                      toolDefs,
		toolByName:                    toolByName,
		compactor:                     cfg.Compactor,
		toolOutputSummarizer:          cfg.ToolOutputSummarizer,
		toolOutputSummarizationPolicy: cfg.ToolOutputSummarizationPolicy.normalized(),
		sessionStore:                  cfg.SessionStore,
		session:                       cfg.Session,
		messages:                      messages,
	}, nil
}

type Conversation struct {
	cwd          string
	systemPrompt string
	maxTurns     int
	now          func() time.Time

	modelClient  llm.ModelClient
	contextUsage llm.Usage

	toolDefs   []llm.Tool
	toolByName map[string]Tool

	compactor Compactor

	toolOutputSummarizer          ToolOutputSummarizer
	toolOutputSummarizationPolicy ToolOutputSummarizationPolicy

	sessionStore session.Store
	session      session.Session
	messages     []llm.Message
}

func (c *Conversation) CWD() string                        { return c.cwd }
func (c *Conversation) SessionID() string                  { return c.session.ID }
func (c *Conversation) SessionTitle() string               { return c.session.Title }
func (c *Conversation) SessionUpdatedAt() time.Time        { return c.session.UpdatedAt }
func (c *Conversation) Model() llm.Model                   { return c.modelClient.Model() }
func (c *Conversation) ReasoningLevel() llm.ReasoningLevel { return c.modelClient.ReasoningLevel() }
func (c *Conversation) ContextUsage() llm.Usage            { return c.contextUsage }

func (c *Conversation) Messages() []llm.Message {
	return append([]llm.Message(nil), c.messages...)
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

func (c *Conversation) SwitchModel(model llm.Model) error {
	newModelClient, err := llm.LoadModelClient(model, c.modelClient.ReasoningLevel())
	if err != nil {
		return fmt.Errorf("select model client: %w", err)
	}
	updated := c.session
	updated.Model = config.Model{Provider: model.Provider, Name: model.Name}
	updated.UpdatedAt = c.now().UTC()
	if err := c.updateMetadata(context.Background(), updated); err != nil {
		return fmt.Errorf("save session model: %w", err)
	}
	c.modelClient = newModelClient
	if compactor, ok := c.compactor.(*DefaultCompactor); ok {
		compactor.modelClient = newModelClient
	}
	c.session = updated
	return nil
}

func (c *Conversation) SwitchReasoningLevel(lvl llm.ReasoningLevel) error {
	prevLevel := c.modelClient.ReasoningLevel()
	if err := c.modelClient.SetReasoningLevel(lvl); err != nil {
		return fmt.Errorf("set reasoning level: %w", err)
	}
	updated := c.session
	updated.ReasoningLevel = string(lvl)
	updated.UpdatedAt = c.now().UTC()
	if err := c.updateMetadata(context.Background(), updated); err != nil {
		if rollbackErr := c.modelClient.SetReasoningLevel(prevLevel); rollbackErr != nil {
			return errors.Join(fmt.Errorf("save session reasoning level: %w", err), fmt.Errorf("rollback reasoning level: %w", rollbackErr))
		}
		return fmt.Errorf("save session reasoning level: %w", err)
	}
	c.session = updated
	return nil
}

func (c *Conversation) CompactConversation(ctx context.Context) error {
	if c.compactor == nil {
		return errors.New("compactor is not configured")
	}
	messages, err := c.compactor.Compact(ctx, append([]llm.Message(nil), c.messages...))
	if err != nil {
		return err
	}
	if c.sessionStore != nil && c.session.ID != "" {
		if err := c.sessionStore.Append(ctx, c.session.ID, session.Event{Type: session.EventCompaction, CreatedAt: c.now(), Compacted: messages}); err != nil {
			return fmt.Errorf("save compacted session: %w", err)
		}
		c.session.UpdatedAt = c.now().UTC()
	}
	c.messages = append([]llm.Message(nil), messages...)
	return nil
}

func (c *Conversation) RecordWorkflowResult(message llm.WorkflowResultMessage) error {
	if message.Timestamp.IsZero() {
		message.Timestamp = c.now()
	}
	if err := c.appendMessage(context.Background(), message); err != nil {
		return fmt.Errorf("save workflow result: %w", err)
	}
	return nil
}

func (c *Conversation) NewConversation() error {
	if c.sessionStore != nil && c.session.ID != "" {
		model := c.modelClient.Model()
		newSession, err := c.sessionStore.Create(context.Background(), c.cwd, session.Metadata{
			Model:          config.Model{Provider: model.Provider, Name: model.Name},
			ReasoningLevel: string(c.modelClient.ReasoningLevel()),
		})
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		c.session = newSession
	}
	c.contextUsage = llm.Usage{}
	c.messages = nil
	return nil
}

const maxTitleRunes = 60

func deriveTitle(messages []llm.Message) string {
	for _, message := range messages {
		user, ok := message.(llm.UserMessage)
		if !ok {
			continue
		}
		line := strings.TrimSpace(user.Text)
		if idx := strings.IndexAny(line, "\r\n"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			return ""
		}
		runes := []rune(line)
		if len(runes) > maxTitleRunes {
			return strings.TrimSpace(string(runes[:maxTitleRunes])) + "…"
		}
		return line
	}
	return ""
}

// Prompt is not safe for concurrent use on the same Conversation.
func (c *Conversation) Prompt(ctx context.Context, prompt string) (<-chan Event, <-chan error) {
	events := make(chan Event, 32)
	errs := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errs)
		if err := c.run(ctx, prompt, events); err != nil {
			errs <- err
		}
	}()
	return events, errs
}

func (c *Conversation) reportSaveFailure(ctx context.Context, events chan<- Event, runErr, saveErr error) error {
	select {
	case events <- SessionSaveFailed{Error: saveErr}:
	case <-ctx.Done():
	}
	if runErr != nil {
		return errors.Join(runErr, fmt.Errorf("failed to save session: %w", saveErr))
	}
	return fmt.Errorf("failed to save session: %w", saveErr)
}

func (c *Conversation) appendMessage(ctx context.Context, message llm.Message) error {
	if c.sessionStore != nil && c.session.ID != "" {
		if err := c.sessionStore.Append(ctx, c.session.ID, session.Event{Type: session.EventMessage, CreatedAt: c.now(), Message: message}); err != nil {
			return err
		}
		c.session.UpdatedAt = c.now().UTC()
	}
	c.messages = append(c.messages, message)
	return nil
}

func (c *Conversation) repairInterruptedToolCalls(ctx context.Context) error {
	messages, repaired := repairInterruptedToolCalls(c.messages, c.now())
	if !repaired {
		return nil
	}
	if c.sessionStore != nil && c.session.ID != "" {
		event := session.Event{Type: session.EventCompaction, CreatedAt: c.now(), Compacted: messages}
		if err := c.sessionStore.Append(ctx, c.session.ID, event); err != nil {
			return fmt.Errorf("repair interrupted tool calls: %w", err)
		}
		c.session.UpdatedAt = c.now().UTC()
	}
	c.messages = messages
	return nil
}

func repairInterruptedToolCalls(messages []llm.Message, timestamp time.Time) ([]llm.Message, bool) {
	repaired := make([]llm.Message, 0, len(messages))
	var pending []llm.ToolCallBlock
	changed := false

	flushPending := func() {
		for _, toolCall := range pending {
			repaired = append(repaired, llm.ToolErrorMessage{
				Timestamp:  timestamp,
				ToolName:   toolCall.Name,
				ToolCallID: toolCall.ID,
				Error:      errors.New(interruptedToolCallErrorText),
			})
		}
		changed = changed || len(pending) > 0
		pending = nil
	}

	for _, message := range messages {
		switch typed := message.(type) {
		case llm.AssistantMessage:
			flushPending()
			repaired = append(repaired, message)
			for _, block := range typed.Blocks {
				if toolCall, ok := block.(llm.ToolCallBlock); ok {
					pending = append(pending, toolCall)
				}
			}
		case llm.ToolOutputMessage:
			if removePendingToolCall(&pending, typed.ToolCallID) {
				repaired = append(repaired, message)
				continue
			}
			flushPending()
			repaired = append(repaired, message)
		case llm.ToolErrorMessage:
			if removePendingToolCall(&pending, typed.ToolCallID) {
				repaired = append(repaired, message)
				continue
			}
			flushPending()
			repaired = append(repaired, message)
		default:
			flushPending()
			repaired = append(repaired, message)
		}
	}
	flushPending()
	if !changed {
		return messages, false
	}
	return repaired, true
}

func removePendingToolCall(toolCalls *[]llm.ToolCallBlock, id string) bool {
	for i, toolCall := range *toolCalls {
		if toolCall.ID == id {
			*toolCalls = append((*toolCalls)[:i], (*toolCalls)[i+1:]...)
			return true
		}
	}
	return false
}

func (c *Conversation) updateTitle(ctx context.Context) error {
	if c.session.Title != "" && c.session.Title != defaultSessionTitle {
		return nil
	}
	title := deriveTitle(c.messages)
	if title == "" {
		return nil
	}
	updated := c.session
	updated.Title = title
	updated.UpdatedAt = c.now().UTC()

	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), metadataSaveTimeout)
	defer cancel()
	if err := c.updateMetadata(saveCtx, updated); err != nil {
		return err
	}
	c.session = updated
	return nil
}

func (c *Conversation) updateMetadata(ctx context.Context, updated session.Session) error {
	if c.sessionStore == nil || c.session.ID == "" {
		return nil
	}
	return c.sessionStore.UpdateMetadata(ctx, c.session.ID, session.Metadata{
		Title: updated.Title, Model: updated.Model, ReasoningLevel: updated.ReasoningLevel,
	})
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

	if err := c.repairInterruptedToolCalls(ctx); err != nil {
		return finish(c.reportSaveFailure(ctx, events, nil, err))
	}
	user := llm.UserMessage{Timestamp: c.now(), Text: prompt}
	if err := c.appendMessage(ctx, user); err != nil {
		return finish(c.reportSaveFailure(ctx, events, nil, err))
	}
	if err := c.updateTitle(ctx); err != nil {
		return finish(c.reportSaveFailure(ctx, events, nil, err))
	}

	for turn := range c.maxTurns {
		select {
		case <-ctx.Done():
			return finish(ctx.Err())
		case events <- ConversationTurnStarted{Turn: turn}:
		}
		var blocks []llm.AssistantBlock
		var toolCalls []llm.ToolCallBlock
		var usage llm.Usage
		request := llm.PredictNextRequest{SystemPrompt: c.systemPrompt, Tools: append([]llm.Tool(nil), c.toolDefs...), Messages: append([]llm.Message(nil), c.messages...)}
		predictionEventsCh, predictionErrCh := c.modelClient.PredictNext(ctx, request)
		for event := range predictionEventsCh {
			switch typed := event.(type) {
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
				case events <- AssistantThinkingDeltaReceived{Text: typed.Text}:
				}
			case llm.TextDelta:
				select {
				case <-ctx.Done():
					return finish(ctx.Err())
				case events <- AssistantMessageDeltaReceived{Text: typed.Text}:
				}
			case llm.BlockEnded:
				blocks = append(blocks, typed.Block)
				if call, ok := typed.Block.(llm.ToolCallBlock); ok {
					toolCalls = append(toolCalls, call)
				}
			case llm.PredictionFinished:
				c.contextUsage, usage = typed.Usage, typed.Usage
			}
		}
		if err := <-predictionErrCh; err != nil {
			return finish(fmt.Errorf("llm prediction error: %w", err))
		}
		message := llm.AssistantMessage{Timestamp: c.now(), Blocks: blocks, Usage: usage}
		if err := c.appendMessage(ctx, message); err != nil {
			return finish(c.reportSaveFailure(ctx, events, nil, err))
		}
		select {
		case <-ctx.Done():
			return finish(ctx.Err())
		case events <- AssistantMessageEnded{Message: message}:
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
	message := llm.ErrorMessage{Timestamp: c.now(), Error: err}
	if appendErr := c.appendMessage(ctx, message); appendErr != nil {
		return finish(c.reportSaveFailure(ctx, events, err, appendErr))
	}
	return finish(err)
}

func (c *Conversation) executeToolCall(ctx context.Context, events chan<- Event, toolCall llm.ToolCallBlock) error {
	t, ok := c.toolByName[toolCall.Name]
	if !ok {
		execErr := fmt.Errorf("tool %q not found", toolCall.Name)
		message := llm.ToolErrorMessage{Timestamp: c.now(), ToolCallID: toolCall.ID, ToolName: toolCall.Name, Error: execErr}
		if err := c.appendMessage(ctx, message); err != nil {
			return c.reportSaveFailure(ctx, events, nil, err)
		}
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
	case events <- ToolExecutionStarted{Tool: t, CallID: toolCall.ID, CallArguments: toolCall.Arguments, CallTitle: callTitle}:
	}
	if incrementalTool, ok := t.(IncrementalTool); ok {
		result, err := c.callIncrementalTool(ctx, events, incrementalTool, toolCall)
		return c.finishToolCall(ctx, events, t, toolCall, result, err)
	}
	result, err := t.Call(ctx, toolCall.Arguments)
	return c.finishToolCall(ctx, events, t, toolCall, result, err)
}

func (c *Conversation) callIncrementalTool(ctx context.Context, events chan<- Event, incrementalTool IncrementalTool, toolCall llm.ToolCallBlock) (any, error) {
	emit := func(artifact tool.Artifact) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case events <- ToolExecutionOutputDeltaReceived{Tool: incrementalTool, CallID: toolCall.ID, Artifact: artifact}:
			return nil
		}
	}
	return incrementalTool.CallIncremental(ctx, toolCall.Arguments, emit)
}

func (c *Conversation) modelVisibleToolOutput(ctx context.Context, toolCall llm.ToolCallBlock, rawOutput string, wasError bool, toolError string) (string, string, bool) {
	policy := c.toolOutputSummarizationPolicy.normalized()
	if !policy.Enabled || c.toolOutputSummarizer == nil || (wasError && !policy.SummarizeErrors) || policy.ExcludedTools[toolCall.Name] || len(rawOutput) < policy.MinBytes || (policy.MinEstimatedTokens > 0 && estimateTokens(rawOutput) < policy.MinEstimatedTokens) {
		return rawOutput, "", false
	}
	summary, err := c.toolOutputSummarizer.SummarizeToolOutput(ctx, ToolOutputSummaryRequest{
		ToolName: toolCall.Name, ToolCallID: toolCall.ID, ToolArguments: string(toolCall.Arguments), ToolOutput: rawOutput,
		ToolError: toolError, Origin: c.toolOutputOrigin(toolCall), WasError: wasError,
	})
	if err != nil || strings.TrimSpace(summary.Summary) == "" {
		return rawOutput, "", false
	}
	wrapped, err := json.Marshal(map[string]any{"summarized": true, "raw_output_retained": true, "summary": summary.Summary, "omitted": summary.Omitted})
	if err != nil {
		return rawOutput, "", false
	}
	return string(wrapped), rawOutput, true
}

func (c *Conversation) toolOutputOrigin(toolCall llm.ToolCallBlock) string {
	var latestUser string
	for i := len(c.messages) - 1; i >= 0; i-- {
		if msg, ok := c.messages[i].(llm.UserMessage); ok {
			latestUser = msg.Text
			break
		}
	}
	var assistantText string
	for i := len(c.messages) - 1; i >= 0; i-- {
		msg, ok := c.messages[i].(llm.AssistantMessage)
		if !ok {
			continue
		}
		for _, block := range msg.Blocks {
			if text, ok := block.(llm.TextBlock); ok {
				assistantText += text.Text
			}
		}
		break
	}
	var b strings.Builder
	if strings.TrimSpace(latestUser) != "" {
		b.WriteString("Latest user message:\n")
		b.WriteString(latestUser)
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(assistantText) != "" {
		b.WriteString("Assistant text before tool call:\n")
		b.WriteString(assistantText)
		b.WriteString("\n\n")
	}
	b.WriteString("Tool call requested: ")
	b.WriteString(toolCall.Name)
	if len(toolCall.Arguments) > 0 {
		b.WriteString(" with arguments ")
		b.WriteString(string(toolCall.Arguments))
	}
	return strings.TrimSpace(b.String())
}

func estimateTokens(text string) int { return (len(text) + 3) / 4 }

func (c *Conversation) finishToolCall(ctx context.Context, events chan<- Event, executedTool Tool, toolCall llm.ToolCallBlock, toolResult any, execErr error) error {
	if execErr != nil {
		message := llm.ToolErrorMessage{Timestamp: c.now(), ToolCallID: toolCall.ID, ToolName: toolCall.Name, Error: execErr}
		if err := c.appendMessage(ctx, message); err != nil {
			return c.reportSaveFailure(ctx, events, nil, err)
		}
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
	data, err := json.Marshal(toolResult)
	if err != nil {
		execErr = fmt.Errorf("marshal tool %q result: %w", toolCall.Name, err)
		message := llm.ToolErrorMessage{Timestamp: c.now(), ToolCallID: toolCall.ID, ToolName: toolCall.Name, Error: execErr}
		if appendErr := c.appendMessage(ctx, message); appendErr != nil {
			return c.reportSaveFailure(ctx, events, nil, appendErr)
		}
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
	modelOutput, rawOutput, summarized := c.modelVisibleToolOutput(ctx, toolCall, string(data), false, "")
	message := llm.ToolOutputMessage{Timestamp: c.now(), ToolCallID: toolCall.ID, ToolName: toolCall.Name, ToolOutput: modelOutput, RawToolOutput: rawOutput, ToolOutputWasSummarized: summarized}
	if err := c.appendMessage(ctx, message); err != nil {
		return c.reportSaveFailure(ctx, events, nil, err)
	}
	var artifacts []tool.Artifact
	if result, ok := toolResult.(tool.Result); ok {
		artifacts = result.Artifacts()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case events <- ToolExecutionResultReceived{Tool: executedTool, CallID: toolCall.ID, Artifacts: artifacts}:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case events <- ToolExecutionEnded{Tool: executedTool, CallID: toolCall.ID}:
		return nil
	}
}
