package openai_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/openai"
)

func TestStructuredStreamRetries(t *testing.T) {
	const partial = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"discard me\"}\n\n"
	const complete = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"{\\\"ok\\\":true}\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{}}}\n\n"
	for _, provider := range []string{"openai", "xai"} {
		for _, tc := range []struct {
			name         string
			stream       string
			readErr      bool
			recover      bool
			wantRequests int
			wantErr      error
		}{
			{name: "transport EOF after partial output", stream: partial, readErr: true, recover: true, wantRequests: 2},
			{name: "missing completion after partial output", stream: partial, recover: true, wantRequests: 2},
			{name: "exhausted retries", stream: partial, readErr: true, wantRequests: 3, wantErr: io.ErrUnexpectedEOF},
			{name: "invalid JSON is not retried", stream: partial + "data: {\"type\":\"response.completed\",\"response\":{}}\n\n", wantRequests: 1},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				requests, closed := 0, 0
				transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
					if closed != requests {
						t.Error("previous response body was not closed before retry")
					}
					requests++
					var body io.Reader = strings.NewReader(tc.stream)
					if tc.readErr {
						body = io.MultiReader(body, unexpectedEOFReader{})
					}
					if tc.recover && requests > 1 {
						body = strings.NewReader(complete)
					}
					resp := streamResponse(body)
					resp.Body = &trackedStructuredBody{Reader: body, close: func() { closed++ }}
					return resp, nil
				})
				client, err := openai.NewLLM(openai.LLMConfig{
					APIKey: "key", Model: llm.Model{Provider: provider, Name: "test"},
					ReasoningLevel: llm.ReasoningLevelOff, Client: &http.Client{Transport: transport},
				})
				if err != nil {
					t.Fatal(err)
				}
				result, err := client.PredictNextStructured(t.Context(), llm.PredictNextStructuredRequest{Schema: &jsonschema.Schema{Type: "object"}})
				if tc.recover {
					if err != nil {
						t.Fatal(err)
					}
					if string(result.JSON) != `{"ok":true}` {
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

func TestStructuredStreamRetryCancellation(t *testing.T) {
	for _, preCanceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "after failed response", true: "before request"}[preCanceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if preCanceled {
				cancel()
			}
			requests := 0
			client := newTestClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				resp := streamResponse(unexpectedEOFReader{})
				resp.Body = &trackedStructuredBody{Reader: unexpectedEOFReader{}, close: cancel}
				return resp, nil
			})})
			_, err := client.PredictNextStructured(ctx, llm.PredictNextStructuredRequest{Schema: &jsonschema.Schema{Type: "object"}})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want canceled", err)
			}
			want := 1
			if preCanceled {
				want = 0
			}
			if requests != want {
				t.Fatalf("requests = %d, want %d", requests, want)
			}
		})
	}
}

type trackedStructuredBody struct {
	io.Reader
	close func()
}

func (b *trackedStructuredBody) Close() error {
	b.close()
	return nil
}
