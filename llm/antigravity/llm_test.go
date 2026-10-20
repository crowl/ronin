package antigravity_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/antigravity"
)

func TestRuntimeModelRoutesReasoningLevelToBackendModel(t *testing.T) {
	// Antigravity selects effort by model id, so the level must change the
	// envelope's model rather than a request field.
	for _, test := range []struct {
		model string
		level llm.ReasoningLevel
		want  string
	}{
		{"gemini-3.1-pro", llm.ReasoningLevelLow, "gemini-3.1-pro-low"},
		{"gemini-3.1-pro", llm.ReasoningLevelHigh, "gemini-pro-agent"},
		{"gemini-3.1-pro", llm.ReasoningLevelExtraHigh, "gemini-pro-agent"},
		{"gemini-3.8-flash", llm.ReasoningLevelOff, "gemini-3.8-flash-low"},
		{"gemini-3.8-flash", llm.ReasoningLevelMedium, "gemini-3.8-flash-medium"},
		{"gemini-3.8-flash", llm.ReasoningLevelHigh, "gemini-3.8-flash-high"},
		{"claude-opus-4-6", llm.ReasoningLevelLow, "claude-opus-4-6-thinking"},
		// An unlisted model passes through, so a newly entitled model works
		// before the routing table knows about it.
		{"gemini-4-argon", llm.ReasoningLevelHigh, "gemini-4-argon"},
	} {
		t.Run(test.model+"/"+string(test.level), func(t *testing.T) {
			payload := captureRequest(t, modelNamed(test.model), test.level, llm.PredictNextRequest{})
			if payload.Model != test.want {
				t.Fatalf("model = %q, want %q", payload.Model, test.want)
			}
		})
	}
}

func TestThinkingBudgetMatchesTheModelFamily(t *testing.T) {
	// The backend rejects out-of-range budgets outright, and the Gemini 4
	// family additionally rejects a zero budget, so "off" must omit the
	// config there instead of sending zero.
	for _, test := range []struct {
		name            string
		model           string
		level           llm.ReasoningLevel
		wantAbsent      bool
		wantBudget      int
		wantIncludeFlag bool
	}{
		{"pro off sends zero", "gemini-3.1-pro", llm.ReasoningLevelOff, false, 0, false},
		{"pro high", "gemini-3.1-pro", llm.ReasoningLevelHigh, false, 10001, true},
		{"pro low", "gemini-3.1-pro", llm.ReasoningLevelLow, false, 1001, true},
		{"flash medium", "gemini-3.8-flash", llm.ReasoningLevelMedium, false, 4000, true},
		{"flash high is dynamic", "gemini-3.8-flash", llm.ReasoningLevelHigh, false, -1, true},
		{"argon off omits config", "gemini-4-argon", llm.ReasoningLevelOff, true, 0, false},
		{"argon low", "gemini-4-argon", llm.ReasoningLevelLow, false, 1000, true},
		{"claude carries no budget", "claude-opus-4-6", llm.ReasoningLevelHigh, true, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := captureRequest(t, modelNamed(test.model), test.level, llm.PredictNextRequest{})
			thinking := payload.Request.GenerationConfig.ThinkingConfig
			if test.wantAbsent {
				if thinking != nil {
					t.Fatalf("thinkingConfig = %#v, want absent", thinking)
				}
				return
			}
			if thinking == nil {
				t.Fatal("thinkingConfig is absent")
			}
			if thinking.ThinkingBudget != test.wantBudget || thinking.IncludeThoughts != test.wantIncludeFlag {
				t.Fatalf("thinkingConfig = %#v", thinking)
			}
		})
	}
}

