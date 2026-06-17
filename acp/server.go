package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/tool"
)

// Conversation is the Ronin conversation surface used by the ACP adapter.
type Conversation interface {
	Prompt(context.Context, string) (<-chan runtime.Event, <-chan error)
	CWD() string
	Model() llm.Model
	ReasoningLevel() llm.ReasoningLevel
	ContextUsage() llm.Usage
	ToolCallTitle(name string, arguments []byte) string
}

type Server struct {
	conv Conversation
	in   io.Reader
	out  io.Writer
	log  io.Writer

	writeMu sync.Mutex

	mu          sync.Mutex
	initialized bool
	closed      bool
	sessionID   string
	activeTurn  *activeTurn
}

type activeTurn struct {
	requestID json.RawMessage
	sessionID string
	cancel    context.CancelFunc
}

type Config struct {
	Conversation Conversation
	Input        io.Reader
	Output       io.Writer
	Log          io.Writer
}

func Serve(ctx context.Context, cfg Config) error {
	if cfg.Conversation == nil {
		return errors.New("conversation is required")
	}
	if cfg.Input == nil {
		return errors.New("input is required")
	}
	if cfg.Output == nil {
		return errors.New("output is required")
	}
	log := cfg.Log
	if log == nil {
		log = io.Discard
	}

	s := &Server{
		conv: cfg.Conversation,
		in:   cfg.Input,
		out:  cfg.Output,
		log:  log,
	}
	return s.serve(ctx)
}

func (s *Server) serve(ctx context.Context) error {
	scanner := bufio.NewScanner(s.in)
	const maxMessageSize = 16 * 1024 * 1024
	scanner.Buffer(make([]byte, 0, 64*1024), maxMessageSize)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			s.cancelActiveTurn()
			return ctx.Err()
		default:
		}

		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := s.handleLine(ctx, append([]byte(nil), line...)); err != nil {
			s.cancelActiveTurn()
			return err
		}
	}

	s.cancelActiveTurn()
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read acp message: %w", err)
	}
	return nil
}

func (s *Server) handleLine(ctx context.Context, line []byte) error {
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return s.writeError(nil, errParse, "parse error")
	}
	if msg.JSONRPC != "2.0" || msg.Method == "" {
		return s.writeError(msg.ID, errInvalidRequest, "invalid request")
	}

	isNotification := len(msg.ID) == 0
	if isNotification {
		return s.handleNotification(msg)
	}
	return s.handleRequest(ctx, msg)
}

func (s *Server) handleRequest(ctx context.Context, msg rpcMessage) error {
	if msg.Method != methodInitialize && !s.isInitialized() {
		return s.writeError(msg.ID, errInvalidRequest, "connection is not initialized")
	}

	switch msg.Method {
	case methodInitialize:
		return s.handleInitialize(msg)
	case methodSessionNew:
		return s.handleSessionNew(msg)
	case methodSessionPrompt:
		return s.handleSessionPrompt(ctx, msg)
	default:
		return s.writeError(msg.ID, errMethodNotFound, "method not found")
	}
}

func (s *Server) handleNotification(msg rpcMessage) error {
	if msg.Method != methodSessionCancel && !s.isInitialized() {
		return nil
	}

	switch msg.Method {
	case methodSessionCancel:
		var params cancelParams
		if err := decodeParams(msg.Params, &params); err != nil {
			_, _ = fmt.Fprintf(s.log, "invalid session/cancel params: %v\n", err)
			return nil
		}
		s.cancelSession(params.SessionID)
	}
	return nil
}

func (s *Server) handleInitialize(msg rpcMessage) error {
	var params initializeParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.writeError(msg.ID, errInvalidParams, err.Error())
	}

	version := 1
	if params.ProtocolVersion == 1 {
		version = params.ProtocolVersion
	}

	s.mu.Lock()
	s.initialized = true
	s.mu.Unlock()

	result := initializeResult{
		ProtocolVersion: version,
		AgentCapabilities: agentCapabilities{
			PromptCapabilities:  promptCapabilities{},
			MCPCapabilities:     mcpCapabilities{},
			SessionCapabilities: map[string]any{},
			Auth:                map[string]any{},
		},
		AgentInfo:   implementation{Name: "ronin", Title: "Ronin", Version: "dev"},
		AuthMethods: []any{},
	}
	return s.writeResult(msg.ID, result)
}

