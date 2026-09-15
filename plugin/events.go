package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// Event is a sealed marker for engine lifecycle events. Event payloads are
// plain data so they can be serialized by bridging plugins.
type Event interface{ event() }

// Operation identifies a started/ended pair. ParentID links an operation to
// the enclosing one (empty at the root), letting plugins rebuild the
// execution tree: workflow > prompt turn > cycle > request/tool call, and
// request > HTTP attempt.
type Operation struct {
	ID       string
	ParentID string
}

// NewOperation allocates a fresh identifier under the parent recorded in ctx.
func NewOperation(ctx context.Context) Operation {
	return Operation{ID: newID(), ParentID: ParentID(ctx)}
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("plugin: crypto/rand failure: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// Model identifies the requested provider model.
type Model struct {
	Provider string
	Name     string
}

// Usage is provider-reported token usage with an estimated cost. Token
// categories follow the provider: InputTokens includes cached and
// cache-written input tokens.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	// Cost is the estimated USD cost; valid only when CostAvailable is true.
	Cost          float64
	CostAvailable bool
}

// WorkflowStarted marks the start of a Lua workflow script run.
type WorkflowStarted struct {
	Operation
	Name string
}

// WorkflowEnded marks the end of a workflow run.
type WorkflowEnded struct {
	Operation
	Err error
}

// PromptTurnStarted marks the processing of one user prompt.
type PromptTurnStarted struct {
	Operation
	SessionID string
	Model     Model
}

// PromptTurnEnded marks the end of prompt processing.
type PromptTurnEnded struct {
	Operation
	Cycles int
	Err    error
}

// CycleStarted marks one model request plus its tool calls within a prompt turn.
type CycleStarted struct {
	Operation
	Index int
	Model Model
}

// CycleEnded marks the end of a cycle.
type CycleEnded struct {
	Operation
	Err error
}

// ModelRequestStarted marks a model request. Purpose is "conversation" for
// the main loop and a task label for auxiliary structured requests such as
// compaction.
type ModelRequestStarted struct {
	Operation
	Model   Model
	Purpose string
}

// ModelRequestFirstOutput marks the first streamed output of a request.
type ModelRequestFirstOutput struct {
	Operation
}

// ModelRequestEnded marks request completion. Usage is nil when the provider
// reported none; StopReason is empty for structured requests.
type ModelRequestEnded struct {
	Operation
	Model      Model
	Purpose    string
	Usage      *Usage
	StopReason string
	Err        error
}

// HTTPAttemptStarted marks one HTTP attempt of a model request.
type HTTPAttemptStarted struct {
	Operation
	Attempt int
}

// HTTPAttemptEnded marks the response headers or transport failure of an
// attempt. StatusCode is zero without a response.
type HTTPAttemptEnded struct {
	Operation
	Attempt    int
	StatusCode int
	Err        error
}

// ToolCallStarted marks a tool invocation after gates allowed it. Arguments
// are the gated (possibly rewritten) arguments.
type ToolCallStarted struct {
	Operation
	Call  ToolCall
	Model Model
}

// ToolCallDenied reports a tool call blocked before execution, either by a
// ToolGate or because the tool is unknown (Rejected).
type ToolCallDenied struct {
	Operation
	Call     ToolCall
	Model    Model
	Rejected bool
	Err      error
}

// ToolCallEnded marks tool completion. ResultSize is the serialized result
// length after filtering, zero on failure.
type ToolCallEnded struct {
	Operation
	Call       ToolCall
	Model      Model
	ResultSize int
	Err        error
}

// ContextCompacted reports that the conversation context was replaced.
type ContextCompacted struct {
	ParentID  string
	SessionID string
}

// SessionSaveFailed reports a persistence failure during prompt processing.
type SessionSaveFailed struct {
	ParentID  string
	SessionID string
	Err       error
}

func (WorkflowStarted) event()         {}
func (WorkflowEnded) event()           {}
func (PromptTurnStarted) event()       {}
func (PromptTurnEnded) event()         {}
func (CycleStarted) event()            {}
func (CycleEnded) event()              {}
func (ModelRequestStarted) event()     {}
func (ModelRequestFirstOutput) event() {}
func (ModelRequestEnded) event()       {}
func (HTTPAttemptStarted) event()      {}
func (HTTPAttemptEnded) event()        {}
func (ToolCallStarted) event()         {}
func (ToolCallDenied) event()          {}
func (ToolCallEnded) event()           {}
func (ContextCompacted) event()        {}
func (SessionSaveFailed) event()       {}