func TestBuildEnvelopeOmitsProjectAndNamesTheSession(t *testing.T) {
	payload := captureRequest(t, modelNamed("gemini-3.1-pro"), llm.ReasoningLevelLow, llm.PredictNextRequest{
		CacheKey:     "session-1",
		SystemPrompt: "be brief",
		MaxTokens:    256,
	})
	// The Limes proxy owns the credential and injects the project.
	if _, present := payload.raw["project"]; present {
		t.Fatalf("envelope carries a project: %v", payload.raw)
	}
	if payload.RequestType != "agent" || payload.UserAgent != "antigravity" || payload.RequestID == "" {
		t.Fatalf("envelope = %#v", payload)
	}
	if payload.Request.SessionID != "session-1" {
		t.Fatalf("sessionId = %q", payload.Request.SessionID)
	}
	if payload.Request.SystemInstruction == nil || payload.Request.SystemInstruction.Parts[0].Text != "be brief" {
		t.Fatalf("systemInstruction = %#v", payload.Request.SystemInstruction)
	}
	if payload.Request.GenerationConfig.MaxOutputTokens != 256 {
		t.Fatalf("maxOutputTokens = %d", payload.Request.GenerationConfig.MaxOutputTokens)
	}
}

func TestConvertMessagesBuildsGeminiContents(t *testing.T) {
	payload := captureRequest(t, modelNamed("gemini-3.1-pro"), llm.ReasoningLevelLow, llm.PredictNextRequest{
		Messages: []llm.Message{
			llm.UserMessage{Text: "hello"},
			llm.AssistantMessage{Blocks: []llm.AssistantBlock{
				llm.ThinkingBlock{Text: "pondering", Signature: "sig-1"},
				llm.TextBlock{Text: "sure"},
				llm.ToolCallBlock{ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"q":1}`), ThoughtSignature: "sig-2"},
			}},
			llm.ToolOutputMessage{ToolName: "lookup", ToolCallID: "call-1", ToolOutput: "result"},
			llm.ToolErrorMessage{ToolName: "lookup", ToolCallID: "call-2", Error: errors.New("boom")},
		},
		Tools: []llm.Tool{stubTool{}},
	})

	contents := payload.Request.Contents
	if len(contents) != 3 {
		t.Fatalf("contents = %d, want 3 merged turns: %#v", len(contents), contents)
	}
	if contents[0].Role != "user" || contents[0].Parts[0].Text != "hello" {
		t.Fatalf("user turn = %#v", contents[0])
	}

	model := contents[1]
	if model.Role != "model" || len(model.Parts) != 3 {
		t.Fatalf("model turn = %#v", model)
	}
	if !model.Parts[0].Thought || model.Parts[0].ThoughtSignature != "sig-1" {
		t.Fatalf("thought part = %#v", model.Parts[0])
	}
	if model.Parts[2].FunctionCall == nil || model.Parts[2].FunctionCall.Name != "lookup" || model.Parts[2].ThoughtSignature != "sig-2" {
		t.Fatalf("function call part = %#v", model.Parts[2])
	}

	// Consecutive tool results merge into a single user turn.
	results := contents[2]
	if results.Role != "user" || len(results.Parts) != 2 {
		t.Fatalf("tool result turn = %#v", results)
	}
	if results.Parts[0].FunctionResponse.Response["output"] != "result" {
		t.Fatalf("tool output = %#v", results.Parts[0].FunctionResponse)
	}
	if results.Parts[1].FunctionResponse.Response["error"] != "boom" {
		t.Fatalf("tool error = %#v", results.Parts[1].FunctionResponse)
	}

	if len(payload.Request.Tools) != 1 || payload.Request.Tools[0].FunctionDeclarations[0].Name != "lookup" {
		t.Fatalf("tools = %#v", payload.Request.Tools)
	}
}

func TestPredictNextStreamsBlocksFromCandidateParts(t *testing.T) {
	stream := sse(
		`{"response":{"candidates":[{"content":{"parts":[{"text":"think","thought":true}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"ing","thought":true,"thoughtSignature":"sig"}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"hel"},{"text":"lo"}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"id":"call-1","name":"lookup","args":{"q":1}},"thoughtSignature":"tsig"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"thoughtsTokenCount":5,"cachedContentTokenCount":2,"totalTokenCount":14}}}`,
	)
	events, err := predictWithStream(t, stream)
	if err != nil {
		t.Fatalf("predict: %v", err)
	}

	if got := kinds(events); !reflect.DeepEqual(got, []llm.BlockKind{llm.BlockKindThinking, llm.BlockKindText, llm.BlockKindToolCall}) {
		t.Fatalf("block kinds = %v", got)
	}
	blocks := endedBlocks(events)
	thinking, ok := blocks[0].(llm.ThinkingBlock)
	if !ok || thinking.Text != "thinking" || thinking.Signature != "sig" || thinking.Provider != "antigravity" {
		t.Fatalf("thinking block = %#v", blocks[0])
	}
	text, ok := blocks[1].(llm.TextBlock)
	if !ok || text.Text != "hello" {
		t.Fatalf("text block = %#v", blocks[1])
	}
	call, ok := blocks[2].(llm.ToolCallBlock)
	if !ok || call.ID != "call-1" || call.Name != "lookup" || string(call.Arguments) != `{"q":1}` {
		t.Fatalf("tool call block = %#v", blocks[2])
	}
	if call.ThoughtSignature != "tsig" || call.ThoughtProvider != "antigravity" {
		t.Fatalf("tool call signature = %#v", call)
	}

	finished := lastFinished(t, events)
	if finished.StopReason != llm.StopReasonToolUse {
		t.Fatalf("stop reason = %q", finished.StopReason)
	}
	want := llm.Usage{InputTokens: 3, OutputTokens: 9, CachedTokens: 2, TotalTokens: 14}
	finished.Usage.Cost = llm.Cost{}
	if finished.Usage != want {
		t.Fatalf("usage = %#v, want %#v", finished.Usage, want)
	}
}

func TestPredictNextMapsFinishReasons(t *testing.T) {
	for reason, want := range map[string]llm.StopReason{
		"STOP":                 llm.StopReasonFinished,
		"MAX_TOKENS":           llm.StopReasonMaxTokens,
		"SAFETY":               llm.StopReasonRefusal,
		"PROHIBITED_CONTENT":   llm.StopReasonRefusal,
		"SOMETHING_UNEXPECTED": llm.StopReasonFinished,
	} {
		t.Run(reason, func(t *testing.T) {
			stream := sse(`{"response":{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"` + reason + `"}]}}`)
			events, err := predictWithStream(t, stream)
			if err != nil {
				t.Fatalf("predict: %v", err)
			}
			if got := lastFinished(t, events).StopReason; got != want {
				t.Fatalf("stop reason = %q, want %q", got, want)
			}
		})
	}
}

func TestPredictNextAcceptsUnwrappedChunks(t *testing.T) {
	// Some Cloud Code Assist endpoints omit the "response" wrapper.
	stream := sse(`{"candidates":[{"content":{"parts":[{"text":"bare"}]},"finishReason":"STOP"}]}`)
	events, err := predictWithStream(t, stream)
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	block, ok := endedBlocks(events)[0].(llm.TextBlock)
	if !ok || block.Text != "bare" {
		t.Fatalf("block = %#v", endedBlocks(events)[0])
	}
}

func TestPredictNextReportsTruncatedStream(t *testing.T) {
	stream := sse(`{"response":{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}}`)
	if _, err := predictWithStream(t, stream); err == nil || !strings.Contains(err.Error(), "ended before completion") {
		t.Fatalf("error = %v, want truncation error", err)
	}
}

func TestPredictNextSynthesizesMissingToolCallIDs(t *testing.T) {
	stream := sse(
		`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup"}},{"functionCall":{"name":"lookup"}}]},"finishReason":"STOP"}]}}`,
	)
	events, err := predictWithStream(t, stream)
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	blocks := endedBlocks(events)
	first, second := blocks[0].(llm.ToolCallBlock), blocks[1].(llm.ToolCallBlock)
	if first.ID != "lookup_1" || second.ID != "lookup_2" {
		t.Fatalf("ids = %q, %q", first.ID, second.ID)
	}
	if string(first.Arguments) != "{}" {
		t.Fatalf("empty arguments = %q, want {}", first.Arguments)
	}
}

func TestRequestAuthenticatesWithBearerToken(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		if request.URL.Path != "/v1internal:streamGenerateContent" || request.URL.RawQuery != "alt=sse" {
			t.Errorf("request URL = %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		_, _ = io.WriteString(w, sse(`{"response":{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"STOP"}]}}`))
	}))
	defer server.Close()

	client := newClient(t, server.URL, modelNamed("gemini-3.1-pro"), llm.ReasoningLevelLow)
	events, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{})
	drainEvents(events)
	if err := <-errs; err != nil {
		t.Fatalf("predict: %v", err)
	}
	if authorization != "Bearer key" {
		t.Fatalf("authorization = %q", authorization)
	}
}

func TestPredictNextStructuredRequiresValidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1internal:generateContent" {
			t.Errorf("path = %s", request.URL.Path)
		}
		_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"parts":[{"text":"not json"}]}}],"usageMetadata":{"promptTokenCount":1,"totalTokenCount":2}}}`)
	}))
	defer server.Close()

	client := newClient(t, server.URL, modelNamed("gemini-3.1-pro"), llm.ReasoningLevelLow)
	result, err := client.PredictNextStructured(context.Background(), llm.PredictNextStructuredRequest{
		Schema: &jsonschema.Schema{Type: "object"},
	})
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("error = %v", err)
	}
	// Usage survives a validation failure so the request is still accounted.
	if result == nil || result.Usage == nil || result.Usage.InputTokens != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestSetReasoningLevelValidates(t *testing.T) {
	client := newClient(t, "https://example.test", modelNamed("gemini-3.1-pro"), llm.ReasoningLevelLow)
	if err := client.SetReasoningLevel("invalid"); err == nil || !strings.Contains(err.Error(), "not a valid level") {
		t.Fatalf("error = %v", err)
	}
	if client.ReasoningLevel() != llm.ReasoningLevelLow {
		t.Fatalf("level changed to %q", client.ReasoningLevel())
	}
}

func TestNewLLMRequiresCredential(t *testing.T) {
	_, err := antigravity.NewLLM(antigravity.LLMConfig{Model: modelNamed("gemini-3.1-pro"), ReasoningLevel: llm.ReasoningLevelLow})
	if err == nil || !strings.Contains(err.Error(), "ANTIGRAVITY_API_KEY") {
		t.Fatalf("error = %v", err)
	}
}

// --- helpers ---

func modelNamed(name string) llm.Model {
	return llm.Model{
		Provider:           "antigravity",
		Name:               name,
		ContextWindow:      1048576,
		ReasoningMode:      llm.ReasoningModeEffort,
		SupportedReasoning: llm.NewReasoningSet(llm.ReasoningLevels()...),
	}
}

func newClient(t *testing.T, baseURL string, model llm.Model, level llm.ReasoningLevel) *antigravity.LLM {
	t.Helper()
	client, err := antigravity.NewLLM(antigravity.LLMConfig{
		BaseURL:        baseURL,
		APIKey:         "key",
		Model:          model,
		ReasoningLevel: level,
	})
	if err != nil {
		t.Fatalf("new llm: %v", err)
	}
	return client
}

// capturedEnvelope mirrors the wire shape so tests assert on JSON actually
// sent rather than on internal structs.
type capturedEnvelope struct {
	Model       string `json:"model"`
	RequestType string `json:"requestType"`
	UserAgent   string `json:"userAgent"`
	RequestID   string `json:"requestId"`
	Request     struct {
		SystemInstruction *capturedContent `json:"systemInstruction"`
		Contents          []capturedContent
		SessionID         string `json:"sessionId"`
		Tools             []struct {
			FunctionDeclarations []struct {
				Name string `json:"name"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
		GenerationConfig capturedGenerationConfig `json:"generationConfig"`
	} `json:"request"`
	raw map[string]json.RawMessage
}

