package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
)

const (
	defaultBaseURL      = "https://api.anthropic.com/v1/messages"
	anthropicVersion    = "2023-06-01"
	defaultOutputTokens = 4096
)

type LLMConfig struct {
	BaseURL        string
	APIKey         string
	Model          llm.Model
	ReasoningLevel llm.ReasoningLevel
	Client         *http.Client
}

func NewLLM(cfg LLMConfig) (*LLM, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("ANTHROPIC_API_KEY is required")
	}
	if !llm.IsValidReasoningLevel(cfg.ReasoningLevel) {
		return nil, fmt.Errorf("reasoning level %v is not valid", cfg.ReasoningLevel)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	return &LLM{
		baseURL:        cfg.BaseURL,
		apiKey:         cfg.APIKey,
		model:          cfg.Model,
		reasoningLevel: cfg.ReasoningLevel,
		client:         cfg.Client,
	}, nil
}

type LLM struct {
	baseURL string
	apiKey  string

	model llm.Model

	reasoningMu    sync.RWMutex
	reasoningLevel llm.ReasoningLevel

	client *http.Client
}

func (s *LLM) Model() llm.Model {
	return s.model
}

func (s *LLM) ReasoningLevel() llm.ReasoningLevel {
	s.reasoningMu.RLock()
	defer s.reasoningMu.RUnlock()
	return s.reasoningLevel
}

func (s *LLM) SetReasoningLevel(level llm.ReasoningLevel) error {
	if !llm.IsValidReasoningLevel(level) {
		return fmt.Errorf("reasoning level %v is not valid", level)
	}
	s.reasoningMu.Lock()
	defer s.reasoningMu.Unlock()
	s.reasoningLevel = level
	return nil
}

func (s *LLM) PredictNext(ctx context.Context, req llm.PredictNextRequest) (<-chan llm.PredictionEvent, <-chan error) {
	events := make(chan llm.PredictionEvent, 32)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		if err := s.stream(ctx, req, events); err != nil {
			errs <- err
		}
	}()
	return events, errs
}

func (s *LLM) PredictNextStructured(ctx context.Context, req llm.PredictNextStructuredRequest) (json.RawMessage, error) {
	payload, err := s.buildStructuredPayload(req)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic structured request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create anthropic structured request: %w", err)
	}
	setHeaders(httpReq, s.apiKey, false)

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send anthropic structured request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Error("failed to close anthropic structured response", "error", err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, fmt.Errorf("anthropic structured status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read anthropic structured response: %w", err)
	}

	var structuredResp anthropicResponse
	if err := json.Unmarshal(data, &structuredResp); err != nil {
		return nil, fmt.Errorf("parse anthropic structured response: %w", err)
	}
	for _, block := range structuredResp.Content {
		if block.Type == "text" {
			text := strings.TrimSpace(block.Text)
			if text == "" {
				return nil, errors.New("anthropic structured response contained empty text")
			}
			if !json.Valid([]byte(text)) {
				return nil, errors.New("anthropic structured response text is not valid JSON")
			}
			return json.RawMessage(text), nil
		}
	}
	return nil, errors.New("anthropic structured response contained no text JSON")
}

