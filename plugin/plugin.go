// Package plugin defines the extension contract between Ronin's execution
// engine and in-process plugins. The engine publishes lifecycle events and
// consults tool hooks through a Host; plugins implement Plugin plus any of the
// optional capability interfaces.
//
// The package is a dependency leaf: it imports only the standard library so
// that every layer of the engine, including model clients, can publish events.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
)

// Plugin is the minimum contract. Capabilities are discovered by type
// assertion against the optional interfaces below.
type Plugin interface {
	// Name identifies the plugin in diagnostics and denial messages.
	Name() string
}

// Starter is implemented by plugins that acquire resources before use.
// A plugin whose Start fails is dropped from the Host.
type Starter interface {
	Start(ctx context.Context) error
}

// Closer is implemented by plugins that must flush or release resources.
type Closer interface {
	Close(ctx context.Context) error
}

// Observer receives events synchronously on the publishing goroutine, in
// plugin registration order. Implementations must return promptly and must
// not initiate model calls. Panics are recovered and logged.
type Observer interface {
	Observe(ctx context.Context, event Event)
}

// ToolGate decides whether a tool call may run. Gates run in registration
// order; a rewrite is visible to subsequent gates and the first denial wins.
// A returned error or panic denies the call.
type ToolGate interface {
	GateToolCall(ctx context.Context, call ToolCall) (Decision, error)
}

// ToolResultFilter transforms the JSON result of a successful tool call
// before it is persisted and shown to the model. Filters run in registration
// order, each receiving the previous output. A returned error or panic fails
// the tool call.
type ToolResultFilter interface {
	FilterToolResult(ctx context.Context, call ToolCall, result json.RawMessage) (json.RawMessage, error)
}

// ToolCall describes a tool invocation requested by the model.
type ToolCall struct {
	// ID is the provider-assigned tool call identifier.
	ID string
	// Name is the tool name.
	Name string
	// Arguments is the JSON arguments payload. Hooks must not mutate it.
	Arguments json.RawMessage
	// SessionID is the owning session, empty when not yet persisted.
	SessionID string
	// WorkingDir is the conversation's working directory.
	WorkingDir string
}

// Decision is a ToolGate verdict. The zero value allows the call unchanged.
type Decision struct {
	denied    bool
	reason    string
	arguments json.RawMessage
}

// Allow permits the call with unchanged arguments.
func Allow() Decision { return Decision{} }

// Deny blocks the call. The reason is reported to the model.
func Deny(reason string) Decision { return Decision{denied: true, reason: reason} }

// Rewrite permits the call with replacement arguments.
func Rewrite(arguments json.RawMessage) Decision { return Decision{arguments: arguments} }

// DeniedError reports that a ToolGate blocked a tool call.
type DeniedError struct {
	Plugin string
	Reason string
}

func (e *DeniedError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("tool call denied by plugin %q", e.Plugin)
	}
	return fmt.Sprintf("tool call denied by plugin %q: %s", e.Plugin, e.Reason)
}
