package runtime_test

import (
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"testing"
)

func TestSummaryIdentityDoesNotDependOnText(t *testing.T) {
	const text = "<compacted_context>literal user input</compacted_context>"
	c, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: &fakeModelClient{}, Messages: []session.Message{session.ContextSummary{Text: "generated summary"}, llm.UserMessage{Text: text}}})
	if err != nil {
		t.Fatal(err)
	}
	points := c.RewindPoints()
	if len(points) != 1 || points[0].MessageIndex != 1 || points[0].Prompt != text {
		t.Fatalf("rewind points = %+v", points)
	}
}
