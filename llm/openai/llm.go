package openai

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
	"github.com/crowl/ronin/llm/internal/httpretry"
)

const (
	defaultBaseURL = "https://api.openai.com/v1/responses"
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
		return nil, errors.New("OPENAI_API_KEY is required")
	}
	if !llm.IsValidReasoningLevel(cfg.ReasoningLevel) {
		return nil, fmt.Errorf("reasoning level %v is not valid", cfg.ReasoningLevel)
	}
	if !cfg.Model.SupportsReasoning(cfg.ReasoningLevel) {
		return nil, fmt.Errorf("reasoning level %q is not supported by model %s", cfg.ReasoningLevel, cfg.Model)
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
	if !s.model.SupportsReasoning(level) {
		return fmt.Errorf("reasoning level %q is not supported by model %s", level, s.model)
	}
	s.reasoningMu.Lock()
	defer s.reasoningMu.Unlock()
	s.reasoningLevel = level
	return nil
}

func (s *LLM) PredictNext(ctx context.Context, req llm.PredictNextRequest) (<-chan llm.PredictionEvent, <-chan error) {
	events := make(chan llm.PredictionEvent, 16)
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
		return nil, fmt.Errorf("marshal %s structured request: %w", s.provider(), err)
	}

	resp, err := httpretry.Do(ctx, s.client, func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create %s structured request: %w", s.provider(), err)
		}

		httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		return httpReq, nil
	})
	if err != nil {
		return nil, fmt.Errorf("send %s structured request: %w", s.provider(), err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Error("failed to close structured response", "provider", s.provider(), "error", err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, fmt.Errorf("%s structured status %d: %s", s.provider(), resp.StatusCode, strings.TrimSpace(string(data)))
	}

	mediaType := resp.Header.Get("Content-Type")
	reader := bufio.NewReader(resp.Body)
	var text string
	if isStructuredStream(mediaType, reader) {
		text, err = s.readStructuredStream(reader)
	} else {
		text, err = s.readStructuredResponse(reader)
	}
	if err != nil {
		return nil, err
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("%s structured response contained no output text", s.provider())
	}
	if !json.Valid([]byte(text)) {
		return nil, fmt.Errorf("%s structured response output is not valid JSON", s.provider())
	}
	return json.RawMessage(text), nil
}

func isStructuredStream(mediaType string, reader *bufio.Reader) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "text/event-stream") {
		return true
	}

	const maxLeadingWhitespace = 64
	start := 0
leadingWhitespace:
	for start < maxLeadingWhitespace {
		prefix, _ := reader.Peek(start + 1)
		if len(prefix) <= start {
			return false
		}
		switch prefix[start] {
		case ' ', '\t', '\r', '\n':
			start++
		default:
			break leadingWhitespace
		}
	}

	prefix, _ := reader.Peek(start + len("event:"))
	prefix = prefix[start:]
	return bytes.HasPrefix(prefix, []byte("event:")) ||
		bytes.HasPrefix(prefix, []byte("data:")) ||
		bytes.HasPrefix(prefix, []byte(":"))
}

func (s *LLM) readStructuredResponse(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, 10*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read %s structured response: %w", s.provider(), err)
	}

	var structuredResp openAIStructuredResponse
	if err := json.Unmarshal(data, &structuredResp); err != nil {
		return "", fmt.Errorf("parse %s structured response: %w", s.provider(), err)
	}
	return structuredResp.OutputText(), nil
}

func (s *LLM) readStructuredStream(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024), 10*1024*1024)

	var text strings.Builder
	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := s.appendStructuredData(strings.Join(dataLines, "\n"), &text); err != nil {
				return "", err
			}
			dataLines = nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimSpace(after))
		}
	}
	if len(dataLines) > 0 {
		if err := s.appendStructuredData(strings.Join(dataLines, "\n"), &text); err != nil {
			return "", err
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read %s structured stream: %w", s.provider(), err)
	}
	return text.String(), nil
}

