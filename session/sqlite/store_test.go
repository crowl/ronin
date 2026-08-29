package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/session/sqlite"
)

func TestStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ronin.db")
	workingDir := t.TempDir()
	ctx := context.Background()
	createdAt := time.Date(2026, 6, 6, 12, 0, 0, 123, time.UTC)

	store, err := sqlite.Open(ctx, sqlite.StoreConfig{Path: path, Now: func() time.Time { return createdAt }})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	created, err := store.Create(ctx, workingDir, session.Metadata{
		Title:          "Durable session",
		Model:          config.Model{Provider: "openai", Name: "gpt"},
		ReasoningLevel: "high",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	wantMessages := []llm.Message{
		llm.UserMessage{Timestamp: createdAt, Text: "before restart"},
		llm.AssistantMessage{
			Timestamp: createdAt.Add(time.Second),
			Blocks: []llm.AssistantBlock{
				llm.TextBlock{Text: "reply"},
				llm.ToolCallBlock{ID: "call-1", Name: "shell", Arguments: []byte(`{"command":"go test ./..."}`)},
			},
			StopReason: llm.StopReasonToolUse,
			Usage:      llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15, Cost: llm.Cost{Total: 0.12, Available: true}},
		},
		llm.ToolOutputMessage{Timestamp: createdAt.Add(2 * time.Second), ToolName: "shell", ToolCallID: "call-1", ToolOutput: "ok"},
	}
	for _, message := range wantMessages {
		if err := store.Append(ctx, created.ID, session.Event{Type: session.EventMessage, Message: message}); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store, err = sqlite.Open(ctx, sqlite.StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("Open(reopen) error = %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(reopened) error = %v", err)
		}
	}()
	loaded, gotMessages, ok, err := store.Load(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("Load() ok = %v, error = %v", ok, err)
	}
	if loaded.ID != created.ID || loaded.Title != created.Title || loaded.Model != created.Model || loaded.ReasoningLevel != created.ReasoningLevel || !loaded.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("Load() session = %#v, want %#v", loaded, created)
	}
	if loaded.Cost != (llm.SessionCost{Total: 0.12, Available: true}) {
		t.Fatalf("Load() session cost = %#v", loaded.Cost)
	}
	if !reflect.DeepEqual(gotMessages, wantMessages) {
		t.Fatalf("Load() messages = %#v, want %#v", gotMessages, wantMessages)
	}
}

func TestStoreConcurrentAppendAcrossConnectionsPreservesEveryEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ronin.db")
	ctx := context.Background()
	first, err := sqlite.Open(ctx, sqlite.StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("Open(first) error = %v", err)
	}
	defer first.Close()
	second, err := sqlite.Open(ctx, sqlite.StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("Open(second) error = %v", err)
	}
	defer second.Close()
	created, err := first.Create(ctx, t.TempDir(), session.Metadata{})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	const count = 20
	errs := make(chan error, count)
	for i := range count {
		store := first
		if i%2 != 0 {
			store = second
		}
		go func(i int) {
			errs <- store.Append(ctx, created.ID, session.Event{
				Type:    session.EventMessage,
				Message: llm.UserMessage{Text: fmt.Sprintf("message %d", i)},
			})
		}(i)
	}
	for range count {
		if err := <-errs; err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}
	_, messages, ok, err := first.Load(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("Load() ok = %v, error = %v", ok, err)
	}
	if len(messages) != count {
		t.Fatalf("Load() message count = %d, want %d", len(messages), count)
	}
}

