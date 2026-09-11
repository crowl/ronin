package httpretry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// NewClient copies the caller's client and adds response-header and body-read
// deadlines without limiting total generation time. Environment overrides use
// Go duration syntax; zero disables the corresponding deadline.
func NewClient(client *http.Client) (*http.Client, error) {
	header, err := timeoutFromEnv("RONIN_LLM_HEADER_TIMEOUT", time.Minute)
	if err != nil {
		return nil, err
	}
	idle, err := timeoutFromEnv("RONIN_LLM_IDLE_TIMEOUT", 5*time.Minute)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	base := copy.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	copy.Transport = deadlineTransport{base: base, header: header, idle: idle}
	return &copy, nil
}

func timeoutFromEnv(name string, fallback time.Duration) (time.Duration, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 {
		return 0, fmt.Errorf("%s must be a non-negative duration", name)
	}
	return duration, nil
}

type deadlineTransport struct {
	base         http.RoundTripper
	header, idle time.Duration
}

func (t deadlineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	stop := deadline(ctx, cancel, t.header, "response headers")
	resp, err := t.base.RoundTrip(req.Clone(ctx))
	stop()
	if err != nil {
		cause := context.Cause(ctx)
		cancel(err)
		if cause != nil {
			return nil, cause
		}
		return nil, err
	}
	resp.Body = &deadlineBody{ReadCloser: resp.Body, ctx: ctx, cancel: cancel, idle: t.idle}
	return resp, nil
}

func deadline(ctx context.Context, cancel context.CancelCauseFunc, delay time.Duration, phase string) func() {
	if delay == 0 {
		return func() {}
	}
	timer := time.AfterFunc(delay, func() { cancel(fmt.Errorf("LLM %s timeout after %s: %w", phase, delay, context.DeadlineExceeded)) })
	return func() { timer.Stop() }
}

type deadlineBody struct {
	io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	idle   time.Duration
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	stop := deadline(b.ctx, b.cancel, b.idle, "stream idle")
	n, err := b.ReadCloser.Read(p)
	stop()
	if cause := context.Cause(b.ctx); cause != nil {
		return n, cause
	}
	return n, err
}

func (b *deadlineBody) Close() error {
	b.cancel(context.Canceled)
	return b.ReadCloser.Close()
}