func (s *LLM) appendStructuredData(data string, text *strings.Builder) error {
	if data == "" || data == "[DONE]" {
		return nil
	}

	var event struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return fmt.Errorf("parse %s structured event: %w", s.provider(), err)
	}
	if strings.Contains(event.Type, "error") {
		return fmt.Errorf("%s structured error event: %s", s.provider(), data)
	}
	if strings.Contains(event.Type, "output_text.delta") {
		text.WriteString(event.Delta)
	}
	return nil
}

func (s *LLM) stream(ctx context.Context, req llm.PredictNextRequest, events chan<- llm.PredictionEvent) error {
	payload, err := s.buildPayload(req)
	if err != nil {
		return err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s request: %w", s.provider(), err)
	}

	resp, err := httpretry.Do(ctx, s.client, func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create %s request: %w", s.provider(), err)
		}

		httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		return httpReq, nil
	})
	if err != nil {
		return fmt.Errorf("send %s request: %w", s.provider(), err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Error("failed to close response", "provider", s.provider(), "error", err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return fmt.Errorf("%s status %d: %s", s.provider(), resp.StatusCode, strings.TrimSpace(string(data)))
	}

	if err := sendEvent(ctx, events, llm.PredictionStarted{}); err != nil {
		return err
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024), 10*1024*1024)

	var dataLines []string
	state := newStreamState(s.model.Provider)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := s.handleData(ctx, strings.Join(dataLines, "\n"), state, events); err != nil {
				return err
			}
			dataLines = nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimSpace(after))
		}
	}

	if len(dataLines) > 0 {
		if err := s.handleData(ctx, strings.Join(dataLines, "\n"), state, events); err != nil {
			return err
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s stream: %w", s.provider(), err)
	}

	if !state.finished {
		if err := state.finishOpenBlock(ctx, events); err != nil {
			return err
		}
		return sendEvent(ctx, events, llm.PredictionFinished{StopReason: state.stopReason()})
	}
	return nil
}

func (s *LLM) buildPayload(req llm.PredictNextRequest) (*openAIRequest, error) {
	input, err := convertMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	payload := &openAIRequest{
		Model:  s.model.Name,
		Input:  input,
		Stream: true,
		Store:  false,
	}

	if req.SystemPrompt != "" {
		payload.Instructions = req.SystemPrompt
	}
	if req.MaxTokens > 0 {
		payload.MaxOutputTokens = req.MaxTokens
	}
	if reasoningLevel := s.ReasoningLevel(); reasoningLevel != llm.ReasoningLevelOff && s.model.ReasoningMode != llm.ReasoningModeNone {
		payload.Reasoning = &openAIReasoning{
			Effort:  reasoningLevel,
			Summary: "auto",
		}
	}

	if len(req.Tools) > 0 {
		tools := make([]openAITool, 0, len(req.Tools))
		for _, tool := range req.Tools {
			tools = append(tools, openAITool{
				Type:        "function",
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters:  tool.Parameters(),
			})
		}
		payload.Tools = tools
	}

	return payload, nil
}

func (s *LLM) buildStructuredPayload(req llm.PredictNextStructuredRequest) (*openAIRequest, error) {
	if req.Schema == nil {
		return nil, errors.New("structured output schema is required")
	}
	input, err := convertMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	payload := &openAIRequest{
		Model:  s.model.Name,
		Input:  input,
		Stream: true,
		Store:  false,
		Text: &openAITextConfig{Format: openAITextFormat{
			Type:   "json_schema",
			Name:   "structured_output",
			Strict: true,
			Schema: req.Schema,
		}},
	}

	if req.SystemPrompt != "" {
		payload.Instructions = req.SystemPrompt
	}
	if req.MaxTokens > 0 {
		payload.MaxOutputTokens = req.MaxTokens
	}
	if reasoningLevel := s.ReasoningLevel(); reasoningLevel != llm.ReasoningLevelOff && s.model.ReasoningMode != llm.ReasoningModeNone {
		payload.Reasoning = &openAIReasoning{
			Effort:  reasoningLevel,
			Summary: "auto",
		}
	}

	return payload, nil
}

