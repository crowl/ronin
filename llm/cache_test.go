package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/anthropic"
	"github.com/crowl/ronin/llm/openai"
)

type cacheTransport func(*http.Request) (*http.Response, error)

func (f cacheTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func cacheResponse(body, media string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{media}}, Body: io.NopCloser(strings.NewReader(body))}
}
func cacheModel(provider string) llm.Model {
	return llm.Model{Provider: provider, Name: "test", Pricing: llm.ModelPricing{Input: 2, Output: 10, CacheRead: .2, CacheWrite: 2.5, HasInput: true, HasOutput: true, HasCacheRead: true, HasCacheWrite: true}}
}

func TestCacheRoutingAndConversationUsage(t *testing.T) {
	for _, tt := range []struct{ provider, url, keyField, header string }{
		{"openai", "https://api.openai.com/v1/responses", "prompt_cache_key", ""},
		{"xai", "https://api.x.ai/v1/responses", "prompt_cache_key", ""},
		{"custom", "https://example.com/responses", "", ""},
		{"openai", "https://proxy.example.com/responses", "", ""},
		{"xai", "https://api.x.ai.evil.example/responses", "", ""},
	} {
		t.Run(tt.provider+tt.url, func(t *testing.T) {
			transport := cacheTransport(func(r *http.Request) (*http.Response, error) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					return nil, err
				}
				if tt.keyField != "" {
					if body[tt.keyField] != "opaque-key" {
						t.Errorf("cache key = %v", body[tt.keyField])
					}
				} else if body["prompt_cache_key"] != nil {
					t.Error("unexpected cache key")
				}
				want := ""
				if tt.header != "" {
					want = "opaque-key"
				}
				if got := r.Header.Get("x-grok-conv-id"); got != want {
					t.Errorf("affinity = %q, want %q", got, want)
				}
				return cacheResponse("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":100,\"output_tokens\":5,\"total_tokens\":105,\"input_tokens_details\":{\"cached_tokens\":60,\"cache_write_tokens\":30}}}}\n\n", "text/event-stream"), nil
			})
			client, err := openai.NewLLM(openai.LLMConfig{APIKey: "test", BaseURL: tt.url, Model: cacheModel(tt.provider), ReasoningLevel: llm.ReasoningLevelOff, Client: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatal(err)
			}
			events, errs := client.PredictNext(t.Context(), llm.PredictNextRequest{CacheKey: "opaque-key"})
			found := false
			for e := range events {
				if done, ok := e.(llm.PredictionFinished); ok {
					found = true
					if done.Usage.CachedTokens != 60 {
						t.Fatalf("usage = %+v", done.Usage)
					}
				}
			}
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
			if !found {
				t.Fatal("missing usage")
			}
		})
	}
}

func TestAnthropicAutomaticCachePayload(t *testing.T) {
	transport := cacheTransport(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		control, ok := body["cache_control"].(map[string]any)
		if !ok || control["type"] != "ephemeral" || control["ttl"] != nil {
			t.Errorf("cache control = %#v", body["cache_control"])
		}
		return cacheResponse("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":60,\"cache_creation_input_tokens\":30}}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\ndata: {\"type\":\"message_stop\"}\n\n", "text/event-stream"), nil
	})
	client, err := anthropic.NewLLM(anthropic.LLMConfig{APIKey: "test", Model: cacheModel("anthropic"), ReasoningLevel: llm.ReasoningLevelOff, Client: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	events, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{})
	var usage llm.Usage
	for e := range events {
		if done, ok := e.(llm.PredictionFinished); ok {
			usage = done.Usage
		}
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 100 || usage.CacheWriteTokens != 30 || usage.CachedTokens != 60 || usage.TotalTokens != 105 {
		t.Fatalf("usage = %+v", usage)
	}
}
