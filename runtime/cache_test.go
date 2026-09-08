package runtime_test

import (
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
)

func TestConversationCacheIdentity(t *testing.T) {
	client := &fakeModelClient{events: []llm.PredictionEvent{llm.PredictionFinished{}}}
	conv, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client})
	if err != nil {
		t.Fatal(err)
	}
	prompt := func(c *runtime.Conversation) {
		t.Helper()
		events, errs := c.Prompt(t.Context(), "hello")
		for range events {
		}
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	prompt(conv)
	prompt(conv)
	key := client.requests[0].CacheKey
	if key == "" || client.requests[1].CacheKey != key {
		t.Fatal("cache identity not stable")
	}
	other, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client})
	if err != nil {
		t.Fatal(err)
	}
	prompt(other)
	if client.requests[2].CacheKey == key {
		t.Fatal("unrelated conversations share identity")
	}
	if err := conv.NewConversation(); err != nil {
		t.Fatal(err)
	}
	prompt(conv)
	if client.requests[3].CacheKey == key {
		t.Fatal("new conversation retained identity")
	}
	for range 2 {
		resumed, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client, Session: session.Session{ID: "persisted-session"}})
		if err != nil {
			t.Fatal(err)
		}
		prompt(resumed)
	}
	if client.requests[4].CacheKey != client.requests[5].CacheKey || client.requests[4].CacheKey == "persisted-session" {
		t.Fatal("resumed identity must be stable and opaque")
	}
}