type openAIRequest struct {
	Model           string            `json:"model"`
	Input           []openAIInput     `json:"input"`
	Stream          bool              `json:"stream"`
	Store           bool              `json:"store"`
	Instructions    string            `json:"instructions,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	Reasoning       *openAIReasoning  `json:"reasoning,omitempty"`
	Tools           []openAITool      `json:"tools,omitempty"`
	Text            *openAITextConfig `json:"text,omitempty"`
}

type openAITextConfig struct {
	Format openAITextFormat `json:"format"`
}

type openAITextFormat struct {
	Type   string             `json:"type"`
	Name   string             `json:"name"`
	Strict bool               `json:"strict"`
	Schema *jsonschema.Schema `json:"schema"`
}

type openAIReasoning struct {
	Effort  llm.ReasoningLevel `json:"effort"`
	Summary string             `json:"summary"`
}

type openAITool struct {
	Type        string             `json:"type"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Parameters  *jsonschema.Schema `json:"parameters"`
}

type openAIMessageInput struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIFunctionCallInput struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIInput interface{ openAIInput() }

func (openAIMessageInput) openAIInput()      {}
func (openAIFunctionCallInput) openAIInput() {}

type openAIFunctionCallOutputInput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

func (openAIFunctionCallOutputInput) openAIInput() {}

type openAIStructuredResponse struct {
	Output []openAIStructuredOutput `json:"output"`
}

type openAIStructuredOutput struct {
	Type    string                          `json:"type"`
	Content []openAIStructuredOutputContent `json:"content"`
}

type openAIStructuredOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (r openAIStructuredResponse) OutputText() string {
	var b strings.Builder
	for _, output := range r.Output {
		if output.Type != "message" && output.Type != "" {
			continue
		}
		for _, content := range output.Content {
			if content.Type == "output_text" || content.Type == "text" || content.Type == "" {
				b.WriteString(content.Text)
			}
		}
	}
	return b.String()
}

func convertMessages(messages []llm.Message) ([]openAIInput, error) {
	input := make([]openAIInput, 0, len(messages))
	for _, msg := range messages {
		switch typedMsg := msg.(type) {
		case llm.UserMessage:
			input = append(input, openAIMessageInput{
				Role:    "user",
				Content: typedMsg.Text,
			})
		case llm.AssistantMessage:
			var text strings.Builder
			for _, block := range typedMsg.Blocks {
				switch b := block.(type) {
				case llm.TextBlock:
					text.WriteString(b.Text)
				case llm.ThinkingBlock:
					continue
				case llm.RedactedThinkingBlock:
					continue
				case llm.ToolCallBlock:
					if text.Len() != 0 {
						input = append(input, openAIMessageInput{
							Role:    "assistant",
							Content: text.String(),
						})
						text.Reset()
					}
					args, err := rawArgumentsString(b.Arguments)
					if err != nil {
						return nil, fmt.Errorf("tool call %q arguments: %w", b.ID, err)
					}
					input = append(input, openAIFunctionCallInput{
						Type:      "function_call",
						CallID:    b.ID,
						Name:      b.Name,
						Arguments: args,
					})
				default:
					return nil, fmt.Errorf("unsupported assistant block %T", block)
				}
			}
			if text.Len() != 0 {
				input = append(input, openAIMessageInput{
					Role:    "assistant",
					Content: text.String(),
				})
			}
		case llm.WorkflowResultMessage:
			input = append(input, openAIMessageInput{
				Role:    "user",
				Content: typedMsg.Text(),
			})
		case llm.ToolOutputMessage:
			input = append(input, openAIFunctionCallOutputInput{
				Type:   "function_call_output",
				CallID: typedMsg.ToolCallID,
				Output: typedMsg.ToolOutput,
			})
		case llm.ToolErrorMessage:
			input = append(input, openAIFunctionCallOutputInput{
				Type:   "function_call_output",
				CallID: typedMsg.ToolCallID,
				Output: "error: " + errorText(typedMsg.Error),
			})
		case llm.ErrorMessage:
			input = append(input, openAIMessageInput{
				Role:    "user",
				Content: "error: " + errorText(typedMsg.Error),
			})
		default:
			return nil, fmt.Errorf("unsupported message %T", msg)
		}
	}
	return input, nil
}

