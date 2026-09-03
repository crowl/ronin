package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crowl/ronin/tool"
)

func TestConnectStdioServer(t *testing.T) {
	client, err := Connect(t.Context(), t.TempDir(), map[string]ServerConfig{
		"test": {
			Command: os.Args[0],
			Args:    []string{"-test.run=^TestMCPHelperProcess$"},
			Env:     map[string]string{"RONIN_MCP_HELPER": "1"},
		},
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer client.Close()

	instructions := client.Instructions()
	if len(instructions) != 1 {
		t.Fatalf("Instructions() = %#v", instructions)
	}
	if instructions[0].Server != "test" || instructions[0].Content != "Use echo to repeat text." {
		t.Fatalf("Instructions()[0] = %#v", instructions[0])
	}
	if len(instructions[0].Tools) != 1 || instructions[0].Tools[0] != "echo" {
		t.Fatalf("Instructions()[0].Tools = %#v", instructions[0].Tools)
	}

	tools := client.Tools()
	if len(tools) != 1 || tools[0].Name() != "test__echo" {
		t.Fatalf("Tools() = %#v", tools)
	}
	if tools[0].Description() != "Echoes text" {
		t.Fatalf("Description() = %q", tools[0].Description())
	}

	schema, err := json.Marshal(tools[0].Parameters())
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var gotSchema map[string]any
	if err := json.Unmarshal(schema, &gotSchema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	if gotSchema["type"] != "object" || gotSchema["additionalProperties"] != false {
		t.Fatalf("schema = %s", schema)
	}
	properties, ok := gotSchema["properties"].(map[string]any)
	if !ok || properties["text"] == nil {
		t.Fatalf("schema properties = %#v", gotSchema["properties"])
	}

	value, err := tools[0].Call(t.Context(), json.RawMessage(`{"text":"stdio"}`))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	result := value.(Result)
	if len(result.Artifacts()) != 1 {
		t.Fatalf("artifacts = %#v", result.Artifacts())
	}
	data, err := json.Marshal(result)
	if err != nil || !containsJSONText(data, "stdio") {
		t.Fatalf("result = %s, error = %v", data, err)
	}
}

func TestConnectRemoteServer(t *testing.T) {
	server := newTestSSEServer()
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	server.baseURL = httpServer.URL

	connectCtx, cancel := context.WithCancel(t.Context())
	workspace := t.TempDir()
	currentDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	relativeWorkspace, err := filepath.Rel(currentDir, workspace)
	if err != nil {
		t.Fatalf("Rel() error = %v", err)
	}
	client, err := Connect(connectCtx, relativeWorkspace, map[string]ServerConfig{
		"test": {URL: httpServer.URL},
	})
	cancel()
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer client.Close()

	instructions := client.Instructions()
	if len(instructions) != 1 || instructions[0].Content != "Use echo to repeat text." {
		t.Fatalf("Instructions() = %#v", instructions)
	}
	tools := client.Tools()
	if len(tools) != 1 || tools[0].Name() != "test__echo" {
		t.Fatalf("Tools() = %#v", tools)
	}
	value, err := tools[0].Call(t.Context(), json.RawMessage(`{"text":"remote"}`))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	data, err := json.Marshal(value)
	if err != nil || !containsJSONText(data, "remote") {
		t.Fatalf("result = %s, error = %v", data, err)
	}

	roots := server.roots()
	wantRoot := (&url.URL{Scheme: "file", Path: workspace}).String()
	if len(roots) != 1 || roots[0] != wantRoot {
		t.Fatalf("roots = %#v, want [%q]", roots, wantRoot)
	}

	other, err := Connect(t.Context(), t.TempDir(), map[string]ServerConfig{
		"test": {URL: httpServer.URL},
	})
	if err != nil {
		t.Fatalf("connect second client: %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatalf("close second client: %v", err)
	}
	if _, err := tools[0].Call(t.Context(), json.RawMessage(`{"text":"still running"}`)); err != nil {
		t.Fatalf("Call() after another client closed = %v", err)
	}
}

func TestConnectRemoteServerAgainstGopls(t *testing.T) {
	gopls, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls is not installed")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	command := exec.CommandContext(ctx, gopls, "mcp", "-listen="+address)
	var log bytes.Buffer
	command.Stdout = &log
	command.Stderr = &log
	if err := command.Start(); err != nil {
		t.Fatalf("start gopls: %v", err)
	}
	defer func() {
		cancel()
		_ = command.Wait()
	}()

	var client *Client
	for range 100 {
		attemptCtx, attemptCancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
		client, err = Connect(attemptCtx, t.TempDir(), map[string]ServerConfig{
			"gopls": {URL: "http://" + address},
		})
		attemptCancel()
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("connect to gopls: %v\n%s", err, log.String())
	}
	defer client.Close()
	if len(client.Tools()) == 0 {
		t.Fatal("gopls exposed no MCP tools")
	}
}

func TestConnectRemoteServerHonorsInitializationCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := Connect(ctx, t.TempDir(), map[string]ServerConfig{
		"test": {URL: server.URL},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Connect() error = %v, want context deadline exceeded", err)
	}
}

func TestConnectRemoteServerRejectsCrossOriginMessageEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: endpoint\ndata: http://example.com/messages\n\n")
	}))
	defer server.Close()

	_, err := Connect(t.Context(), t.TempDir(), map[string]ServerConfig{
		"test": {URL: server.URL},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid SSE message endpoint") {
		t.Fatalf("Connect() error = %v", err)
	}
}

func TestConnectRemoteServerRejectsUnexpectedContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	_, err := Connect(t.Context(), t.TempDir(), map[string]ServerConfig{
		"test": {URL: server.URL},
	})
	if err == nil || !strings.Contains(err.Error(), "expected text/event-stream") {
		t.Fatalf("Connect() error = %v", err)
	}
}

