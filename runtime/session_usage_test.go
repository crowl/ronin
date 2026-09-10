package runtime_test

import (
	"path/filepath"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/session/sqlite"
)

func TestSessionUsageLifecycle(t *testing.T) {
	store, err := sqlite.Open(t.Context(), sqlite.StoreConfig{Path: filepath.Join(t.TempDir(), "usage.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	saved, err := store.Create(t.Context(), t.TempDir(), session.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	first := llm.Usage{InputTokens: 100, OutputTokens: 10, CachedTokens: 40, CacheWriteTokens: 20, TotalTokens: 110}
	second := llm.Usage{InputTokens: 200, OutputTokens: 30, CachedTokens: 80, TotalTokens: 230}
	client := &fakeModelClient{eventBatches: [][]llm.PredictionEvent{
		{llm.BlockEnded{Block: llm.ToolCallBlock{ID: "call", Name: "missing"}}, llm.PredictionFinished{Usage: first}},
		{llm.PredictionFinished{Usage: second}},
		{llm.PredictionFinished{Usage: first}},
	}}
	conv, err := runtime.NewConversation(runtime.ConversationConfig{CWD: t.TempDir(), ModelClient: client, SessionStore: store, Session: saved, SessionCost: saved.Cost, Compactor: &fakeCompactor{messages: []llm.Message{llm.UserMessage{Text: "summary"}}}})
	if err != nil {
		t.Fatal(err)
	}
	prompt := func(text string) {
		t.Helper()
		events, errs := conv.Prompt(t.Context(), text)
		_ = collectEvents(events)
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	assertTokens := func(c *runtime.Conversation, want llm.Usage) {
		t.Helper()
		got := c.SessionUsage()
		got.Cost = llm.Cost{}
		if got != want {
			t.Fatalf("session usage = %+v, want %+v", got, want)
		}
	}
	prompt("one")
	assertTokens(conv, llm.Usage{InputTokens: 300, OutputTokens: 40, CachedTokens: 120, CacheWriteTokens: 20, TotalTokens: 340})
	prompt("two")
	want := llm.Usage{InputTokens: 400, OutputTokens: 50, CachedTokens: 160, CacheWriteTokens: 40, TotalTokens: 450}
	assertTokens(conv, want)
	if got := conv.ContextUsage(); got.InputTokens != 100 || got.OutputTokens != 10 {
		t.Fatalf("context = %+v", got)
	}
	aux := llm.Usage{InputTokens: 50, OutputTokens: 5, CachedTokens: 10, TotalTokens: 55}
	if err := conv.RecordStructuredUsage(t.Context(), llm.StructuredUsage{Usage: &aux}); err != nil {
		t.Fatal(err)
	}
	want = llm.Usage{InputTokens: 450, OutputTokens: 55, CachedTokens: 170, CacheWriteTokens: 40, TotalTokens: 505}
	if err := conv.Rewind(t.Context(), conv.RewindPoints()[1]); err != nil {
		t.Fatal(err)
	}
	assertTokens(conv, want)
	if err := conv.CompactConversation(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertTokens(conv, want)
	loaded, messages, found, err := store.Load(t.Context(), saved.ID)
	if err != nil || !found {
		t.Fatalf("load: found=%v err=%v", found, err)
	}
	resumed, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client, Session: loaded, Messages: messages, SessionCost: loaded.Cost, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	assertTokens(resumed, want)
	if err := resumed.Fork(t.Context(), resumed.RewindPoints()[0]); err != nil {
		t.Fatal(err)
	}
	assertTokens(resumed, llm.Usage{})
	if err := conv.NewConversation(); err != nil {
		t.Fatal(err)
	}
	assertTokens(conv, llm.Usage{})
}

func TestSessionUsageFromMessages(t *testing.T) {
	conv, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: &fakeModelClient{}, Messages: []llm.Message{
		llm.AssistantMessage{Usage: llm.Usage{InputTokens: 10, OutputTokens: 2}},
		llm.AssistantMessage{Usage: llm.Usage{InputTokens: 20, OutputTokens: 3}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := conv.SessionUsage(); got.InputTokens != 30 || got.OutputTokens != 5 {
		t.Fatalf("usage = %+v", got)
	}
}
