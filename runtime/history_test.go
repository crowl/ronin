package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/session/sqlite"
)

func TestHistoryLineageAcrossReopenAndFork(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := sqlite.Open(ctx, sqlite.StoreConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	s, err := store.Create(ctx, t.TempDir(), session.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	requirement := llm.UserMessage{Text: "retained original requirement"}
	summary := llm.UserMessage{Text: "<compacted_context>summary</compacted_context>"}
	cutoff := llm.UserMessage{Text: "discard this branch"}
	events := []session.Event{
		{Type: session.EventMessage, Message: requirement},
		{Type: session.EventShellCommand, ShellCommand: &session.ShellCommandEntry{ID: "local", Command: "LOCAL_SECRET"}},
		{Type: session.EventCompaction, Compacted: []llm.Message{summary}},
		{Type: session.EventMessage, Message: cutoff},
		{Type: session.EventMessage, Message: llm.UserMessage{Text: "DISCARDED_SECRET"}},
	}
	for _, event := range events {
		if err := store.Append(ctx, s.ID, event); err != nil {
			t.Fatal(err)
		}
	}
	s, messages, _, err := store.Load(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewConversation(ConversationConfig{ModelClient: &fakeStructuredModelClient{}, SessionStore: store, Session: s, Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Rewind(ctx, RewindPoint{MessageIndex: 1, Prompt: cutoff.Text}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = sqlite.Open(ctx, sqlite.StoreConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	s, messages, _, err = store.Load(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	c, err = NewConversation(ConversationConfig{ModelClient: &fakeStructuredModelClient{}, SessionStore: store, Session: s, Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	assertHistory := func(c *Conversation) {
		t.Helper()
		result, err := (&historyTool{c}).Call(ctx, json.RawMessage(`{"limit":20}`))
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(result)
		if !strings.Contains(string(data), requirement.Text) {
			t.Fatalf("lost retained requirement: %s", data)
		}
		for _, secret := range []string{"LOCAL_SECRET", "DISCARDED_SECRET", cutoff.Text} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("leaked %q", secret)
			}
		}
	}
	assertHistory(c)
	forkPrompt := llm.UserMessage{Text: "fork here"}
	if err := c.appendMessage(ctx, forkPrompt); err != nil {
		t.Fatal(err)
	}
	if err := c.Fork(ctx, RewindPoint{MessageIndex: 1, Prompt: forkPrompt.Text}); err != nil {
		t.Fatal(err)
	}
	assertHistory(c)
	child, messages, _, err := store.Load(ctx, c.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	c, err = NewConversation(ConversationConfig{ModelClient: &fakeStructuredModelClient{}, SessionStore: store, Session: child, Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	assertHistory(c)
}

func TestHistoryPagingAndReferences(t *testing.T) {
	message := llm.UserMessage{Text: strings.Repeat("abc", 10000)}
	ref, want := historyMessage(message)
	c, _ := NewConversation(ConversationConfig{ModelClient: &fakeStructuredModelClient{}, Messages: []llm.Message{message}})
	var got strings.Builder
	offset := 0
	for {
		args, _ := json.Marshal(historyArgs{Reference: ref, Offset: offset})
		raw, err := (&historyTool{c}).Call(t.Context(), args)
		if err != nil {
			t.Fatal(err)
		}
		result := raw.(historyResult)
		if len(result.Entries) != 1 {
			t.Fatal(result)
		}
		entry := result.Entries[0]
		got.WriteString(entry.Content)
		offset = entry.NextOffset
		if offset == 0 {
			break
		}
	}
	if got.String() != want {
		t.Fatal("paging lost content")
	}
}

func TestCancelledKnownResultPersistsInSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.db")
	store, err := sqlite.Open(t.Context(), sqlite.StoreConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Create(t.Context(), t.TempDir(), session.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := NewConversation(ConversationConfig{ModelClient: &fakeStructuredModelClient{}, SessionStore: store, Session: s})
	call := llm.ToolCallBlock{ID: "mutation", Name: "shell"}
	if err := c.appendMessage(t.Context(), llm.AssistantMessage{Blocks: []llm.AssistantBlock{call}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_ = c.finishToolCall(ctx, make(chan Event), nil, call, map[string]bool{"success": true}, nil)
	store.Close()
	store, err = sqlite.Open(t.Context(), sqlite.StoreConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, messages, _, err := store.Load(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatal(messages)
	}
	if result, ok := messages[1].(llm.ToolOutputMessage); !ok || !strings.Contains(result.ToolOutput, "true") {
		t.Fatalf("lost result: %#v", messages[1])
	}
}
