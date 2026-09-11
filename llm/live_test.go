//go:build live

package llm_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/anthropic"
	"github.com/crowl/ronin/llm/google"
	"github.com/crowl/ronin/llm/openai"
)

// Explicit acknowledgement is required because provider billing cannot be
// capped atomically by the client. No tools, repository context, or secrets are
// included in the request; retries may still incur additional provider charges.
func TestLiveProviderSmoke(t *testing.T) {
	if os.Getenv("RONIN_LIVE_ACK") != "accept-provider-charges" {
		t.Skip("requires explicit billing acknowledgement")
	}
	adapter, name := os.Getenv("RONIN_LIVE_ADAPTER"), os.Getenv("RONIN_LIVE_MODEL")
	if name == "" {
		t.Fatal("RONIN_LIVE_MODEL is required")
	}
	model := llm.Model{Provider: adapter, Name: name}
	var client llm.ModelClient
	var err error
	switch adapter {
	case "openai":
		client, err = openai.NewLLM(openai.LLMConfig{APIKey: os.Getenv("OPENAI_API_KEY"), Model: model, ReasoningLevel: llm.ReasoningLevelOff})
	case "anthropic":
		client, err = anthropic.NewLLM(anthropic.LLMConfig{APIKey: os.Getenv("ANTHROPIC_API_KEY"), Model: model, ReasoningLevel: llm.ReasoningLevelOff})
	case "google":
		client, err = google.NewLLM(google.LLMConfig{APIKey: os.Getenv("GEMINI_API_KEY"), Model: model, ReasoningLevel: llm.ReasoningLevelOff})
	default:
		t.Fatal("RONIN_LIVE_ADAPTER must be openai, anthropic, or google")
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	events, errs := client.PredictNext(ctx, llm.PredictNextRequest{MaxTokens: 64, Messages: []llm.Message{llm.UserMessage{Text: "Reply with OK only."}}})
	finished := false
	for event := range events {
		if _, ok := event.(llm.PredictionFinished); ok {
			finished = true
		}
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if !finished {
		t.Fatal("missing completion")
	}
}
