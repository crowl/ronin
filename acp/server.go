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
	"time"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/tool"
)

// Conversation is the Ronin conversation surface used by the ACP adapter.
// Each ACP session is backed by its own Conversation.
type Conversation interface {
	Prompt(context.Context, string) (<-chan runtime.Event, <-chan error)
	NewConversation() error
	CompactConversation(context.Context) error
	SwitchModel(llm.Model) error
	SwitchReasoningLevel(llm.ReasoningLevel) error
	CWD() string
	SessionID() string
	SessionTitle() string
	SessionUpdatedAt() time.Time
	Messages() []llm.Message
	Model() llm.Model
	ReasoningLevel() llm.ReasoningLevel
	ContextUsage() llm.Usage
	ToolCallTitle(name string, arguments []byte) string
}

type Server struct {
	newConversation  func() (Conversation, error)
	loadConversation func(sessionID string) (Conversation, bool, error)
	deleteSession    func(sessionID string) error
	listSessions     func(cwd string) ([]SessionSummary, error)
	cwd              string
	in               io.Reader
	out              io.Writer
	log              io.Writer

	writeMu sync.Mutex

	mu          sync.Mutex
	initialized bool
	sessions    map[string]*acpSession
}

// acpSession holds a single ACP session and its in-flight turn, if any.
type acpSession struct {
	id   string
	conv Conversation

	// activeTurn and lastTitle are guarded by Server.mu.
	activeTurn *activeTurn
	lastTitle  string
}

type activeTurn struct {
	requestID json.RawMessage
	sessionID string
	cancel    context.CancelFunc
}

type Config struct {
	// NewConversation creates a fresh Conversation for a new ACP session. Each
	// call must return a Conversation with a distinct SessionID.
	NewConversation func() (Conversation, error)
	// LoadConversation resumes a persisted Conversation by session ID. When
	// non-nil, the agent advertises the loadSession capability and supports
	// session/load. The returned bool reports whether the session exists.
	LoadConversation func(sessionID string) (Conversation, bool, error)
	// DeleteSession removes a persisted session by ID. When non-nil, the agent
	// advertises the delete capability and supports session/delete. Deleting a
	// missing session must succeed silently.
	DeleteSession func(sessionID string) error
	// ListSessions returns known sessions, optionally filtered by working
	// directory. When non-nil, the agent advertises the list capability and
	// supports session/list. An empty cwd means no filter.
	ListSessions func(cwd string) ([]SessionSummary, error)
	// CWD is the working directory Ronin serves. session/new and session/load
	// requests must reference this same directory.
	CWD    string
	Input  io.Reader
	Output io.Writer
	Log    io.Writer
}

// SessionSummary describes a known session for session/list responses.
type SessionSummary struct {
	SessionID string
	CWD       string
	Title     string
	UpdatedAt time.Time
}