type capturedContent struct {
	Role  string         `json:"role"`
	Parts []capturedPart `json:"parts"`
}

type capturedPart struct {
	Text             string `json:"text"`
	Thought          bool   `json:"thought"`
	ThoughtSignature string `json:"thoughtSignature"`
	FunctionCall     *struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
	FunctionResponse *struct {
		Name     string            `json:"name"`
		Response map[string]string `json:"response"`
	} `json:"functionResponse"`
}

type capturedGenerationConfig struct {
	MaxOutputTokens int                     `json:"maxOutputTokens"`
	ThinkingConfig  *capturedThinkingConfig `json:"thinkingConfig"`
}

type capturedThinkingConfig struct {
	IncludeThoughts bool `json:"includeThoughts"`
	ThinkingBudget  int  `json:"thinkingBudget"`
}

func captureRequest(t *testing.T, model llm.Model, level llm.ReasoningLevel, req llm.PredictNextRequest) capturedEnvelope {
	t.Helper()
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ = io.ReadAll(request.Body)
		_, _ = io.WriteString(w, sse(`{"response":{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"STOP"}]}}`))
	}))
	defer server.Close()

	client := newClient(t, server.URL, model, level)
	events, errs := client.PredictNext(context.Background(), req)
	drainEvents(events)
	if err := <-errs; err != nil {
		t.Fatalf("predict: %v", err)
	}

	var captured capturedEnvelope
	if err := json.Unmarshal(body, &captured); err != nil {
		t.Fatalf("parse request: %v (%s)", err, body)
	}
	if err := json.Unmarshal(body, &captured.raw); err != nil {
		t.Fatalf("parse request: %v", err)
	}
	return captured
}

