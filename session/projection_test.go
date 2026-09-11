package session_test

import (
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
	"reflect"
	"testing"
)

func TestModelMessages(t *testing.T) {
	workflow := session.WorkflowResultMessage{Name: "review", Summary: "done", Status: session.WorkflowStatusCompleted}
	messages := []session.Message{session.ContextSummary{Text: "summary"}, workflow, llm.UserMessage{Text: "request"}}
	want := []llm.Message{llm.UserMessage{Text: "summary"}, llm.UserMessage{Text: workflow.Text()}, llm.UserMessage{Text: "request"}}
	got, err := session.ModelMessages(messages)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("model messages = %#v", got)
	}
	if _, ok := messages[0].(session.ContextSummary); !ok {
		t.Fatal("projection mutated transcript")
	}
	if _, err := session.ModelMessages([]session.Message{unknownEntry{}}); err == nil {
		t.Fatal("unknown transcript entry silently accepted")
	}
}

type unknownEntry struct{}

func (unknownEntry) TranscriptMessage() {}
