package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
)

func TestValidateBaseURL(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    string
		valid   bool
	}{
		{baseURL: "", valid: false},
		{baseURL: "://bad", valid: false},
		{baseURL: "/v1", valid: false},
		{baseURL: "https:/v1", valid: false},
		{baseURL: "https://:8080/v1", valid: false},
		{baseURL: "ftp://proxy.example.com/v1", valid: false},
		{baseURL: "https://proxy.example.com/v1?version=1", valid: false},
		{baseURL: "https://proxy.example.com/v1?", valid: false},
		{baseURL: "https://proxy.example.com/v1#section", valid: false},
		{baseURL: "https://proxy.example.com/v1#", valid: false},
		{baseURL: "HTTP://proxy.example.com/v1///", want: "HTTP://proxy.example.com/v1", valid: true},
	} {
		t.Run(tc.baseURL, func(t *testing.T) {
			got, err := validateBaseURL(tc.baseURL)
			if !tc.valid {
				if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_BASE_URL") {
					t.Fatalf("validateBaseURL() error = %v, want ANTHROPIC_BASE_URL validation error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateBaseURL() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("validateBaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewLLMUsesDefaultMessagesEndpoint(t *testing.T) {
	client, err := NewLLM(LLMConfig{APIKey: "key", Model: models[0], ReasoningLevel: llm.ReasoningLevelOff})
	if err != nil {
		t.Fatalf("NewLLM() error = %v", err)
	}
	if client.baseURL != defaultBaseURL {
		t.Errorf("base URL = %q, want %q", client.baseURL, defaultBaseURL)
	}
}

func TestSetupWithBaseURLRegistersModels(t *testing.T) {
	if os.Getenv("RONIN_ANTHROPIC_SETUP_WITH_BASE_URL") == "1" {
		const root = "https://proxy.example.com/v1"
		if err := SetupWithBaseURL("key", root+"///"); err != nil {
			t.Fatalf("SetupWithBaseURL() error = %v", err)
		}
		if got, want := len(llm.Models()), len(models); got != want {
			t.Fatalf("registered model count = %d, want %d", got, want)
		}
		for _, model := range models {
			client, err := llm.LoadModelClient(model, llm.ReasoningLevelOff)
			if err != nil {
				t.Fatalf("LoadModelClient(%s) error = %v", model, err)
			}
			anthropicClient, ok := client.(*LLM)
			if !ok {
				t.Fatalf("client type = %T, want *LLM", client)
			}
			if anthropicClient.baseURL != root+"/messages" {
				t.Errorf("model %s base URL = %q, want %q", model, anthropicClient.baseURL, root+"/messages")
			}
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSetupWithBaseURLRegistersModels$")
	cmd.Env = append(os.Environ(), "RONIN_ANTHROPIC_SETUP_WITH_BASE_URL=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("SetupWithBaseURL subprocess failed: %v\n%s", err, output)
	}
}

func TestSetupWithBaseURLRoutesRequests(t *testing.T) {
	if os.Getenv("RONIN_ANTHROPIC_SETUP_WITH_BASE_URL_ROUTING") == "1" {
		var paths []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			if r.Header.Get("Accept") == "text/event-stream" {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"message_start\"}\n\n"))
				_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"))
				_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"ok\":true}"}]}`))
		}))
		defer server.Close()

		if err := SetupWithBaseURL("key", server.URL+"/v1///"); err != nil {
			t.Fatalf("SetupWithBaseURL() error = %v", err)
		}
		client, err := llm.LoadModelClient(models[0], llm.ReasoningLevelOff)
		if err != nil {
			t.Fatalf("LoadModelClient() error = %v", err)
		}
		events, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{})
		for range events {
		}
		if err := <-errs; err != nil {
			t.Fatalf("PredictNext() error = %v", err)
		}
		if _, err := client.PredictNextStructured(context.Background(), llm.PredictNextStructuredRequest{Schema: &jsonschema.Schema{Type: "object"}}); err != nil {
			t.Fatalf("PredictNextStructured() error = %v", err)
		}
		if want := []string{"/v1/messages", "/v1/messages"}; !reflect.DeepEqual(paths, want) {
			t.Fatalf("request paths = %#v, want %#v", paths, want)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSetupWithBaseURLRoutesRequests$")
	cmd.Env = append(os.Environ(), "RONIN_ANTHROPIC_SETUP_WITH_BASE_URL_ROUTING=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("routing subprocess failed: %v\n%s", err, output)
	}
}
