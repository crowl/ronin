// Package antigravity speaks Google's Cloud Code Assist protocol, the backend
// behind the Antigravity CLI. It is a distinct wire format from the public
// Gemini Interactions API served by the google package: requests are classic
// GenerateContent payloads wrapped in a Code Assist envelope, and reasoning
// effort selects a backend model rather than a request field.
//
// Authentication is a bearer token. Behind the Limes proxy the configured key
// is a placeholder that the proxy replaces with the local Antigravity login,
// and the proxy also supplies the Cloud Code Assist project.
package antigravity

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/crowl/ronin/llm/internal/streamretry"
)

const (
	defaultBaseURL  = "https://cloudcode-pa.googleapis.com"
	streamPath      = "/v1internal:streamGenerateContent?alt=sse"
	generatePath    = "/v1internal:generateContent"
	envelopeAgent   = "antigravity"
	envelopeRequest = "agent"
	maxResponseSize = 10 * 1024 * 1024
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
		return nil, errors.New("ANTIGRAVITY_API_KEY is required")
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
	client, err := httpretry.NewClient(cfg.Client)
	if err != nil {
		return nil, err
	}
	return &LLM{
		baseURL:        strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:         cfg.APIKey,
		model:          cfg.Model,
		reasoningLevel: cfg.ReasoningLevel,
		client:         client,
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
	return streamretry.Predict(ctx, 32, func(attemptCtx context.Context, events chan<- llm.PredictionEvent) error {
		return s.stream(attemptCtx, req, events)
	})
}

func (s *LLM) PredictNextStructured(ctx context.Context, req llm.PredictNextStructuredRequest) (*llm.StructuredResult, error) {
	if req.Schema == nil {
		return nil, errors.New("structured output schema is required")
	}
	contents, err := convertMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	payload := s.buildEnvelope(contents, req.SystemPrompt, "", nil, req.MaxTokens)
	payload.Request.GenerationConfig = withSchema(payload.Request.GenerationConfig, req.Schema)

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal antigravity structured request: %w", err)
	}
	return streamretry.PredictStructured(ctx, func(attemptCtx context.Context) (*llm.StructuredResult, error) {
		return s.predictStructuredAttempt(attemptCtx, body)
	})
}

func (s *LLM) predictStructuredAttempt(ctx context.Context, body []byte) (*llm.StructuredResult, error) {
	resp, err := s.post(ctx, s.baseURL+generatePath, body, "application/json")
	if err != nil {
		return nil, fmt.Errorf("send antigravity structured request: %w", err)
	}
	defer closeBody(resp, "structured")

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, llm.HTTPError(providerLabel, resp.StatusCode, string(data))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("read antigravity structured response: %w", err)
	}
	var chunk streamChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return nil, fmt.Errorf("parse antigravity structured response: %w", err)
	}
	reply := chunk.reply()

	result := &llm.StructuredResult{}
	if usage := reply.UsageMetadata; usage != nil {
		converted := convertUsage(*usage)
		converted.Cost = llm.EstimateCost(s.model, converted)
		result.Usage = &converted
	}
	text := strings.TrimSpace(outputText(reply))
	if text == "" {
		return result, errors.New("antigravity structured response contained no output text")
	}
	if !json.Valid([]byte(text)) {
		return result, errors.New("antigravity structured response output is not valid JSON")
	}
	result.JSON = json.RawMessage(text)
	return result, nil
}

