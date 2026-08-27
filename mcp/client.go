package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/tool"
)

const (
	initializationTimeout = 15 * time.Second
	shutdownTimeout       = 5 * time.Second
	maxMessageBytes       = 16 << 20
	protocolVersion       = "2025-06-18"
)

var exposedNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type ServerConfig struct {
	Command string
	Args    []string
	Env     map[string]string
}

type Client struct {
	sessions     []*session
	tools        []runtime.Tool
	instructions []runtime.MCPInstruction
}

func Connect(ctx context.Context, cwd string, configs map[string]ServerConfig) (*Client, error) {
	client := &Client{}
	if len(configs) == 0 {
		return client, nil
	}

	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := validateServer(name, configs[name]); err != nil {
			_ = client.Close()
			return nil, err
		}
		if err := client.connectServer(ctx, cwd, name, configs[name]); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("initialize MCP server %q: %w", name, err)
		}
	}
	return client, nil
}

func (c *Client) Tools() []runtime.Tool {
	return append([]runtime.Tool(nil), c.tools...)
}

func (c *Client) Instructions() []runtime.MCPInstruction {
	instructions := make([]runtime.MCPInstruction, len(c.instructions))
	for i, instruction := range c.instructions {
		instructions[i] = instruction
		instructions[i].Tools = append([]string(nil), instruction.Tools...)
	}
	return instructions
}

func (c *Client) Close() error {
	var errs []error
	for i := len(c.sessions) - 1; i >= 0; i-- {
		if err := c.sessions[i].Close(); err != nil {
			errs = append(errs, err)
		}
	}
	c.sessions = nil
	return errors.Join(errs...)
}

func (c *Client) connectServer(ctx context.Context, cwd, serverName string, cfg ServerConfig) error {
	initCtx, cancel := context.WithTimeout(ctx, initializationTimeout)
	defer cancel()

	session, err := startSession(cwd, cfg)
	if err != nil {
		return err
	}
	c.sessions = append(c.sessions, session)

	instructions, err := session.initialize(initCtx)
	if err != nil {
		return err
	}
	definitions, err := session.listTools(initCtx)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	toolNames := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		wrapped, err := newRemoteTool(serverName, session, definition)
		if err != nil {
			return err
		}
		for _, existing := range c.tools {
			if existing.Name() == wrapped.Name() {
				return fmt.Errorf("duplicate MCP tool name %q", wrapped.Name())
			}
		}
		c.tools = append(c.tools, wrapped)
		toolNames = append(toolNames, definition.Name)
	}
	c.instructions = append(c.instructions, runtime.MCPInstruction{
		Server:  serverName,
		Content: strings.TrimSpace(instructions),
		Tools:   toolNames,
	})
	return nil
}

func validateServer(name string, cfg ServerConfig) error {
	if !exposedNamePattern.MatchString(name) {
		return fmt.Errorf("invalid MCP server name %q: use letters, numbers, underscores, or hyphens", name)
	}
	if strings.TrimSpace(cfg.Command) == "" {
		return fmt.Errorf("MCP server %q command must not be empty", name)
	}
	for key := range cfg.Env {
		if key == "" || strings.ContainsRune(key, '=') {
			return fmt.Errorf("MCP server %q has invalid environment variable name %q", name, key)
		}
	}
	return nil
}

func environment(overrides map[string]string) []string {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	for key, value := range overrides {
		env[key] = value
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result
}

type session struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcResponse
	closed  bool
	readErr error

	closeOnce sync.Once
	closeErr  error
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if len(e.Data) == 0 {
		return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("MCP error %d: %s (%s)", e.Code, e.Message, e.Data)
}

func startSession(cwd string, cfg ServerConfig) (*session, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cwd
	cmd.Env = environment(cfg.Env)
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdout: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start command: %w", err)
	}

	s := &session{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  stdout,
		pending: make(map[int64]chan rpcResponse),
	}
	go s.readLoop()
	return s, nil
}

