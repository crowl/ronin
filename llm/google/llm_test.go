package google_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/google"
)

func TestSetReasoningLevelValidates(t *testing.T) {
	model := llm.Model{Provider: "google", Name: "test"}
	client, err := google.NewLLM(google.LLMConfig{APIKey: "key", Model: model, ReasoningLevel: llm.ReasoningLevelMedium})
	if err != nil {
		t.Fatalf("new llm: %v", err)
	}

	err = client.SetReasoningLevel("invalid")
	if err == nil || !strings.Contains(err.Error(), "not a valid level") {
		t.Fatalf("invalid reasoning error = %v, want validation error", err)
	}
	if client.ReasoningLevel() != llm.ReasoningLevelMedium {
		t.Fatalf("reasoning level changed to %q", client.ReasoningLevel())
	}
}

func TestPredictNext(t *testing.T) {
	t.Run("streams tool call stop reason and usage", func(t *testing.T) {
		stream := strings.Join([]string{
			`data: {"candidates":[{"content":{"parts":[{"text":"thinking","thought":true},{"text":"hello"},{"thoughtSignature":"sig","functionCall":{"id":"call-1","name":"tool","args":{"x":1}}}]}}]}`,
			``,
			`data: {"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"thoughtsTokenCount":5,"cachedContentTokenCount":2,"totalTokenCount":12}}`,
			``,
		}, "\n")

		events, err := predictWithStream(t, stream)
		if err != nil {
			t.Fatalf("predict: %v", err)
		}

		var thinking llm.ThinkingDelta
		var text llm.TextDelta
		var toolCall llm.ToolCallBlock
		var finished llm.PredictionFinished
		for _, event := range events {
			switch typedEvent := event.(type) {
			case llm.ThinkingDelta:
				thinking = typedEvent
			case llm.TextDelta:
				text = typedEvent
			case llm.BlockEnded:
				if block, ok := typedEvent.Block.(llm.ToolCallBlock); ok {
					toolCall = block
				}
			case llm.PredictionFinished:
				finished = typedEvent
			}
		}

		if thinking.Text != "thinking" {
			t.Fatalf("thinking = %q", thinking.Text)
		}
		if text.Text != "hello" {
			t.Fatalf("text = %q", text.Text)
		}
		if toolCall.ID != "call-1" || toolCall.Name != "tool" || string(toolCall.Arguments) != `{"x":1}` || toolCall.ThoughtSignature != "sig" {
			t.Fatalf("tool call = %#v", toolCall)
		}
		if finished.StopReason != llm.StopReasonToolUse {
			t.Fatalf("stop reason = %q, want tool use", finished.StopReason)
		}
		if finished.Usage.InputTokens != 3 || finished.Usage.OutputTokens != 9 || finished.Usage.CachedTokens != 2 || finished.Usage.TotalTokens != 12 {
			t.Fatalf("usage = %#v", finished.Usage)
		}
	})

	t.Run("emits finish after earlier usage and later content", func(t *testing.T) {
		stream := strings.Join([]string{
			`data: {"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`,
			``,
			`data: {"candidates":[{"content":{"parts":[{"text":"late"}]}}]}`,
			``,
		}, "\n")

		events, err := predictWithStream(t, stream)
		if err != nil {
			t.Fatalf("predict: %v", err)
		}
		if _, ok := events[len(events)-3].(llm.TextDelta); !ok {
			t.Fatalf("event before block end = %#v, want TextDelta", events[len(events)-3])
		}
		if _, ok := events[len(events)-2].(llm.BlockEnded); !ok {
			t.Fatalf("penultimate event = %#v, want BlockEnded", events[len(events)-2])
		}
		finished, ok := events[len(events)-1].(llm.PredictionFinished)
		if !ok {
			t.Fatalf("last event = %#v, want PredictionFinished", events[len(events)-1])
		}
		if finished.Usage.InputTokens != 1 || finished.Usage.OutputTokens != 2 || finished.Usage.TotalTokens != 3 {
			t.Fatalf("usage = %#v", finished.Usage)
		}
	})

	t.Run("emits fallback finish at EOF", func(t *testing.T) {
		events, err := predictWithStream(t, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n\n")
		if err != nil {
			t.Fatalf("predict: %v", err)
		}
		last, ok := events[len(events)-1].(llm.PredictionFinished)
		if !ok {
			t.Fatalf("last event = %#v, want PredictionFinished", events[len(events)-1])
		}
		if last.StopReason != llm.StopReasonFinished {
			t.Fatalf("stop reason = %q, want finished", last.StopReason)
		}
	})

	t.Run("returns invalid tool arguments on error channel", func(t *testing.T) {
		stream := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"id\":\"call-1\",\"name\":\"tool\",\"args\": invalid}}]}}]}\n\n"
		_, err := predictWithStream(t, stream)
		if err == nil || !strings.Contains(err.Error(), "parse gemini event") {
			t.Fatalf("error = %v, want parse gemini event", err)
		}
	})

	t.Run("generated tool call IDs are request local", func(t *testing.T) {
		stream := strings.Join([]string{
			`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"tool","args":{} }},{"functionCall":{"name":"tool","args":{}}}]}}]}`,
			``,
		}, "\n")

		firstEvents, err := predictWithStream(t, stream)
		if err != nil {
			t.Fatalf("first predict: %v", err)
		}
		secondEvents, err := predictWithStream(t, stream)
		if err != nil {
			t.Fatalf("second predict: %v", err)
		}

		firstIDs := toolCallIDs(firstEvents)
		secondIDs := toolCallIDs(secondEvents)
		want := []string{"tool_1", "tool_2"}
		if !reflect.DeepEqual(firstIDs, want) {
			t.Fatalf("first IDs = %#v, want %#v", firstIDs, want)
		}
		if !reflect.DeepEqual(secondIDs, want) {
			t.Fatalf("second IDs = %#v, want %#v", secondIDs, want)
		}
	})

	t.Run("converts error messages", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"usageMetadata\":{\"totalTokenCount\":1}}\n\n"))
		}))
		defer server.Close()

		client, err := google.NewLLM(google.LLMConfig{
			BaseURL:        server.URL,
			APIKey:         "key",
			Model:          llm.Model{Provider: "google", Name: "test"},
			ReasoningLevel: llm.ReasoningLevelOff,
		})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}

		events, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{Messages: []llm.Message{
			llm.ErrorMessage{Error: errors.New("oops")},
		}})
		_ = drainEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("predict: %v", err)
		}

		contents := body["contents"].([]any)
		first := contents[0].(map[string]any)
		parts := first["parts"].([]any)
		part := parts[0].(map[string]any)
		if part["text"] != "error: oops" {
			t.Fatalf("text = %#v, want error message", part["text"])
		}
	})
}

