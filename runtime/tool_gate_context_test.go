package runtime

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
)

func TestToolGateContextKeepsConversationOverToolOutput(t *testing.T) {
	big := strings.Repeat("x", 4<<10)
	messages := []session.Message{
		llm.UserMessage{Text: "refactor the parser"},
		llm.AssistantMessage{Blocks: []llm.AssistantBlock{
			llm.ThinkingBlock{Text: "need to see the parser first"},
			llm.TextBlock{Text: "Let me look at the parser."},
			llm.ToolCallBlock{ID: "call-1", Name: "read_file", Arguments: json.RawMessage(`{"path":"parser.go"}`)},
		}},
	}
	for i := 0; i < 40; i++ {
		messages = append(messages,
			llm.ToolOutputMessage{ToolName: "read_file", ToolOutput: big},
			llm.AssistantMessage{Blocks: []llm.AssistantBlock{llm.ToolCallBlock{ID: "loop", Name: "read_file", Arguments: json.RawMessage(`{"path":"other.go"}`)}}},
		)
	}
	messages = append(messages,
		llm.UserMessage{Text: "yes, do it"},
		llm.AssistantMessage{Blocks: []llm.AssistantBlock{llm.ToolCallBlock{ID: "call-2", Name: "edit_file", Arguments: json.RawMessage(`{"path":"parser.go"}`)}}},
	)
	c, err := NewConversation(ConversationConfig{ModelClient: &fakeStructuredModelClient{}, Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	data := c.toolGateContext("call-2")
	if len(data) > toolGateContextBytes {
		t.Fatalf("context is %d bytes, budget %d", len(data), toolGateContextBytes)
	}
	var got []toolGateContextMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Role != "user" || got[0].Text != "refactor the parser" {
		t.Fatalf("first entry = %+v, want original request", got[0])
	}
	var thinking, pending, outputs int
	for _, m := range got {
		switch {
		case m.Role == "assistant_thinking":
			thinking++
		case m.Pending:
			pending++
			if m.Name != "edit_file" {
				t.Fatalf("pending call = %+v", m)
			}
		case m.Role == "tool_output":
			outputs++
			if !strings.HasPrefix(m.Text, strings.Repeat("x", toolGateContextOutputBytes)) || !strings.HasSuffix(m.Text, "[... truncated]") || len(m.Text) > toolGateContextOutputBytes+32 {
				t.Fatalf("tool output = %q, want head-only truncation", m.Text)
			}
		}
	}
	if thinking != 1 || pending != 1 {
		t.Fatalf("thinking = %d, pending = %d", thinking, pending)
	}
	if outputs == 0 {
		t.Fatal("expected some tool output in remaining budget")
	}
	last := got[len(got)-1]
	if !last.Pending {
		t.Fatalf("last entry = %+v, want pending call", last)
	}
}

func TestSelectToolGateEntriesDropsOldestConversationWhenOverBudget(t *testing.T) {
	entry := func(role, text string, output bool) toolGateEntry {
		return toolGateEntry{message: toolGateContextMessage{Role: role, Text: text}, output: output}
	}
	entries := []toolGateEntry{
		entry("user", strings.Repeat("a", 100), false),
		entry("tool_output", strings.Repeat("o", 40), true),
		entry("user", strings.Repeat("b", 100), false),
		entry("assistant", strings.Repeat("c", 100), false),
	}
	got := selectToolGateEntries(entries, 300)
	if len(got) != 2 || got[0].Role != "user" || got[0].Text[0] != 'b' || got[1].Role != "assistant" {
		t.Fatalf("selected = %+v", got)
	}
}

func TestToolGateContextKeepsToolErrorsOverOutput(t *testing.T) {
	messages := []session.Message{
		llm.UserMessage{Text: "run tests"},
		llm.AssistantMessage{Blocks: []llm.AssistantBlock{llm.ToolCallBlock{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{"command":"go test"}`)}}},
		llm.ToolErrorMessage{ToolName: "shell", Error: errors.New("denied")},
	}
	if entries := toolGateContextEntries(messages[2].(llm.Message), ""); len(entries) != 1 || entries[0].output {
		t.Fatalf("tool error entries = %+v, want conversation tier", entries)
	}
	c, err := NewConversation(ConversationConfig{ModelClient: &fakeStructuredModelClient{}, Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	var got []toolGateContextMessage
	if err := json.Unmarshal(c.toolGateContext(""), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].Role != "tool_error" || got[2].Text != "denied" {
		t.Fatalf("context = %+v", got)
	}
}

func TestTruncateHeadRespectsRuneBoundary(t *testing.T) {
	if got := truncateHead("abc", 10); got != "abc" {
		t.Fatalf("short = %q", got)
	}
	got := truncateHead("aé", 2)
	if !strings.HasPrefix(got, "a\n[... truncated]") || !utf8.ValidString(got) {
		t.Fatalf("cut = %q", got)
	}
}