func (s *session) initialize(ctx context.Context) (string, error) {
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		Instructions    string `json:"instructions,omitempty"`
	}
	if err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "ronin",
			"version": "dev",
		},
	}, &result); err != nil {
		return "", fmt.Errorf("initialize: %w", err)
	}
	if result.ProtocolVersion == "" {
		return "", errors.New("initialize: server returned no protocol version")
	}
	if err := s.notify("notifications/initialized", map[string]any{}); err != nil {
		return "", fmt.Errorf("send initialized notification: %w", err)
	}
	return result.Instructions, nil
}

type toolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func (s *session) listTools(ctx context.Context) ([]toolDefinition, error) {
	var tools []toolDefinition
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result struct {
			Tools      []toolDefinition `json:"tools"`
			NextCursor string           `json:"nextCursor,omitempty"`
		}
		if err := s.call(ctx, "tools/list", params, &result); err != nil {
			return nil, err
		}
		tools = append(tools, result.Tools...)
		if result.NextCursor == "" {
			return tools, nil
		}
		if result.NextCursor == cursor {
			return nil, fmt.Errorf("server repeated tools/list cursor %q", cursor)
		}
		cursor = result.NextCursor
	}
}

func (s *session) callTool(ctx context.Context, name string, args any) (callToolResult, error) {
	var result callToolResult
	err := s.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &result)
	return result, err
}

func (s *session) call(ctx context.Context, method string, params any, result any) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("MCP connection is closed")
	}
	if s.readErr != nil {
		err := s.readErr
		s.mu.Unlock()
		return err
	}
	s.nextID++
	id := s.nextID
	responses := make(chan rpcResponse, 1)
	s.pending[id] = responses
	s.mu.Unlock()

	if err := s.write(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		s.removePending(id)
		return err
	}

	select {
	case response := <-responses:
		if response.Error != nil {
			return response.Error
		}
		if result == nil {
			return nil
		}
		if len(response.Result) == 0 {
			return errors.New("MCP response has no result")
		}
		if err := json.Unmarshal(response.Result, result); err != nil {
			return fmt.Errorf("decode MCP response: %w", err)
		}
		return nil
	case <-ctx.Done():
		if s.removePending(id) {
			_ = s.notify("notifications/cancelled", map[string]any{"requestId": id, "reason": ctx.Err().Error()})
		}
		return ctx.Err()
	}
}

func (s *session) notify(method string, params any) error {
	return s.write(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *session) write(message any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	closed := s.closed
	readErr := s.readErr
	s.mu.Unlock()
	if closed {
		return errors.New("MCP connection is closed")
	}
	if readErr != nil {
		return readErr
	}
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode MCP message: %w", err)
	}
	data = append(data, '\n')
	if _, err := s.stdin.Write(data); err != nil {
		return fmt.Errorf("write MCP message: %w", err)
	}
	return nil
}

