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
			`data: {"event_type":"step.start","index":0,"step":{"type":"thought","id":"sig"}}`,
			``,
			`data: {"event_type":"step.delta","index":0,"delta":{"type":"thought","text":"thinking"}}`,
			``,
			`data: {"event_type":"step.stop","index":0}`,
			``,
			`data: {"event_type":"step.start","index":1,"step":{"type":"model_output"}}`,
			``,
			`data: {"event_type":"step.delta","index":1,"delta":{"type":"text","text":"hello"}}`,
			``,
			`data: {"event_type":"step.stop","index":1}`,
			``,
			`data: {"event_type":"step.start","index":2,"step":{"type":"function_call","id":"call-1","name":"tool"}}`,
			``,
			`data: {"event_type":"step.delta","index":2,"delta":{"type":"arguments","partial_arguments":"{\"x\":1}"}}`,
			``,
			`data: {"event_type":"step.stop","index":2,"metadata":{"total_usage":{"total_input_tokens":3,"total_output_tokens":4,"total_thought_tokens":5,"total_cached_tokens":2,"total_tokens":12}}}`,
			``,
			`data: {"event_type":"interaction.requires_action"}`,
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
		if toolCall.ID != "call-1" || toolCall.Name != "tool" || string(toolCall.Arguments) != `{"x":1}` {
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
			`data: {"event_type":"step.stop","index":0,"metadata":{"total_usage":{"total_input_tokens":1,"total_output_tokens":2,"total_tokens":3}}}`,
			``,
			`data: {"event_type":"step.start","index":1,"step":{"type":"model_output"}}`,
			``,
			`data: {"event_type":"step.delta","index":1,"delta":{"type":"text","text":"late"}}`,
			``,
			`data: {"event_type":"step.stop","index":1}`,
			``,
			`data: {"event_type":"interaction.completed"}`,
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

	t.Run("rejects premature EOF", func(t *testing.T) {
		_, err := predictWithStream(t, "data: {\"event_type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"model_output\"}}\n\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"hello\"}}\n\ndata: {\"event_type\":\"step.stop\",\"index\":0}\n\n")
		if err == nil || !strings.Contains(err.Error(), "before interaction completion") {
			t.Fatalf("predict error = %v, want premature EOF error", err)
		}
	})

	t.Run("returns invalid tool arguments on error channel", func(t *testing.T) {
		stream := "data: {\"type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"function_call\",\"id\":\"call-1\",\"name\":\"tool\"}}\n\ndata: {\"type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"arguments\",\"partial_arguments\":invalid}}\n\n"
		_, err := predictWithStream(t, stream)
		if err == nil || !strings.Contains(err.Error(), "parse gemini event") {
			t.Fatalf("error = %v, want parse gemini event", err)
		}
	})

	t.Run("generated tool call IDs are request local", func(t *testing.T) {
		stream := strings.Join([]string{
			`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","name":"tool"}}`,
			``,
			`data: {"event_type":"step.delta","index":0,"delta":{"type":"arguments","partial_arguments":"{}"}}`,
			``,
			`data: {"event_type":"step.stop","index":0}`,
			``,
			`data: {"event_type":"step.start","index":1,"step":{"type":"function_call","name":"tool"}}`,
			``,
			`data: {"event_type":"step.delta","index":1,"delta":{"type":"arguments","partial_arguments":"{}"}}`,
			``,
			`data: {"event_type":"step.stop","index":1}`,
			``,
			`data: {"event_type":"interaction.requires_action"}`,
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
			_, _ = w.Write([]byte("data: {\"event_type\":\"interaction.completed\",\"metadata\":{\"total_usage\":{\"total_tokens\":1}}}\n\n"))
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

		input := body["input"].([]any)
		first := input[0].(map[string]any)
		contents := first["content"].([]any)
		content := contents[0].(map[string]any)
		if content["text"] != "error: oops" {
			t.Fatalf("text = %#v, want error message", content["text"])
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
			_, _ = w.Write([]byte(`{"steps":[{"type":"model_output","content":[{"type":"text","text":"{\"answer\":\"ok\"}"}]}]}`))
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
		if !strings.HasSuffix(escapedPath, "/interactions") {
			t.Fatalf("escaped path = %q, want interactions endpoint", escapedPath)
		}

		responseFormat, ok := body["response_format"].([]any)
		if !ok || len(responseFormat) == 0 {
			t.Fatalf("response_format = %#v, want slice", body["response_format"])
		}
		format := responseFormat[0].(map[string]any)
		if format["mime_type"] != "application/json" {
			t.Fatalf("mime_type = %#v, want application/json", format["mime_type"])
		}
		schema, ok := format["schema"].(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Fatalf("schema = %#v, want object schema", format["schema"])
		}

		generationConfig, ok := body["generation_config"].(map[string]any)
		if !ok {
			t.Fatalf("generation_config = %#v, want map", body["generation_config"])
		}
		if generationConfig["max_output_tokens"] != float64(123) {
			t.Fatalf("max_output_tokens = %#v, want 123", generationConfig["max_output_tokens"])
		}
		if body["system_instruction"] != "system" {
			t.Fatalf("system_instruction = %#v, want system", body["system_instruction"])
		}
	})

	t.Run("does not retry status before structured response", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			if attempts == 1 {
				w.Header().Set("Retry-After", "0")
				http.Error(w, "temporary", http.StatusGatewayTimeout)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"steps":[{"type":"model_output","content":[{"type":"text","text":"{\"answer\":\"ok\"}"}]}]}`))
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
		_, err = client.PredictNextStructured(context.Background(), llm.PredictNextStructuredRequest{Schema: &jsonschema.Schema{Type: "object"}})
		if err == nil || !strings.Contains(err.Error(), "status 504") {
			t.Fatalf("PredictNextStructured() error = %v, want status 504", err)
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1", attempts)
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
			_, _ = w.Write([]byte(`{"steps":[{"type":"model_output","content":[{"type":"text","text":"not json"}]}]}`))
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

func predictWithStream(t *testing.T, stream string) ([]llm.PredictionEvent, error) {
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

func drainEvents(events <-chan llm.PredictionEvent) []llm.PredictionEvent {
	var drained []llm.PredictionEvent
	for event := range events {
		drained = append(drained, event)
	}
	return drained
}

func toolCallIDs(events []llm.PredictionEvent) []string {
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
