package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/crowl/ronin/llm"
)

func TestToolFailureFinalization(t *testing.T) {
	for _, kind := range []string{"unknown", "execution", "marshal"} {
		t.Run(kind, func(t *testing.T) {
			c, err := NewConversation(ConversationConfig{ModelClient: &fakeStructuredModelClient{}})
			if err != nil {
				t.Fatal(err)
			}
			events := make(chan Event, 4)
			call := llm.ToolCallBlock{ID: "call", Name: "test"}
			want := "execution failed"
			switch kind {
			case "unknown":
				want = "not found"
				err = c.executeToolCall(t.Context(), events, call)
			case "execution":
				err = c.finishToolCall(t.Context(), events, nil, call, nil, errors.New(want))
			case "marshal":
				want = "marshal tool"
				err = c.finishToolCall(t.Context(), events, nil, call, make(chan int), nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			messages := c.Messages()
			if len(messages) != 1 {
				t.Fatalf("messages = %v", messages)
			}
			message, ok := messages[0].(llm.ToolErrorMessage)
			if !ok || message.ToolCallID != call.ID || !strings.Contains(message.Error.Error(), want) {
				t.Fatalf("message = %#v", messages[0])
			}
			if len(events) != 2 {
				t.Fatalf("events = %d", len(events))
			}
			if event, ok := (<-events).(ToolExecutionFailed); !ok || event.CallID != call.ID {
				t.Fatalf("failure = %#v", event)
			}
			if event, ok := (<-events).(ToolExecutionEnded); !ok || event.CallID != call.ID {
				t.Fatalf("end = %#v", event)
			}
		})
	}
}