func (s *LLM) stream(ctx context.Context, req llm.PredictNextRequest, events chan<- llm.PredictionEvent) error {
	payload, err := s.buildPayload(req)
	if err != nil {
		return err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal anthropic request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create anthropic request: %w", err)
	}
	setHeaders(httpReq, s.apiKey, true)

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send anthropic request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Error("failed to close anthropic response", "error", err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return fmt.Errorf("anthropic status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024), 10*1024*1024)

	var dataLines []string
	state := newAnthropicStreamState()

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := handleData(ctx, strings.Join(dataLines, "\n"), state, events); err != nil {
				return err
			}
			dataLines = nil
			continue
		}
		if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if len(dataLines) > 0 {
		if err := handleData(ctx, strings.Join(dataLines, "\n"), state, events); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read anthropic stream: %w", err)
	}
	if !state.finished {
		return sendEvent(ctx, events, llm.PredictionFinished{
			Usage:      state.usage,
			StopReason: state.stopReason(),
		})
	}
	return nil
}

func (s *LLM) buildPayload(req llm.PredictNextRequest) (*anthropicRequest, error) {
	messages, err := convertMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	var thinking *anthropicThinkingConfig
	var outputConfig *anthropicOutputConfig
	if s.reasoningLevel != llm.ReasoningLevelOff {
		thinking = &anthropicThinkingConfig{
			Type:    "adaptive",
			Display: "summarized",
		}
		outputConfig = &anthropicOutputConfig{
			Effort: string(s.reasoningLevel),
		}
	}

	maxTokens := defaultOutputTokens
	if req.MaxTokens > 0 {
		maxTokens = req.MaxTokens
	}

	payload := &anthropicRequest{
		Model:        s.model.Name,
		MaxTokens:    maxTokens,
		Messages:     messages,
		Stream:       true,
		Thinking:     thinking,
		OutputConfig: outputConfig,
	}
	if req.SystemPrompt != "" {
		payload.System = req.SystemPrompt
	}
	if len(req.Tools) > 0 {
		payload.Tools = convertTools(req.Tools)
	}
	return payload, nil
}

func (s *LLM) buildStructuredPayload(req llm.PredictNextStructuredRequest) (*anthropicRequest, error) {
	if req.Schema == nil {
		return nil, errors.New("structured output schema is required")
	}
	messages, err := convertMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	maxTokens := defaultOutputTokens
	if req.MaxTokens > 0 {
		maxTokens = req.MaxTokens
	}

	payload := &anthropicRequest{
		Model:     s.model.Name,
		MaxTokens: maxTokens,
		Messages:  messages,
		Stream:    false,
		OutputConfig: &anthropicOutputConfig{
			Format: &anthropicOutputConfigFormat{
				Type:   "json_schema",
				Schema: req.Schema,
			},
		},
	}
	if req.SystemPrompt != "" {
		payload.System = req.SystemPrompt
	}
	return payload, nil
}

type anthropicRequest struct {
	Model        string                   `json:"model"`
	MaxTokens    int                      `json:"max_tokens"`
	Messages     []anthropicMessage       `json:"messages"`
	System       string                   `json:"system,omitempty"`
	Stream       bool                     `json:"stream,omitempty"`
	Tools        []anthropicTool          `json:"tools,omitempty"`
	ToolChoice   *anthropicToolChoice     `json:"tool_choice,omitempty"`
	Thinking     *anthropicThinkingConfig `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig   `json:"output_config,omitempty"`
}

type anthropicMessage struct {
	Role    string                         `json:"role"`
	Content []anthropicMessageContentBlock `json:"content"`
}

type anthropicMessageContentBlock interface {
	anthropicMessageContentBlock()
}

type anthropicTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicThinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type anthropicToolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthropicToolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

func (anthropicTextBlock) anthropicMessageContentBlock()       {}
func (anthropicThinkingBlock) anthropicMessageContentBlock()   {}
func (anthropicToolUseBlock) anthropicMessageContentBlock()    {}
func (anthropicToolResultBlock) anthropicMessageContentBlock() {}

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	InputSchema *jsonschema.Schema `json:"input_schema"`
}

type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type anthropicThinkingConfig struct {
	Type    string `json:"type"`
	Display string `json:"display"`
}

type anthropicOutputConfig struct {
	Effort string                       `json:"effort,omitempty"`
	Format *anthropicOutputConfigFormat `json:"format,omitempty"`
}

type anthropicOutputConfigFormat struct {
	Type   string             `json:"type"`
	Schema *jsonschema.Schema `json:"schema"`
}

type anthropicResponse struct {
	Content []anthropicContentBlock `json:"content"`
	Usage   anthropicUsage          `json:"usage"`
}

type anthropicStreamEvent struct {
	Type         string                  `json:"type"`
	Index        int                     `json:"index"`
	Message      *anthropicMessageStart  `json:"message"`
	ContentBlock *anthropicContentBlock  `json:"content_block"`
	Delta        *anthropicStreamDelta   `json:"delta"`
	Usage        *anthropicUsage         `json:"usage"`
	Error        *anthropicErrorResponse `json:"error"`
}

type anthropicMessageStart struct {
	Usage anthropicUsage `json:"usage"`
}

type anthropicStreamDelta struct {
	Type        string          `json:"type"`
	Text        string          `json:"text"`
	Thinking    string          `json:"thinking"`
	Signature   string          `json:"signature"`
	PartialJSON string          `json:"partial_json"`
	StopReason  string          `json:"stop_reason"`
	Input       json.RawMessage `json:"input"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type anthropicErrorResponse struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func convertMessages(messages []llm.Message) ([]anthropicMessage, error) {
	converted := make([]anthropicMessage, 0, len(messages))
	for _, msg := range messages {
		switch typedMsg := msg.(type) {
		case llm.UserMessage:
			converted = append(converted, anthropicMessage{
				Role:    "user",
				Content: []anthropicMessageContentBlock{anthropicTextBlock{Type: "text", Text: typedMsg.Text}},
			})
		case llm.AssistantMessage:
			content, err := convertAssistantBlocks(typedMsg.Blocks)
			if err != nil {
				return nil, err
			}
			if len(content) > 0 {
				converted = append(converted, anthropicMessage{Role: "assistant", Content: content})
			}
		case llm.ToolOutputMessage:
			converted = append(converted, anthropicMessage{
				Role: "user",
				Content: []anthropicMessageContentBlock{anthropicToolResultBlock{
					Type:      "tool_result",
					ToolUseID: typedMsg.ToolCallID,
					Content:   typedMsg.ToolOutput,
				}},
			})
		case llm.ToolErrorMessage:
			converted = append(converted, anthropicMessage{
				Role: "user",
				Content: []anthropicMessageContentBlock{anthropicToolResultBlock{
					Type:      "tool_result",
					ToolUseID: typedMsg.ToolCallID,
					Content:   "error: " + errorText(typedMsg.Error),
					IsError:   true,
				}},
			})
		case llm.ErrorMessage:
			converted = append(converted, anthropicMessage{
				Role:    "user",
				Content: []anthropicMessageContentBlock{anthropicTextBlock{Type: "text", Text: "error: " + errorText(typedMsg.Error)}},
			})
		default:
			return nil, fmt.Errorf("unsupported message %T", msg)
		}
	}
	return converted, nil
}

func convertAssistantBlocks(blocks []llm.AssistantBlock) ([]anthropicMessageContentBlock, error) {
	content := make([]anthropicMessageContentBlock, 0, len(blocks))
	for _, block := range blocks {
		switch b := block.(type) {
		case llm.ThinkingBlock:
			content = append(content, anthropicThinkingBlock{Type: "thinking", Thinking: b.Text, Signature: b.Signature})
		case llm.TextBlock:
			content = append(content, anthropicTextBlock{Type: "text", Text: b.Text})
		case llm.ToolCallBlock:
			args, err := rawArguments(b.Arguments)
			if err != nil {
				return nil, fmt.Errorf("tool call %q arguments: %w", b.ID, err)
			}
			content = append(content, anthropicToolUseBlock{Type: "tool_use", ID: b.ID, Name: b.Name, Input: args})
		default:
			return nil, fmt.Errorf("unsupported assistant block %T", block)
		}
	}
	return content, nil
}

func convertTools(tools []llm.Tool) []anthropicTool {
	converted := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		converted = append(converted, anthropicTool{
			Name:        tool.Name(),
			Description: tool.Description(),
			InputSchema: tool.Parameters(),
		})
	}
	return converted
}

type anthropicStreamState struct {
	blocks      map[int]*anthropicPartialBlock
	usage       llm.Usage
	toolEmitted bool
	started     bool
	finished    bool
}

type anthropicPartialBlock struct {
	kind      llm.BlockKind
	id        string
	name      string
	text      strings.Builder
	thinking  strings.Builder
	signature strings.Builder
	input     strings.Builder
}

func newAnthropicStreamState() *anthropicStreamState {
	return &anthropicStreamState{blocks: map[int]*anthropicPartialBlock{}}
}

func (state *anthropicStreamState) stopReason() llm.StopReason {
	if state.toolEmitted {
		return llm.StopReasonToolUse
	}
	return llm.StopReasonFinished
}

func handleData(ctx context.Context, data string, state *anthropicStreamState, events chan<- llm.PredictionEvent) error {
	if data == "" || data == "[DONE]" {
		return nil
	}

	var event anthropicStreamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return fmt.Errorf("parse anthropic event: %w", err)
	}

	switch event.Type {
	case "message_start":
		state.started = true
		if event.Message != nil {
			state.usage.InputTokens = event.Message.Usage.InputTokens
			state.usage.OutputTokens = event.Message.Usage.OutputTokens
			state.usage.CachedTokens = event.Message.Usage.CacheReadInputTokens
			state.usage.TotalTokens = state.usage.InputTokens + state.usage.OutputTokens
		}
		return sendEvent(ctx, events, llm.PredictionStarted{})
	case "content_block_start":
		return state.startBlock(ctx, event.Index, event.ContentBlock, events)
	case "content_block_delta":
		return state.applyDelta(ctx, event.Index, event.Delta, events)
	case "content_block_stop":
		return state.stopBlock(ctx, event.Index, events)
	case "message_delta":
		if event.Delta != nil && event.Delta.StopReason == "tool_use" {
			state.toolEmitted = true
		}
		if event.Usage != nil {
			state.usage.OutputTokens = event.Usage.OutputTokens
			state.usage.TotalTokens = state.usage.InputTokens + state.usage.OutputTokens
		}
		return nil
	case "message_stop":
		if !state.started {
			if err := sendEvent(ctx, events, llm.PredictionStarted{}); err != nil {
				return err
			}
		}
		state.finished = true
		return sendEvent(ctx, events, llm.PredictionFinished{Usage: state.usage, StopReason: state.stopReason()})
	case "ping":
		return nil
	case "error":
		if event.Error == nil {
			return fmt.Errorf("anthropic error event: %s", data)
		}
		return fmt.Errorf("anthropic error event %s: %s", event.Error.Type, event.Error.Message)
	default:
		return nil
	}
}

