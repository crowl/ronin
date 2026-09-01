package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	SessionCost                   llm.SessionCost
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

	toolDefs, toolByName, err := indexTools(cfg.Tools)
	if err != nil {
		return nil, err
	}

	messages := append([]llm.Message(nil), cfg.Messages...)
	var contextUsage llm.Usage
	for _, message := range slices.Backward(messages) {
		assistantMessage, ok := message.(llm.AssistantMessage)
		if ok {
			contextUsage = assistantMessage.Usage
			break
		}
	}

	contextUsage.Cost = llm.Cost{Total: cfg.SessionCost.Total, Available: cfg.SessionCost.Available}
	if cfg.Session.ID == "" && len(messages) == 0 && !cfg.SessionCost.Available {
		cfg.SessionCost.Available = true
		contextUsage.Cost.Available = true
	}

	return &Conversation{
		cwd:                           cfg.CWD,
		systemPrompt:                  cfg.SystemPrompt,
		maxTurns:                      maxTurns,
		now:                           now,
		modelClient:                   cfg.ModelClient,
		contextUsage:                  contextUsage,
		sessionCost:                   cfg.SessionCost,
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
	sessionCost  llm.SessionCost

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

func (c *Conversation) SetToolsAndSystemPrompt(tools []Tool, systemPrompt string) error {
	toolDefs, toolByName, err := indexTools(tools)
	if err != nil {
		return err
	}
	c.toolDefs = toolDefs
	c.toolByName = toolByName
	c.systemPrompt = systemPrompt
	return nil
}

func indexTools(tools []Tool) ([]llm.Tool, map[string]Tool, error) {
	toolDefs := make([]llm.Tool, 0, len(tools))
	toolByName := make(map[string]Tool, len(tools))
	for i, t := range tools {
		if t == nil {
			return nil, nil, fmt.Errorf("tool %d is nil", i)
		}
		name := strings.TrimSpace(t.Name())
		if name == "" {
			return nil, nil, fmt.Errorf("tool %d has empty name", i)
		}
		if _, exists := toolByName[name]; exists {
			return nil, nil, fmt.Errorf("duplicate tool name %q", name)
		}
		toolDefs = append(toolDefs, t)
		toolByName[name] = t
	}
	return toolDefs, toolByName, nil
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
	level := c.modelClient.ReasoningLevel()
	if model.SupportedReasoning != 0 && !model.SupportsReasoning(level) {
		nearest, ok := model.NearestReasoningLevel(level)
		if !ok {
			return fmt.Errorf("model %s has no supported reasoning levels", model)
		}
		level = nearest
	}
	newModelClient, err := llm.LoadModelClient(model, level)
	if err != nil {
		return fmt.Errorf("select model client: %w", err)
	}
	updated := c.session
	updated.Model = config.Model{Provider: model.Provider, Name: model.Name}
	updated.ReasoningLevel = string(level)
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
	if !c.modelClient.Model().SupportsReasoning(lvl) {
		return fmt.Errorf("reasoning level %q is not supported by model %s", lvl, c.modelClient.Model())
	}
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
	c.contextUsage = llm.Usage{Cost: llm.Cost{Available: true}}
	c.sessionCost = llm.SessionCost{Available: true}
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
		var stopReason llm.StopReason
		predictionFinished := false
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
				typed.Usage.Cost = llm.EstimateCost(c.modelClient.Model(), typed.Usage)
				usage = typed.Usage
				stopReason = typed.StopReason
				predictionFinished = true
			}
		}
		if err := <-predictionErrCh; err != nil {
			return finish(fmt.Errorf("llm prediction error: %w", err))
		}
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		if !predictionFinished {
			return finish(errors.New("llm prediction ended without a completion event"))
		}
		message := llm.AssistantMessage{Timestamp: c.now(), Blocks: blocks, StopReason: stopReason, Usage: usage}
		if err := completionError(stopReason); err != nil {
			if appendErr := c.appendMessage(ctx, message); appendErr != nil {
				return finish(c.reportSaveFailure(ctx, events, err, appendErr))
			}
			c.recordUsage(usage)
			select {
			case <-ctx.Done():
				return finish(ctx.Err())
			case events <- AssistantMessageEnded{Message: message}:
			}
			return finish(err)
		}
		if err := c.appendMessage(ctx, message); err != nil {
			return finish(c.reportSaveFailure(ctx, events, nil, err))
		}
		c.recordUsage(usage)
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

func (c *Conversation) recordUsage(usage llm.Usage) {
	if usage.Cost.Available {
		c.sessionCost.Total += usage.Cost.Total
	} else {
		c.sessionCost.Available = false
	}
	c.contextUsage = usage
	c.contextUsage.Cost = llm.Cost{Total: c.sessionCost.Total, Available: c.sessionCost.Available}
}

func completionError(reason llm.StopReason) error {
	switch reason {
	case llm.StopReasonMaxTokens, llm.StopReasonModelContextWindowExceeded:
		return fmt.Errorf("llm response was truncated (%s)", reason)
	case llm.StopReasonRefusal:
		return errors.New("llm response was refused")
	case llm.StopReasonPauseTurn:
		return errors.New("llm response paused before completion")
	default:
		return nil
	}
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
	for _, v := range slices.Backward(c.messages) {
		if msg, ok := v.(llm.UserMessage); ok {
			latestUser = msg.Text
			break
		}
	}
	var assistantText strings.Builder
	for _, v := range slices.Backward(c.messages) {
		msg, ok := v.(llm.AssistantMessage)
		if !ok {
			continue
		}
		for _, block := range msg.Blocks {
			if text, ok := block.(llm.TextBlock); ok {
				assistantText.WriteString(text.Text)
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
	if strings.TrimSpace(assistantText.String()) != "" {
		b.WriteString("Assistant text before tool call:\n")
		b.WriteString(assistantText.String())
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
