package google

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
	defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"
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
		return nil, errors.New("GEMINI_API_KEY or GOOGLE_API_KEY is required")
	}
	if !llm.IsValidReasoningLevel(cfg.ReasoningLevel) {
		return nil, fmt.Errorf("reasoning level %v is not a valid level", cfg.ReasoningLevel)
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
		return fmt.Errorf("reasoning level %v is not a valid level", level)
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
		return nil, fmt.Errorf("marshal gemini structured request: %w", err)
	}

	endpoint := fmt.Sprintf(
		"%s/interactions",
		strings.TrimRight(s.baseURL, "/"),
	)

	resp, err := httpretry.Do(ctx, s.client, func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create gemini structured request: %w", err)
		}

		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("x-goog-api-key", s.apiKey)
		return httpReq, nil
	})
	if err != nil {
		return nil, fmt.Errorf("send gemini structured request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Error("failed to close gemini structured response", "error", err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, fmt.Errorf("gemini structured status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read gemini structured response: %w", err)
	}

	var structuredResp geminiInteractionResponse
	if err := json.Unmarshal(data, &structuredResp); err != nil {
		return nil, fmt.Errorf("parse gemini structured response: %w", err)
	}

	text := strings.TrimSpace(structuredResp.OutputText())
	if text == "" {
		return nil, errors.New("gemini structured response contained no output text")
	}
	if !json.Valid([]byte(text)) {
		return nil, errors.New("gemini structured response output is not valid JSON")
	}
	return json.RawMessage(text), nil
}

