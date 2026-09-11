package httpretry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientDeadlines(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			t.Setenv("RONIN_LLM_HEADER_TIMEOUT", "30ms")
			t.Setenv("RONIN_LLM_IDLE_TIMEOUT", "30ms")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if phase == "body" {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			client, err := NewClient(nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
			resp, err := client.Do(req)
			if err == nil {
				defer resp.Body.Close()
				_, err = io.ReadAll(resp.Body)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v", err)
			}
			if ctx.Err() != nil {
				t.Fatal("outer deadline expired instead of client deadline")
			}
		})
	}
}

func TestInvalidDeadline(t *testing.T) {
	t.Setenv("RONIN_LLM_IDLE_TIMEOUT", "-1s")
	if _, err := NewClient(nil); err == nil {
		t.Fatal("accepted negative timeout")
	}
}
