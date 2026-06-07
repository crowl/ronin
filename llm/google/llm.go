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
	"net/url"
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
		"%s/models/%s:generateContent",
		strings.TrimRight(s.baseURL, "/"),
		url.PathEscape(s.model.Name),
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

	var structuredResp geminiStreamEvent
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
		"%s/models/%s:streamGenerateContent?alt=sse&",
		strings.TrimRight(s.baseURL, "/"),
		url.PathEscape(s.model.Name),
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
	state := geminiStreamState{}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := handleData(ctx, strings.Join(dataLines, "\n"), &state, events); err != nil {
				return err
			}
			dataLines = nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
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

	return sendEvent(ctx, events, llm.PredictionFinished{
		Usage:      state.usage,
		StopReason: state.stopReason(),
	})
}

func (s *LLM) buildPayload(req llm.PredictNextRequest) (*geminiRequest, error) {
	contents, err := convertMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	payload := &geminiRequest{
		Contents: contents,
	}

	if req.SystemPrompt != "" {
		payload.SystemInstruction = &geminiSystemInstruction{
			Parts: []geminiPart{{Text: req.SystemPrompt}},
		}
	}

	if len(req.Tools) > 0 {
		decls := make([]geminiFunctionDeclaration, 0, len(req.Tools))
		for _, tool := range req.Tools {
			decls = append(decls, geminiFunctionDeclaration{
				Name:                 tool.Name(),
				Description:          tool.Description(),
				ParametersJSONSchema: tool.Parameters(),
			})
		}
		payload.Tools = []geminiTool{{FunctionDeclarations: decls}}
	}

	generationConfig := geminiGenerationConfig{}
	if req.MaxTokens > 0 {
		generationConfig.MaxOutputTokens = req.MaxTokens
	}
	if reasoningLevel := s.ReasoningLevel(); reasoningLevel != llm.ReasoningLevelOff {
		generationConfig.ThinkingConfig = buildThinkingConfig(reasoningLevel)
	}
	if generationConfig.MaxOutputTokens > 0 || generationConfig.ThinkingConfig != nil {
		payload.GenerationConfig = &generationConfig
	}

	return payload, nil
}

func (s *LLM) buildStructuredPayload(req llm.PredictNextStructuredRequest) (*geminiRequest, error) {
	if req.Schema == nil {
		return nil, errors.New("structured output schema is required")
	}
	contents, err := convertMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	payload := &geminiRequest{
		Contents: contents,
	}

	if req.SystemPrompt != "" {
		payload.SystemInstruction = &geminiSystemInstruction{
			Parts: []geminiPart{{Text: req.SystemPrompt}},
		}
	}

	generationConfig := geminiGenerationConfig{
		ResponseMimeType:   "application/json",
		ResponseJSONSchema: req.Schema,
	}
	if req.MaxTokens > 0 {
		generationConfig.MaxOutputTokens = req.MaxTokens
	}
	if reasoningLevel := s.ReasoningLevel(); reasoningLevel != llm.ReasoningLevelOff {
		generationConfig.ThinkingConfig = buildThinkingConfig(reasoningLevel)
	}
	payload.GenerationConfig = &generationConfig

	return payload, nil
}

type geminiRequest struct {
	Contents          []geminiContent          `json:"contents"`
	SystemInstruction *geminiSystemInstruction `json:"systemInstruction,omitempty"`
	Tools             []geminiTool             `json:"tools,omitempty"`
	GenerationConfig  *geminiGenerationConfig  `json:"generationConfig,omitempty"`
}

type geminiSystemInstruction struct {
	Parts []geminiPart `json:"parts"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

type geminiFunctionDeclaration struct {
	Name                 string             `json:"name"`
	Description          string             `json:"description"`
	ParametersJSONSchema *jsonschema.Schema `json:"parametersJsonSchema"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens    int                   `json:"maxOutputTokens,omitempty"`
	ThinkingConfig     *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
	ResponseMimeType   string                `json:"responseMimeType,omitempty"`
	ResponseJSONSchema *jsonschema.Schema    `json:"responseJsonSchema,omitempty"`
}

type geminiThinkingConfig struct {
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
}

func buildThinkingConfig(reasoning llm.ReasoningLevel) *geminiThinkingConfig {
	if reasoning == llm.ReasoningLevelOff {
		budget := 0
		return &geminiThinkingConfig{ThinkingBudget: new(budget)}
	}

	level := strings.ToUpper(string(reasoning))
	if reasoning == llm.ReasoningLevelExtraHigh {
		level = "HIGH"
	}

	return &geminiThinkingConfig{
		ThinkingLevel:   level,
		IncludeThoughts: true,
	}
}