func (s *LLM) stream(ctx context.Context, req llm.PredictNextRequest, events chan<- llm.PredictionEvent) error {
	payload, err := s.buildPayload(req)
	if err != nil {
		return err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal gemini request: %w", err)
	}

	endpoint := fmt.Sprintf(
		"%s/interactions",
		strings.TrimRight(s.baseURL, "/"),
	)

	resp, err := httpretry.Do(ctx, s.client, func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create gemini request: %w", err)
		}

		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("x-goog-api-key", s.apiKey)
		return httpReq, nil
	})
	if err != nil {
		return fmt.Errorf("send gemini request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Error("failed to close gemini response", "error", err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return fmt.Errorf("gemini status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	if err := sendEvent(ctx, events, llm.PredictionStarted{}); err != nil {
		return err
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024), 10*1024*1024)

	var dataLines []string
	state := geminiStreamState{
		activeSteps: make(map[int]*geminiActiveStep),
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := handleData(ctx, strings.Join(dataLines, "\n"), &state, events); err != nil {
				return err
			}
			dataLines = nil
			if state.finished {
				break
			}
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
		if err := handleData(ctx, strings.Join(dataLines, "\n"), &state, events); err != nil {
			return err
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read gemini stream: %w", err)
	}

	if !state.finished {
		return errors.New("gemini stream ended before interaction completion")
	}
	return nil
}

func (s *LLM) buildPayload(req llm.PredictNextRequest) (*geminiInteractionRequest, error) {
	input, err := convertMessagesToSteps(req.Messages)
	if err != nil {
		return nil, err
	}

	payload := &geminiInteractionRequest{
		Model:             s.model.Name,
		Input:             input,
		SystemInstruction: req.SystemPrompt,
		Store:             false,
		Stream:            true,
	}

	if len(req.Tools) > 0 {
		decls := make([]geminiTool, 0, len(req.Tools))
		for _, tool := range req.Tools {
			decls = append(decls, geminiTool{
				Type:        "function",
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters:  tool.Parameters(),
			})
		}
		payload.Tools = decls
	}

	generationConfig := geminiGenerationConfig{}
	if req.MaxTokens > 0 {
		generationConfig.MaxOutputTokens = req.MaxTokens
	}
	if reasoningLevel := s.ReasoningLevel(); reasoningLevel != llm.ReasoningLevelOff && s.model.ReasoningMode != llm.ReasoningModeNone {
		generationConfig.ThinkingLevel = buildThinkingLevel(reasoningLevel)
		generationConfig.ThinkingSummaries = "auto"
	}
	if generationConfig.MaxOutputTokens > 0 || generationConfig.ThinkingLevel != "" {
		payload.GenerationConfig = &generationConfig
	}

	return payload, nil
}

func (s *LLM) buildStructuredPayload(req llm.PredictNextStructuredRequest) (*geminiInteractionRequest, error) {
	if req.Schema == nil {
		return nil, errors.New("structured output schema is required")
	}
	input, err := convertMessagesToSteps(req.Messages)
	if err != nil {
		return nil, err
	}

	payload := &geminiInteractionRequest{
		Model:             s.model.Name,
		Input:             input,
		SystemInstruction: req.SystemPrompt,
		Store:             false,
		ResponseFormat: []geminiResponseFormat{
			{
				Type:     "text",
				MimeType: "application/json",
				Schema:   req.Schema,
			},
		},
	}

	generationConfig := geminiGenerationConfig{}
	if req.MaxTokens > 0 {
		generationConfig.MaxOutputTokens = req.MaxTokens
	}
	if reasoningLevel := s.ReasoningLevel(); reasoningLevel != llm.ReasoningLevelOff && s.model.ReasoningMode != llm.ReasoningModeNone {
		generationConfig.ThinkingLevel = buildThinkingLevel(reasoningLevel)
		generationConfig.ThinkingSummaries = "auto"
	}
	if generationConfig.MaxOutputTokens > 0 || generationConfig.ThinkingLevel != "" {
		payload.GenerationConfig = &generationConfig
	}

	return payload, nil
}

type geminiInteractionRequest struct {
	Model             string                  `json:"model"`
	Input             []geminiStep            `json:"input"`
	SystemInstruction string                  `json:"system_instruction,omitempty"`
	Tools             []geminiTool            `json:"tools,omitempty"`
	ResponseFormat    []geminiResponseFormat  `json:"response_format,omitempty"`
	Stream            bool                    `json:"stream,omitempty"`
	Store             bool                    `json:"store"`
	GenerationConfig  *geminiGenerationConfig `json:"generation_config,omitempty"`
}

type geminiStep struct {
	Type      string          `json:"type"`
	Content   []geminiContent `json:"content,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Result    []geminiContent `json:"result,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Summary   []geminiContent `json:"summary,omitempty"`
}

type geminiContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type geminiTool struct {
	Type        string             `json:"type"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Parameters  *jsonschema.Schema `json:"parameters,omitempty"`
}

type geminiResponseFormat struct {
	Type     string             `json:"type"`
	MimeType string             `json:"mime_type,omitempty"`
	Schema   *jsonschema.Schema `json:"schema,omitempty"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens   int    `json:"max_output_tokens,omitempty"`
	ThinkingLevel     string `json:"thinking_level,omitempty"`
	ThinkingSummaries string `json:"thinking_summaries,omitempty"`
}

func buildThinkingLevel(reasoning llm.ReasoningLevel) string {
	if reasoning == llm.ReasoningLevelOff {
		return "minimal"
	}
	level := string(reasoning)
	if level == "xhigh" {
		return "high"
	}
	return level
}

func convertMessagesToSteps(messages []llm.Message) ([]geminiStep, error) {
	steps := make([]geminiStep, 0, len(messages))
	for _, msg := range messages {
		switch typedMsg := msg.(type) {
		case llm.UserMessage:
			steps = append(steps, geminiStep{
				Type: "user_input",
				Content: []geminiContent{
					{Type: "text", Text: typedMsg.Text},
				},
			})
		case llm.AssistantMessage:
			for _, block := range typedMsg.Blocks {
				switch b := block.(type) {
				case llm.TextBlock:
					if b.Text != "" {
						steps = append(steps, geminiStep{
							Type: "model_output",
							Content: []geminiContent{
								{Type: "text", Text: b.Text},
							},
						})
					}
				case llm.ThinkingBlock:
					step := geminiStep{
						Type: "thought",
					}
					if b.Signature != "" {
						step.Signature = b.Signature
					}
					if b.Text != "" {
						step.Summary = []geminiContent{
							{Type: "text", Text: b.Text},
						}
					}
					steps = append(steps, step)
				case llm.RedactedThinkingBlock:
					continue
				case llm.ToolCallBlock:
					args := b.Arguments
					if len(args) == 0 || strings.TrimSpace(string(args)) == "" {
						args = json.RawMessage("{}")
					}
					if !json.Valid(args) {
						return nil, fmt.Errorf("tool call %q arguments: invalid JSON", b.ID)
					}
					steps = append(steps, geminiStep{
						Type:      "function_call",
						ID:        b.ID,
						Name:      b.Name,
						Arguments: args,
					})
				default:
					return nil, fmt.Errorf("unsupported assistant block %T", block)
				}
			}
		case llm.WorkflowResultMessage:
			steps = append(steps, geminiStep{
				Type: "user_input",
				Content: []geminiContent{
					{Type: "text", Text: typedMsg.Text()},
				},
			})
		case llm.ToolOutputMessage:
			steps = append(steps, geminiStep{
				Type:   "function_result",
				Name:   typedMsg.ToolName,
				CallID: typedMsg.ToolCallID,
				Result: []geminiContent{
					{Type: "text", Text: typedMsg.ToolOutput},
				},
			})
		case llm.ToolErrorMessage:
			steps = append(steps, geminiStep{
				Type:    "function_result",
				Name:    typedMsg.ToolName,
				CallID:  typedMsg.ToolCallID,
				IsError: true,
				Result: []geminiContent{
					{Type: "text", Text: errorText(typedMsg.Error)},
				},
			})
		case llm.ErrorMessage:
			steps = append(steps, geminiStep{
				Type: "user_input",
				Content: []geminiContent{
					{Type: "text", Text: "error: " + errorText(typedMsg.Error)},
				},
			})
		default:
			return nil, fmt.Errorf("unsupported message %T", msg)
		}
	}
	return steps, nil
}

type geminiStreamState struct {
	toolEmitted bool
	usage       llm.Usage
	callCounter uint64
	blockIndex  int
	activeSteps map[int]*geminiActiveStep
	finished    bool
}

type geminiActiveStep struct {
	typeStr     string
	id          string
	name        string
	blockIndex  int
	accumulated strings.Builder
}

type geminiStreamEvent struct {
	Type        string                 `json:"event_type"`
	Index       int                    `json:"index"`
	Step        *geminiStepDetail      `json:"step,omitempty"`
	Delta       *geminiDeltaDetail     `json:"delta,omitempty"`
	Status      string                 `json:"status,omitempty"`
	Metadata    *geminiStreamMetadata  `json:"metadata,omitempty"`
	Interaction *geminiInteractionInfo `json:"interaction,omitempty"`
}

type geminiStepDetail struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Signature string `json:"signature,omitempty"`
}