func Serve(ctx context.Context, cfg Config) error {
	if cfg.NewConversation == nil {
		return errors.New("new conversation factory is required")
	}
	if strings.TrimSpace(cfg.CWD) == "" {
		return errors.New("cwd is required")
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
		newConversation:  cfg.NewConversation,
		loadConversation: cfg.LoadConversation,
		deleteSession:    cfg.DeleteSession,
		listSessions:     cfg.ListSessions,
		cwd:              cfg.CWD,
		in:               cfg.Input,
		out:              cfg.Output,
		log:              log,
		sessions:         make(map[string]*acpSession),
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
			s.cancelAllTurns()
			return ctx.Err()
		default:
		}

		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := s.handleLine(ctx, append([]byte(nil), line...)); err != nil {
			s.cancelAllTurns()
			return err
		}
	}

	s.cancelAllTurns()
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
	case methodSessionLoad:
		return s.handleSessionLoad(msg)
	case methodSessionResume:
		return s.handleSessionResume(msg)
	case methodSessionClose:
		return s.handleSessionClose(msg)
	case methodSessionDelete:
		return s.handleSessionDelete(msg)
	case methodSessionList:
		return s.handleSessionList(msg)
	case methodSessionPrompt:
		return s.handleSessionPrompt(ctx, msg)
	case methodSessionSetConfigOption:
		return s.handleSetConfigOption(msg)
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

	sessionCapabilities := map[string]any{
		// close cancels in-flight work and frees the in-memory session; it is
		// always supported.
		"close": map[string]any{},
	}
	if s.loadConversation != nil {
		// resume restores a stored session without replaying history, using the
		// same store-backed loader as session/load.
		sessionCapabilities["resume"] = map[string]any{}
	}
	if s.deleteSession != nil {
		sessionCapabilities["delete"] = map[string]any{}
	}
	if s.listSessions != nil {
		sessionCapabilities["list"] = map[string]any{}
	}

	result := initializeResult{
		ProtocolVersion: version,
		AgentCapabilities: agentCapabilities{
			LoadSession:         s.loadConversation != nil,
			PromptCapabilities:  promptCapabilities{},
			MCPCapabilities:     mcpCapabilities{},
			SessionCapabilities: sessionCapabilities,
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
	if err := s.checkCWD(params.CWD); err != nil {
		return s.writeError(msg.ID, errInvalidParams, err.Error())
	}

	conv, err := s.newConversation()
	if err != nil {
		return s.writeError(msg.ID, errInternal, fmt.Sprintf("create session: %v", err))
	}
	id := conv.SessionID()
	if id == "" {
		return s.writeError(msg.ID, errInternal, "created session has no id")
	}

	s.mu.Lock()
	if _, exists := s.sessions[id]; exists {
		s.mu.Unlock()
		return s.writeError(msg.ID, errInternal, fmt.Sprintf("duplicate session id %q", id))
	}
	s.sessions[id] = &acpSession{id: id, conv: conv}
	s.mu.Unlock()

	result := sessionNewResult{SessionID: id, ConfigOptions: configOptions(conv)}
	if err := s.writeResult(msg.ID, result); err != nil {
		return err
	}
	return s.writeAvailableCommandsUpdate(id)
}

func (s *Server) checkCWD(requestCWD string) error {
	serverCWD, err := filepath.Abs(s.cwd)
	if err != nil {
		return errors.New("resolve ronin working directory")
	}
	abs, err := filepath.Abs(requestCWD)
	if err != nil {
		return errors.New("resolve cwd")
	}
	if filepath.Clean(abs) != filepath.Clean(serverCWD) {
		return errors.New("cwd does not match ronin working directory")
	}
	return nil
}

func (s *Server) handleSessionLoad(msg rpcMessage) error {
	if s.loadConversation == nil {
		return s.writeError(msg.ID, errMethodNotFound, "session/load is not supported")
	}
	var params sessionLoadParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.writeError(msg.ID, errInvalidParams, err.Error())
	}
	sess, ok, err := s.attachSession(msg, params.SessionID, params.CWD)
	if !ok {
		return err
	}

	if err := s.replayHistory(sess.id, sess.conv); err != nil {
		return err
	}
	if err := s.writeResult(msg.ID, sessionLoadResult{ConfigOptions: configOptions(sess.conv)}); err != nil {
		return err
	}
	return s.writeAvailableCommandsUpdate(sess.id)
}

func (s *Server) handleSessionResume(msg rpcMessage) error {
	if s.loadConversation == nil {
		return s.writeError(msg.ID, errMethodNotFound, "session/resume is not supported")
	}
	var params sessionResumeParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.writeError(msg.ID, errInvalidParams, err.Error())
	}
	sess, ok, err := s.attachSession(msg, params.SessionID, params.CWD)
	if !ok {
		return err
	}

	// Unlike session/load, resume must not replay history before responding.
	if err := s.writeResult(msg.ID, sessionResumeResult{ConfigOptions: configOptions(sess.conv)}); err != nil {
		return err
	}
	return s.writeAvailableCommandsUpdate(sess.id)
}

func (s *Server) handleSessionClose(msg rpcMessage) error {
	var params sessionCloseParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.writeError(msg.ID, errInvalidParams, err.Error())
	}
	if strings.TrimSpace(params.SessionID) == "" {
		return s.writeError(msg.ID, errInvalidParams, "sessionId is required")
	}
	if !s.removeSession(params.SessionID) {
		return s.writeError(msg.ID, errInvalidParams, "unknown sessionId")
	}
	return s.writeResult(msg.ID, emptyResult{})
}