func TestScanSSEEvent(t *testing.T) {
	scanner := newSSEScanner(strings.NewReader(": comment\r\nevent: message\r\ndata: first\r\ndata: second\r\n\r\n"))
	event, err := scanSSEEvent(scanner)
	if err != nil {
		t.Fatalf("scanSSEEvent() error = %v", err)
	}
	if event.name != "message" || string(event.data) != "first\nsecond" {
		t.Fatalf("event = %#v", event)
	}
}

func TestCallSendsCancellationNotification(t *testing.T) {
	transport := newRecordingTransport()
	session := newSession(transport, nil, "")
	defer session.Close()

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- session.call(ctx, "tools/call", map[string]any{}, nil)
	}()

	request := <-transport.writes
	var sent rpcRequest
	if err := json.Unmarshal(request, &sent); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	cancel()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("call() error = %v, want context canceled", err)
	}
	select {
	case notification := <-transport.writes:
		if err := json.Unmarshal(notification, &sent); err != nil {
			t.Fatalf("decode notification: %v", err)
		}
		if sent.Method != "notifications/cancelled" {
			t.Fatalf("notification method = %q", sent.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation notification was not sent")
	}
}

func TestStdioTransportWriteHonorsCancellation(t *testing.T) {
	writer := newBlockingWriteCloser()
	transport := &stdioTransport{stdin: writer}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- transport.WriteMessage(ctx, []byte(`{"jsonrpc":"2.0"}`))
	}()

	<-writer.started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WriteMessage() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WriteMessage() did not return after cancellation")
	}
}

func TestBoundedWriter(t *testing.T) {
	var output bytes.Buffer
	writer := &boundedWriter{writer: &output, remaining: 5}

	for _, input := range []string{"abc", "def", "ignored"} {
		written, err := writer.Write([]byte(input))
		if err != nil {
			t.Fatalf("Write(%q) error = %v", input, err)
		}
		if written != len(input) {
			t.Fatalf("Write(%q) = %d, want %d", input, written, len(input))
		}
	}
	if got := output.String(); got != "abcde" {
		t.Fatalf("output = %q, want %q", got, "abcde")
	}
}

func TestStartSessionWritesBoundedServerLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(logPath, []byte("old log contents"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	session, err := startSession(t.TempDir(), ServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestMCPHelperProcess$"},
		Env: map[string]string{
			"RONIN_MCP_HELPER":        "1",
			"RONIN_MCP_HELPER_STDERR": "server log",
		},
		LogPath: logPath,
	})
	if err != nil {
		t.Fatalf("startSession() error = %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := string(log); got != "server log" {
		t.Fatalf("log = %q, want %q", got, "server log")
	}
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("RONIN_MCP_HELPER") != "1" {
		return
	}

	if stderr := os.Getenv("RONIN_MCP_HELPER_STDERR"); stderr != "" {
		_, _ = os.Stderr.WriteString(stderr)
	}

	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		if len(request.ID) == 0 {
			continue
		}

		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			response["result"] = map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "test", "version": "1"},
				"instructions":    "Use echo to repeat text.",
			}
		case "tools/list":
			response["result"] = map[string]any{"tools": []any{map[string]any{
				"name":        "echo",
				"description": "Echoes text",
				"inputSchema": json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
			}}}
		case "tools/call":
			var params struct {
				Arguments struct {
					Text string `json:"text"`
				} `json:"arguments"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				os.Exit(2)
			}
			response["result"] = map[string]any{
				"content":           []any{map[string]string{"type": "text", "text": params.Arguments.Text}},
				"structuredContent": map[string]string{"echo": params.Arguments.Text},
			}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		if err := encoder.Encode(response); err != nil {
			os.Exit(2)
		}
	}
	if scanner.Err() != nil {
		os.Exit(2)
	}
}

type blockingWriteCloser struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func newBlockingWriteCloser() *blockingWriteCloser {
	return &blockingWriteCloser{started: make(chan struct{}), closed: make(chan struct{})}
}

func (w *blockingWriteCloser) Write([]byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.closed
	return 0, os.ErrClosed
}

func (w *blockingWriteCloser) Close() error {
	select {
	case <-w.closed:
	default:
		close(w.closed)
	}
	return nil
}

type testSSEServer struct {
	baseURL string

	mu        sync.Mutex
	sessions  map[string]chan []byte
	seenRoots []string
	nextID    int
}

func newTestSSEServer() *testSSEServer {
	return &testSSEServer{sessions: make(map[string]chan []byte)}
}

func (s *testSSEServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/":
		s.serveStream(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/messages/"):
		s.receiveMessage(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *testSSEServer) serveStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.nextID++
	id := strconv.Itoa(s.nextID)
	messages := make(chan []byte, 16)
	s.sessions[id] = messages
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, id)
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "event: endpoint\ndata: %s/messages/%s\n\n", s.baseURL, id)
	flusher.Flush()

	for {
		select {
		case message := <-messages:
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", message)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *testSSEServer) receiveMessage(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var message struct {
		ID     json.RawMessage `json:"id,omitempty"`
		Method string          `json:"method,omitempty"`
		Params json.RawMessage `json:"params,omitempty"`
		Result json.RawMessage `json:"result,omitempty"`
	}
	if err := json.Unmarshal(data, &message); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/messages/")
	s.mu.Lock()
	messages := s.sessions[id]
	s.mu.Unlock()
	if messages == nil {
		http.NotFound(w, r)
		return
	}

	if message.Method == "" {
		var roots struct {
			Roots []struct {
				URI string `json:"uri"`
			} `json:"roots"`
		}
		if len(message.Result) > 0 && json.Unmarshal(message.Result, &roots) == nil {
			s.mu.Lock()
			for _, root := range roots.Roots {
				s.seenRoots = append(s.seenRoots, root.URI)
			}
			s.mu.Unlock()
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if len(message.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	response := map[string]any{"jsonrpc": "2.0", "id": message.ID}
	switch message.Method {
	case "initialize":
		response["result"] = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "test", "version": "1"},
			"instructions":    "Use echo to repeat text.",
		}
		requestID := json.RawMessage(`9001`)
		rootRequest, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": requestID, "method": "roots/list", "params": map[string]any{}})
		messages <- rootRequest
	case "tools/list":
		response["result"] = map[string]any{"tools": []any{map[string]any{
			"name":        "echo",
			"description": "Echoes text",
			"inputSchema": json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
		}}}
	case "tools/call":
		var params struct {
			Arguments struct {
				Text string `json:"text"`
			} `json:"arguments"`
		}
		_ = json.Unmarshal(message.Params, &params)
		response["result"] = map[string]any{
			"content":           []any{map[string]string{"type": "text", "text": params.Arguments.Text}},
			"structuredContent": map[string]string{"echo": params.Arguments.Text},
		}
	default:
		response["error"] = map[string]any{"code": -32601, "message": "method not found"}
	}
	encoded, _ := json.Marshal(response)
	messages <- encoded
	w.WriteHeader(http.StatusAccepted)
}

func (s *testSSEServer) roots() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seenRoots...)
}

func TestMCPErrorResult(t *testing.T) {
	_, err := newResult(callToolResult{
		IsError: true,
		Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"permission denied"}`)},
	})
	if err == nil {
		t.Fatal("newResult() error = nil")
	}
	var toolErr tool.Error
	if !errors.As(err, &toolErr) {
		t.Fatalf("newResult() error = %T, want tool.Error", err)
	}
	if toolErr.Code != "mcp_tool_error" || !strings.Contains(toolErr.Message, "permission denied") {
		t.Fatalf("newResult() error = %#v", toolErr)
	}
}

