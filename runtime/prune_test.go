package runtime

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/session/sqlite"
)

func toolTurn(id string, output string) []session.Message {
	return []session.Message{
		llm.AssistantMessage{Blocks: []llm.AssistantBlock{llm.ToolCallBlock{ID: id, Name: "read_file", Arguments: json.RawMessage(`{}`)}}},
		llm.ToolOutputMessage{ToolCallID: id, ToolName: "read_file", ToolOutput: output},
	}
}

func TestPrunableToolResultsKeepRecentTurnsAndSmallResults(t *testing.T) {
	large := strings.Repeat("x", pruneMinResultBytes)
	var messages []session.Message
	messages = append(messages, llm.UserMessage{Text: "one"})
	messages = append(messages, toolTurn("old-large", large)...)
	messages = append(messages, toolTurn("old-small", "tiny")...)
	messages = append(messages, llm.UserMessage{Text: "two"})
	messages = append(messages, toolTurn("previous", large)...)
	messages = append(messages, llm.UserMessage{Text: "three"})
	messages = append(messages, toolTurn("current", large)...)

	got := prunableToolResults(messages)
	if len(got) != 1 || messages[got[0]].(llm.ToolOutputMessage).ToolCallID != "old-large" {
		t.Fatalf("prunable = %v", got)
	}
	if got := prunableToolResults(messages[7:]); got != nil {
		t.Fatalf("fewer than %d user turns must prune nothing: %v", pruneKeepUserTurns, got)
	}
}

func TestShouldPruneRequiresEnoughReclaimableBytes(t *testing.T) {
	build := func(n int) []session.Message {
		messages := []session.Message{llm.UserMessage{Text: "one"}}
		for i := range n {
			messages = append(messages, toolTurn("call-"+string(rune('a'+i)), strings.Repeat("x", 8<<10))...)
		}
		return append(messages, llm.UserMessage{Text: "two"}, llm.UserMessage{Text: "three"})
	}
	small, _ := NewConversation(ConversationConfig{ModelClient: &fakeStructuredModelClient{}, Messages: build(2)})
	if small.shouldPrune() {
		t.Fatal("16 KiB reclaimable must not trigger a cache-invalidating prune")
	}
	enough, _ := NewConversation(ConversationConfig{ModelClient: &fakeStructuredModelClient{}, Messages: build(10)})
	if !enough.shouldPrune() {
		t.Fatal("80 KiB of stale results should be pruned")
	}
}

func TestPruneContextStubsAreRecoverableFromHistory(t *testing.T) {
	ctx := t.Context()
	store, err := sqlite.Open(ctx, sqlite.StoreConfig{Path: filepath.Join(t.TempDir(), "prune.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	s, err := store.Create(ctx, t.TempDir(), session.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	original := "SECRET_FILE_BODY " + strings.Repeat("y", 70<<10)
	messages := []session.Message{llm.UserMessage{Text: "one"}}
	messages = append(messages, toolTurn("stale", original)...)
	messages = append(messages, llm.UserMessage{Text: "two"}, llm.UserMessage{Text: "three"})
	for _, message := range messages {
		if err := store.Append(ctx, s.ID, session.Event{Type: session.EventMessage, Message: message}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := NewConversation(ConversationConfig{ModelClient: &fakeStructuredModelClient{}, SessionStore: store, Session: s, Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	if !c.shouldPrune() {
		t.Fatal("expected prune")
	}
	if err := c.pruneContext(ctx); err != nil {
		t.Fatal(err)
	}
	if len(c.messages) != len(messages) {
		t.Fatalf("pruning changed message count: %d != %d", len(c.messages), len(messages))
	}
	var stub prunedToolOutput
	output := c.messages[2].(llm.ToolOutputMessage)
	if err := json.Unmarshal([]byte(output.ToolOutput), &stub); err != nil || !stub.Pruned || stub.Tool != "read_file" || stub.Bytes != len(original) || stub.HistoryRef == "" {
		t.Fatalf("stub = %s (%v)", output.ToolOutput, err)
	}
	if c.shouldPrune() {
		t.Fatal("stubs must not be pruned again")
	}
	args, _ := json.Marshal(historyArgs{Reference: stub.HistoryRef})
	raw, err := (&historyTool{c}).Call(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	result := raw.(historyResult)
	if len(result.Entries) != 1 || !strings.Contains(result.Entries[0].Content, "SECRET_FILE_BODY") {
		t.Fatalf("history lost pruned result: %+v", result)
	}
	_, reloaded, _, err := store.Load(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded[2].(llm.ToolOutputMessage).ToolOutput != output.ToolOutput {
		t.Fatal("pruned context not persisted")
	}
}