func (s *session) readLoop() {
	scanner := bufio.NewScanner(s.stdout)
	scanner.Buffer(make([]byte, 64<<10), maxMessageBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var envelope struct {
			ID     json.RawMessage `json:"id,omitempty"`
			Method string          `json:"method,omitempty"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			s.failPending(fmt.Errorf("decode MCP message: %w", err))
			return
		}
		if envelope.Method != "" {
			if len(envelope.ID) > 0 && string(envelope.ID) != "null" {
				_ = s.write(map[string]any{
					"jsonrpc": "2.0",
					"id":      envelope.ID,
					"error": map[string]any{
						"code":    -32601,
						"message": "method not supported by ronin",
					},
				})
			}
			continue
		}

		var response rpcResponse
		if err := json.Unmarshal(line, &response); err != nil {
			s.failPending(fmt.Errorf("decode MCP response: %w", err))
			return
		}
		id, err := responseID(response.ID)
		if err != nil {
			continue
		}
		s.mu.Lock()
		responses := s.pending[id]
		delete(s.pending, id)
		s.mu.Unlock()
		if responses != nil {
			responses <- response
		}
	}
	if err := scanner.Err(); err != nil {
		s.failPending(fmt.Errorf("read MCP message: %w", err))
	} else {
		s.failPending(io.EOF)
	}
}

func responseID(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, errors.New("missing response ID")
	}
	id, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid response ID %s", raw)
	}
	return id, nil
}

func (s *session) removePending(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pending[id]; !ok {
		return false
	}
	delete(s.pending, id)
	return true
}

func (s *session) failPending(err error) {
	s.mu.Lock()
	if s.readErr == nil {
		s.readErr = err
	}
	pending := s.pending
	s.pending = make(map[int64]chan rpcResponse)
	s.mu.Unlock()
	response := rpcResponse{Error: &rpcError{Code: -32000, Message: err.Error()}}
	for _, responses := range pending {
		responses <- response
	}
}

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.failPending(errors.New("MCP connection closed"))

		_ = s.stdin.Close()
		wait := make(chan error, 1)
		go func() { wait <- s.cmd.Wait() }()
		select {
		case err := <-wait:
			if err != nil && !isExpectedExit(err) {
				s.closeErr = fmt.Errorf("wait for MCP server: %w", err)
			}
		case <-time.After(shutdownTimeout):
			if err := s.cmd.Process.Kill(); err != nil {
				s.closeErr = fmt.Errorf("kill MCP server: %w", err)
			}
			if err := <-wait; err != nil && !isExpectedExit(err) && s.closeErr == nil {
				s.closeErr = fmt.Errorf("wait for killed MCP server: %w", err)
			}
		}
		_ = s.stdout.Close()
	})
	return s.closeErr
}

func isExpectedExit(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

type remoteTool struct {
	name        string
	remoteName  string
	description string
	parameters  *jsonschema.Schema
	session     *session
}

func newRemoteTool(serverName string, session *session, definition toolDefinition) (*remoteTool, error) {
	if !exposedNamePattern.MatchString(definition.Name) {
		return nil, fmt.Errorf("MCP server %q returned invalid tool name %q", serverName, definition.Name)
	}
	parameters, err := jsonschema.FromRaw(definition.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("MCP tool %q schema: %w", definition.Name, err)
	}
	return &remoteTool{
		name:        serverName + "__" + definition.Name,
		remoteName:  definition.Name,
		description: definition.Description,
		parameters:  parameters,
		session:     session,
	}, nil
}

func (t *remoteTool) Name() string                   { return t.name }
func (t *remoteTool) Description() string            { return t.description }
func (t *remoteTool) Parameters() *jsonschema.Schema { return t.parameters }

func (t *remoteTool) Call(ctx context.Context, rawArgs json.RawMessage) (any, error) {
	var args any = map[string]any{}
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return nil, tool.Error{Code: "invalid_args", Message: err.Error()}
		}
	}
	result, err := t.session.callTool(ctx, t.remoteName, args)
	if err != nil {
		return nil, fmt.Errorf("call MCP tool %q: %w", t.remoteName, err)
	}
	return newResult(result)
}

type callToolResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent any               `json:"structuredContent,omitempty"`
	IsError           bool              `json:"isError,omitempty"`
}

type Result struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent any               `json:"structured_content,omitempty"`
	IsError           bool              `json:"is_error,omitempty"`
	artifacts         []tool.Artifact
}

func newResult(result callToolResult) (Result, error) {
	converted := Result{
		Content:           append([]json.RawMessage(nil), result.Content...),
		StructuredContent: result.StructuredContent,
		IsError:           result.IsError,
	}
	for _, content := range result.Content {
		var textContent struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(content, &textContent); err != nil {
			return Result{}, fmt.Errorf("decode MCP tool content: %w", err)
		}
		if textContent.Type == "text" {
			converted.artifacts = append(converted.artifacts, tool.TextArtifact{Text: textContent.Text})
		}
	}
	return converted, nil
}

func (r Result) Artifacts() []tool.Artifact {
	return append([]tool.Artifact(nil), r.artifacts...)
}
