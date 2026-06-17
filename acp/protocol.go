package acp

import "encoding/json"

const (
	methodInitialize    = "initialize"
	methodSessionNew    = "session/new"
	methodSessionPrompt = "session/prompt"
	methodSessionCancel = "session/cancel"
	methodSessionUpdate = "session/update"

	contentText         = "text"
	contentResourceLink = "resource_link"

	updateAgentMessageChunk = "agent_message_chunk"
	updateToolCall          = "tool_call"
	updateToolCallUpdate    = "tool_call_update"

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
	SessionID string `json:"sessionId"`
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

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
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