type geminiDeltaDetail struct {
	Type             string         `json:"type"`
	Text             string         `json:"text,omitempty"`
	PartialArguments string         `json:"partial_arguments,omitempty"`
	Arguments        string         `json:"arguments,omitempty"`
	Signature        string         `json:"signature,omitempty"`
	Content          *geminiContent `json:"content,omitempty"`
}

type geminiStreamMetadata struct {
	TotalUsage *geminiUsage `json:"total_usage,omitempty"`
}

type geminiInteractionInfo struct {
	ID     string       `json:"id"`
	Status string       `json:"status"`
	Usage  *geminiUsage `json:"usage,omitempty"`
}

type geminiInteractionResponse struct {
	ID    string       `json:"id"`
	Steps []geminiStep `json:"steps"`
	Usage *geminiUsage `json:"usage"`
}

type geminiUsage struct {
	TotalCachedTokens  int `json:"total_cached_tokens"`
	TotalInputTokens   int `json:"total_input_tokens"`
	TotalOutputTokens  int `json:"total_output_tokens"`
	TotalThoughtTokens int `json:"total_thought_tokens"`
	TotalTokens        int `json:"total_tokens"`
}

func (resp geminiInteractionResponse) OutputText() string {
	var b strings.Builder
	for _, step := range resp.Steps {
		if step.Type == "model_output" {
			for _, content := range step.Content {
				if content.Type == "text" {
					b.WriteString(content.Text)
				}
			}
		}
	}
	return b.String()
}

func (state *geminiStreamState) stopReason() llm.StopReason {
	if state.toolEmitted {
		return llm.StopReasonToolUse
	}
	return llm.StopReasonFinished
}

