package runtime_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
)

type unsupportedTranscriptEntry struct{}

func (unsupportedTranscriptEntry) TranscriptMessage() {}

type unexpectedCompactor struct{ calls int }

func (c *unexpectedCompactor) Compact(context.Context, llm.ModelClient, []session.Message) ([]session.Message, error) {
	c.calls++
	return nil, errors.New("unexpected compaction")
}

func TestInvalidTranscriptFailsBeforeAutomaticCompaction(t *testing.T) {
	client := &fakeModelClient{model: llm.Model{ContextWindow: 1024}}
	compactor := &unexpectedCompactor{}
	original := []session.Message{unsupportedTranscriptEntry{}, llm.UserMessage{Text: strings.Repeat("large context ", 4000)}}
	c, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client, Compactor: compactor, Messages: original})
	if err != nil {
		t.Fatal(err)
	}
	events, errs := c.Prompt(t.Context(), "continue")
	for range events {
	}
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "unsupported transcript entry") {
		t.Fatalf("Prompt error = %v", err)
	}
	if compactor.calls != 0 || client.predictCalls != 0 || client.structuredCalls != 0 {
		t.Fatalf("invalid transcript caused calls: compact=%d predict=%d structured=%d", compactor.calls, client.predictCalls, client.structuredCalls)
	}
	got := c.Messages()
	if len(got) != len(original)+1 || !reflect.DeepEqual(got[:len(original)], original) {
		t.Fatalf("original context replaced: %#v", got)
	}
}

func TestProviderReceivesProjectedTranscript(t *testing.T) {
	client := &fakeModelClient{events: []llm.PredictionEvent{llm.PredictionFinished{StopReason: llm.StopReasonEndTurn}}}
	result := session.WorkflowResultMessage{Name: "review", Summary: "approved", Status: session.WorkflowStatusCompleted}
	c, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client, Messages: []session.Message{session.ContextSummary{Text: "summary"}, result}})
	if err != nil {
		t.Fatal(err)
	}
	events, errs := c.Prompt(t.Context(), "continue")
	for range events {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests=%d", len(client.requests))
	}
	messages := client.requests[0].Messages
	for i, want := range []string{"summary", result.Text(), "continue"} {
		got, ok := messages[i].(llm.UserMessage)
		if !ok || got.Text != want {
			t.Fatalf("provider message %d = %#v", i, messages[i])
		}
	}
	if _, ok := c.Messages()[0].(session.ContextSummary); !ok {
		t.Fatal("projection replaced stored summary")
	}
}
