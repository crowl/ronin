package session_test

import (
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
	"reflect"
	"testing"
)

func TestAccountingLiveAndReplay(t *testing.T) {
	billed := llm.AssistantMessage{Usage: llm.Usage{InputTokens: 10, OutputTokens: 2, Cost: llm.Cost{Total: 0.25, Available: true}}}
	events := []session.Event{
		{Type: session.EventMessage, Message: billed},
		{Type: session.EventCompaction, Compacted: []session.Message{billed}},
		{Type: session.EventContextReset, Compacted: []session.Message{billed}},
		{Type: session.EventUsage, Usage: &llm.StructuredUsage{Usage: &llm.Usage{InputTokens: 3, Cost: llm.Cost{Total: 0.5, Available: true}}}},
		{Type: session.EventUsage, Usage: &llm.StructuredUsage{}},
	}
	live := session.NewAccounting()
	for i, event := range events {
		live.Apply(event)
		if replay := session.Account(events[:i+1]); !reflect.DeepEqual(live, replay) {
			t.Fatalf("prefix %d: live %+v != replay %+v", i, live, replay)
		}
	}
	if live.Tokens.InputTokens != 13 || live.Tokens.OutputTokens != 2 || live.Cost.Total != .75 || live.Cost.Available {
		t.Fatalf("accounting = %+v", live)
	}
}
