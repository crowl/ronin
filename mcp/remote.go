package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
)

const maxHTTPErrorBytes = 64 << 10

type sseTransport struct {
	client       *http.Client
	endpoint     *url.URL
	body         io.ReadCloser
	messages     chan []byte
	stop         chan struct{}
	done         chan struct{}
	cancelStream context.CancelFunc

	mu        sync.Mutex
	closed    bool
	streamErr error
	closeOnce sync.Once
}

func connectRemoteSession(ctx context.Context, cwd, endpoint string) (*session, error) {
	absoluteCWD, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve MCP workspace root %q: %w", cwd, err)
	}
	transport, err := connectSSETransport(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	root := (&url.URL{Scheme: "file", Path: absoluteCWD}).String()
	capabilities := map[string]any{
		"roots": map[string]any{"listChanged": false},
	}
	return newSession(transport, capabilities, root), nil
}

func connectSSETransport(ctx context.Context, endpoint string) (*sseTransport, error) {
	base, err := url.Parse(endpoint)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		return nil, fmt.Errorf("invalid remote MCP URL %q", endpoint)
	}

	streamCtx, cancel := context.WithCancel(context.Background())
	stopCancellation := context.AfterFunc(ctx, cancel)
	defer stopCancellation()
	requestURL := *base
	requestURL.User = nil
	request, err := http.NewRequestWithContext(streamCtx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create SSE request: %w", err)
	}
	request.Header.Set("Accept", "text/event-stream")

	client := &http.Client{
		Transport: http.DefaultTransport,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			if !sameOrigin(base, request.URL) {
				return errors.New("redirect changes MCP server origin")
			}
			return nil
		},
	}
	response, err := doRequest(ctx, cancel, client, request)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("connect to %q: %w", endpoint, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := readHTTPError(response.Body)
		_ = response.Body.Close()
		cancel()
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("connect to %q: %w", endpoint, err)
		}
		return nil, fmt.Errorf("connect to %q: HTTP %s%s", endpoint, response.Status, detail)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		_ = response.Body.Close()
		cancel()
		return nil, fmt.Errorf("connect to %q: expected text/event-stream response", endpoint)
	}

	scanner := newSSEScanner(response.Body)
	event, err := scanInitialSSEEvent(ctx, cancel, response.Body, scanner)
	if err != nil {
		_ = response.Body.Close()
		cancel()
		return nil, fmt.Errorf("connect to %q: read endpoint event: %w", endpoint, err)
	}
	if event.name != "endpoint" {
		_ = response.Body.Close()
		cancel()
		return nil, fmt.Errorf("connect to %q: first SSE event is %q, want %q", endpoint, event.name, "endpoint")
	}
	postEndpoint, err := base.Parse(string(event.data))
	if err != nil || !sameOrigin(base, postEndpoint) || postEndpoint.User != nil {
		_ = response.Body.Close()
		cancel()
		return nil, fmt.Errorf("connect to %q: invalid SSE message endpoint %q", endpoint, event.data)
	}
	postEndpoint.User = nil
	if !stopCancellation() || ctx.Err() != nil {
		cancel()
		_ = response.Body.Close()
		return nil, fmt.Errorf("connect to %q: %w", endpoint, ctx.Err())
	}

	transport := &sseTransport{
		client:       client,
		endpoint:     postEndpoint,
		body:         response.Body,
		messages:     make(chan []byte),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
		cancelStream: cancel,
	}
	go transport.readStream(scanner)
	return transport, nil
}

func scanInitialSSEEvent(ctx context.Context, cancel context.CancelFunc, body io.Closer, scanner *bufio.Scanner) (sseEvent, error) {
	result := make(chan struct {
		event sseEvent
		err   error
	}, 1)
	go func() {
		event, err := scanSSEEvent(scanner)
		result <- struct {
			event sseEvent
			err   error
		}{event: event, err: err}
	}()
	select {
	case result := <-result:
		return result.event, result.err
	case <-ctx.Done():
		cancel()
		_ = body.Close()
		<-result
		return sseEvent{}, ctx.Err()
	}
}

func doRequest(ctx context.Context, cancel context.CancelFunc, client *http.Client, request *http.Request) (*http.Response, error) {
	result := make(chan struct {
		response *http.Response
		err      error
	}, 1)
	go func() {
		response, err := client.Do(request)
		result <- struct {
			response *http.Response
			err      error
		}{response: response, err: err}
	}()
	select {
	case result := <-result:
		return result.response, result.err
	case <-ctx.Done():
		cancel()
		go func() {
			result := <-result
			if result.response != nil {
				_ = result.response.Body.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

func (t *sseTransport) ReadMessage() ([]byte, error) {
	select {
	case message := <-t.messages:
		return message, nil
	case <-t.stop:
		return nil, io.EOF
	case <-t.done:
		t.mu.Lock()
		err := t.streamErr
		t.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
}

func (t *sseTransport) WriteMessage(ctx context.Context, data []byte) error {
	if len(data) > maxMessageBytes {
		return fmt.Errorf("MCP message exceeds %d bytes", maxMessageBytes)
	}
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return errors.New("MCP connection is closed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint.String(), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create MCP request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := t.client.Do(request)
	if err != nil {
		return fmt.Errorf("send MCP message: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("send MCP message: HTTP %s%s", response.Status, readHTTPError(response.Body))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxHTTPErrorBytes))
	return nil
}

func (t *sseTransport) Close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		t.mu.Unlock()
		t.cancelStream()
		close(t.stop)
		_ = t.body.Close()
	})
	return nil
}

func (t *sseTransport) readStream(scanner *bufio.Scanner) {
	defer close(t.done)
	for {
		event, err := scanSSEEvent(scanner)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.mu.Lock()
				t.streamErr = err
				t.mu.Unlock()
			}
			return
		}
		if event.name != "message" {
			continue
		}
		select {
		case t.messages <- event.data:
		case <-t.stop:
			return
		}
	}
}

type sseEvent struct {
	name string
	data []byte
}

func newSSEScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxMessageBytes)
	return scanner
}

func scanSSEEvent(scanner *bufio.Scanner) (sseEvent, error) {
	var event sseEvent
	var data []string
	totalDataBytes := 0
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if event.name == "" && len(data) == 0 {
				continue
			}
			event.data = []byte(strings.Join(data, "\n"))
			return event, nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			event.name = value
		case "data":
			totalDataBytes += len(value)
			if len(data) > 0 {
				totalDataBytes++
			}
			if totalDataBytes > maxMessageBytes {
				return sseEvent{}, fmt.Errorf("SSE event exceeds %d bytes", maxMessageBytes)
			}
			data = append(data, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return sseEvent{}, err
	}
	return sseEvent{}, io.EOF
}

func sameOrigin(left, right *url.URL) bool {
	return left.Scheme == right.Scheme && left.Host == right.Host
}

func readHTTPError(body io.Reader) string {
	data, err := io.ReadAll(io.LimitReader(body, maxHTTPErrorBytes+1))
	if err != nil || len(data) == 0 {
		return ""
	}
	if len(data) > maxHTTPErrorBytes {
		data = data[:maxHTTPErrorBytes]
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return ""
	}
	return ": " + text
}