func handleData(ctx context.Context, data string, state *geminiStreamState, events chan<- llm.PredictionEvent) error {
	if data == "" || data == "[DONE]" {
		return nil
	}

	var event geminiStreamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return fmt.Errorf("parse gemini event: %w", err)
	}

	// Handle token usage metadata updates
	if event.Metadata != nil && event.Metadata.TotalUsage != nil {
		state.usage = llm.Usage{
			InputTokens:  event.Metadata.TotalUsage.TotalInputTokens,
			OutputTokens: event.Metadata.TotalUsage.TotalOutputTokens + event.Metadata.TotalUsage.TotalThoughtTokens,
			CachedTokens: event.Metadata.TotalUsage.TotalCachedTokens,
			TotalTokens:  event.Metadata.TotalUsage.TotalTokens,
		}
	}
	if event.Interaction != nil && event.Interaction.Usage != nil {
		state.usage = llm.Usage{
			InputTokens:  event.Interaction.Usage.TotalInputTokens,
			OutputTokens: event.Interaction.Usage.TotalOutputTokens + event.Interaction.Usage.TotalThoughtTokens,
			CachedTokens: event.Interaction.Usage.TotalCachedTokens,
			TotalTokens:  event.Interaction.Usage.TotalTokens,
		}
	}

	switch event.Type {
	case "interaction.completed", "interaction.requires_action":
		state.finished = true
		return sendEvent(ctx, events, llm.PredictionFinished{
			Usage:      state.usage,
			StopReason: state.stopReason(),
		})

	case "step.start":
		if event.Step == nil {
			return nil
		}
		blockIndex := state.blockIndex
		state.blockIndex++

		var kind llm.BlockKind
		switch event.Step.Type {
		case "thought":
			kind = llm.BlockKindThinking
		case "model_output":
			kind = llm.BlockKindText
		case "function_call":
			kind = llm.BlockKindToolCall
			state.toolEmitted = true
		default:
			// ignore unknown step types
			return nil
		}

		active := &geminiActiveStep{
			typeStr:    event.Step.Type,
			id:         event.Step.ID,
			name:       event.Step.Name,
			blockIndex: blockIndex,
		}
		if state.activeSteps == nil {
			state.activeSteps = make(map[int]*geminiActiveStep)
		}
		state.activeSteps[event.Index] = active

		if err := sendEvent(ctx, events, llm.BlockStarted{Index: blockIndex, Kind: kind}); err != nil {
			return err
		}

	case "step.delta":
		if event.Delta == nil {
			return nil
		}
		active, ok := state.activeSteps[event.Index]
		if !ok {
			return nil
		}

		switch event.Delta.Type {
		case "thought", "thought_summary":
			textVal := event.Delta.Text
			if event.Delta.Content != nil {
				textVal = event.Delta.Content.Text
			}
			active.accumulated.WriteString(textVal)
			if err := sendEvent(ctx, events, llm.ThinkingDelta{Index: active.blockIndex, Text: textVal}); err != nil {
				return err
			}
		case "thought_signature":
			if event.Delta.Signature != "" {
				active.id = event.Delta.Signature
			}
		case "text":
			active.accumulated.WriteString(event.Delta.Text)
			if err := sendEvent(ctx, events, llm.TextDelta{Index: active.blockIndex, Text: event.Delta.Text}); err != nil {
				return err
			}
		case "arguments", "arguments_delta":
			argsVal := event.Delta.Arguments
			if argsVal == "" {
				argsVal = event.Delta.PartialArguments
			}
			active.accumulated.WriteString(argsVal)
			if err := sendEvent(ctx, events, llm.ToolCallArgumentsDelta{Index: active.blockIndex, Arguments: argsVal}); err != nil {
				return err
			}
		}

	case "step.stop":
		active, ok := state.activeSteps[event.Index]
		if !ok {
			return nil
		}

		var block llm.AssistantBlock
		switch active.typeStr {
		case "thought":
			block = llm.ThinkingBlock{
				Text:      active.accumulated.String(),
				Signature: active.id,
				Provider:  "google",
			}
		case "model_output":
			block = llm.TextBlock{
				Text: active.accumulated.String(),
			}
		case "function_call":
			id := active.id
			if id == "" {
				state.callCounter++
				id = fmt.Sprintf("%s_%d", active.name, state.callCounter)
			}
			args := []byte(active.accumulated.String())
			if len(args) == 0 || strings.TrimSpace(string(args)) == "" {
				args = []byte("{}")
			}
			if !json.Valid(args) {
				return fmt.Errorf("gemini function call %q arguments: invalid JSON", active.name)
			}
			block = llm.ToolCallBlock{
				ID:        id,
				Name:      active.name,
				Arguments: json.RawMessage(args),
			}
		default:
			return nil
		}

		if err := sendEvent(ctx, events, llm.BlockEnded{Index: active.blockIndex, Block: block}); err != nil {
			return err
		}
		delete(state.activeSteps, event.Index)
	}

	return nil
}

func sendEvent(ctx context.Context, events chan<- llm.PredictionEvent, event llm.PredictionEvent) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case events <- event:
		return nil
	}
}

func errorText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}