func (s *Server) handleSessionDelete(msg rpcMessage) error {
	if s.deleteSession == nil {
		return s.writeError(msg.ID, errMethodNotFound, "session/delete is not supported")
	}
	var params sessionDeleteParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.writeError(msg.ID, errInvalidParams, err.Error())
	}
	if strings.TrimSpace(params.SessionID) == "" {
		return s.writeError(msg.ID, errInvalidParams, "sessionId is required")
	}
	// Free any active in-memory session before removing persisted history.
	s.removeSession(params.SessionID)
	if err := s.deleteSession(params.SessionID); err != nil {
		return s.writeError(msg.ID, errInternal, fmt.Sprintf("delete session: %v", err))
	}
	return s.writeResult(msg.ID, emptyResult{})
}

func (s *Server) handleSessionList(msg rpcMessage) error {
	if s.listSessions == nil {
		return s.writeError(msg.ID, errMethodNotFound, "session/list is not supported")
	}
	var params sessionListParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.writeError(msg.ID, errInvalidParams, err.Error())
	}
	if params.CWD != "" && !filepath.IsAbs(params.CWD) {
		return s.writeError(msg.ID, errInvalidParams, "cwd must be absolute")
	}

	summaries, err := s.listSessions(params.CWD)
	if err != nil {
		return s.writeError(msg.ID, errInternal, fmt.Sprintf("list sessions: %v", err))
	}

	sessions := make([]sessionInfo, 0, len(summaries))
	for _, summary := range summaries {
		info := sessionInfo{
			SessionID: summary.SessionID,
			CWD:       summary.CWD,
			Title:     summary.Title,
		}
		if !summary.UpdatedAt.IsZero() {
			info.UpdatedAt = summary.UpdatedAt.UTC().Format(time.RFC3339)
		}
		sessions = append(sessions, info)
	}
	return s.writeResult(msg.ID, sessionListResult{Sessions: sessions})
}

// emitSessionInfo notifies the client of the session's current title and last
// activity timestamp after a turn, keeping session/list results in sync. The
// title is sent only when it changes; updatedAt is sent whenever known.
func (s *Server) emitSessionInfo(sess *acpSession) {
	title := sess.conv.SessionTitle()
	updatedAt := sess.conv.SessionUpdatedAt()

	update := sessionInfoUpdate{SessionUpdate: updateSessionInfo}
	changed := false

	s.mu.Lock()
	if title != "" && title != sess.lastTitle {
		sess.lastTitle = title
		update.Title = &title
		changed = true
	}
	s.mu.Unlock()

	if !updatedAt.IsZero() {
		update.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		changed = true
	}
	if !changed {
		return
	}
	if err := s.writeSessionUpdate(sess.id, update); err != nil {
		_, _ = fmt.Fprintf(s.log, "write session info update: %v\n", err)
	}
}

// removeSession cancels any in-flight turn for the session and removes it from
// the registry. It reports whether the session was present.
func (s *Server) removeSession(sessionID string) bool {
	s.mu.Lock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return false
	}
	delete(s.sessions, sessionID)
	var cancel context.CancelFunc
	if sess.activeTurn != nil {
		cancel = sess.activeTurn.cancel
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

// attachSession validates a session/load or session/resume request, loads the
// stored conversation by ID, and registers it. When ok is false an error
// response has already been written; the returned error is non-nil only on a
// fatal transport failure.
func (s *Server) attachSession(msg rpcMessage, sessionID, cwd string) (sess *acpSession, ok bool, err error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, false, s.writeError(msg.ID, errInvalidParams, "sessionId is required")
	}
	if e := s.checkCWD(cwd); e != nil {
		return nil, false, s.writeError(msg.ID, errInvalidParams, e.Error())
	}

	s.mu.Lock()
	if existing, exists := s.sessions[sessionID]; exists && existing.activeTurn != nil {
		s.mu.Unlock()
		return nil, false, s.writeError(msg.ID, errInvalidRequest, "session is busy")
	}
	s.mu.Unlock()

	conv, found, e := s.loadConversation(sessionID)
	if e != nil {
		return nil, false, s.writeError(msg.ID, errInternal, fmt.Sprintf("load session: %v", e))
	}
	if !found {
		return nil, false, s.writeError(msg.ID, errInvalidParams, "unknown sessionId")
	}
	id := conv.SessionID()
	if id == "" {
		return nil, false, s.writeError(msg.ID, errInternal, "loaded session has no id")
	}

	sess = &acpSession{id: id, conv: conv}
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	return sess, true, nil
}

