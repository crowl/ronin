package runtime_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/session/sqlite"
)

func TestShellHistoryPersistenceAndModelIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := sqlite.Open(t.Context(), sqlite.StoreConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(t.Context(), t.TempDir(), session.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	client := &recoveryModelClient{}
	c, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client, SessionStore: store, Session: created})
	if err != nil {
		t.Fatal(err)
	}
	entries := []session.Event{
		{Type: session.EventShellCommand, ShellCommand: &session.ShellCommandEntry{ID: "local", Command: "echo SHELL_SECRET"}},
		{Type: session.EventShellOutput, ShellOutput: &session.ShellOutputEntry{ID: "local", Stream: session.ShellStdout, Text: "SHELL_SECRET"}},
		{Type: session.EventShellStatus, ShellStatus: &session.ShellStatusEntry{ID: "local", Status: session.ShellSucceeded, HasExitCode: true}},
	}
	for _, event := range entries {
		if err := c.RecordShellEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.Messages()) != 0 || len(client.requests) != 0 {
		t.Fatal("shell execution changed model context")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.Open(t.Context(), sqlite.StoreConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, messages, found, err := store.Load(t.Context(), created.ID)
	if err != nil || !found {
		t.Fatalf("load: %v %v", found, err)
	}
	if len(messages) != 0 || len(loaded.History) != 3 {
		t.Fatalf("messages=%v history=%v", messages, loaded.History)
	}
	compactor := &fakeCompactor{messages: []session.Message{llm.UserMessage{Text: "summary"}}}
	c, err = runtime.NewConversation(runtime.ConversationConfig{ModelClient: client, Compactor: compactor, SessionStore: store, Session: loaded, Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	events, errs := c.Prompt(t.Context(), "hello")
	for range events {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v", client.requests), "SHELL_SECRET") {
		t.Fatal("shell history leaked into request")
	}
	if err := c.CompactConversation(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v", compactor.input), "SHELL_SECRET") {
		t.Fatal("shell history leaked into compaction")
	}
	if len(c.DisplayHistory()) != 4 {
		t.Fatalf("display after compaction: %v", c.DisplayHistory())
	}
	points := c.RewindPoints()
	if len(points) != 1 {
		t.Fatalf("rewind points: %v", points)
	}
	if err := c.Rewind(t.Context(), points[0]); err != nil {
		t.Fatal(err)
	}
	if len(c.Messages()) != 0 || len(c.DisplayHistory()) != 3 {
		t.Fatal("rewind must retain local audit, not model messages")
	}
	events, errs = c.Prompt(t.Context(), "fork here")
	for range events {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := c.Fork(t.Context(), c.RewindPoints()[0]); err != nil {
		t.Fatal(err)
	}
	if len(c.DisplayHistory()) != 0 {
		t.Fatal("fork copied local audit from parent")
	}
	loaded, messages, found, err = store.Load(t.Context(), created.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	if len(loaded.History) < 3 || strings.Contains(fmt.Sprintf("%+v", messages), "SHELL_SECRET") {
		t.Fatal("parent history or context damaged")
	}
}
