package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRemoteInitializationCancelsStalledErrorBody(t *testing.T) {
	headers := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.(http.Flusher).Flush()
		close(headers)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := Connect(ctx, t.TempDir(), map[string]ServerConfig{"test": {URL: server.URL}})
		result <- err
	}()
	<-headers
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("initialization ignored cancellation while reading error body")
	}
}

func TestSessionWriteWaitHonorsCancellation(t *testing.T) {
	transport := &fakeBlockedTransport{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	s := newSession(transport, nil, "")
	defer s.Close()
	first := make(chan error, 1)
	go func() { first <- s.writeContext(t.Context(), rpcRequest{Method: "first"}) }()
	<-transport.started
	defer func() { close(transport.release); <-first }()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- s.writeContext(ctx, rpcRequest{Method: "second"}) }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("write slot acquisition ignored cancellation")
	}
}

type fakeBlockedTransport struct{ started, release, closed chan struct{} }

func (f *fakeBlockedTransport) ReadMessage() ([]byte, error) {
	<-f.closed
	return nil, context.Canceled
}
func (f *fakeBlockedTransport) WriteMessage(context.Context, []byte) error {
	close(f.started)
	<-f.release
	return nil
}
func (f *fakeBlockedTransport) Close() error { close(f.closed); return nil }