func TestStoreRoundTripAndCompaction(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock.Now)
	ctx := context.Background()
	workingDir := t.TempDir()

	created, err := store.Create(ctx, workingDir, session.Metadata{
		Title:          "Initial",
		Model:          config.Model{Provider: "openai", Name: "gpt"},
		ReasoningLevel: "medium",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	first := llm.UserMessage{Timestamp: clock.now, Text: "first"}
	if err := store.Append(ctx, created.ID, session.Event{Type: session.EventMessage, Message: first}); err != nil {
		t.Fatalf("Append(first) error = %v", err)
	}
	compacted := llm.UserMessage{Timestamp: clock.now, Text: "summary"}
	priced := llm.AssistantMessage{Timestamp: clock.now, Usage: llm.Usage{Cost: llm.Cost{Total: 0.25, Available: true}}}
	if err := store.Append(ctx, created.ID, session.Event{Type: session.EventMessage, Message: priced}); err != nil {
		t.Fatalf("Append(priced) error = %v", err)
	}
	if err := store.Append(ctx, created.ID, session.Event{Type: session.EventCompaction, Compacted: []llm.Message{compacted}}); err != nil {
		t.Fatalf("Append(compaction) error = %v", err)
	}
	last := llm.ErrorMessage{Timestamp: clock.now, Error: errors.New("after")}
	if err := store.Append(ctx, created.ID, session.Event{Type: session.EventMessage, Message: last}); err != nil {
		t.Fatalf("Append(last) error = %v", err)
	}

	loaded, messages, ok, err := store.Load(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("Load() ok = %v, error = %v", ok, err)
	}
	if loaded.ID != created.ID || loaded.Title != "Initial" || loaded.Model != created.Model {
		t.Fatalf("Load() session = %#v, want %#v", loaded, created)
	}
	if loaded.Cost != (llm.SessionCost{Total: 0.25, Available: true}) {
		t.Fatalf("Load() cost after compaction = %#v", loaded.Cost)
	}
	if len(messages) != 2 || messages[0].(llm.UserMessage).Text != "summary" || messages[1].(llm.ErrorMessage).Error.Error() != "after" {
		t.Fatalf("Load() messages = %#v, want compacted history", messages)
	}
}

func TestStoreLatestMetadataListDeleteAndClear(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock.Now)
	ctx := context.Background()
	workingDir := t.TempDir()
	otherDir := t.TempDir()

	first, err := store.Create(ctx, workingDir, session.Metadata{Title: "First"})
	if err != nil {
		t.Fatalf("Create(first) error = %v", err)
	}
	clock.Advance(time.Second)
	second, err := store.Create(ctx, workingDir, session.Metadata{Title: "Second"})
	if err != nil {
		t.Fatalf("Create(second) error = %v", err)
	}
	if _, err := store.Create(ctx, otherDir, session.Metadata{Title: "Other"}); err != nil {
		t.Fatalf("Create(other) error = %v", err)
	}

	clock.Advance(time.Second)
	if err := store.Append(ctx, first.ID, session.Event{Type: session.EventMessage, Message: llm.UserMessage{Text: "new activity"}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	latest, messages, ok, err := store.Latest(ctx, workingDir)
	if err != nil || !ok {
		t.Fatalf("Latest() ok = %v, error = %v", ok, err)
	}
	if latest.ID != first.ID || len(messages) != 1 {
		t.Fatalf("Latest() = %q, %d messages; want %q, 1", latest.ID, len(messages), first.ID)
	}

	clock.Advance(time.Second)
	metadata := session.Metadata{Title: "Renamed", Model: config.Model{Provider: "anthropic", Name: "claude"}, ReasoningLevel: "high"}
	if err := store.UpdateMetadata(ctx, first.ID, metadata); err != nil {
		t.Fatalf("UpdateMetadata() error = %v", err)
	}
	updated, _, ok, err := store.Load(ctx, first.ID)
	if err != nil || !ok {
		t.Fatalf("Load(updated) ok = %v, error = %v", ok, err)
	}
	if updated.Title != metadata.Title || updated.Model != metadata.Model || updated.ReasoningLevel != metadata.ReasoningLevel {
		t.Fatalf("updated metadata = %#v, want %#v", updated, metadata)
	}

	refs, err := store.List(ctx, workingDir)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(refs) != 2 || refs[0].ID != first.ID || refs[1].ID != second.ID {
		t.Fatalf("List() = %#v, want first then second", refs)
	}

	if err := store.Delete(ctx, first.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, _, ok, err := store.Load(ctx, first.ID); err != nil || ok {
		t.Fatalf("Load(deleted) ok = %v, error = %v", ok, err)
	}
	if err := store.Delete(ctx, first.ID); err != nil {
		t.Fatalf("Delete(missing) error = %v", err)
	}
	if err := store.Clear(ctx, workingDir); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if _, _, ok, err := store.Latest(ctx, workingDir); err != nil || ok {
		t.Fatalf("Latest(cleared) ok = %v, error = %v", ok, err)
	}
	if refs, err := store.List(ctx, otherDir); err != nil || len(refs) != 1 {
		t.Fatalf("List(other) = %#v, error = %v, want one", refs, err)
	}
}

func TestStoreLatestUsesSubMillisecondActivity(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock.Now)
	ctx := context.Background()
	workingDir := t.TempDir()

	first, err := store.Create(ctx, workingDir, session.Metadata{Title: "First"})
	if err != nil {
		t.Fatalf("Create(first) error = %v", err)
	}
	clock.Advance(100 * time.Nanosecond)
	second, err := store.Create(ctx, workingDir, session.Metadata{Title: "Second"})
	if err != nil {
		t.Fatalf("Create(second) error = %v", err)
	}
	clock.Advance(100 * time.Nanosecond)
	if err := store.Append(ctx, first.ID, session.Event{Type: session.EventMessage, Message: llm.UserMessage{Text: "latest"}}); err != nil {
		t.Fatalf("Append(first) error = %v", err)
	}

	latest, _, ok, err := store.Latest(ctx, workingDir)
	if err != nil || !ok {
		t.Fatalf("Latest() ok = %v, error = %v", ok, err)
	}
	if latest.ID != first.ID {
		t.Fatalf("Latest() ID = %q, want recently active session %q instead of %q", latest.ID, first.ID, second.ID)
	}
}

func TestStoreRejectsMissingSessionAppend(t *testing.T) {
	store := openStore(t, time.Now)
	err := store.Append(context.Background(), "missing", session.Event{Type: session.EventMessage, Message: llm.UserMessage{Text: "no"}})
	if err == nil {
		t.Fatal("Append(missing) error = nil")
	}
}

func TestStoreConcurrentAppendPreservesEveryEvent(t *testing.T) {
	store := openStore(t, time.Now)
	ctx := context.Background()
	created, err := store.Create(ctx, t.TempDir(), session.Metadata{})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	const count = 20
	errs := make(chan error, count)
	for i := range count {
		go func() {
			errs <- store.Append(ctx, created.ID, session.Event{
				Type:    session.EventMessage,
				Message: llm.UserMessage{Text: fmt.Sprintf("message %d", i)},
			})
		}()
	}
	for range count {
		if err := <-errs; err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	_, messages, ok, err := store.Load(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("Load() ok = %v, error = %v", ok, err)
	}
	if len(messages) != count {
		t.Fatalf("Load() message count = %d, want %d", len(messages), count)
	}
}

func openStore(t *testing.T, now func() time.Time) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(t.Context(), sqlite.StoreConfig{Path: filepath.Join(t.TempDir(), "ronin.db"), Now: now})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }
