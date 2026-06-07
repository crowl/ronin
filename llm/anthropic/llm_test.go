package anthropic_test

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
	"github.com/crowl/ronin/llm/anthropic"
)

func TestSetReasoningLevelValidates(t *testing.T) {
	model := llm.Model{Provider: "anthropic", Name: "test"}
	client, err := anthropic.NewLLM(anthropic.LLMConfig{APIKey: "key", Model: model, ReasoningLevel: llm.ReasoningLevelMedium})
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
	t.Run("sends ordered blocks tools and thinking config", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("x-api-key") != "key" {
				t.Fatalf("x-api-key = %q, want key", r.Header.Get("x-api-key"))
			}
			if r.Header.Get("anthropic-version") == "" {
				t.Fatal("anthropic-version header missing")
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"))
			_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
		}))
		defer server.Close()

		client, err := anthropic.NewLLM(anthropic.LLMConfig{
			BaseURL:        server.URL,
			APIKey:         "key",
			Model:          llm.Model{Provider: "anthropic", Name: "test"},
			ReasoningLevel: llm.ReasoningLevelLow,
		})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}

		events, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{
			SystemPrompt: "system",
			Messages: []llm.Message{
				llm.UserMessage{Text: "prompt"},
				llm.AssistantMessage{Blocks: []llm.AssistantBlock{
					llm.ThinkingBlock{Text: "think", Signature: "sig"},
					llm.TextBlock{Text: "before"},
					llm.ToolCallBlock{ID: "call-1", Name: "tool", Arguments: json.RawMessage(`{"x":1}`)},
				}},
				llm.ToolErrorMessage{ToolCallID: "call-1", Error: errors.New("oops")},
			},
			Tools:     []llm.Tool{fakeTool{name: "tool"}},
			MaxTokens: 2048,
		})
		_ = drainEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("predict: %v", err)
		}

		if body["system"] != "system" || body["model"] != "test" || body["stream"] != true {
			t.Fatalf("basic payload fields = %#v", body)
		}
		if body["max_tokens"] != float64(2048) {
			t.Fatalf("max_tokens = %#v, want 2048", body["max_tokens"])
		}
		thinking := body["thinking"].(map[string]any)
		if thinking["type"] != "adaptive" || thinking["display"] != "summarized" {
			t.Fatalf("thinking = %#v, want adaptive summarized", thinking)
		}
		outputConfig := body["output_config"].(map[string]any)
		if outputConfig["effort"] != string(llm.ReasoningLevelLow) {
			t.Fatalf("output_config = %#v, want low effort", outputConfig)
		}
		tools := body["tools"].([]any)
		tool := tools[0].(map[string]any)
		if tool["name"] != "tool" || tool["input_schema"] == nil {
			t.Fatalf("tool = %#v, want schema", tool)
		}

		messages := body["messages"].([]any)
		assistant := messages[1].(map[string]any)
		content := assistant["content"].([]any)
		if content[0].(map[string]any)["type"] != "thinking" || content[0].(map[string]any)["signature"] != "sig" {
			t.Fatalf("thinking block = %#v", content[0])
		}
		if content[1].(map[string]any)["type"] != "text" || content[1].(map[string]any)["text"] != "before" {
			t.Fatalf("text block = %#v", content[1])
		}
		if content[2].(map[string]any)["type"] != "tool_use" || content[2].(map[string]any)["id"] != "call-1" {
			t.Fatalf("tool block = %#v", content[2])
		}
		toolResult := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
		if toolResult["type"] != "tool_result" || toolResult["is_error"] != true || toolResult["content"] != "error: oops" {
			t.Fatalf("tool result = %#v", toolResult)
		}
	})

	t.Run("sends empty thinking key", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"message_start\"}\n\n"))
			_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
		}))
		defer server.Close()

		client, err := anthropic.NewLLM(anthropic.LLMConfig{
			BaseURL:        server.URL,
			APIKey:         "key",
			Model:          llm.Model{Provider: "anthropic", Name: "test"},
			ReasoningLevel: llm.ReasoningLevelOff,
		})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}

		events, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{
			Messages: []llm.Message{
				llm.AssistantMessage{Blocks: []llm.AssistantBlock{
					llm.ThinkingBlock{Text: "", Signature: "sig"},
					llm.ToolCallBlock{ID: "call-1", Name: "tool", Arguments: json.RawMessage(`{}`)},
				}},
				llm.ToolOutputMessage{ToolCallID: "call-1", ToolOutput: "ok"},
			},
		})
		_ = drainEvents(events)
		if err := <-errs; err != nil {
			t.Fatalf("predict: %v", err)
		}

		messages := body["messages"].([]any)
		assistant := messages[0].(map[string]any)
		content := assistant["content"].([]any)
		thinking := content[0].(map[string]any)
		thinkingText, ok := thinking["thinking"]
		if thinking["type"] != "thinking" || !ok || thinkingText != "" {
			t.Fatalf("thinking block = %#v, want empty thinking key", thinking)
		}
	})

	t.Run("streams text thinking tool usage and stop reason", func(t *testing.T) {
		stream := strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
			``,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
			``,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think"}}`,
			``,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
			``,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
			``,
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hello"}}`,
			``,
			`data: {"type":"content_block_stop","index":1}`,
			``,
			`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call-1","name":"tool"}}`,
			``,
			`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"x\":"}}`,
			``,
			`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"1}"}}`,
			``,
			`data: {"type":"content_block_stop","index":2}`,
			``,
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
			``,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")

		events, err := predictWithStream(t, stream)
		if err != nil {
			t.Fatalf("predict: %v", err)
		}

		var thinking llm.ThinkingBlock
		var text llm.TextBlock
		var toolCall llm.ToolCallBlock
		var finished llm.PredictionFinished
		for _, event := range events {
			switch typedEvent := event.(type) {
			case llm.BlockEnded:
				switch block := typedEvent.Block.(type) {
				case llm.ThinkingBlock:
					thinking = block
				case llm.TextBlock:
					text = block
				case llm.ToolCallBlock:
					toolCall = block
				}
			case llm.PredictionFinished:
				finished = typedEvent
			}
		}
		if thinking.Text != "think" || thinking.Signature != "sig" {
			t.Fatalf("thinking = %#v", thinking)
		}
		if text.Text != "hello" {
			t.Fatalf("text = %#v", text)
		}
		if toolCall.ID != "call-1" || toolCall.Name != "tool" || string(toolCall.Arguments) != `{"x":1}` {
			t.Fatalf("tool call = %#v", toolCall)
		}
		if finished.StopReason != llm.StopReasonToolUse || finished.Usage.InputTokens != 3 || finished.Usage.OutputTokens != 5 || finished.Usage.TotalTokens != 8 {
			t.Fatalf("finished = %#v", finished)
		}
	})

	t.Run("returns error event on error channel", func(t *testing.T) {
		_, err := predictWithStream(t, `data: {"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`+"\n\n")
		if err == nil || !strings.Contains(err.Error(), "bad") {
			t.Fatalf("error = %v, want bad error", err)
		}
	})
}

