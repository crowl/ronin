package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/crowl/ronin/llm"
)

func TestResponsesEndpoint(t *testing.T) {
	for _, tc := range []struct {
		baseURL  string
		endpoint string
		valid    bool
	}{
		{"", "", false},
		{"://bad", "", false},
		{"/v1", "", false},
		{"https:/v1", "", false},
		{"https://:8080/v1", "", false},
		{"ftp://proxy.example.com/v1", "", false},
		{"https://proxy.example.com/v1?version=1", "", false},
		{"https://proxy.example.com/v1#section", "", false},
		{"http://proxy.example.com", "http://proxy.example.com/responses", true},
		{"https://proxy.example.com/v1", "https://proxy.example.com/v1/responses", true},
		{"https://proxy.example.com/v1/", "https://proxy.example.com/v1/responses", true},
		{"https://proxy.example.com/v1///", "https://proxy.example.com/v1/responses", true},
		{"https://proxy.example.com/responses", "https://proxy.example.com/responses/responses", true},
	} {
		t.Run(tc.baseURL, func(t *testing.T) {
			endpoint, err := responsesEndpoint(tc.baseURL)
			if !tc.valid {
				if err == nil || !strings.Contains(err.Error(), "OPENAI_BASE_URL") {
					t.Fatalf("responsesEndpoint(%q) error = %v, want OPENAI_BASE_URL validation error", tc.baseURL, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("responsesEndpoint(%q) error = %v", tc.baseURL, err)
			}
			if endpoint != tc.endpoint {
				t.Errorf("responsesEndpoint(%q) = %q, want %q", tc.baseURL, endpoint, tc.endpoint)
			}
		})
	}
}

func TestSetupUsesDefaultResponsesEndpoint(t *testing.T) {
	if os.Getenv("OPENAI_SETUP_DEFAULT_SUBPROCESS") == "1" {
		if err := Setup("test-api-key"); err != nil {
			t.Fatalf("Setup() error = %v", err)
		}
		client, err := llm.LoadModelClient(Gpt56Sol, llm.ReasoningLevelOff)
		if err != nil {
			t.Fatalf("LoadModelClient() error = %v", err)
		}
		openaiClient, ok := client.(*LLM)
		if !ok {
			t.Fatalf("LoadModelClient() = %T, want *LLM", client)
		}
		if openaiClient.baseURL != defaultBaseURL {
			t.Errorf("default endpoint = %q, want %q", openaiClient.baseURL, defaultBaseURL)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSetupUsesDefaultResponsesEndpoint$")
	cmd.Env = append(os.Environ(), "OPENAI_SETUP_DEFAULT_SUBPROCESS=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("default Setup subprocess failed: %v\n%s", err, output)
	}
}

func TestSetupWithBaseURLUsesResponsesEndpoint(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{}}\n\n"))
	}))
	defer server.Close()

	if err := SetupWithBaseURL("test-api-key", server.URL+"/v1///"); err != nil {
		t.Fatalf("SetupWithBaseURL() error = %v", err)
	}

	registered := make(map[string]llm.Model)
	for _, model := range llm.Models() {
		registered[model.Name] = model
	}
	for _, name := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		model, ok := registered[name]
		if !ok {
			t.Errorf("model %q was not registered", name)
			continue
		}
		if model.Provider != "openai" {
			t.Errorf("model %q provider = %q, want openai", name, model.Provider)
		}
		if model.ContextWindow != 272_000 {
			t.Errorf("model %q context window = %d, want 272000", name, model.ContextWindow)
		}
	}

	client, err := llm.LoadModelClient(Gpt56Sol, llm.ReasoningLevelOff)
	if err != nil {
		t.Fatalf("LoadModelClient() error = %v", err)
	}
	events, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{})
	for range events {
	}
	if err := <-errs; err != nil {
		t.Fatalf("PredictNext() error = %v", err)
	}
	if gotPath != "/v1/responses" {
		t.Errorf("request path = %q, want /v1/responses", gotPath)
	}
}
