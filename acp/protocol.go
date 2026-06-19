package acp

import "encoding/json"

const (
	methodInitialize             = "initialize"
	methodSessionNew             = "session/new"
	methodSessionLoad            = "session/load"
	methodSessionResume          = "session/resume"
	methodSessionClose           = "session/close"
	methodSessionDelete          = "session/delete"
	methodSessionList            = "session/list"
	methodSessionPrompt          = "session/prompt"
	methodSessionSetConfigOption = "session/set_config_option"
	methodSessionCancel          = "session/cancel"
	methodSessionUpdate          = "session/update"

	contentText         = "text"
	contentResourceLink = "resource_link"

	updateUserMessageChunk  = "user_message_chunk"
	updateAgentMessageChunk = "agent_message_chunk"
	updateToolCall          = "tool_call"
	updateToolCallUpdate    = "tool_call_update"
	updateAvailableCommands = "available_commands_update"
	updateSessionInfo       = "session_info_update"

	toolStatusInProgress = "in_progress"
	toolStatusCompleted  = "completed"
	toolStatusFailed     = "failed"

	toolCallContentContent = "content"

	stopReasonEndTurn   = "end_turn"
	stopReasonCancelled = "cancelled"
)

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

type rpcErrorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Error   rpcError        `json:"error"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeParams struct {
	Meta               map[string]any  `json:"_meta,omitempty"`
	ProtocolVersion    int             `json:"protocolVersion"`
	ClientCapabilities map[string]any  `json:"clientCapabilities,omitempty"`
	ClientInfo         *implementation `json:"clientInfo,omitempty"`
}

type initializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities agentCapabilities `json:"agentCapabilities"`
	AgentInfo         implementation    `json:"agentInfo"`
	AuthMethods       []any             `json:"authMethods"`
}

type agentCapabilities struct {
	LoadSession         bool               `json:"loadSession"`
	PromptCapabilities  promptCapabilities `json:"promptCapabilities"`
	MCPCapabilities     mcpCapabilities    `json:"mcpCapabilities"`
	SessionCapabilities map[string]any     `json:"sessionCapabilities"`
	Auth                map[string]any     `json:"auth"`
}

type promptCapabilities struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

type mcpCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

type implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

type sessionNewParams struct {
	Meta       map[string]any `json:"_meta,omitempty"`
	CWD        string         `json:"cwd"`
	MCPServers []any          `json:"mcpServers,omitempty"`
}

type sessionNewResult struct {
	SessionID     string         `json:"sessionId"`
	ConfigOptions []configOption `json:"configOptions,omitempty"`
}

type sessionLoadResult struct {
	ConfigOptions []configOption `json:"configOptions,omitempty"`
}

type sessionResumeResult struct {
	ConfigOptions []configOption `json:"configOptions,omitempty"`
}

type sessionLoadParams struct {
	Meta       map[string]any `json:"_meta,omitempty"`
	SessionID  string         `json:"sessionId"`
	CWD        string         `json:"cwd"`
	MCPServers []any          `json:"mcpServers,omitempty"`
}

type sessionResumeParams struct {
	Meta       map[string]any `json:"_meta,omitempty"`
	SessionID  string         `json:"sessionId"`
	CWD        string         `json:"cwd"`
	MCPServers []any          `json:"mcpServers,omitempty"`
}

type sessionCloseParams struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	SessionID string         `json:"sessionId"`
}

type sessionDeleteParams struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	SessionID string         `json:"sessionId"`
}

type sessionListParams struct {
	Meta   map[string]any `json:"_meta,omitempty"`
	CWD    string         `json:"cwd,omitempty"`
	Cursor string         `json:"cursor,omitempty"`
}

type sessionInfo struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Title     string `json:"title,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type sessionListResult struct {
	Sessions   []sessionInfo `json:"sessions"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// emptyResult marshals to an empty object, as required by session/close and
// session/delete.
type emptyResult struct{}

type configOption struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Description  string              `json:"description,omitempty"`
	Category     string              `json:"category,omitempty"`
	Type         string              `json:"type"`
	CurrentValue string              `json:"currentValue"`
	Options      []configOptionValue `json:"options"`
}

type configOptionValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type setConfigOptionParams struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	SessionID string         `json:"sessionId"`
	ConfigID  string         `json:"configId"`
	Value     string         `json:"value"`
}

type setConfigOptionResult struct {
	ConfigOptions []configOption `json:"configOptions"`
}

type sessionPromptParams struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

type sessionPromptResult struct {
	StopReason string `json:"stopReason"`
}

type cancelParams struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	SessionID string         `json:"sessionId"`
}

type contentBlock struct {
	Meta  map[string]any `json:"_meta,omitempty"`
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	URI   string         `json:"uri,omitempty"`
	Name  string         `json:"name,omitempty"`
	Title string         `json:"title,omitempty"`
}

type sessionUpdateParams struct {
	SessionID string `json:"sessionId"`
	Update    any    `json:"update"`
}

type availableCommandsUpdate struct {
	SessionUpdate     string             `json:"sessionUpdate"`
	AvailableCommands []availableCommand `json:"availableCommands"`
}

type sessionInfoUpdate struct {
	SessionUpdate string  `json:"sessionUpdate"`
	Title         *string `json:"title,omitempty"`
	UpdatedAt     string  `json:"updatedAt,omitempty"`
}

type availableCommand struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Input       *availableCommandInput `json:"input,omitempty"`
}

type availableCommandInput struct {
	Hint string `json:"hint"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type userMessageChunkUpdate struct {
	SessionUpdate string      `json:"sessionUpdate"`
	MessageID     string      `json:"messageId,omitempty"`
	Content       textContent `json:"content"`
}

type agentMessageChunkUpdate struct {
	SessionUpdate string      `json:"sessionUpdate"`
	MessageID     string      `json:"messageId,omitempty"`
	Content       textContent `json:"content"`
}

type toolCallUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	ToolCallID    string `json:"toolCallId"`
	Title         string `json:"title"`
	Kind          string `json:"kind,omitempty"`
	Status        string `json:"status,omitempty"`
	RawInput      any    `json:"rawInput,omitempty"`
}

type toolCallDeltaUpdate struct {
	SessionUpdate string            `json:"sessionUpdate"`
	ToolCallID    string            `json:"toolCallId"`
	Status        string            `json:"status,omitempty"`
	Content       []toolCallContent `json:"content,omitempty"`
}

type toolCallContent struct {
	Type    string      `json:"type"`
	Content textContent `json:"content"`
}