func (state *anthropicStreamState) startBlock(ctx context.Context, index int, block *anthropicContentBlock, events chan<- llm.PredictionEvent) error {
	if block == nil {
		return errors.New("anthropic content_block_start missing content_block")
	}
	partial := &anthropicPartialBlock{id: block.ID, name: block.Name}
	kind := llm.BlockKindText
	switch block.Type {
	case "text":
		kind = llm.BlockKindText
		partial.text.WriteString(block.Text)
	case "thinking":
		kind = llm.BlockKindThinking
		partial.thinking.WriteString(block.Thinking)
		partial.signature.WriteString(block.Signature)
	case "tool_use":
		kind = llm.BlockKindToolCall
	default:
		return fmt.Errorf("unsupported anthropic content block type %q", block.Type)
	}
	partial.kind = kind
	state.blocks[index] = partial
	return sendEvent(ctx, events, llm.BlockStarted{Index: index, Kind: kind})
}

func (state *anthropicStreamState) applyDelta(ctx context.Context, index int, delta *anthropicStreamDelta, events chan<- llm.PredictionEvent) error {
	if delta == nil {
		return errors.New("anthropic content_block_delta missing delta")
	}
	partial := state.blocks[index]
	if partial == nil {
		return fmt.Errorf("anthropic delta for unknown content block index %d", index)
	}
	switch delta.Type {
	case "text_delta":
		partial.text.WriteString(delta.Text)
		return sendEvent(ctx, events, llm.TextDelta{Index: index, Text: delta.Text})
	case "thinking_delta":
		partial.thinking.WriteString(delta.Thinking)
		return sendEvent(ctx, events, llm.ThinkingDelta{Index: index, Text: delta.Thinking})
	case "signature_delta":
		partial.signature.WriteString(delta.Signature)
		return nil
	case "input_json_delta":
		partial.input.WriteString(delta.PartialJSON)
		return sendEvent(ctx, events, llm.ToolCallArgumentsDelta{Index: index, Arguments: delta.PartialJSON})
	default:
		return nil
	}
}