func (s *LLM) stream(ctx context.Context, req llm.PredictNextRequest, events chan<- llm.PredictionEvent) error {
	contents, err := convertMessages(req.Messages)
	if err != nil {
		return err
	}
	payload := s.buildEnvelope(contents, req.SystemPrompt, req.CacheKey, req.Tools, req.MaxTokens)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal antigravity request: %w", err)
	}

	resp, err := s.post(ctx, s.baseURL+streamPath, body, "text/event-stream")
	if err != nil {
		return fmt.Errorf("send antigravity request: %w", err)
	}
	defer closeBody(resp, "stream")

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return llm.HTTPError(providerLabel, resp.StatusCode, string(data))
	}
	if err := sendEvent(ctx, events, llm.PredictionStarted{}); err != nil {
		return err
	}

	state := &streamState{provider: s.model.Provider}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024), maxResponseSize)

	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if err := state.handleData(ctx, strings.Join(dataLines, "\n"), events); err != nil {
				return err
			}
			dataLines = nil
		case strings.HasPrefix(line, ":"):
		default:
			if after, found := strings.CutPrefix(line, "data:"); found {
				dataLines = append(dataLines, strings.TrimSpace(after))
			}
		}
	}
	if len(dataLines) > 0 {
		if err := state.handleData(ctx, strings.Join(dataLines, "\n"), events); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read antigravity stream: %w", err)
	}
	if !state.finished {
		return fmt.Errorf("antigravity stream ended before completion: %w", io.ErrUnexpectedEOF)
	}
	if err := state.closeBlock(ctx, events); err != nil {
		return err
	}
	return sendEvent(ctx, events, llm.PredictionFinished{
		Usage:      state.usageWithCost(s.model),
		StopReason: state.stopReason(),
	})
}

func (s *LLM) post(ctx context.Context, endpoint string, body []byte, accept string) (*http.Response, error) {
	return httpretry.Do(ctx, s.client, func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create antigravity request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", accept)
		// Cloud Code Assist authenticates with a bearer token, not an API key.
		// Behind Limes this value is a placeholder the proxy replaces.
		httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)
		return httpReq, nil
	})
}

func (s *LLM) buildEnvelope(contents []content, systemPrompt, cacheKey string, tools []llm.Tool, maxTokens int) *envelope {
	level := s.ReasoningLevel()
	runtime := runtimeModel(s.model.Name, level)

	request := generateRequest{Contents: contents, SessionID: cacheKey}
	if systemPrompt != "" {
		request.SystemInstruction = &content{Role: "user", Parts: []part{{Text: systemPrompt}}}
	}
	if len(tools) > 0 {
		declarations := make([]functionDeclaration, 0, len(tools))
		for _, tool := range tools {
			declarations = append(declarations, functionDeclaration{
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters:  tool.Parameters(),
			})
		}
		request.Tools = []toolDeclaration{{FunctionDeclarations: declarations}}
	}

	config := generationConfig{MaxOutputTokens: maxTokens}
	if s.model.ReasoningMode != llm.ReasoningModeNone {
		config.ThinkingConfig = thinkingFor(runtime, level)
	}
	if config != (generationConfig{}) {
		request.GenerationConfig = &config
	}

	return &envelope{
		Model:       runtime,
		RequestType: envelopeRequest,
		UserAgent:   envelopeAgent,
		RequestID:   newRequestID(),
		Request:     request,
	}
}

func withSchema(config *generationConfig, schema *jsonschema.Schema) *generationConfig {
	if config == nil {
		config = &generationConfig{}
	}
	config.ResponseMimeType = "application/json"
	config.ResponseJSONSchema = schema
	return config
}

func convertUsage(usage usageMetadata) llm.Usage {
	return llm.Usage{
		InputTokens:  usage.PromptTokenCount,
		OutputTokens: usage.CandidatesTokenCount + usage.ThoughtsTokenCount,
		CachedTokens: usage.CachedContentTokenCount,
		TotalTokens:  usage.TotalTokenCount,
	}
}

func outputText(reply generateResponse) string {
	var text strings.Builder
	for _, candidate := range reply.Candidates {
		for _, piece := range candidate.Content.Parts {
			if !piece.Thought {
				text.WriteString(piece.Text)
			}
		}
	}
	return text.String()
}

// newRequestID produces the opaque per-request identifier the envelope
// carries. It only has to be unique, so a random v4 UUID avoids pulling in a
// dependency for it.
func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	digits := hex.EncodeToString(raw[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", digits[0:8], digits[8:12], digits[12:16], digits[16:20], digits[20:])
}

func sendEvent(ctx context.Context, events chan<- llm.PredictionEvent, event llm.PredictionEvent) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case events <- event:
		return nil
	}
}

func closeBody(resp *http.Response, kind string) {
	if err := resp.Body.Close(); err != nil {
		slog.Error("failed to close antigravity response", "kind", kind, "error", err)
	}
}

func errorText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}