// replayHistory streams a loaded conversation's history to the client as
// session/update notifications, mirroring the updates emitted during a live
// prompt turn.
func (s *Server) replayHistory(sessionID string, conv Conversation) error {
	var seq int
	nextID := func(role string) string {
		seq++
		return fmt.Sprintf("msg_%s_%d", role, seq)
	}

	for _, message := range conv.Messages() {
		switch m := message.(type) {
		case llm.UserMessage:
			if m.Text == "" {
				continue
			}
			if err := s.writeSessionUpdate(sessionID, userMessageChunkUpdate{
				SessionUpdate: updateUserMessageChunk,
				MessageID:     nextID("user"),
				Content:       textContent{Type: contentText, Text: m.Text},
			}); err != nil {
				return err
			}
		case llm.AssistantMessage:
			if err := s.replayAssistantMessage(sessionID, conv, nextID, m); err != nil {
				return err
			}
		case llm.ToolOutputMessage:
			if err := s.writeSessionUpdate(sessionID, toolCallDeltaUpdate{
				SessionUpdate: updateToolCallUpdate,
				ToolCallID:    m.ToolCallID,
				Status:        toolStatusCompleted,
				Content:       toolOutputContent(m.ToolOutput),
			}); err != nil {
				return err
			}
		case llm.ToolErrorMessage:
			var text string
			if m.Error != nil {
				text = m.Error.Error()
			}
			if err := s.writeSessionUpdate(sessionID, toolCallDeltaUpdate{
				SessionUpdate: updateToolCallUpdate,
				ToolCallID:    m.ToolCallID,
				Status:        toolStatusFailed,
				Content:       toolOutputContent(text),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) replayAssistantMessage(sessionID string, conv Conversation, nextID func(string) string, m llm.AssistantMessage) error {
	for _, block := range m.Blocks {
		switch b := block.(type) {
		case llm.TextBlock:
			if b.Text == "" {
				continue
			}
			if err := s.writeSessionUpdate(sessionID, agentMessageChunkUpdate{
				SessionUpdate: updateAgentMessageChunk,
				MessageID:     nextID("agent"),
				Content:       textContent{Type: contentText, Text: b.Text},
			}); err != nil {
				return err
			}
		case llm.ToolCallBlock:
			if err := s.writeSessionUpdate(sessionID, toolCallUpdate{
				SessionUpdate: updateToolCall,
				ToolCallID:    b.ID,
				Title:         nonEmpty(conv.ToolCallTitle(b.Name, b.Arguments), b.Name),
				Kind:          toolKind(b.Name),
				Status:        toolStatusInProgress,
				RawInput:      rawObject(b.Arguments),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func toolOutputContent(text string) []toolCallContent {
	if text == "" {
		return nil
	}
	return []toolCallContent{{
		Type:    toolCallContentContent,
		Content: textContent{Type: contentText, Text: text},
	}}
}

func (s *Server) handleSetConfigOption(msg rpcMessage) error {
	var params setConfigOptionParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.writeError(msg.ID, errInvalidParams, err.Error())
	}
	sess, ok := s.session(params.SessionID)
	if !ok {
		return s.writeError(msg.ID, errInvalidParams, "unknown sessionId")
	}

	switch params.ConfigID {
	case configIDModel:
		model, ok := modelByValue(params.Value)
		if !ok {
			return s.writeError(msg.ID, errInvalidParams, fmt.Sprintf("unknown model %q", params.Value))
		}
		if err := sess.conv.SwitchModel(model); err != nil {
			return s.writeError(msg.ID, errInternal, err.Error())
		}
	case configIDReasoning:
		level, ok := reasoningLevelByValue(params.Value)
		if !ok {
			return s.writeError(msg.ID, errInvalidParams, fmt.Sprintf("unknown reasoning level %q", params.Value))
		}
		if err := sess.conv.SwitchReasoningLevel(level); err != nil {
			return s.writeError(msg.ID, errInternal, err.Error())
		}
	default:
		return s.writeError(msg.ID, errInvalidParams, fmt.Sprintf("unknown configId %q", params.ConfigID))
	}

	return s.writeResult(msg.ID, setConfigOptionResult{ConfigOptions: configOptions(sess.conv)})
}

func (s *Server) handleSessionPrompt(ctx context.Context, msg rpcMessage) error {
	var params sessionPromptParams
	if err := decodeParams(msg.Params, &params); err != nil {
		return s.writeError(msg.ID, errInvalidParams, err.Error())
	}
	sess, ok := s.session(params.SessionID)
	if !ok {
		return s.writeError(msg.ID, errInvalidParams, "unknown sessionId")
	}

	prompt, err := promptText(params.Prompt)
	if err != nil {
		return s.writeError(msg.ID, errInvalidParams, err.Error())
	}
	trimmedPrompt := strings.TrimSpace(prompt)

	if strings.HasPrefix(trimmedPrompt, "/") {
		return s.startActiveTurn(ctx, msg, sess, func(turnCtx context.Context, turn *activeTurn) {
			s.runSlashPrompt(turnCtx, sess, turn, trimmedPrompt)
		})
	}

	return s.startActiveTurn(ctx, msg, sess, func(turnCtx context.Context, turn *activeTurn) {
		s.runPrompt(turnCtx, sess, turn, prompt)
	})
}

func (s *Server) startActiveTurn(ctx context.Context, msg rpcMessage, sess *acpSession, run func(context.Context, *activeTurn)) error {
	turnCtx, cancel := context.WithCancel(ctx)
	turn := &activeTurn{requestID: append(json.RawMessage(nil), msg.ID...), sessionID: sess.id, cancel: cancel}

	s.mu.Lock()
	if sess.activeTurn != nil {
		s.mu.Unlock()
		cancel()
		return s.writeError(msg.ID, errInvalidRequest, "conversation already running")
	}
	sess.activeTurn = turn
	s.mu.Unlock()

	go run(turnCtx, turn)
	return nil
}

func (s *Server) runSlashPrompt(ctx context.Context, sess *acpSession, turn *activeTurn, prompt string) {
	defer turn.cancel()
	defer s.clearActiveTurn(sess, turn)
	defer s.emitSessionInfo(sess)

	cmd, ok, message := lookupSlashCommand(sess.conv, prompt)
	if !ok {
		_ = s.writeError(turn.requestID, errInvalidParams, message)
		return
	}

	if err := cmd.Execute(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			_ = s.writeResult(turn.requestID, sessionPromptResult{StopReason: stopReasonCancelled})
			return
		}
		_ = s.writeError(turn.requestID, errInternal, err.Error())
		return
	}

	if cmd.Feedback != "" {
		_ = s.writeSessionUpdate(turn.sessionID, agentMessageChunkUpdate{
			SessionUpdate: updateAgentMessageChunk,
			MessageID:     "msg_agent_1",
			Content:       textContent{Type: contentText, Text: cmd.Feedback},
		})
	}
	_ = s.writeResult(turn.requestID, sessionPromptResult{StopReason: stopReasonEndTurn})
}

func (s *Server) runPrompt(ctx context.Context, sess *acpSession, turn *activeTurn, prompt string) {
	defer turn.cancel()
	defer s.clearActiveTurn(sess, turn)
	defer s.emitSessionInfo(sess)

	events, errs := sess.conv.Prompt(ctx, prompt)
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

func (s *Server) writeAvailableCommandsUpdate(sessionID string) error {
	return s.writeSessionUpdate(sessionID, availableCommandsUpdate{
		SessionUpdate:     updateAvailableCommands,
		AvailableCommands: availableSlashCommands(),
	})
}

func (s *Server) isInitialized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initialized
}

func (s *Server) session(sessionID string) (*acpSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	return sess, ok
}

func (s *Server) cancelSession(sessionID string) {
	s.mu.Lock()
	sess, ok := s.sessions[sessionID]
	if !ok || sess.activeTurn == nil {
		s.mu.Unlock()
		return
	}
	cancel := sess.activeTurn.cancel
	s.mu.Unlock()
	cancel()
}

func (s *Server) cancelAllTurns() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if sess.activeTurn != nil {
			cancels = append(cancels, sess.activeTurn.cancel)
		}
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (s *Server) clearActiveTurn(sess *acpSession, turn *activeTurn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess.activeTurn == turn {
		sess.activeTurn = nil
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
