package llm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/anthropic"
	"github.com/crowl/ronin/llm/google"
	"github.com/crowl/ronin/llm/openai"
)

func TestProviderContextErrorFixtures(t *testing.T) {
	for _, adapter := range []string{"openai", "anthropic", "google"} {
		t.Run(adapter, func(t *testing.T) {
			fixtures := map[string]string{
				"openai":    `{"error":{"code":"context_length_exceeded","message":"too large"}}`,
				"anthropic": `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1000 > 900"}}`,
				"google":    `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"input token count exceeds the maximum"}}`,
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(400); w.Write([]byte(fixtures[adapter])) }))
			defer server.Close()
			client := fixtureClient(t, adapter, server.URL)
			events, errs := client.PredictNext(t.Context(), llm.PredictNextRequest{})
			for range events {
			}
			if err := <-errs; !errors.Is(err, llm.ErrContextLimit) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func fixtureClient(t *testing.T, adapter, endpoint string) llm.ModelClient {
	t.Helper()
	model := llm.Model{Provider: adapter, Name: "fixture"}
	var client llm.ModelClient
	var err error
	switch adapter {
	case "openai":
		client, err = openai.NewLLM(openai.LLMConfig{APIKey: "fixture", BaseURL: endpoint, Model: model, ReasoningLevel: llm.ReasoningLevelOff})
	case "anthropic":
		client, err = anthropic.NewLLM(anthropic.LLMConfig{APIKey: "fixture", BaseURL: endpoint, Model: model, ReasoningLevel: llm.ReasoningLevelOff})
	case "google":
		client, err = google.NewLLM(google.LLMConfig{APIKey: "fixture", BaseURL: endpoint, Model: model, ReasoningLevel: llm.ReasoningLevelOff})
	}
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestProviderCancellationBeforeHeaders(t *testing.T) {
	for _, adapter := range []string{"openai", "anthropic", "google"} {
		t.Run(adapter, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cancel()
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)
			events, errs := fixtureClient(t, adapter, server.URL).PredictNext(ctx, llm.PredictNextRequest{})
			for range events {
			}
			if err := <-errs; !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
