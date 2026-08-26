package runtime_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/session/sqlite"
)

func TestInterruptedToolCallRecoveryAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ronin.db")
	workingDir := t.TempDir()
	store, err := sqlite.Open(t.Context(), sqlite.StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	created, err := store.Create(t.Context(), workingDir, session.Metadata{})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	interrupted := llm.AssistantMessage{
		Blocks: []llm.AssistantBlock{
			llm.TextBlock{Text: "I will inspect it."},
			llm.ToolCallBlock{ID: "call-1", Name: "shell", Arguments: json.RawMessage(`{"command":"go test ./..."}`)},
		},
		StopReason: llm.StopReasonToolUse,
	}
	if err := store.Append(t.Context(), created.ID, session.Event{Type: session.EventMessage, Message: llm.UserMessage{Text: "test it"}}); err != nil {
		t.Fatalf("Append(user) error = %v", err)
	}
	if err := store.Append(t.Context(), created.ID, session.Event{Type: session.EventMessage, Message: interrupted}); err != nil {
		t.Fatalf("Append(assistant) error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store, err = sqlite.Open(t.Context(), sqlite.StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("Open(reopen) error = %v", err)
	}
	defer store.Close()
	loaded, messages, ok, err := store.Load(t.Context(), created.ID)
	if err != nil || !ok {
		t.Fatalf("Load() ok = %v, error = %v", ok, err)
	}
	client := &recoveryModelClient{}
	conversation, err := runtime.NewConversation(runtime.ConversationConfig{
		CWD:          workingDir,
		ModelClient:  client,
		SessionStore: store,
		Session:      loaded,
		Messages:     messages,
	})
	if err != nil {
		t.Fatalf("NewConversation() error = %v", err)
	}
	events, errs := conversation.Prompt(t.Context(), "continue")
	for range events {
	}
	if err := <-errs; err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	if len(client.requests) != 1 {
		t.Fatalf("PredictNext() requests = %d, want 1", len(client.requests))
	}
	requestMessages := client.requests[0].Messages
	if len(requestMessages) != 4 {
		t.Fatalf("request message count = %d, want user, assistant, recovery error, and new user", len(requestMessages))
	}
	recovery, ok := requestMessages[2].(llm.ToolErrorMessage)
	if !ok || recovery.ToolCallID != "call-1" || recovery.Error == nil || !strings.Contains(recovery.Error.Error(), "interrupted") {
		t.Fatalf("recovery message = %#v, want interrupted tool error", requestMessages[2])
	}

	_, persisted, ok, err := store.Load(t.Context(), created.ID)
	if err != nil || !ok {
		t.Fatalf("Load(after recovery) ok = %v, error = %v", ok, err)
	}
	if len(persisted) != 5 {
		t.Fatalf("persisted message count = %d, want repaired history plus prompt response", len(persisted))
	}
	persistedRecovery, ok := persisted[2].(llm.ToolErrorMessage)
	if !ok || persistedRecovery.ToolCallID != "call-1" || persistedRecovery.Error == nil || !strings.Contains(persistedRecovery.Error.Error(), "interrupted") {
		t.Fatalf("persisted recovery message = %#v, want interrupted tool error", persisted[2])
	}
}

type recoveryModelClient struct {
	requests []llm.PredictNextRequest
}

func (*recoveryModelClient) Model() llm.Model { return llm.Model{} }
func (*recoveryModelClient) ReasoningLevel() llm.ReasoningLevel {
	return llm.ReasoningLevelOff
}
func (*recoveryModelClient) SetReasoningLevel(llm.ReasoningLevel) error { return nil }
func (c *recoveryModelClient) PredictNext(_ context.Context, request llm.PredictNextRequest) (<-chan llm.PredictionEvent, <-chan error) {
	c.requests = append(c.requests, request)
	events := make(chan llm.PredictionEvent, 2)
	events <- llm.BlockEnded{Block: llm.TextBlock{Text: "done"}}
	events <- llm.PredictionFinished{}
	close(events)
	errs := make(chan error)
	close(errs)
	return events, errs
}
func (*recoveryModelClient) PredictNextStructured(context.Context, llm.PredictNextStructuredRequest) (json.RawMessage, error) {
	return nil, nil
}
