package session_test

import (
	"github.com/crowl/ronin/session"
	"reflect"
	"testing"
)

func TestSummaryRoundTrip(t *testing.T) {
	summary := session.ContextSummary{Text: "summary"}
	for _, event := range []session.Event{{Type: session.EventMessage, Message: summary}, {Type: session.EventCompaction, Compacted: []session.Message{summary}}} {
		kind, data, err := session.EncodeEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		got, err := session.DecodeEvent(kind, data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(session.Reconstruct([]session.Event{got}), []session.Message{summary}) {
			t.Fatalf("summary identity lost: %+v", got)
		}
	}
}
