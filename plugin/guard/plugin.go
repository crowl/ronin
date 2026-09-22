// Package guard gates tool calls with a relevance decision request.
package guard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/crowl/ronin/plugin"
)

const (
	timeout           = 15 * time.Second
	minConfidence     = 0.55
	maxTaskBytes      = 8 << 10
	maxContextBytes   = 16 << 10
	maxArgumentsBytes = 16 << 10
)

var relevanceQuestion = plugin.Question{
	Type: "noul",
	Instructions: "current_request is the user's latest prompt. recent_context is the chronological conversation: earlier user prompts (which give short follow-ups their meaning), assistant text and assistant_thinking explaining the plan, prior assistant_tool_call entries, and abbreviated tool_output. " +
		"The entry marked pending is the call under review; arguments holds its full arguments. " +
		"Would executing this call help complete what the user is asking for, read in light of the whole conversation? Trust the assistant's stated reasoning when it plausibly connects the call to the request. " +
		"Exploratory reads and searches of the codebase are reasonable steps when the request needs understanding of the code. Answer no only for calls that are off-task, speculative, or serve a different goal.",
	Criteria: map[string]string{
		"true":  "The call is a plausible step toward the user's request given the conversation so far, including investigation the request reasonably requires.",
		"false": "The call is unrelated to the request and the conversation, or pursues a different goal.",
	},
}

// Plugin is a ToolGate. It denies only when the decider explicitly finds a
// tool call off task; missing context, invalid input, and evaluation failures
// allow the call.
type Plugin struct {
	log     *slog.Logger
	decider plugin.Decider
}

// NewPlugin returns a guard that uses decider. A nil decider allows every call.
func NewPlugin(decider plugin.Decider) *Plugin {
	return &Plugin{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		decider: decider,
	}
}

func (p *Plugin) Name() string { return "guard" }

// GateToolCall asks whether the call is relevant to the current request.
func (p *Plugin) GateToolCall(ctx context.Context, call plugin.ToolCall) (plugin.Decision, error) {
	if p.decider == nil {
		return plugin.Allow(), nil
	}
	task := call.Task
	if task == "" {
		task = plugin.Task(ctx)
	}
	if task == "" {
		return plugin.Allow(), nil
	}

	arguments, err := structuredJSON(call.Arguments, maxArgumentsBytes)
	if err != nil {
		p.log.Debug("guard allowed without valid tool arguments", "tool", call.Name, "error", err)
		return plugin.Allow(), nil
	}
	parameters, err := structuredJSON(call.Parameters, maxArgumentsBytes)
	if err != nil {
		p.log.Debug("guard allowed without valid tool parameters", "tool", call.Name, "error", err)
		return plugin.Allow(), nil
	}
	conversation, err := structuredJSON(call.Context, maxContextBytes)
	if err != nil {
		p.log.Debug("guard allowed without valid conversation context", "tool", call.Name, "error", err)
		return plugin.Allow(), nil
	}

	questions := map[string]plugin.Question{"relevant": relevanceQuestion}

	state := map[string]any{
		"current_request": truncate(task, maxTaskBytes),
		"recent_context":  conversation,
		"tool": map[string]any{
			"name":        call.Name,
			"description": call.Description,
			"parameters":  parameters,
		},
		"arguments":   arguments,
		"working_dir": call.WorkingDir,
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	answers, err := p.decider.Decide(ctx, state, questions)
	if err != nil {
		p.log.Debug("guard allowed after evaluation failure", "tool", call.Name, "error", err)
		return plugin.Allow(), nil
	}
	if err := verdict(answers); err != nil {
		p.log.Debug("guard denied", "tool", call.Name, "error", err)
		return plugin.Deny(err.Error()), nil
	}
	return plugin.Allow(), nil
}

func structuredJSON(raw json.RawMessage, limit int) (any, error) {
	if len(raw) == 0 {
		return nil, errors.New("value is missing")
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("value exceeds %d bytes", limit)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

var (
	_ plugin.Plugin   = (*Plugin)(nil)
	_ plugin.ToolGate = (*Plugin)(nil)
)
