package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/llm"
)

func TestDefaultCompactor(t *testing.T) {
	t.Run("replaces older messages with compacted context and keeps recent messages", func(t *testing.T) {
		assistant := &fakeStructuredAssistant{raw: json.RawMessage(`{
			"current_goal":"Implement compaction",
			"user_preferences":["Use structured output"],
			"decisions":["Keep recent messages"],
			"files_and_code_state":["runtime/compaction.go changed"],
			"tests_and_tool_results":["go test pending"],
			"open_tasks":["Run full suite"],
			"recovery":["Resume from compacted context"]
		}`)}
		wantTime := time.Unix(1700000000, 0)
		compactor, err := NewDefaultCompactor(DefaultCompactorConfig{
			LLM: assistant,
			Now: func() time.Time { return wantTime },
		})
		if err != nil {
			t.Fatalf("NewDefaultCompactor() error = %v", err)
		}

		messages := makeCompactionMessages(14)
		got, err := compactor.Compact(context.Background(), messages)
		if err != nil {
			t.Fatalf("Compact() error = %v", err)
		}

		if len(got) != 13 {
			t.Fatalf("len(compacted) = %d, want 13", len(got))
		}
		compacted, ok := got[0].(llm.UserMessage)
		if !ok {
			t.Fatalf("first message = %T, want llm.UserMessage", got[0])
		}
		if !compacted.Timestamp.Equal(wantTime) {
			t.Fatalf("timestamp = %v, want %v", compacted.Timestamp, wantTime)
		}
		for _, want := range []string{
			"<compacted_context>",
			"# Current Goal",
			"Implement compaction",
			"# User Preferences",
			"- Use structured output",
			"# Automatically Preserved Facts",
			"Message count being compacted: 2",
		} {
			if !strings.Contains(compacted.Text, want) {
				t.Fatalf("compacted text missing %q:\n%s", want, compacted.Text)
			}
		}
		for i := 1; i < len(got); i++ {
			if got[i] != messages[i+1] {
				t.Fatalf("message %d was not preserved", i)
			}
		}
		if assistant.lastRequest.Schema == nil {
			t.Fatal("structured schema = nil, want schema")
		}
		if len(assistant.lastRequest.Messages) != 1 {
			t.Fatalf("structured messages = %d, want 1", len(assistant.lastRequest.Messages))
		}
		prompt := assistant.lastRequest.Messages[0].(llm.UserMessage).Text
		if !strings.Contains(prompt, "Source facts") || !strings.Contains(prompt, "message 1") {
			t.Fatalf("structured prompt missing fact sheet:\n%s", prompt)
		}
	})

	t.Run("keeps_tool_call_with_recent_tool_result", func(t *testing.T) {
		assistant := &fakeStructuredAssistant{raw: json.RawMessage(`{"current_goal":"goal"}`)}
		compactor, err := NewDefaultCompactor(DefaultCompactorConfig{LLM: assistant})
		if err != nil {
			t.Fatalf("NewDefaultCompactor() error = %v", err)
		}

		call := llm.AssistantMessage{Blocks: []llm.AssistantBlock{llm.ToolCallBlock{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{}`)}}}
		result := llm.ToolOutputMessage{ToolCallID: "call-1", ToolName: "read", ToolOutput: `{"ok":true}`}
		messages := []llm.Message{llm.UserMessage{Text: "message 1"}, call}
		messages = append(messages, makeCompactionMessages(11)...)
		messages = append(messages, result)

		got, err := compactor.Compact(context.Background(), messages)
		if err != nil {
			t.Fatalf("Compact() error = %v", err)
		}
		gotCall, ok := got[1].(llm.AssistantMessage)
		if !ok || len(gotCall.Blocks) != 1 {
			t.Fatalf("first recent message = %#v, want assistant tool call", got[1])
		}
		gotToolCall, ok := gotCall.Blocks[0].(llm.ToolCallBlock)
		if !ok || gotToolCall.ID != "call-1" {
			t.Fatalf("first recent message = %#v, want tool call preserved with result", got[1])
		}
		if got[len(got)-1] != result {
			t.Fatalf("last recent message = %#v, want tool result", got[len(got)-1])
		}
	})

	t.Run("returns_error_when_not_enough_messages", func(t *testing.T) {
		assistant := &fakeStructuredAssistant{raw: json.RawMessage(`{"current_goal":"goal"}`)}
		compactor, err := NewDefaultCompactor(DefaultCompactorConfig{LLM: assistant})
		if err != nil {
			t.Fatalf("NewDefaultCompactor() error = %v", err)
		}

		_, err = compactor.Compact(context.Background(), makeCompactionMessages(12))
		if err == nil || !strings.Contains(err.Error(), "not enough") {
			t.Fatalf("Compact() error = %v, want not enough error", err)
		}
	})

	t.Run("propagates_structured_prediction_error", func(t *testing.T) {
		wantErr := errors.New("structured failed")
		assistant := &fakeStructuredAssistant{err: wantErr}
		compactor, err := NewDefaultCompactor(DefaultCompactorConfig{LLM: assistant})
		if err != nil {
			t.Fatalf("NewDefaultCompactor() error = %v", err)
		}

		_, err = compactor.Compact(context.Background(), makeCompactionMessages(14))
		if !errors.Is(err, wantErr) {
			t.Fatalf("Compact() error = %v, want wrapped structured error", err)
		}
	})
}

func makeCompactionMessages(count int) []llm.Message {
	messages := make([]llm.Message, 0, count)
	for i := range count {
		messages = append(messages, llm.UserMessage{Text: "message " + string(rune('1'+i))})
	}
	return messages
}

type fakeStructuredAssistant struct {
	raw         json.RawMessage
	err         error
	lastRequest llm.PredictNextStructuredRequest
}

func (f *fakeStructuredAssistant) Model() llm.Model {
	return llm.Model{Provider: "fake", Name: "structured"}
}

func (f *fakeStructuredAssistant) ReasoningLevel() llm.ReasoningLevel {
	return llm.ReasoningLevelOff
}

func (f *fakeStructuredAssistant) SetReasoningLevel(llm.ReasoningLevel) error {
	return nil
}

func (f *fakeStructuredAssistant) PredictNext(context.Context, llm.PredictNextRequest) (<-chan llm.PredictionEvent, <-chan error) {
	panic("not implemented")
}

func (f *fakeStructuredAssistant) PredictNextStructured(_ context.Context, req llm.PredictNextStructuredRequest) (json.RawMessage, error) {
	f.lastRequest = req
	if f.err != nil {
		return nil, f.err
	}
	return f.raw, nil
}