func TestPredictNextStructured(t *testing.T) {
	t.Run("sends_json_schema_and_returns_candidate_text", func(t *testing.T) {
		var body map[string]any
		var escapedPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			escapedPath = r.URL.EscapedPath()
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"answer\":\"ok\"}"}]}}]}`))
		}))
		defer server.Close()

		client, err := google.NewLLM(google.LLMConfig{
			BaseURL:        server.URL,
			APIKey:         "key",
			Model:          llm.Model{Provider: "google", Name: "test model"},
			ReasoningLevel: llm.ReasoningLevelOff,
		})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}

		got, err := client.PredictNextStructured(context.Background(), llm.PredictNextStructuredRequest{
			SystemPrompt: "system",
			Messages:     []llm.Message{llm.UserMessage{Text: "prompt"}},
			Schema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"answer": {Type: "string"},
				},
				Required: []string{"answer"},
			},
			MaxTokens: 123,
		})
		if err != nil {
			t.Fatalf("PredictNextStructured() error = %v", err)
		}
		if string(got) != `{"answer":"ok"}` {
			t.Fatalf("structured output = %s, want answer", got)
		}
		if !strings.HasSuffix(escapedPath, "/models/test%20model:generateContent") {
			t.Fatalf("escaped path = %q, want generateContent endpoint", escapedPath)
		}

		generationConfig, ok := body["generationConfig"].(map[string]any)
		if !ok {
			t.Fatalf("generationConfig = %#v, want map", body["generationConfig"])
		}
		if generationConfig["responseMimeType"] != "application/json" {
			t.Fatalf("responseMimeType = %#v, want application/json", generationConfig["responseMimeType"])
		}
		if generationConfig["maxOutputTokens"] != float64(123) {
			t.Fatalf("maxOutputTokens = %#v, want 123", generationConfig["maxOutputTokens"])
		}
		schema, ok := generationConfig["responseJsonSchema"].(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Fatalf("responseJsonSchema = %#v, want object schema", generationConfig["responseJsonSchema"])
		}
		systemInstruction, ok := body["systemInstruction"].(map[string]any)
		if !ok {
			t.Fatalf("systemInstruction = %#v, want map", body["systemInstruction"])
		}
		parts := systemInstruction["parts"].([]any)
		if parts[0].(map[string]any)["text"] != "system" {
			t.Fatalf("systemInstruction parts = %#v, want system", parts)
		}
	})

	t.Run("requires_schema", func(t *testing.T) {
		client, err := google.NewLLM(google.LLMConfig{APIKey: "key", Model: llm.Model{Provider: "google", Name: "test"}, ReasoningLevel: llm.ReasoningLevelOff})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}
		_, err = client.PredictNextStructured(context.Background(), llm.PredictNextStructuredRequest{})
		if err == nil || !strings.Contains(err.Error(), "schema") {
			t.Fatalf("error = %v, want schema error", err)
		}
	})

	t.Run("rejects_invalid_json_output", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"not json"}]}}]}`))
		}))
		defer server.Close()

		client, err := google.NewLLM(google.LLMConfig{BaseURL: server.URL, APIKey: "key", Model: llm.Model{Provider: "google", Name: "test"}, ReasoningLevel: llm.ReasoningLevelOff})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}
		_, err = client.PredictNextStructured(context.Background(), llm.PredictNextStructuredRequest{Schema: &jsonschema.Schema{Type: "object"}})
		if err == nil || !strings.Contains(err.Error(), "valid JSON") {
			t.Fatalf("error = %v, want valid JSON error", err)
		}
	})
}

func predictWithStream(t *testing.T, stream string) ([]llm.Event, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream))
	}))
	defer server.Close()

	client, err := google.NewLLM(google.LLMConfig{
		BaseURL:        server.URL,
		APIKey:         "key",
		Model:          llm.Model{Provider: "google", Name: "test"},
		ReasoningLevel: llm.ReasoningLevelOff,
	})
	if err != nil {
		t.Fatalf("new llm: %v", err)
	}

	eventsCh, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{})
	events := drainEvents(eventsCh)
	return events, <-errs
}

func drainEvents(events <-chan llm.Event) []llm.Event {
	var drained []llm.Event
	for event := range events {
		drained = append(drained, event)
	}
	return drained
}

func toolCallIDs(events []llm.Event) []string {
	var ids []string
	for _, event := range events {
		if ended, ok := event.(llm.BlockEnded); ok {
			if block, ok := ended.Block.(llm.ToolCallBlock); ok {
				ids = append(ids, block.ID)
			}
		}
	}
	return ids
}
