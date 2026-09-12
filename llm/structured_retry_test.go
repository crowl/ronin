package llm_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/anthropic"
	"github.com/crowl/ronin/llm/google"
)

func TestNonStreamingStructuredRetries(t *testing.T) {
	for _, provider := range []struct {
		name      string
		response  string
		newClient func(*http.Client) (llm.ModelClient, error)
	}{
		{
			name:     "anthropic",
			response: `{"content":[{"type":"text","text":"{\"answer\":\"ok\"}"}]}`,
			newClient: func(c *http.Client) (llm.ModelClient, error) {
				return anthropic.NewLLM(anthropic.LLMConfig{APIKey: "key", Model: llm.Model{Provider: "anthropic", Name: "test"}, ReasoningLevel: llm.ReasoningLevelOff, Client: c})
			},
		},
		{
			name:     "google",
			response: `{"steps":[{"type":"model_output","content":[{"type":"text","text":"{\"answer\":\"ok\"}"}]}]}`,
			newClient: func(c *http.Client) (llm.ModelClient, error) {
				return google.NewLLM(google.LLMConfig{APIKey: "key", Model: llm.Model{Provider: "google", Name: "test"}, ReasoningLevel: llm.ReasoningLevelOff, Client: c})
			},
		},
	} {
		for _, tc := range []struct {
			name         string
			wantRequests int
			wantErr      error
		}{
			{name: "recovery", wantRequests: 2},
			{name: "exhaustion", wantRequests: 3, wantErr: io.ErrUnexpectedEOF},
			{name: "cancellation", wantRequests: 1, wantErr: context.Canceled},
			{name: "pre-canceled", wantRequests: 0, wantErr: context.Canceled},
			{name: "invalid JSON", wantRequests: 1},
			{name: "unauthorized", wantRequests: 1},
		} {
			t.Run(provider.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tc.name == "pre-canceled" {
					cancel()
				}
				requests, closed := 0, 0
				var firstRequest string
				client, err := provider.newClient(&http.Client{Transport: structuredRoundTrip(func(req *http.Request) (*http.Response, error) {
					if closed != requests {
						t.Error("previous response was not closed")
					}
					requests++
					data, err := io.ReadAll(req.Body)
					if err != nil {
						t.Fatal(err)
					}
					if requests == 1 {
						firstRequest = string(data)
					} else if string(data) != firstRequest {
						t.Error("retry changed request payload")
					}
					var body io.Reader = io.MultiReader(strings.NewReader(`{"partial":`), structuredEOFReader{})
					status := http.StatusOK
					if tc.name == "recovery" && requests > 1 {
						body = strings.NewReader(provider.response)
					}
					if tc.name == "invalid JSON" {
						body = strings.NewReader(`invalid`)
					}
					if tc.name == "unauthorized" {
						status = http.StatusUnauthorized
						body = strings.NewReader(`unauthorized`)
					}
					return &http.Response{
						StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}},
						Body: &structuredRetryBody{Reader: body, close: func() {
							closed++
							if tc.name == "cancellation" {
								cancel()
							}
						}},
					}, nil
				})})
				if err != nil {
					t.Fatal(err)
				}
				result, err := client.PredictNextStructured(ctx, llm.PredictNextStructuredRequest{Schema: &jsonschema.Schema{Type: "object"}})
				if tc.name == "recovery" {
					if err != nil {
						t.Fatal(err)
					}
					if string(result.JSON) != `{"answer":"ok"}` {
						t.Fatalf("JSON = %s", result.JSON)
					}
				} else {
					if err == nil {
						t.Fatal("expected error")
					}
					if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
						t.Fatalf("error = %v, want %v", err, tc.wantErr)
					}
					if result != nil && len(result.JSON) != 0 {
						t.Fatalf("failed response exposed JSON: %s", result.JSON)
					}
				}
				if requests != tc.wantRequests || closed != requests {
					t.Fatalf("requests = %d, closed = %d, want %d", requests, closed, tc.wantRequests)
				}
			})
		}
	}
}

type structuredRoundTrip func(*http.Request) (*http.Response, error)

func (f structuredRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type structuredEOFReader struct{}

func (structuredEOFReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type structuredRetryBody struct {
	io.Reader
	close func()
}

func (b *structuredRetryBody) Close() error { b.close(); return nil }