type streamState struct {
	calls                    map[string]*partialCall
	toolEmitted              bool
	finished                 bool
	blockStarted             bool
	blockKind                llm.BlockKind
	blockIndex               int
	text                     strings.Builder
	thinking                 strings.Builder
	thinkingSeparatorPending bool
	provider                 string
}

type partialCall struct {
	ID            string
	Name          string
	Arguments     string
	ArgumentsDone bool
	Emitted       bool
}

func newStreamState(provider string) *streamState {
	return &streamState{
		calls:    map[string]*partialCall{},
		provider: provider,
	}
}

func (state *streamState) callForRaw(raw map[string]any) *partialCall {
	keys := callKeys(raw)
	var call *partialCall
	for _, key := range keys {
		if existing := state.calls[key]; existing != nil {
			call = existing
			break
		}
	}
	if call == nil {
		call = &partialCall{}
	}

	for _, key := range keys {
		if existing := state.calls[key]; existing != nil && existing != call {
			mergePartialCall(call, existing)
			state.rebindCall(existing, call)
		}
		state.calls[key] = call
	}

	return call
}

func (state *streamState) rebindCall(oldCall, newCall *partialCall) {
	for key, call := range state.calls {
		if call == oldCall {
			state.calls[key] = newCall
		}
	}
}

func callKeys(raw map[string]any) []string {
	var keys []string
	if itemID := stringField(raw, "item_id"); itemID != "" {
		keys = append(keys, "item:"+itemID)
	}
	if callID := stringField(raw, "call_id"); callID != "" {
		keys = append(keys, "call:"+callID)
	}
	if item, ok := raw["item"].(map[string]any); ok {
		if id := stringField(item, "id"); id != "" {
			keys = append(keys, "item:"+id)
		}
		if callID := stringField(item, "call_id"); callID != "" {
			keys = append(keys, "call:"+callID)
		}
	}
	if hasNumberField(raw, "output_index") {
		keys = append(keys, fmt.Sprintf("index:%d", intField(raw, "output_index")))
	}
	if len(keys) == 0 {
		keys = append(keys, "index:0")
	}
	return keys
}

func (state *streamState) stopReason() llm.StopReason {
	if state.toolEmitted {
		return llm.StopReasonToolUse
	}
	return llm.StopReasonFinished
}

func (call *partialCall) toToolCall() (llm.ToolCallBlock, error) {
	arguments := call.Arguments
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}
	if !json.Valid([]byte(arguments)) {
		return llm.ToolCallBlock{}, fmt.Errorf("invalid tool call arguments for %q", call.Name)
	}
	return llm.ToolCallBlock{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: json.RawMessage(arguments),
	}, nil
}

func (s *LLM) provider() string {
	if provider := s.model.Provider; provider != "" {
		return provider
	}
	return "openai"
}

