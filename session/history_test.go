package session_test

import (
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
	"reflect"
	"testing"
)

func TestHistoryProjectionsHaveDistinctVisibility(t *testing.T) {
	original := llm.UserMessage{Text: "original"}
	discarded := llm.UserMessage{Text: "discarded"}
	summary := session.ContextSummary{Text: "summary"}
	shell := session.Event{Type: session.EventShellCommand, ShellCommand: &session.ShellCommandEntry{ID: "local", Command: "secret"}}
	events := []session.Event{
		{Type: session.EventMessage, Message: original}, shell,
		{Type: session.EventCompaction, Compacted: []session.Message{summary}},
		{Type: session.EventMessage, Message: discarded},
		{Type: session.EventContextReset, Compacted: []session.Message{summary}, RetainedHistory: []session.Message{original}},
	}
	if got := session.Reconstruct(events); !reflect.DeepEqual(got, []session.Message{summary}) {
		t.Fatalf("context = %#v", got)
	}
	if got := session.RetainedHistory(events); !reflect.DeepEqual(got, []session.Message{original, summary}) {
		t.Fatalf("archive = %#v", got)
	}
	wantDisplay := []session.Event{shell, {Type: session.EventMessage, Message: summary}}
	if got := session.DisplayHistory(events); !reflect.DeepEqual(got, wantDisplay) {
		t.Fatalf("display = %#v", got)
	}
	// A fork starts with its own reset, not its parent's local audit.
	if got := session.DisplayHistory(events[len(events)-1:]); !reflect.DeepEqual(got, wantDisplay[1:]) {
		t.Fatalf("fork display = %#v", got)
	}
}
