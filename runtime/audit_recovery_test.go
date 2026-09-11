package runtime

import (
	"context"
	"github.com/crowl/ronin/llm"
	"strings"
	"testing"
)

func TestCompactionRetainsLongRequirementsAndResultTail(t *testing.T) {
	facts := buildCompactionFactSheet([]llm.Message{
		llm.UserMessage{Text: strings.Repeat("requirement detail ", 100) + "NEVER DELETE CUSTOMER DATA"},
		llm.ToolOutputMessage{ToolName: "shell", ToolOutput: strings.Repeat("test output ", 1000) + "FAIL: regression_test"},
	}, "")
	for _, want := range []string{"NEVER DELETE CUSTOMER DATA", "FAIL: regression_test", "content omitted"} {
		if !strings.Contains(facts, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestCancelledToolRetainsKnownOutcome(t *testing.T) {
	c, err := NewConversation(ConversationConfig{ModelClient: &fakeStructuredModelClient{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	events := make(chan Event)
	_ = c.finishToolCall(ctx, events, nil, llm.ToolCallBlock{ID: "mutation", Name: "shell"}, map[string]bool{"success": true}, nil)
	messages := c.Messages()
	if len(messages) != 1 {
		t.Fatalf("messages = %v", messages)
	}
	if _, ok := messages[0].(llm.ToolOutputMessage); !ok {
		t.Fatalf("lost known result: %v", messages)
	}
	if !strings.Contains(interruptedToolCallErrorText, "side effects may have occurred") {
		t.Fatal("recovery must warn about uncertain effects")
	}
}