func (s *LLM) handleData(ctx context.Context, data string, state *streamState, events chan<- llm.PredictionEvent) error {
	if data == "" || data == "[DONE]" {
		return nil
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return fmt.Errorf("parse %s event: %w", s.provider(), err)
	}

	typeName, _ := raw["type"].(string)

	if strings.Contains(typeName, "error") {
		return fmt.Errorf("%s error event: %s", s.provider(), data)
	}

	if delta, ok := raw["delta"].(string); ok && strings.Contains(typeName, "reasoning") && strings.Contains(typeName, "delta") {
		if err := state.startBlock(ctx, events, llm.BlockKindThinking); err != nil {
			return err
		}
		if state.thinkingSeparatorPending && state.thinking.Len() > 0 {
			delta = "... " + delta
		}
		state.thinkingSeparatorPending = false
		state.thinking.WriteString(delta)
		return sendEvent(ctx, events, llm.ThinkingDelta{Index: state.blockIndex, Text: delta})
	}

	if delta, ok := raw["delta"].(string); ok && (strings.Contains(typeName, "text.delta") || strings.Contains(typeName, "output_text.delta")) {
		if err := state.startBlock(ctx, events, llm.BlockKindText); err != nil {
			return err
		}
		state.text.WriteString(delta)
		return sendEvent(ctx, events, llm.TextDelta{Index: state.blockIndex, Text: delta})
	}

	if !strings.Contains(typeName, "reasoning") && (strings.Contains(typeName, "output_text.done") || strings.Contains(typeName, "text.done")) {
		return state.finishText(ctx, events)
	}

	if strings.Contains(typeName, "reasoning_summary") && strings.Contains(typeName, "done") {
		if strings.Contains(typeName, "summary_text.done") && state.thinking.Len() > 0 {
			state.thinkingSeparatorPending = true
		}
		return nil
	}

	if strings.Contains(typeName, "reasoning") && strings.Contains(typeName, "done") {
		return state.finishThinking(ctx, events)
	}

	if strings.Contains(typeName, "function_call_arguments.delta") {
		if err := state.finishOpenBlock(ctx, events); err != nil {
			return err
		}
		call := state.callForRaw(raw)
		call.Arguments += stringField(raw, "delta")
		return nil
	}

	if strings.Contains(typeName, "function_call_arguments.done") {
		if err := state.finishOpenBlock(ctx, events); err != nil {
			return err
		}
		call := state.callForRaw(raw)
		if arguments := stringField(raw, "arguments"); arguments != "" {
			call.Arguments = arguments
		}
		call.ArgumentsDone = true
		return emitToolCallIfReady(ctx, state, call, events)
	}

	if item, ok := raw["item"].(map[string]any); ok {
		if itemType, _ := item["type"].(string); itemType == "function_call" {
			if err := state.finishOpenBlock(ctx, events); err != nil {
				return err
			}
			call := state.callForRaw(raw)
			mergeCallItem(call, item)
			if strings.Contains(typeName, "output_item.done") {
				call.ArgumentsDone = true
			}
			return emitToolCallIfReady(ctx, state, call, events)
		}
	}

	if strings.Contains(typeName, "completed") || typeName == "response.completed" {
		if err := state.finishOpenBlock(ctx, events); err != nil {
			return err
		}
		state.finished = true
		return sendEvent(ctx, events, llm.PredictionFinished{
			Usage:      usageFromRaw(raw),
			StopReason: state.stopReason(),
		})
	}

	return nil
}

func (state *streamState) startBlock(ctx context.Context, events chan<- llm.PredictionEvent, kind llm.BlockKind) error {
	if state.blockStarted {
		if state.blockKind == kind {
			return nil
		}
		if err := state.finishOpenBlock(ctx, events); err != nil {
			return err
		}
	}
	state.blockStarted = true
	state.blockKind = kind
	return sendEvent(ctx, events, llm.BlockStarted{Index: state.blockIndex, Kind: kind})
}

func (state *streamState) finishOpenBlock(ctx context.Context, events chan<- llm.PredictionEvent) error {
	if state.text.Len() > 0 {
		return state.finishText(ctx, events)
	}
	if state.thinking.Len() > 0 {
		return state.finishThinking(ctx, events)
	}
	return nil
}

