package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/openai"
)

func TestSetReasoningLevelValidates(t *testing.T) {
	model := llm.Model{Provider: "openai", Name: "test"}
	client, err := openai.NewLLM(openai.LLMConfig{APIKey: "key", Model: model, ReasoningLevel: llm.ReasoningLevelMedium})
	if err != nil {
		t.Fatalf("new llm: %v", err)
	}

	err = client.SetReasoningLevel("invalid")
	if err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Fatalf("invalid reasoning error = %v, want validation error", err)
	}
	if client.ReasoningLevel() != llm.ReasoningLevelMedium {
		t.Fatalf("reasoning level changed to %q", client.ReasoningLevel())
	}
}

func TestPredictNext(t *testing.T) {
	t.Run("converts raw tool arguments and error messages", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"))
		}))
		defer server.Close()

		client, err := openai.NewLLM(openai.LLMConfig{
			BaseURL:        server.URL,
			APIKey:         "key",
			Model:          llm.Model{Provider: "openai", Name: "test"},
			ReasoningLevel: llm.ReasoningLevelOff,
		})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}

		events, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{
			Messages: []llm.Message{
				llm.AssistantMessage{Blocks: []llm.AssistantBlock{
					llm.ToolCallBlock{ID: "call-1", Name: "tool", Arguments: json.RawMessage(`{"x":1}`)},
				}},
				llm.ErrorMessage{Error: errors.New("oops")},
			},
		})
		_ = drainEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("predict: %v", err)
		}

		input, ok := body["input"].([]any)
		if !ok {
			t.Fatalf("input = %#v, want array", body["input"])
		}
		call, ok := input[0].(map[string]any)
		if !ok {
			t.Fatalf("input[0] = %#v, want map", input[0])
		}
		if call["arguments"] != `{"x":1}` {
			t.Fatalf("arguments = %#v, want raw JSON string", call["arguments"])
		}
		errMsg, ok := input[1].(map[string]any)
		if !ok {
			t.Fatalf("input[1] = %#v, want map", input[1])
		}
		if errMsg["content"] != "error: oops" {
			t.Fatalf("error content = %#v, want error message", errMsg["content"])
		}
	})

	t.Run("streams tool call stop reason and usage", func(t *testing.T) {
		stream := strings.Join([]string{
			`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"item-1","type":"function_call","call_id":"call-1","name":"tool"}}`,
			``,
			`data: {"type":"response.function_call_arguments.delta","output_index":0,"item_id":"item-1","delta":"{\"x\":"}`,
			``,
			`data: {"type":"response.function_call_arguments.done","output_index":0,"item_id":"item-1","arguments":"{\"x\":1}"}`,
			``,
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}}`,
			``,
		}, "\n")

		events, err := predictWithStream(t, stream)
		if err != nil {
			t.Fatalf("predict: %v", err)
		}

		var toolCall llm.ToolCallBlock
		var finished llm.PredictionFinished
		for _, event := range events {
			switch typedEvent := event.(type) {
			case llm.BlockEnded:
				if block, ok := typedEvent.Block.(llm.ToolCallBlock); ok {
					toolCall = block
				}
			case llm.PredictionFinished:
				finished = typedEvent
			}
		}
		if toolCall.ID != "call-1" || toolCall.Name != "tool" || string(toolCall.Arguments) != `{"x":1}` {
			t.Fatalf("tool call = %#v", toolCall)
		}
		if finished.StopReason != llm.StopReasonToolUse {
			t.Fatalf("stop reason = %q, want tool use", finished.StopReason)
		}
		if finished.Usage.InputTokens != 3 || finished.Usage.OutputTokens != 4 || finished.Usage.TotalTokens != 7 {
			t.Fatalf("usage = %#v", finished.Usage)
		}
	})

	t.Run("merges out-of-order tool call metadata", func(t *testing.T) {
		stream := strings.Join([]string{
			`data: {"type":"response.function_call_arguments.done","output_index":0,"item_id":"item-1","arguments":"{\"x\":1}"}`,
			``,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"item-1","type":"function_call","call_id":"call-1","name":"tool"}}`,
			``,
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}}`,
			``,
		}, "\n")

		events, err := predictWithStream(t, stream)
		if err != nil {
			t.Fatalf("predict: %v", err)
		}

		var toolCall llm.ToolCallBlock
		for _, event := range events {
			if ended, ok := event.(llm.BlockEnded); ok {
				if block, ok := ended.Block.(llm.ToolCallBlock); ok {
					toolCall = block
				}
			}
		}
		if toolCall.ID != "call-1" || toolCall.Name != "tool" || string(toolCall.Arguments) != `{"x":1}` {
			t.Fatalf("tool call = %#v", toolCall)
		}
	})

	t.Run("merges late call id metadata with index arguments", func(t *testing.T) {
		stream := strings.Join([]string{
			`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"x\":"}`,
			``,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call-1","name":"tool","arguments":"{\"x\":1}"}}`,
			``,
			`data: {"type":"response.completed"}`,
			``,
		}, "\n")

		events, err := predictWithStream(t, stream)
		if err != nil {
			t.Fatalf("predict: %v", err)
		}

		var toolCalls []llm.ToolCallBlock
		for _, event := range events {
			if ended, ok := event.(llm.BlockEnded); ok {
				if block, ok := ended.Block.(llm.ToolCallBlock); ok {
					toolCalls = append(toolCalls, block)
				}
			}
		}
		if len(toolCalls) != 1 {
			t.Fatalf("tool calls = %#v, want exactly one", toolCalls)
		}
		if toolCalls[0].ID != "call-1" || toolCalls[0].Name != "tool" || string(toolCalls[0].Arguments) != `{"x":1}` {
			t.Fatalf("tool call = %#v", toolCalls[0])
		}
	})

	t.Run("preserves assistant block order", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
		}))
		defer server.Close()

		client, err := openai.NewLLM(openai.LLMConfig{
			BaseURL:        server.URL,
			APIKey:         "key",
			Model:          llm.Model{Provider: "openai", Name: "test"},
			ReasoningLevel: llm.ReasoningLevelOff,
		})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}

		events, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{
			Messages: []llm.Message{
				llm.AssistantMessage{Blocks: []llm.AssistantBlock{
					llm.TextBlock{Text: "before"},
					llm.ToolCallBlock{ID: "call-1", Name: "tool", Arguments: json.RawMessage(`{}`)},
					llm.TextBlock{Text: "after"},
				}},
			},
		})
		_ = drainEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("predict: %v", err)
		}

		input := body["input"].([]any)
		if input[0].(map[string]any)["content"] != "before" {
			t.Fatalf("input[0] = %#v, want assistant text before", input[0])
		}
		if input[1].(map[string]any)["type"] != "function_call" {
			t.Fatalf("input[1] = %#v, want function call", input[1])
		}
		if input[2].(map[string]any)["content"] != "after" {
			t.Fatalf("input[2] = %#v, want assistant text after", input[2])
		}
	})

	t.Run("retries status before streaming", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			if attempts == 1 {
				w.Header().Set("Retry-After", "0")
				http.Error(w, "try again", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
		}))
		defer server.Close()

		client, err := openai.NewLLM(openai.LLMConfig{
			BaseURL:        server.URL,
			APIKey:         "key",
			Model:          llm.Model{Provider: "openai", Name: "test"},
			ReasoningLevel: llm.ReasoningLevelOff,
		})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}

		events, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{})
		_ = drainEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("predict: %v", err)
		}
		if attempts != 2 {
			t.Fatalf("attempts = %d, want 2", attempts)
		}
	})

	t.Run("separates reasoning summary parts with ellipses", func(t *testing.T) {
		stream := strings.Join([]string{
			`data: {"type":"response.reasoning_summary_text.delta","delta":"Planning API design"}`,
			``,
			`data: {"type":"response.reasoning_summary_text.done","text":"Planning API design"}`,
			``,
			`data: {"type":"response.reasoning_summary_part.done"}`,
			``,
			`data: {"type":"response.reasoning_summary_text.delta","delta":"Designing structured output"}`,
			``,
			`data: {"type":"response.reasoning_summary_text.done","text":"Designing structured output"}`,
			``,
			`data: {"type":"response.completed"}`,
			``,
		}, "\n")

		events, err := predictWithStream(t, stream)
		if err != nil {
			t.Fatalf("predict: %v", err)
		}

		var deltas []string
		var thinkingBlocks []llm.ThinkingBlock
		for _, event := range events {
			switch typedEvent := event.(type) {
			case llm.ThinkingDelta:
				deltas = append(deltas, typedEvent.Text)
			case llm.BlockEnded:
				if block, ok := typedEvent.Block.(llm.ThinkingBlock); ok {
					thinkingBlocks = append(thinkingBlocks, block)
				}
			}
		}

		if got, want := strings.Join(deltas, ""), "Planning API design... Designing structured output"; got != want {
			t.Fatalf("thinking deltas = %q, want %q", got, want)
		}
		if len(thinkingBlocks) != 1 {
			t.Fatalf("thinking blocks = %#v, want exactly one", thinkingBlocks)
		}
		if got, want := thinkingBlocks[0].Text, "Planning API design... Designing structured output"; got != want {
			t.Fatalf("thinking block = %q, want %q", got, want)
		}
	})

	t.Run("emits fallback finish at EOF", func(t *testing.T) {
		events, err := predictWithStream(t, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
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

	t.Run("returns error event on error channel", func(t *testing.T) {
		_, err := predictWithStream(t, "data: {\"type\":\"response.error\",\"message\":\"bad\"}\n\n")
		if err == nil || !strings.Contains(err.Error(), "openai error event") {
			t.Fatalf("error = %v, want openai error event", err)
		}
	})

	t.Run("labels http errors with the model provider", func(t *testing.T) {
		for _, provider := range []string{"openai", "xai"} {
			t.Run(provider, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, "bad request", http.StatusBadRequest)
				}))
				t.Cleanup(server.Close)

				client, err := openai.NewLLM(openai.LLMConfig{
					BaseURL:        server.URL,
					APIKey:         "key",
					Model:          llm.Model{Provider: provider, Name: "test"},
					ReasoningLevel: llm.ReasoningLevelOff,
				})
				if err != nil {
					t.Fatalf("new llm: %v", err)
				}

				events, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{})
				_ = drainEvents(events)
				err = <-errs
				want := provider + " status 400"
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("PredictNext() error = %v, want %q", err, want)
				}
				if provider != "openai" && strings.Contains(err.Error(), "openai") {
					t.Fatalf("PredictNext() error = %v, want no openai label", err)
				}

				_, err = client.PredictNextStructured(context.Background(), llm.PredictNextStructuredRequest{Schema: &jsonschema.Schema{Type: "object"}})
				want = provider + " structured status 400"
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("PredictNextStructured() error = %v, want %q", err, want)
				}
				if provider != "openai" && strings.Contains(err.Error(), "openai") {
					t.Fatalf("PredictNextStructured() error = %v, want no openai label", err)
				}
			})
		}
	})

	t.Run("returns invalid tool arguments on error channel", func(t *testing.T) {
		stream := strings.Join([]string{
			`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"item-1","type":"function_call","call_id":"call-1","name":"tool"}}`,
			``,
			`data: {"type":"response.function_call_arguments.done","output_index":0,"item_id":"item-1","arguments":"{"}`,
			``,
		}, "\n")
		_, err := predictWithStream(t, stream)
		if err == nil || !strings.Contains(err.Error(), "invalid tool call arguments") {
			t.Fatalf("error = %v, want invalid arguments", err)
		}
	})
}

