package xai

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

func TestResponsesEndpoint(t *testing.T) {
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
		{baseURL: "HTTP://proxy.example.com/v1///", want: "HTTP://proxy.example.com/v1/responses", valid: true},
	} {
		t.Run(tc.baseURL, func(t *testing.T) {
			got, err := responsesEndpoint(tc.baseURL)
			if !tc.valid {
				if err == nil || !strings.Contains(err.Error(), "XAI_BASE_URL") {
					t.Fatalf("responsesEndpoint() error = %v, want XAI_BASE_URL validation error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("responsesEndpoint() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("responsesEndpoint() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSetupRegistersGrok46(t *testing.T) {
	if os.Getenv("RONIN_XAI_SETUP") == "1" {
		if err := Setup("key"); err != nil {
			t.Fatalf("Setup() error = %v", err)
		}
		if got, want := llm.Models(), []llm.Model{Grok46}; !reflect.DeepEqual(got, want) {
			t.Fatalf("registered models = %#v, want %#v", got, want)
		}
		client, err := llm.LoadModelClient(Grok46, llm.ReasoningLevelOff)
		if err != nil {
			t.Fatalf("LoadModelClient() error = %v", err)
		}
		if client.Model() != Grok46 {
			t.Fatalf("client model = %#v, want %#v", client.Model(), Grok46)
		}
		if got := reflect.ValueOf(client).Elem().FieldByName("baseURL").String(); got != defaultBaseURL {
			t.Fatalf("base URL = %q, want %q", got, defaultBaseURL)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSetupRegistersGrok46$")
	cmd.Env = append(os.Environ(), "RONIN_XAI_SETUP=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Setup subprocess failed: %v\n%s", err, output)
	}
}

func TestSetupWithBaseURLRoutesRequests(t *testing.T) {
	if os.Getenv("RONIN_XAI_SETUP_WITH_BASE_URL") == "1" {
		var paths []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			if r.Header.Get("Accept") == "text/event-stream" {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{}}\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"ok\":true}"}]}]}`))
		}))
		defer server.Close()

		if err := SetupWithBaseURL("key", server.URL+"/v1///"); err != nil {
			t.Fatalf("SetupWithBaseURL() error = %v", err)
		}
		client, err := llm.LoadModelClient(Grok46, llm.ReasoningLevelOff)
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
		if want := []string{"/v1/responses", "/v1/responses"}; !reflect.DeepEqual(paths, want) {
			t.Fatalf("request paths = %#v, want %#v", paths, want)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSetupWithBaseURLRoutesRequests$")
	cmd.Env = append(os.Environ(), "RONIN_XAI_SETUP_WITH_BASE_URL=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("routing subprocess failed: %v\n%s", err, output)
	}
}

func TestSetupReportsProviderErrors(t *testing.T) {
	if os.Getenv("RONIN_XAI_SETUP_ERROR") == "1" {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "bad request", http.StatusBadRequest)
		}))
		defer server.Close()

		if err := SetupWithBaseURL("key", server.URL+"/v1"); err != nil {
			t.Fatalf("SetupWithBaseURL() error = %v", err)
		}
		client, err := llm.LoadModelClient(Grok46, llm.ReasoningLevelOff)
		if err != nil {
			t.Fatalf("LoadModelClient() error = %v", err)
		}
		events, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{})
		for range events {
		}
		err = <-errs
		if err == nil || !strings.Contains(err.Error(), "xai status 400") || strings.Contains(err.Error(), "openai") {
			t.Fatalf("PredictNext() error = %v, want xai status 400 without openai", err)
		}
		_, err = client.PredictNextStructured(context.Background(), llm.PredictNextStructuredRequest{Schema: &jsonschema.Schema{Type: "object"}})
		if err == nil || !strings.Contains(err.Error(), "xai structured status 400") || strings.Contains(err.Error(), "openai") {
			t.Fatalf("PredictNextStructured() error = %v, want xai structured status 400 without openai", err)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSetupReportsProviderErrors$")
	cmd.Env = append(os.Environ(), "RONIN_XAI_SETUP_ERROR=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("error labeling subprocess failed: %v\n%s", err, output)
	}
}