func (state *streamState) finishText(ctx context.Context, events chan<- llm.PredictionEvent) error {
	if state.text.Len() == 0 {
		return nil
	}
	block := llm.TextBlock{Text: state.text.String()}
	state.text.Reset()
	state.blockStarted = false
	state.blockKind = ""
	if err := sendEvent(ctx, events, llm.BlockEnded{Index: state.blockIndex, Block: block}); err != nil {
		return err
	}
	state.blockIndex++
	return nil
}

func (state *streamState) finishThinking(ctx context.Context, events chan<- llm.PredictionEvent) error {
	if state.thinking.Len() == 0 {
		return nil
	}
	block := llm.ThinkingBlock{Text: state.thinking.String(), Provider: state.provider}
	state.thinking.Reset()
	state.thinkingSeparatorPending = false
	state.blockStarted = false
	state.blockKind = ""
	if err := sendEvent(ctx, events, llm.BlockEnded{Index: state.blockIndex, Block: block}); err != nil {
		return err
	}
	state.blockIndex++
	return nil
}

func emitToolCallIfReady(ctx context.Context, state *streamState, call *partialCall, events chan<- llm.PredictionEvent) error {
	if call.Emitted || call.ID == "" || call.Name == "" || !call.ArgumentsDone {
		return nil
	}
	toolCall, err := call.toToolCall()
	if err != nil {
		return err
	}
	if err := sendEvent(ctx, events, llm.BlockStarted{Index: state.blockIndex, Kind: llm.BlockKindToolCall}); err != nil {
		return err
	}
	if call.Arguments != "" {
		if err := sendEvent(ctx, events, llm.ToolCallArgumentsDelta{Index: state.blockIndex, Arguments: call.Arguments}); err != nil {
			return err
		}
	}
	if err := sendEvent(ctx, events, llm.BlockEnded{Index: state.blockIndex, Block: toolCall}); err != nil {
		return err
	}
	state.blockIndex++
	call.Emitted = true
	state.toolEmitted = true
	return nil
}

func mergeCallItem(call *partialCall, item map[string]any) {
	if id := stringField(item, "call_id"); id != "" {
		call.ID = id
	}
	if call.ID == "" {
		call.ID = stringField(item, "id")
	}
	if name := stringField(item, "name"); name != "" {
		call.Name = name
	}
	if arguments := stringField(item, "arguments"); arguments != "" {
		call.Arguments = arguments
	}
}

func mergePartialCall(dst, src *partialCall) {
	if dst.ID == "" {
		dst.ID = src.ID
	}
	if dst.Name == "" {
		dst.Name = src.Name
	}
	if dst.Arguments == "" || (!dst.ArgumentsDone && src.ArgumentsDone) {
		dst.Arguments = src.Arguments
	}
	dst.ArgumentsDone = dst.ArgumentsDone || src.ArgumentsDone
	dst.Emitted = dst.Emitted || src.Emitted
}

func usageFromRaw(raw map[string]any) llm.Usage {
	usageMap, _ := raw["usage"].(map[string]any)
	if response, ok := raw["response"].(map[string]any); ok {
		if u, ok := response["usage"].(map[string]any); ok {
			usageMap = u
		}
	}
	inputTokenDetails, _ := usageMap["input_tokens_details"].(map[string]any)
	return llm.Usage{
		InputTokens:  intField(usageMap, "input_tokens"),
		OutputTokens: intField(usageMap, "output_tokens"),
		CachedTokens: intField(inputTokenDetails, "cached_tokens"),
		TotalTokens:  intField(usageMap, "total_tokens"),
	}
}

func rawArgumentsString(arguments json.RawMessage) (string, error) {
	if len(arguments) == 0 || strings.TrimSpace(string(arguments)) == "" {
		return "{}", nil
	}
	if !json.Valid(arguments) {
		return "", errors.New("invalid JSON")
	}
	return string(arguments), nil
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

func stringField(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return value
}

func intField(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

func hasNumberField(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	switch m[key].(type) {
	case float64, int:
		return true
	default:
		return false
	}
}