func TestPredictNextStructured(t *testing.T) {
	t.Run("sends_json_schema_and_returns_output_text", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"answer\":\"ok\"}"}]}]}`))
		}))
		defer server.Close()

		client, err := openai.NewLLM(openai.LLMConfig{
			BaseURL:        server.URL,
			APIKey:         "key",
			Model:          llm.Model{Provider: "openai", Name: "test"},
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

		if body["stream"] != true {
			t.Fatalf("stream = %#v, want true", body["stream"])
		}
		if body["instructions"] != "system" {
			t.Fatalf("instructions = %#v, want system", body["instructions"])
		}
		if body["max_output_tokens"] != float64(123) {
			t.Fatalf("max_output_tokens = %#v, want 123", body["max_output_tokens"])
		}
		textConfig, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("text = %#v, want map", body["text"])
		}
		format, ok := textConfig["format"].(map[string]any)
		if !ok {
			t.Fatalf("text.format = %#v, want map", textConfig["format"])
		}
		if format["type"] != "json_schema" || format["name"] != "structured_output" || format["strict"] != true {
			t.Fatalf("format = %#v, want json_schema structured_output strict", format)
		}
		schema, ok := format["schema"].(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Fatalf("schema = %#v, want object schema", format["schema"])
		}
	})

	t.Run("returns_streamed_output_text", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if body["stream"] != true {
				t.Fatalf("stream = %#v, want true", body["stream"])
			}
			if r.Header.Get("Accept") != "text/event-stream" {
				t.Fatalf("Accept = %q, want text/event-stream", r.Header.Get("Accept"))
			}

			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(strings.Join([]string{
				`data: {"type":"response.output_text.delta","delta":"{\"answer\":"}`,
				``,
				`data: {"type":"response.output_text.delta","delta":"\"ok\"}"}`,
				``,
				`data: {"type":"response.completed"}`,
				``,
			}, "\n")))
		}))
		defer server.Close()

		client, err := openai.NewLLM(openai.LLMConfig{
			BaseURL:        server.URL,
			APIKey:         "key",
			Model:          llm.Model{Provider: "openai", Name: "test"},
			ReasoningLevel: llm.ReasoningLevelOff,
		})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}

		got, err := client.PredictNextStructured(context.Background(), llm.PredictNextStructuredRequest{
			Messages: []llm.Message{llm.UserMessage{Text: "prompt"}},
			Schema:   &jsonschema.Schema{Type: "object"},
		})
		if err != nil {
			t.Fatalf("PredictNextStructured() error = %v", err)
		}
		if string(got) != `{"answer":"ok"}` {
			t.Fatalf("structured output = %s, want answer", got)
		}
	})

	t.Run("detects_stream_with_json_content_type", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(strings.Join([]string{
				`event: response.output_text.delta`,
				`data: {"type":"response.output_text.delta","delta":"{\"answer\":\"ok\"}"}`,
				``,
				`event: response.completed`,
				`data: {"type":"response.completed"}`,
				``,
			}, "\n")))
		}))
		defer server.Close()

		client, err := openai.NewLLM(openai.LLMConfig{
			BaseURL:        server.URL,
			APIKey:         "key",
			Model:          llm.Model{Provider: "openai", Name: "test"},
			ReasoningLevel: llm.ReasoningLevelOff,
		})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}

		got, err := client.PredictNextStructured(context.Background(), llm.PredictNextStructuredRequest{
			Messages: []llm.Message{llm.UserMessage{Text: "prompt"}},
			Schema:   &jsonschema.Schema{Type: "object"},
		})
		if err != nil {
			t.Fatalf("PredictNextStructured() error = %v", err)
		}
		if string(got) != `{"answer":"ok"}` {
			t.Fatalf("structured output = %s, want answer", got)
		}
	})

	t.Run("validates_supported_schema_without_request", func(t *testing.T) {
		client, err := openai.NewLLM(openai.LLMConfig{
			APIKey:         "key",
			Model:          llm.Model{Provider: "openai", Name: "test"},
			ReasoningLevel: llm.ReasoningLevelOff,
		})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}
		schema, err := jsonschema.FromRaw([]byte(`{"type":"object","properties":{"tasks":{"type":"array","minItems":1,"maxItems":8,"items":{"type":"string","pattern":"^[a-z]+$"}}},"required":["tasks"],"additionalProperties":false}`))
		if err != nil {
			t.Fatalf("FromRaw() error = %v", err)
		}
		if err := client.ValidateStructuredOutputSchema(schema); err != nil {
			t.Fatalf("ValidateStructuredOutputSchema() error = %v", err)
		}
	})

	t.Run("rejects_unsupported_schema_before_request", func(t *testing.T) {
		var requests int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests++
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		client, err := openai.NewLLM(openai.LLMConfig{
			BaseURL:        server.URL,
			APIKey:         "key",
			Model:          llm.Model{Provider: "openai", Name: "test"},
			ReasoningLevel: llm.ReasoningLevelOff,
		})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}
		schema, err := jsonschema.FromRaw([]byte(`{"type":"object","properties":{"tasks":{"type":"array","uniqueItems":true,"items":{"type":"string"}}}}`))
		if err != nil {
			t.Fatalf("FromRaw() error = %v", err)
		}

		_, err = client.PredictNextStructured(t.Context(), llm.PredictNextStructuredRequest{Schema: schema})
		if err == nil || !strings.Contains(err.Error(), "$.properties.tasks.uniqueItems is not permitted") {
			t.Fatalf("PredictNextStructured() error = %v, want unsupported uniqueItems error", err)
		}
		if requests != 0 {
			t.Fatalf("HTTP requests = %d, want 0", requests)
		}
	})

	t.Run("requires_schema", func(t *testing.T) {
		client, err := openai.NewLLM(openai.LLMConfig{APIKey: "key", Model: llm.Model{Provider: "openai", Name: "test"}, ReasoningLevel: llm.ReasoningLevelOff})
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
			_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"not json"}]}]}`))
		}))
		defer server.Close()

		client, err := openai.NewLLM(openai.LLMConfig{BaseURL: server.URL, APIKey: "key", Model: llm.Model{Provider: "openai", Name: "test"}, ReasoningLevel: llm.ReasoningLevelOff})
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

	client, err := openai.NewLLM(openai.LLMConfig{
		BaseURL:        server.URL,
		APIKey:         "key",
		Model:          llm.Model{Provider: "openai", Name: "test"},
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
