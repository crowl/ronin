package jev

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crowl/ronin/plugin"
)

func TestClientRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{
			"relevant": map[string]any{"type": "noul", "noul": 0.08},
		}})
	}))
	t.Cleanup(server.Close)

	c := &Client{http: server.Client(), endpoint: server.URL, model: defaultModel, apiKey: "test-key"}
	answers, err := c.Decide(t.Context(), map[string]string{"tool": "shell"}, map[string]plugin.Question{
		"relevant": {Type: "noul", Instructions: "is it relevant?"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if answers["relevant"].Noul != 0.08 {
		t.Fatalf("answers = %+v", answers)
	}
}

func TestClientRetriesRateLimitAndOverload(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempt++
		if attempt == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if attempt == 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(529)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{}})
	}))
	t.Cleanup(server.Close)
	c := &Client{http: server.Client(), endpoint: server.URL, model: defaultModel, apiKey: "test-key"}
	if _, err := c.Decide(t.Context(), map[string]string{}, map[string]plugin.Question{}); err != nil {
		t.Fatal(err)
	}
	if attempt != 3 {
		t.Fatalf("attempts = %d, want 3", attempt)
	}
}

func TestClientHonorsRetryDelayCap(t *testing.T) {
	resp := &http.Response{Header: http.Header{"Retry-After": []string{"60"}}}
	if got := retryDelay(resp, 1); got != maxRetryDelay {
		t.Fatalf("retry delay = %s, want %s", got, maxRetryDelay)
	}
}

func TestNewClientRequiresAPIKey(t *testing.T) {
	client, err := NewClient("")
	if err == nil || client != nil {
		t.Fatalf("NewClient empty key = (%v, %v), want error", client, err)
	}
}

func TestNewClientUsesFixedConfiguration(t *testing.T) {
	client, err := NewClient(" key ")
	if err != nil {
		t.Fatal(err)
	}
	if client.endpoint != defaultEndpoint || client.model != defaultModel || client.apiKey != " key " {
		t.Fatalf("client = %+v", client)
	}
	if client.http == nil || client.http.Timeout != 10*time.Second {
		t.Fatalf("http client timeout = %v, want 10s", client.http)
	}
}
