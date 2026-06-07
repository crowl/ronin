package fs_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/session/fs"
)

func TestStoreLoadActiveMissing(t *testing.T) {
	store := fs.NewStore(fs.StoreConfig{
		Dir: t.TempDir(),
		Now: fixedNow(time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)),
	})

	got, ok, err := store.LoadActive(t.TempDir())
	if err != nil {
		t.Fatalf("LoadActive() error = %v", err)
	}
	if ok {
		t.Fatalf("LoadActive() ok = true, session = %#v", got)
	}
}

func TestStoreCreateSaveLoadActive(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	workingDir := t.TempDir()
	store := fs.NewStore(fs.StoreConfig{
		Dir: t.TempDir(),
		Now: fixedNow(now),
	})

	created, err := store.Create(workingDir, session.Metadata{
		Title:          "Initial session",
		Model:          config.Model{Provider: "openai", Name: "gpt-5.5"},
		ReasoningLevel: "medium",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create() returned empty session ID")
	}
	if created.Title != "Initial session" {
		t.Fatalf("Create() title = %q, want Initial session", created.Title)
	}
	if !created.CreatedAt.Equal(now) || !created.UpdatedAt.Equal(now) {
		t.Fatalf("Create() timestamps = %v/%v, want %v", created.CreatedAt, created.UpdatedAt, now)
	}

	created.Messages = sampleMessages(now)
	created.UpdatedAt = now.Add(time.Minute)
	if err := store.Save(created); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, ok, err := store.LoadActive(workingDir)
	if err != nil {
		t.Fatalf("LoadActive() error = %v", err)
	}
	if !ok {
		t.Fatal("LoadActive() ok = false, want true")
	}
	assertSessionEqual(t, loaded, created)
}

func TestStoreCreateMakesNewestSessionActive(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	workingDir := t.TempDir()
	store := fs.NewStore(fs.StoreConfig{
		Dir: t.TempDir(),
		Now: fixedNow(now),
	})

	first, err := store.Create(workingDir, session.Metadata{Title: "First"})
	if err != nil {
		t.Fatalf("Create(first) error = %v", err)
	}
	second, err := store.Create(workingDir, session.Metadata{Title: "Second"})
	if err != nil {
		t.Fatalf("Create(second) error = %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("Create() returned duplicate session IDs")
	}

	loaded, ok, err := store.LoadActive(workingDir)
	if err != nil {
		t.Fatalf("LoadActive() error = %v", err)
	}
	if !ok {
		t.Fatal("LoadActive() ok = false, want true")
	}
	if loaded.ID != second.ID {
		t.Fatalf("active session ID = %q, want newest session %q", loaded.ID, second.ID)
	}
}

func TestStoreClear(t *testing.T) {
	workingDir := t.TempDir()
	store := fs.NewStore(fs.StoreConfig{
		Dir: t.TempDir(),
		Now: fixedNow(time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)),
	})

	if _, err := store.Create(workingDir, session.Metadata{Title: "Clear me"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.Clear(workingDir); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	got, ok, err := store.LoadActive(workingDir)
	if err != nil {
		t.Fatalf("LoadActive() after Clear error = %v", err)
	}
	if ok {
		t.Fatalf("LoadActive() after Clear ok = true, session = %#v", got)
	}
}

func TestStoreSaveRejectsInvalidSession(t *testing.T) {
	store := fs.NewStore(fs.StoreConfig{
		Dir: t.TempDir(),
		Now: fixedNow(time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)),
	})

	err := store.Save(session.Session{WorkingDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "session id") {
		t.Fatalf("Save() error = %v, want session id error", err)
	}
}

func sampleMessages(now time.Time) []llm.Message {
	return []llm.Message{
		llm.UserMessage{Timestamp: now, Text: "hello"},
		llm.AssistantMessage{
			Timestamp: now.Add(time.Second),
			Blocks: []llm.AssistantBlock{
				llm.TextBlock{Text: "hi"},
				llm.ThinkingBlock{Text: "thinking", Signature: "sig"},
				llm.ToolCallBlock{ID: "call-1", Name: "read_file", Arguments: json.RawMessage(`{"path":"main.go"}`), ThoughtSignature: "tool-sig"},
			},
			StopReason: llm.StopReasonToolUse,
			Usage: llm.Usage{
				InputTokens:  10,
				OutputTokens: 20,
				CachedTokens: 3,
				TotalTokens:  33,
				Cost: llm.Cost{
					InputTokens:      1.1,
					OutputTokens:     2.2,
					CacheReadTokens:  0.3,
					CacheWriteTokens: 0.4,
					TotalTokens:      4,
				},
			},
		},
		llm.ToolOutputMessage{Timestamp: now.Add(2 * time.Second), ToolName: "read_file", ToolCallID: "call-1", ToolOutput: "contents"},
		llm.ToolErrorMessage{Timestamp: now.Add(3 * time.Second), ToolName: "shell", ToolCallID: "call-2", Error: errors.New("tool failed")},
		llm.ErrorMessage{Timestamp: now.Add(4 * time.Second), Error: errors.New("agent failed")},
	}
}

func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func assertSessionEqual(t *testing.T, got, want session.Session) {
	t.Helper()
	if got.Version != want.Version || got.ID != want.ID || got.Title != want.Title || got.WorkingDir != want.WorkingDir || got.ParentID != want.ParentID || !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) || got.Model != want.Model || got.ReasoningLevel != want.ReasoningLevel {
		t.Fatalf("session metadata = %#v, want %#v", got, want)
	}
	if len(got.Messages) != len(want.Messages) {
		t.Fatalf("message count = %d, want %d", len(got.Messages), len(want.Messages))
	}
	for i := range want.Messages {
		assertMessageEqual(t, got.Messages[i], want.Messages[i])
	}
}

func assertMessageEqual(t *testing.T, got, want llm.Message) {
	t.Helper()
	switch wantMessage := want.(type) {
	case llm.UserMessage:
		gotMessage, ok := got.(llm.UserMessage)
		if !ok || !gotMessage.Timestamp.Equal(wantMessage.Timestamp) || gotMessage.Text != wantMessage.Text {
			t.Fatalf("message = %#v, want %#v", got, want)
		}
	case llm.AssistantMessage:
		gotMessage, ok := got.(llm.AssistantMessage)
		if !ok || !gotMessage.Timestamp.Equal(wantMessage.Timestamp) || gotMessage.StopReason != wantMessage.StopReason || gotMessage.Usage != wantMessage.Usage || len(gotMessage.Blocks) != len(wantMessage.Blocks) {
			t.Fatalf("message = %#v, want %#v", got, want)
		}
		for i := range wantMessage.Blocks {
			assertBlockEqual(t, gotMessage.Blocks[i], wantMessage.Blocks[i])
		}
	case llm.ToolOutputMessage:
		gotMessage, ok := got.(llm.ToolOutputMessage)
		if !ok || !gotMessage.Timestamp.Equal(wantMessage.Timestamp) || gotMessage.ToolName != wantMessage.ToolName || gotMessage.ToolCallID != wantMessage.ToolCallID || gotMessage.ToolOutput != wantMessage.ToolOutput {
			t.Fatalf("message = %#v, want %#v", got, want)
		}
	case llm.ToolErrorMessage:
		gotMessage, ok := got.(llm.ToolErrorMessage)
		if !ok || !gotMessage.Timestamp.Equal(wantMessage.Timestamp) || gotMessage.ToolName != wantMessage.ToolName || gotMessage.ToolCallID != wantMessage.ToolCallID || gotMessage.Error.Error() != wantMessage.Error.Error() {
			t.Fatalf("message = %#v, want %#v", got, want)
		}
	case llm.ErrorMessage:
		gotMessage, ok := got.(llm.ErrorMessage)
		if !ok || !gotMessage.Timestamp.Equal(wantMessage.Timestamp) || gotMessage.Error.Error() != wantMessage.Error.Error() {
			t.Fatalf("message = %#v, want %#v", got, want)
		}
	default:
		t.Fatalf("unsupported want message %T", want)
	}
}

func assertBlockEqual(t *testing.T, got, want llm.AssistantBlock) {
	t.Helper()
	switch wantBlock := want.(type) {
	case llm.TextBlock:
		gotBlock, ok := got.(llm.TextBlock)
		if !ok || gotBlock != wantBlock {
			t.Fatalf("block = %#v, want %#v", got, want)
		}
	case llm.ThinkingBlock:
		gotBlock, ok := got.(llm.ThinkingBlock)
		if !ok || gotBlock != wantBlock {
			t.Fatalf("block = %#v, want %#v", got, want)
		}
	case llm.ToolCallBlock:
		gotBlock, ok := got.(llm.ToolCallBlock)
		if !ok || gotBlock.ID != wantBlock.ID || gotBlock.Name != wantBlock.Name || !jsonRawMessageEqual(gotBlock.Arguments, wantBlock.Arguments) || gotBlock.ThoughtSignature != wantBlock.ThoughtSignature {
			t.Fatalf("block = %#v, want %#v", got, want)
		}
	default:
		t.Fatalf("unsupported want block %T", want)
	}
}

func jsonRawMessageEqual(got, want json.RawMessage) bool {
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		return false
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		return false
	}
	return reflect.DeepEqual(gotValue, wantValue)
}
