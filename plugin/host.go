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
	call.Task = Task(ctx)
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