func (s *Server) handleSessionNew(msg rpcMessage) error {
	var params sessionNewParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.writeError(msg.ID, errInvalidParams, err.Error())
	}
	if strings.TrimSpace(params.CWD) == "" {
		return s.writeError(msg.ID, errInvalidParams, "cwd is required")
	}
	if !filepath.IsAbs(params.CWD) {
		return s.writeError(msg.ID, errInvalidParams, "cwd must be absolute")
	}
	convCWD, err := filepath.Abs(s.conv.CWD())
	if err != nil {
		return s.writeError(msg.ID, errInternal, "resolve ronin working directory")
	}
	requestCWD, err := filepath.Abs(params.CWD)
	if err != nil {
		return s.writeError(msg.ID, errInvalidParams, "resolve cwd")
	}
	if filepath.Clean(requestCWD) != filepath.Clean(convCWD) {
		return s.writeError(msg.ID, errInvalidParams, "cwd does not match ronin working directory")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionID != "" {
		return s.writeError(msg.ID, errInvalidRequest, "only one ACP session is supported")
	}
	s.sessionID = "ronin-session"
	return s.writeResult(msg.ID, sessionNewResult{SessionID: s.sessionID})
}

func (s *Server) handleSessionPrompt(ctx context.Context, msg rpcMessage) error {
	var params sessionPromptParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.writeError(msg.ID, errInvalidParams, err.Error())
	}
	if !s.validSession(params.SessionID) {
		return s.writeError(msg.ID, errInvalidParams, "unknown sessionId")
	}

	prompt, err := promptText(params.Prompt)
	if err != nil {
		return s.writeError(msg.ID, errInvalidParams, err.Error())
	}

	turnCtx, cancel := context.WithCancel(ctx)
	turn := &activeTurn{requestID: append(json.RawMessage(nil), msg.ID...), sessionID: params.SessionID, cancel: cancel}

	s.mu.Lock()
	if s.activeTurn != nil {
		s.mu.Unlock()
		cancel()
		return s.writeError(msg.ID, errInvalidRequest, "conversation already running")
	}
	s.activeTurn = turn
	s.mu.Unlock()

	go s.runPrompt(turnCtx, turn, prompt)
	return nil
}

func (s *Server) runPrompt(ctx context.Context, turn *activeTurn, prompt string) {
	defer turn.cancel()
	defer s.clearActiveTurn(turn)

	events, errs := s.conv.Prompt(ctx, prompt)
	messageID := "msg_agent_1"

	for event := range events {
		if err := s.writeRuntimeEvent(turn.sessionID, messageID, event); err != nil {
			_, _ = fmt.Fprintf(s.log, "write session update: %v\n", err)
			turn.cancel()
			return
		}
	}

	var runErr error
	if err, ok := <-errs; ok {
		runErr = err
	}
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			_ = s.writeResult(turn.requestID, sessionPromptResult{StopReason: stopReasonCancelled})
			return
		}
		_ = s.writeError(turn.requestID, errInternal, runErr.Error())
		return
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		_ = s.writeResult(turn.requestID, sessionPromptResult{StopReason: stopReasonCancelled})
		return
	}
	_ = s.writeResult(turn.requestID, sessionPromptResult{StopReason: stopReasonEndTurn})
}