func (state *anthropicStreamState) stopBlock(ctx context.Context, index int, events chan<- llm.PredictionEvent) error {
	partial := state.blocks[index]
	if partial == nil {
		return fmt.Errorf("anthropic stop for unknown content block index %d", index)
	}
	defer delete(state.blocks, index)

	switch partial.kind {
	case llm.BlockKindText:
		return sendEvent(ctx, events, llm.BlockEnded{Index: index, Block: llm.TextBlock{Text: partial.text.String()}})
	case llm.BlockKindThinking:
		return sendEvent(ctx, events, llm.BlockEnded{Index: index, Block: llm.ThinkingBlock{Text: partial.thinking.String(), Signature: partial.signature.String()}})
	case llm.BlockKindToolCall:
		args := strings.TrimSpace(partial.input.String())
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return fmt.Errorf("invalid anthropic tool call arguments for %q", partial.name)
		}
		state.toolEmitted = true
		return sendEvent(ctx, events, llm.BlockEnded{Index: index, Block: llm.ToolCallBlock{ID: partial.id, Name: partial.name, Arguments: json.RawMessage(args)}})
	default:
		return fmt.Errorf("unsupported anthropic block kind %q", partial.kind)
	}
}

func setHeaders(req *http.Request, apiKey string, stream bool) {
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
}

func rawArguments(arguments json.RawMessage) (json.RawMessage, error) {
	if len(arguments) == 0 || strings.TrimSpace(string(arguments)) == "" {
		return json.RawMessage("{}"), nil
	}
	if !json.Valid(arguments) {
		return nil, errors.New("invalid JSON")
	}
	return arguments, nil
}

func errorText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

func sendEvent(ctx context.Context, events chan<- llm.PredictionEvent, event llm.PredictionEvent) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case events <- event:
		return nil
	}
}