func convertMessages(messages []llm.Message) ([]geminiContent, error) {
	contents := make([]geminiContent, 0, len(messages))
	for _, msg := range messages {
		switch typedMsg := msg.(type) {
		case llm.UserMessage:
			contents = append(contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{
					{Text: typedMsg.Text},
				},
			})
		case llm.AssistantMessage:
			var parts []geminiPart
			for _, block := range typedMsg.Blocks {
				switch b := block.(type) {
				case llm.TextBlock:
					if b.Text != "" {
						parts = append(parts, geminiPart{Text: b.Text})
					}
				case llm.ThinkingBlock:
					continue
				case llm.ToolCallBlock:
					args := b.Arguments
					if len(args) == 0 || strings.TrimSpace(string(args)) == "" {
						args = json.RawMessage("{}")
					}
					if !json.Valid(args) {
						return nil, fmt.Errorf("tool call %q arguments: invalid JSON", b.ID)
					}
					part := geminiPart{
						FunctionCall: &geminiFunctionCall{
							Name: b.Name,
							Args: args,
						},
					}
					if b.ThoughtSignature != "" {
						part.ThoughtSignature = b.ThoughtSignature
					}
					parts = append(parts, part)
				default:
					return nil, fmt.Errorf("unsupported assistant block %T", block)
				}
			}
			if len(parts) > 0 {
				contents = append(contents, geminiContent{
					Role:  "model",
					Parts: parts,
				})
			}
		case llm.ToolOutputMessage:
			contents = append(contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{
					{
						FunctionResponse: &geminiFunctionResponse{
							Name: typedMsg.ToolName,
							Response: geminiFunctionResponseData{
								Output: typedMsg.ToolOutput,
							},
						},
					},
				},
			})
		case llm.ToolErrorMessage:
			contents = append(contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{
					{
						FunctionResponse: &geminiFunctionResponse{
							Name: typedMsg.ToolName,
							Response: geminiFunctionResponseData{
								Error: errorText(typedMsg.Error),
							},
						},
					},
				},
			})
		case llm.ErrorMessage:
			contents = append(contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{
					{Text: "error: " + errorText(typedMsg.Error)},
				},
			})
		default:
			return nil, fmt.Errorf("unsupported message %T", msg)
		}
	}
	return contents, nil
}

type geminiStreamState struct {
	toolEmitted bool
	usage       llm.Usage
	callCounter uint64
	blockIndex  int
}

type geminiStreamEvent struct {
	Candidates    []geminiCandidate    `json:"candidates"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
}

type geminiCandidate struct {
	Content geminiContent `json:"content"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type geminiFunctionResponse struct {
	Name     string                     `json:"name"`
	Response geminiFunctionResponseData `json:"response"`
}

type geminiFunctionResponseData struct {
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type geminiUsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
}

func (event geminiStreamEvent) OutputText() string {
	var b strings.Builder
	for _, candidate := range event.Candidates {
		for _, part := range candidate.Content.Parts {
			if !part.Thought {
				b.WriteString(part.Text)
			}
		}
	}
	return b.String()
}

func (state geminiStreamState) stopReason() llm.StopReason {
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

	for _, candidate := range event.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				if part.Thought {
					if err := sendEvent(ctx, events, llm.BlockStarted{Index: state.blockIndex, Kind: llm.BlockKindThinking}); err != nil {
						return err
					}
					if err := sendEvent(ctx, events, llm.ThinkingDelta{Index: state.blockIndex, Text: part.Text}); err != nil {
						return err
					}
					if err := sendEvent(ctx, events, llm.BlockEnded{Index: state.blockIndex, Block: llm.ThinkingBlock{Text: part.Text, Signature: part.ThoughtSignature}}); err != nil {
						return err
					}
					state.blockIndex++
				} else {
					if err := sendEvent(ctx, events, llm.BlockStarted{Index: state.blockIndex, Kind: llm.BlockKindText}); err != nil {
						return err
					}
					if err := sendEvent(ctx, events, llm.TextDelta{Index: state.blockIndex, Text: part.Text}); err != nil {
						return err
					}
					if err := sendEvent(ctx, events, llm.BlockEnded{Index: state.blockIndex, Block: llm.TextBlock{Text: part.Text}}); err != nil {
						return err
					}
					state.blockIndex++
				}
			}
			if part.FunctionCall != nil {
				id := part.FunctionCall.ID
				if id == "" {
					state.callCounter++
					id = fmt.Sprintf("%s_%d", part.FunctionCall.Name, state.callCounter)
				}

				args := part.FunctionCall.Args
				if len(args) == 0 || strings.TrimSpace(string(args)) == "" {
					args = json.RawMessage("{}")
				}
				if !json.Valid(args) {
					return fmt.Errorf("gemini function call %q arguments: invalid JSON", part.FunctionCall.Name)
				}

				if err := sendEvent(ctx, events, llm.BlockStarted{Index: state.blockIndex, Kind: llm.BlockKindToolCall}); err != nil {
					return err
				}
				if err := sendEvent(ctx, events, llm.ToolCallArgumentsDelta{Index: state.blockIndex, Arguments: string(args)}); err != nil {
					return err
				}
				if err := sendEvent(ctx, events, llm.BlockEnded{
					Index: state.blockIndex,
					Block: llm.ToolCallBlock{
						ID:               id,
						Name:             part.FunctionCall.Name,
						Arguments:        args,
						ThoughtSignature: part.ThoughtSignature,
					},
				}); err != nil {
					return err
				}
				state.blockIndex++

				state.toolEmitted = true
			}
		}
	}

	if event.UsageMetadata != nil {
		state.usage = llm.Usage{
			InputTokens:  event.UsageMetadata.PromptTokenCount,
			OutputTokens: event.UsageMetadata.CandidatesTokenCount + event.UsageMetadata.ThoughtsTokenCount,
			CachedTokens: event.UsageMetadata.CachedContentTokenCount,
			TotalTokens:  event.UsageMetadata.TotalTokenCount,
		}
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
