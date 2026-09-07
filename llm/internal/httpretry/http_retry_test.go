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
	for _, status := range []int{429, 500, 502, 503, 504, 529} {
		for _, succeeds := range []bool{true, false} {
			t.Run(fmt.Sprintf("status %d succeeds %t", status, succeeds), func(t *testing.T) {
				attempts := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != "body" {
						t.Errorf("request body = %q, error = %v", body, err)
					}
					attempts++
					if !succeeds || attempts == 1 {
						w.Header().Set("Retry-After", "0")
						http.Error(w, fmt.Sprintf("attempt %d", attempts), status)
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
				wantStatus, wantAttempts, wantBody := status, 5, "attempt 5\n"
				if succeeds {
					wantStatus, wantAttempts, wantBody = http.StatusOK, 2, "ok"
				}
				if resp.StatusCode != wantStatus {
					t.Fatalf("status = %d, want %d", resp.StatusCode, wantStatus)
				}
				if attempts != wantAttempts {
					t.Fatalf("attempts = %d, want %d", attempts, wantAttempts)
				}
				body, err := io.ReadAll(resp.Body)
				if err != nil || string(body) != wantBody {
					t.Fatalf("response body = %q, error = %v, want %q", body, err, wantBody)
				}
			})
		}
	}

	t.Run("does not retry 501", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			http.Error(w, "temporary", http.StatusNotImplemented)
		}))
		defer server.Close()

		resp, err := httpretry.Do(context.Background(), server.Client(), func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, server.URL, nil)
		})
		if err != nil {
			t.Fatalf("Do() error = %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("status = %d, want 501", resp.StatusCode)
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1", attempts)
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

	t.Run("does not retry 403", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			w.Header().Set("Retry-After", "0")
			http.Error(w, fmt.Sprintf("attempt %d", attempts), http.StatusForbidden)
		}))
		defer server.Close()

		resp, err := httpretry.Do(context.Background(), server.Client(), func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, server.URL, nil)
		})
		if err != nil {
			t.Fatalf("Do() error = %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1", attempts)
		}
	})

	t.Run("does not retry transport errors", func(t *testing.T) {
		attempts := 0
		client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			attempts++
			return nil, errors.New("temporary network failure")
		})}

		_, err := httpretry.Do(context.Background(), client, func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, "https://example.test", nil)
		})
		if err == nil || !strings.Contains(err.Error(), "temporary network failure") {
			t.Fatalf("Do() error = %v, want transport error", err)
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1", attempts)
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
