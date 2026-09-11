package anthropic_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/anthropic"
)

func TestPredictNextGroupsConsecutiveToolResults(t *testing.T) {
	first := llm.ToolOutputMessage{ToolCallID: "call-1", ToolOutput: "first"}
	second := llm.ToolOutputMessage{ToolCallID: "call-2", ToolOutput: "second"}
	failed := llm.ToolErrorMessage{ToolCallID: "call-3", Error: errors.New("oops")}
	result1 := map[string]any{"type": "tool_result", "tool_use_id": "call-1", "content": "first"}
	result2 := map[string]any{"type": "tool_result", "tool_use_id": "call-2", "content": "second"}
	result3 := map[string]any{"type": "tool_result", "tool_use_id": "call-3", "content": "error: oops", "is_error": true}
	message := func(role string, blocks ...any) any {
		return map[string]any{"role": role, "content": blocks}
	}
	text := func(value string) any { return map[string]any{"type": "text", "text": value} }

	tests := []struct {
		name     string
		messages []llm.Message
		want     []any
	}{
		{"single", []llm.Message{first}, []any{message("user", result1)}},
		{"successes", []llm.Message{first, second}, []any{message("user", result1, result2)}},
		{"mixed", []llm.Message{first, failed, second}, []any{message("user", result1, result3, result2)}},
		{"error first", []llm.Message{failed, first}, []any{message("user", result3, result1)}},
		{"user boundary", []llm.Message{first, llm.UserMessage{Text: "next"}, second}, []any{message("user", result1), message("user", text("next")), message("user", result2)}},
		{"assistant boundary", []llm.Message{first, llm.AssistantMessage{Blocks: []llm.AssistantBlock{llm.TextBlock{Text: "next"}}}, second}, []any{message("user", result1), message("assistant", text("next")), message("user", result2)}},
		{"empty assistant boundary", []llm.Message{first, llm.AssistantMessage{}, second}, []any{message("user", result1), message("user", result2)}},
		{"error boundary", []llm.Message{first, llm.ErrorMessage{Error: errors.New("stopped")}, second}, []any{message("user", result1), message("user", text("error: stopped")), message("user", result2)}},
		{"flush before trailing text", []llm.Message{first, second, llm.UserMessage{Text: "next"}}, []any{message("user", result1, result2), message("user", text("next"))}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body struct {
				Messages []any `json:"messages"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"message_start\"}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
			}))
			defer server.Close()
			client, err := anthropic.NewLLM(anthropic.LLMConfig{
				BaseURL: server.URL, APIKey: "key",
				Model: llm.Model{Provider: "anthropic", Name: "test"}, ReasoningLevel: llm.ReasoningLevelOff,
			})
			if err != nil {
				t.Fatalf("new llm: %v", err)
			}
			events, errs := client.PredictNext(t.Context(), llm.PredictNextRequest{Messages: tt.messages})
			_ = drainEvents(events)
			if err := <-errs; err != nil {
				t.Fatalf("predict: %v", err)
			}
			if !reflect.DeepEqual(body.Messages, tt.want) {
				got, _ := json.Marshal(body.Messages)
				want, _ := json.Marshal(tt.want)
				t.Fatalf("messages = %s, want %s", got, want)
			}
		})
	}
}