func predictWithStream(t *testing.T, stream string) ([]llm.PredictionEvent, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer server.Close()

	client := newClient(t, server.URL, modelNamed("gemini-3.1-pro"), llm.ReasoningLevelLow)
	events, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{})
	drained := drainEvents(events)
	return drained, <-errs
}

func sse(payloads ...string) string {
	var builder strings.Builder
	for _, payload := range payloads {
		builder.WriteString("data: " + payload + "\n\n")
	}
	return builder.String()
}

func drainEvents(events <-chan llm.PredictionEvent) []llm.PredictionEvent {
	var drained []llm.PredictionEvent
	for event := range events {
		drained = append(drained, event)
	}
	return drained
}

func kinds(events []llm.PredictionEvent) []llm.BlockKind {
	var found []llm.BlockKind
	for _, event := range events {
		if started, ok := event.(llm.BlockStarted); ok {
			found = append(found, started.Kind)
		}
	}
	return found
}

func endedBlocks(events []llm.PredictionEvent) []llm.AssistantBlock {
	var blocks []llm.AssistantBlock
	for _, event := range events {
		if ended, ok := event.(llm.BlockEnded); ok {
			blocks = append(blocks, ended.Block)
		}
	}
	return blocks
}

func lastFinished(t *testing.T, events []llm.PredictionEvent) llm.PredictionFinished {
	t.Helper()
	for index := len(events) - 1; index >= 0; index-- {
		if finished, ok := events[index].(llm.PredictionFinished); ok {
			return finished
		}
	}
	t.Fatal("stream produced no PredictionFinished event")
	return llm.PredictionFinished{}
}

type stubTool struct{}

func (stubTool) Name() string        { return "lookup" }
func (stubTool) Description() string { return "look something up" }
func (stubTool) Parameters() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object"}
}
