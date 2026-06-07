package httpretry_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/llm/internal/httpretry"
)

func TestDo(t *testing.T) {
	t.Run("retries 429 then succeeds", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			if attempts == 1 {
				w.Header().Set("Retry-After", "0")
				http.Error(w, "rate limited", http.StatusTooManyRequests)
				return
			}
			_, _ = w.Write([]byte("ok"))
		}))
		defer server.Close()

		resp, err := httpretry.Do(context.Background(), server.Client(), func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, server.URL, strings.NewReader("body"))
		})
		if err != nil {
			t.Fatalf("Do() error = %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if attempts != 2 {
			t.Fatalf("attempts = %d, want 2", attempts)
		}
	})

	t.Run("retries 500 then succeeds", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			if attempts == 1 {
				w.Header().Set("Retry-After", "0")
				http.Error(w, "temporary", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte("ok"))
		}))
		defer server.Close()

		resp, err := httpretry.Do(context.Background(), server.Client(), func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, server.URL, nil)
		})
		if err != nil {
			t.Fatalf("Do() error = %v", err)
		}
		defer resp.Body.Close()
		if attempts != 2 {
			t.Fatalf("attempts = %d, want 2", attempts)
		}
	})

	t.Run("does not retry 400", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			http.Error(w, "bad request", http.StatusBadRequest)
		}))
		defer server.Close()

		resp, err := httpretry.Do(context.Background(), server.Client(), func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, server.URL, nil)
		})
		if err != nil {
			t.Fatalf("Do() error = %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1", attempts)
		}
	})

	t.Run("stops after 5 total status attempts", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			w.Header().Set("Retry-After", "0")
			http.Error(w, fmt.Sprintf("attempt %d", attempts), http.StatusServiceUnavailable)
		}))
		defer server.Close()

		resp, err := httpretry.Do(context.Background(), server.Client(), func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, server.URL, nil)
		})
		if err != nil {
			t.Fatalf("Do() error = %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", resp.StatusCode)
		}
		if attempts != 5 {
			t.Fatalf("attempts = %d, want 5", attempts)
		}
	})

	t.Run("retries transport errors", func(t *testing.T) {
		attempts := 0
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return nil, errors.New("temporary network failure")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("ok")),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		})}

		resp, err := httpretry.Do(context.Background(), client, func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, "https://example.test", nil)
		})
		if err != nil {
			t.Fatalf("Do() error = %v", err)
		}
		defer resp.Body.Close()
		if attempts != 2 {
			t.Fatalf("attempts = %d, want 2", attempts)
		}
	})

	t.Run("returns transport error after 5 total attempts", func(t *testing.T) {
		attempts := 0
		client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			attempts++
			return nil, errors.New("network down")
		})}

		_, err := httpretry.Do(context.Background(), client, func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, "https://example.test", nil)
		})
		if err == nil || !strings.Contains(err.Error(), "after 5 attempts") || !strings.Contains(err.Error(), "network down") {
			t.Fatalf("error = %v, want attempts and transport error", err)
		}
		if attempts != 5 {
			t.Fatalf("attempts = %d, want 5", attempts)
		}
	})

	t.Run("honors context cancellation during backoff", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			w.Header().Set("Retry-After", "1")
			http.Error(w, "try later", http.StatusTooManyRequests)
			cancel()
		}))
		defer server.Close()

		start := time.Now()
		_, err := httpretry.Do(ctx, server.Client(), func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, server.URL, nil)
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1", attempts)
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("canceled retry took %v, want immediate return", elapsed)
		}
	})

	t.Run("closes intermediate retry response bodies", func(t *testing.T) {
		firstBody := &trackingReadCloser{Reader: strings.NewReader("retry")}
		attempts := 0
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header:     http.Header{"Retry-After": []string{"0"}},
					Body:       firstBody,
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("ok")),
				Request:    req,
			}, nil
		})}

		resp, err := httpretry.Do(context.Background(), client, func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, "https://example.test", nil)
		})
		if err != nil {
			t.Fatalf("Do() error = %v", err)
		}
		defer resp.Body.Close()
		if !firstBody.closed {
			t.Fatal("first retry response body was not closed")
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}
