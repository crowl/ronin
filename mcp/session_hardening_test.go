package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TestStdioTransportCancelledWriteReportsBrokenTransport verifies that
// unblocking a stuck stdin write is reported as a transport failure, not as a
// plain cancellation the caller might retry against.
func TestStdioTransportCancelledWriteReportsBrokenTransport(t *testing.T) {
	writer := newBlockingWriteCloser()
	transport := &stdioTransport{stdin: writer}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- transport.WriteMessage(ctx, []byte(`{"jsonrpc":"2.0"}`)) }()

	<-writer.started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, errTransportBroken) || !errors.Is(err, context.Canceled) {
			t.Fatalf("WriteMessage() error = %v, want broken transport wrapping cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WriteMessage() did not return after cancellation")
	}
}

// TestSessionRetiresOnBrokenTransport verifies that once the transport reports
// itself broken, in-flight and later requests fail immediately with that cause
// rather than hanging or surfacing unrelated write errors.
func TestSessionRetiresOnBrokenTransport(t *testing.T) {
	transport := &brokenOnWriteTransport{closed: make(chan struct{})}
	s := newSession(transport, nil, "")
	defer s.Close()

	err := s.call(t.Context(), "tools/call", map[string]any{}, nil)
	if !errors.Is(err, errTransportBroken) {
		t.Fatalf("first call error = %v, want broken transport", err)
	}
	err = s.call(t.Context(), "tools/call", map[string]any{}, nil)
	if !errors.Is(err, errTransportBroken) {
		t.Fatalf("second call error = %v, want retired session", err)
	}
	if transport.writes != 1 {
		t.Fatalf("writes = %d, want 1: retired session must not write again", transport.writes)
	}
}

// TestServerRequestsDoNotStallResponses verifies that a server-initiated
// request whose reply cannot be written does not block delivery of responses
// that arrive after it.
func TestServerRequestsDoNotStallResponses(t *testing.T) {
	transport := &stallingReplyTransport{
		messages: make(chan []byte, 2),
		writes:   make(chan []byte, 1),
		release:  make(chan struct{}),
		closed:   make(chan struct{}),
	}
	s := newSession(transport, nil, "file:///workspace")
	defer s.Close()
	defer close(transport.release)

	result := make(chan error, 1)
	go func() { result <- s.call(t.Context(), "tools/list", nil, nil) }()
	<-transport.writes

	// The server asks for roots, then answers the pending request. The
	// roots reply is stalled by the transport.
	transport.messages <- []byte(`{"jsonrpc":"2.0","id":7,"method":"roots/list"}`)
	transport.messages <- []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("call() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("response was not delivered while a server request reply was stalled")
	}
}

type brokenOnWriteTransport struct {
	writes int
	closed chan struct{}
}

func (f *brokenOnWriteTransport) ReadMessage() ([]byte, error) {
	<-f.closed
	return nil, context.Canceled
}

func (f *brokenOnWriteTransport) WriteMessage(context.Context, []byte) error {
	f.writes++
	return errTransportBroken
}

func (f *brokenOnWriteTransport) Close() error { close(f.closed); return nil }

// stallingReplyTransport forwards the first write (the client request) and
// blocks every later write until released, standing in for a server that has
// stopped reading its stdin while still producing output.
type stallingReplyTransport struct {
	messages chan []byte
	writes   chan []byte
	release  chan struct{}
	closed   chan struct{}
}

func (f *stallingReplyTransport) ReadMessage() ([]byte, error) {
	select {
	case message := <-f.messages:
		return message, nil
	case <-f.closed:
		return nil, context.Canceled
	}
}

func (f *stallingReplyTransport) WriteMessage(ctx context.Context, data []byte) error {
	var envelope struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(data, &envelope) == nil && envelope.Method != "" {
		f.writes <- append([]byte(nil), data...)
		return nil
	}
	select {
	case <-f.release:
	case <-ctx.Done():
	}
	return ctx.Err()
}

func (f *stallingReplyTransport) Close() error { close(f.closed); return nil }