func TestPredictNextStructured(t *testing.T) {
	t.Run("sends JSON schema format and returns text JSON", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"answer\":\"ok\"}"}]}`))
		}))
		defer server.Close()

		client, err := anthropic.NewLLM(anthropic.LLMConfig{
			BaseURL:        server.URL,
			APIKey:         "key",
			Model:          llm.Model{Provider: "anthropic", Name: "test"},
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
		if body["stream"] != nil {
			t.Fatalf("stream = %#v, want omitted false", body["stream"])
		}
		if body["system"] != "system" || body["max_tokens"] != float64(123) {
			t.Fatalf("payload = %#v", body)
		}
		if body["tool_choice"] != nil {
			t.Fatalf("tool_choice = %#v, want omitted", body["tool_choice"])
		}
		if body["tools"] != nil {
			t.Fatalf("tools = %#v, want omitted", body["tools"])
		}
		outputConfig := body["output_config"].(map[string]any)
		format := outputConfig["format"].(map[string]any)
		if format["type"] != "json_schema" {
			t.Fatalf("format type = %#v, want json_schema", format["type"])
		}
		schema := format["schema"].(map[string]any)
		if schema["type"] != "object" {
			t.Fatalf("schema = %#v, want object schema", schema)
		}
	})

	t.Run("retries status before structured response", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			if attempts == 1 {
				w.Header().Set("Retry-After", "0")
				http.Error(w, "temporary", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"answer\":\"ok\"}"}]}`))
		}))
		defer server.Close()

		client, err := anthropic.NewLLM(anthropic.LLMConfig{
			BaseURL:        server.URL,
			APIKey:         "key",
			Model:          llm.Model{Provider: "anthropic", Name: "test"},
			ReasoningLevel: llm.ReasoningLevelOff,
		})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}
		_, err = client.PredictNextStructured(context.Background(), llm.PredictNextStructuredRequest{Schema: &jsonschema.Schema{Type: "object"}})
		if err != nil {
			t.Fatalf("PredictNextStructured() error = %v", err)
		}
		if attempts != 2 {
			t.Fatalf("attempts = %d, want 2", attempts)
		}
	})

	t.Run("requires_schema", func(t *testing.T) {
		client, err := anthropic.NewLLM(anthropic.LLMConfig{APIKey: "key", Model: llm.Model{Provider: "anthropic", Name: "test"}, ReasoningLevel: llm.ReasoningLevelOff})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}
		_, err = client.PredictNextStructured(context.Background(), llm.PredictNextStructuredRequest{})
		if err == nil || !strings.Contains(err.Error(), "schema") {
			t.Fatalf("error = %v, want schema error", err)
		}
	})

	t.Run("rejects_invalid_text_json", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"no"}]}`))
		}))
		defer server.Close()

		client, err := anthropic.NewLLM(anthropic.LLMConfig{BaseURL: server.URL, APIKey: "key", Model: llm.Model{Provider: "anthropic", Name: "test"}, ReasoningLevel: llm.ReasoningLevelOff})
		if err != nil {
			t.Fatalf("new llm: %v", err)
		}
		_, err = client.PredictNextStructured(context.Background(), llm.PredictNextStructuredRequest{Schema: &jsonschema.Schema{Type: "object"}})
		if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
			t.Fatalf("error = %v, want invalid JSON error", err)
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

	client, err := anthropic.NewLLM(anthropic.LLMConfig{
		BaseURL:        server.URL,
		APIKey:         "key",
		Model:          llm.Model{Provider: "anthropic", Name: "test"},
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

type fakeTool struct {
	name string
}

func (f fakeTool) Name() string {
	return f.name
}

func (f fakeTool) Description() string {
	return "fake tool"
}

func (f fakeTool) Parameters() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object"}
}
