package session_test

import (
	"reflect"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
)

func TestRedactedThinkingCodec(t *testing.T) {
	want := llm.AssistantMessage{
		Blocks: []llm.AssistantBlock{llm.RedactedThinkingBlock{Data: "opaque"}},
	}
	eventType, payload, err := session.EncodeEvent(session.Event{Type: session.EventMessage, Message: want})
	if err != nil {
		t.Fatalf("EncodeEvent() error = %v", err)
	}
	gotEvent, err := session.DecodeEvent(eventType, payload)
	if err != nil {
		t.Fatalf("DecodeEvent() error = %v", err)
	}
	if !reflect.DeepEqual(gotEvent.Message, want) {
		t.Fatalf("decoded message = %#v, want %#v", gotEvent.Message, want)
	}
}

func TestReconstruct(t *testing.T) {
	old := llm.UserMessage{Text: "old"}
	compacted := llm.UserMessage{Text: "compacted"}
	newMessage := llm.AssistantMessage{Blocks: []llm.AssistantBlock{llm.TextBlock{Text: "new"}}}

	tests := []struct {
		name   string
		events []session.Event
		want   []llm.Message
	}{
		{
			name: "plain messages",
			events: []session.Event{
				{Type: session.EventMessage, Message: old},
				{Type: session.EventMessage, Message: newMessage},
			},
			want: []llm.Message{old, newMessage},
		},
		{
			name: "compaction resets history",
			events: []session.Event{
				{Type: session.EventMessage, Message: old},
				{Type: session.EventCompaction, Compacted: []llm.Message{compacted}},
			},
			want: []llm.Message{compacted},
		},
		{
			name: "messages continue after compaction",
			events: []session.Event{
				{Type: session.EventMessage, Message: old},
				{Type: session.EventCompaction, Compacted: []llm.Message{compacted}},
				{Type: session.EventMessage, Message: newMessage},
			},
			want: []llm.Message{compacted, newMessage},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := session.Reconstruct(test.events)
			if len(got) != len(test.want) {
				t.Fatalf("Reconstruct() length = %d, want %d", len(got), len(test.want))
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Reconstruct() = %#v, want %#v", got, test.want)
			}
		})
	}
}
