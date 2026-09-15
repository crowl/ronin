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

// Host dispatches to a fixed set of plugins. A nil *Host is valid and does
// nothing, so callers never need to special-case the absence of plugins.
// Host methods are safe for concurrent use.
type Host struct {
	mu      sync.RWMutex
	plugins []Plugin
	log     *slog.Logger
}

// NewHost registers plugins in the order given. Nil plugins are ignored.
func NewHost(plugins ...Plugin) *Host {
	h := &Host{log: slog.Default()}
	for _, p := range plugins {
		if p != nil {
			h.plugins = append(h.plugins, p)
		}
	}
	return h
}

// Start starts every Starter. Plugins whose Start fails are removed from the
// Host and reported in the returned error; the remaining plugins stay active.
func (h *Host) Start(ctx context.Context) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var errs []error
	active := h.plugins[:0:0]
	for _, p := range h.plugins {
		if s, ok := p.(Starter); ok {
			if err := s.Start(ctx); err != nil {
				errs = append(errs, fmt.Errorf("start plugin %q: %w", p.Name(), err))
				continue
			}
		}
		active = append(active, p)
	}
	h.plugins = active
	return errors.Join(errs...)
}

// Close closes every Closer in reverse registration order and joins failures.
func (h *Host) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var errs []error
	for i := len(h.plugins) - 1; i >= 0; i-- {
		if c, ok := h.plugins[i].(Closer); ok {
			if err := c.Close(ctx); err != nil {
				errs = append(errs, fmt.Errorf("close plugin %q: %w", h.plugins[i].Name(), err))
			}
		}
	}
	h.plugins = nil
	return errors.Join(errs...)
}

// Publish delivers event to every Observer. Observer failures never propagate.
func (h *Host) Publish(ctx context.Context, event Event) {
	if h == nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, p := range h.plugins {
		o, ok := p.(Observer)
		if !ok {
			continue
		}
		func() {
			defer h.recoverPanic(p, "observe")
			o.Observe(ctx, event)
		}()
	}
}

// GateToolCall consults every ToolGate and returns the arguments to execute
// with. A denial is returned as a *DeniedError; other errors wrap the failing
// plugin's error. Any error means the call must not run.
func (h *Host) GateToolCall(ctx context.Context, call ToolCall) (json.RawMessage, error) {
	if h == nil {
		return call.Arguments, nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, p := range h.plugins {
		g, ok := p.(ToolGate)
		if !ok {
			continue
		}
		decision, err := h.gate(ctx, p.Name(), g, call)
		if err != nil {
			return nil, fmt.Errorf("plugin %q tool gate: %w", p.Name(), err)
		}
		if decision.denied {
			return nil, &DeniedError{Plugin: p.Name(), Reason: decision.reason}
		}
		if decision.arguments != nil {
			call.Arguments = decision.arguments
		}
	}
	return call.Arguments, nil
}

func (h *Host) gate(ctx context.Context, name string, g ToolGate, call ToolCall) (decision Decision, err error) {
	defer func() {
		if r := recover(); r != nil {
			h.logPanic(name, "gate", r)
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return g.GateToolCall(ctx, call)
}

// FilterToolResult passes result through every ToolResultFilter. Any error
// wraps the failing plugin's error and means the result must not be used.
func (h *Host) FilterToolResult(ctx context.Context, call ToolCall, result json.RawMessage) (json.RawMessage, error) {
	if h == nil {
		return result, nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, p := range h.plugins {
		f, ok := p.(ToolResultFilter)
		if !ok {
			continue
		}
		filtered, err := h.filter(ctx, p.Name(), f, call, result)
		if err != nil {
			return nil, fmt.Errorf("plugin %q tool result filter: %w", p.Name(), err)
		}
		result = filtered
	}
	return result, nil
}

func (h *Host) filter(ctx context.Context, name string, f ToolResultFilter, call ToolCall, result json.RawMessage) (filtered json.RawMessage, err error) {
	defer func() {
		if r := recover(); r != nil {
			h.logPanic(name, "filter", r)
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return f.FilterToolResult(ctx, call, result)
}

func (h *Host) recoverPanic(p Plugin, op string) {
	if r := recover(); r != nil {
		h.logPanic(p.Name(), op, r)
	}
}

func (h *Host) logPanic(name, op string, r any) {
	h.log.Error("plugin panic recovered", "plugin", name, "operation", op, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
}