func TestListToolsRejectsCursorCycle(t *testing.T) {
	transport := newRecordingTransport()
	session := newSession(transport, nil, "")
	defer session.Close()

	result := make(chan error, 1)
	go func() {
		_, err := session.listTools(t.Context())
		result <- err
	}()

	for _, nextCursor := range []string{"a", "b", "a"} {
		request := <-transport.writes
		var sent rpcRequest
		if err := json.Unmarshal(request, &sent); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		response, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      sent.ID,
			"result":  map[string]any{"tools": []any{}, "nextCursor": nextCursor},
		})
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		transport.responses <- response
	}

	if err := <-result; err == nil || !strings.Contains(err.Error(), "repeated tools/list cursor") {
		t.Fatalf("listTools() error = %v, want cursor cycle error", err)
	}
}

func TestListToolsRejectsDuplicateToolNames(t *testing.T) {
	transport := newRecordingTransport()
	session := newSession(transport, nil, "")
	defer session.Close()

	result := make(chan error, 1)
	go func() {
		_, err := session.listTools(t.Context())
		result <- err
	}()

	for page, nextCursor := range []string{"next", ""} {
		request := <-transport.writes
		var sent rpcRequest
		if err := json.Unmarshal(request, &sent); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		response, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      sent.ID,
			"result": map[string]any{
				"tools":      []any{map[string]any{"name": "duplicate", "inputSchema": map[string]any{"type": "object"}}},
				"nextCursor": nextCursor,
			},
		})
		if err != nil {
			t.Fatalf("marshal page %d response: %v", page, err)
		}
		transport.responses <- response
	}

	if err := <-result; err == nil || !strings.Contains(err.Error(), "duplicate tool name") {
		t.Fatalf("listTools() error = %v, want duplicate tool error", err)
	}
}

func TestInitializeRejectsUnsupportedProtocolVersion(t *testing.T) {
	transport := newRecordingTransport()
	session := newSession(transport, nil, "")
	defer session.Close()

	result := make(chan error, 1)
	go func() {
		_, err := session.initialize(t.Context())
		result <- err
	}()
	request := <-transport.writes
	var sent rpcRequest
	if err := json.Unmarshal(request, &sent); err != nil {
		t.Fatalf("decode initialize request: %v", err)
	}
	response, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      sent.ID,
		"result":  map[string]any{"protocolVersion": "1900-01-01"},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	go func() { transport.responses <- response }()

	if err := <-result; err == nil || !strings.Contains(err.Error(), "unsupported protocol version") {
		t.Fatalf("initialize() error = %v", err)
	}
}

func TestValidateServer(t *testing.T) {
	for _, test := range []struct {
		name       string
		serverName string
		config     ServerConfig
		wantErr    bool
	}{
		{name: "valid stdio", serverName: "go-tools", config: ServerConfig{Command: "gopls"}},
		{name: "valid remote", serverName: "go-tools", config: ServerConfig{URL: "http://127.0.0.1:3000"}},
		{name: "invalid name", serverName: "go tools", config: ServerConfig{Command: "gopls"}, wantErr: true},
		{name: "missing transport", serverName: "go", config: ServerConfig{}, wantErr: true},
		{name: "multiple transports", serverName: "go", config: ServerConfig{Command: "gopls", URL: "http://127.0.0.1:3000"}, wantErr: true},
		{name: "invalid env", serverName: "go", config: ServerConfig{Command: "gopls", Env: map[string]string{"BAD=KEY": "value"}}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateServer(test.serverName, test.config)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateServer() error = %v", err)
			}
		})
	}
}

type recordingTransport struct {
	writes    chan []byte
	responses chan []byte
	closed    chan struct{}
	once      sync.Once
}

func newRecordingTransport() *recordingTransport {
	return &recordingTransport{
		writes:    make(chan []byte, 2),
		responses: make(chan []byte, 2),
		closed:    make(chan struct{}),
	}
}

func (t *recordingTransport) ReadMessage() ([]byte, error) {
	select {
	case response := <-t.responses:
		return response, nil
	case <-t.closed:
		return nil, io.EOF
	}
}

func (t *recordingTransport) WriteMessage(ctx context.Context, data []byte) error {
	select {
	case t.writes <- append([]byte(nil), data...):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *recordingTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

func containsJSONText(data []byte, text string) bool {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return false
	}
	return containsText(value, text)
}

func containsText(value any, text string) bool {
	switch value := value.(type) {
	case string:
		return value == text
	case []any:
		for _, item := range value {
			if containsText(item, text) {
				return true
			}
		}
	case map[string]any:
		for _, item := range value {
			if containsText(item, text) {
				return true
			}
		}
	}
	return false
}
