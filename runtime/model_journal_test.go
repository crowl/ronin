package runtime

import (
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/session/sqlite"
	"path/filepath"
	"testing"
)

func TestModelSwitchPublishesJournalAfterPersistence(t *testing.T) {
	store, err := sqlite.Open(t.Context(), sqlite.StoreConfig{Path: filepath.Join(t.TempDir(), "session.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	saved, err := store.Create(t.Context(), t.TempDir(), session.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	model := llm.Model{Provider: "journal-test", Name: t.Name()}
	client := &fakeStructuredModelClient{}
	if err := llm.RegisterModel(model, func(llm.ReasoningLevel) (llm.ModelClient, error) { return client, nil }); err != nil {
		t.Fatal(err)
	}
	c, err := NewConversation(ConversationConfig{ModelClient: &fakeStructuredModelClient{}, SessionStore: store, Session: saved})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SwitchModel(model); err != nil {
		t.Fatal(err)
	}
	replay, _, found, err := store.Load(t.Context(), saved.ID)
	if err != nil || !found {
		t.Fatalf("reload: found=%v err=%v", found, err)
	}
	if len(c.session.History) != 1 || len(replay.History) != 1 {
		t.Fatal("live and persisted journal disagree")
	}
	live, event := c.session.History[0], replay.History[0]
	if live.Type != session.EventModelChanged || live.Model != event.Model || live.PreviousModel != event.PreviousModel || c.session.Model != replay.Model {
		t.Fatal("model transition differs after reload")
	}
	previous := c.modelClient
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.SwitchModel(model); err == nil {
		t.Fatal("expected persistence failure")
	}
	if c.modelClient != previous || len(c.session.History) != 1 {
		t.Fatal("failed switch published state")
	}
}