func (s *Server) writeRuntimeEvent(sessionID, messageID string, event runtime.Event) error {
	switch e := event.(type) {
	case runtime.AssistantMessageDeltaReceived:
		if e.Text == "" {
			return nil
		}
		return s.writeSessionUpdate(sessionID, agentMessageChunkUpdate{
			SessionUpdate: updateAgentMessageChunk,
			MessageID:     messageID,
			Content:       textContent{Type: contentText, Text: e.Text},
		})
	case runtime.ToolExecutionStarted:
		return s.writeSessionUpdate(sessionID, toolCallUpdate{
			SessionUpdate: updateToolCall,
			ToolCallID:    e.CallID,
			Title:         nonEmpty(e.CallTitle, e.Tool.Name()),
			Kind:          toolKind(e.Tool.Name()),
			Status:        toolStatusInProgress,
			RawInput:      rawObject(e.CallArguments),
		})
	case runtime.ToolExecutionOutputDeltaReceived:
		text := artifactText(e.Artifact)
		if text == "" {
			return nil
		}
		return s.writeSessionUpdate(sessionID, toolCallDeltaUpdate{
			SessionUpdate: updateToolCallUpdate,
			ToolCallID:    e.CallID,
			Content: []toolCallContent{{
				Type:    toolCallContentContent,
				Content: textContent{Type: contentText, Text: text},
			}},
		})
	case runtime.ToolExecutionResultReceived:
		return s.writeSessionUpdate(sessionID, toolCallDeltaUpdate{
			SessionUpdate: updateToolCallUpdate,
			ToolCallID:    e.CallID,
			Status:        toolStatusCompleted,
		})
	case runtime.ToolExecutionFailed:
		return s.writeSessionUpdate(sessionID, toolCallDeltaUpdate{
			SessionUpdate: updateToolCallUpdate,
			ToolCallID:    e.CallID,
			Status:        toolStatusFailed,
			Content: []toolCallContent{{
				Type:    toolCallContentContent,
				Content: textContent{Type: contentText, Text: e.Error.Error()},
			}},
		})
	}
	return nil
}

func (s *Server) writeSessionUpdate(sessionID string, update any) error {
	return s.writeNotification(methodSessionUpdate, sessionUpdateParams{SessionID: sessionID, Update: update})
}

func (s *Server) isInitialized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initialized
}

func (s *Server) validSession(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID != "" && sessionID == s.sessionID
}

func (s *Server) cancelSession(sessionID string) {
	s.mu.Lock()
	turn := s.activeTurn
	if turn == nil || turn.sessionID != sessionID {
		s.mu.Unlock()
		return
	}
	cancel := turn.cancel
	s.mu.Unlock()
	cancel()
}

func (s *Server) cancelActiveTurn() {
	s.mu.Lock()
	turn := s.activeTurn
	s.mu.Unlock()
	if turn != nil {
		turn.cancel()
	}
}

func (s *Server) clearActiveTurn(turn *activeTurn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeTurn == turn {
		s.activeTurn = nil
	}
}

func (s *Server) writeResult(id json.RawMessage, result any) error {
	return s.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) writeNotification(method string, params any) error {
	return s.write(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *Server) writeError(id json.RawMessage, code int, message string) error {
	return s.write(rpcErrorResponse{JSONRPC: "2.0", ID: id, Error: rpcError{Code: code, Message: message}})
}

func (s *Server) write(msg any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := s.out.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func decodeParams(raw json.RawMessage, dest any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return nil
}

func promptText(blocks []contentBlock) (string, error) {
	if len(blocks) == 0 {
		return "", errors.New("prompt must contain at least one content block")
	}
	var parts []string
	for _, block := range blocks {
		switch block.Type {
		case contentText:
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		case contentResourceLink:
			parts = append(parts, resourceLinkText(block))
		default:
			return "", fmt.Errorf("unsupported prompt content type %q", block.Type)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func resourceLinkText(block contentBlock) string {
	var b strings.Builder
	b.WriteString("Referenced resource")
	if block.Name != "" {
		b.WriteString(" ")
		b.WriteString(block.Name)
	}
	if block.Title != "" && block.Title != block.Name {
		b.WriteString(" (")
		b.WriteString(block.Title)
		b.WriteString(")")
	}
	if block.URI != "" {
		b.WriteString(": ")
		b.WriteString(block.URI)
	}
	return b.String()
}

func rawObject(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

func artifactText(artifact tool.Artifact) string {
	switch a := artifact.(type) {
	case tool.TextArtifact:
		return a.Text
	case tool.ShellStreamArtifact:
		return a.Content
	case tool.FileArtifact:
		return a.Content
	case tool.FileRangeArtifact:
		return a.Content
	case tool.UnifiedDiffArtifact:
		return a.Diff
	default:
		return ""
	}
}

func toolKind(name string) string {
	switch name {
	case "read_file", "outline_package", "find_symbol":
		return "read"
	case "edit_file", "write_file":
		return "edit"
	case "shell":
		return "execute"
	default:
		return "other"
	}
}

func nonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
