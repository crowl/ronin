// Package guard gates tool calls with one decision request. Every call is
// checked for relevance. Shell calls are also checked for file inspection or
// ad-hoc edits that bypass the harness file tools. Ordinary tooling that must
// rewrite files, such as formatters and git restore, is not a bypass.
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
	shellToolName     = "shell"
	timeout           = 3 * time.Second
	minConfidence     = 0.55
	maxTaskBytes      = 8 << 10
	maxContextBytes   = 16 << 10
	maxArgumentsBytes = 16 << 10
)

var relevanceQuestion = plugin.Question{
	Type:         "noul",
	Instructions: "Given the current request, recent conversation, and proposed tool call, would executing this call help complete the request? Answer yes only if it is a reasonable next step, not a tangent or fishing expedition.",
	Criteria: map[string]string{
		"true":  "The call advances the current request in a direct, proportionate way.",
		"false": "The call is off-task, speculative, or aimed at a different goal.",
	},
}

var fileBypassQuestion = plugin.Question{
	Type: "noul",
	Instructions: "Does this shell command inspect or rewrite file contents in place of the harness file tools (read_file, edit_file, write_file)? " +
		"Count pipelines and chained commands. Answer yes for cat, head, tail, sed, awk, perl, python, dd, tee, redirection, and similar file IO used to view, dump, or patch file contents. " +
		"Answer no for commands that do not touch file contents, such as git status, go test, ls, or echo without redirection. " +
		"Also answer no for ordinary development tooling that must read or rewrite files to do its job, such as gofmt, goimports, rustfmt, prettier, and git checkout or git restore of tracked paths. " +
		"A formatter or git restore is not a bypass even though it rewrites file bytes.",
	Criteria: map[string]string{
		"true":  "The command views, dumps, or patches file contents outside the harness file tools, including through a pipe, chain, or redirection.",
		"false": "The command does not inspect or ad-hoc edit file contents. Formatters, compilers, test runners, and git checkout/restore of tracked files are not bypasses.",
	},
}

// Plugin is a strict ToolGate. A missing decider, malformed input, or
// evaluation failure denies the call.
type Plugin struct {
	log     *slog.Logger
	decider plugin.Decider
}

// NewPlugin returns a guard that uses decider. A nil decider denies every call.
func NewPlugin(decider plugin.Decider) *Plugin {
	return &Plugin{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		decider: decider,
	}
}

func (p *Plugin) Name() string { return "guard" }

// GateToolCall asks whether the call is relevant and, for shell, whether it
// inspects or ad-hoc edits files outside the harness file tools. Both
// questions go in one decision request.
func (p *Plugin) GateToolCall(ctx context.Context, call plugin.ToolCall) (plugin.Decision, error) {
	if p.decider == nil {
		return plugin.Deny("guard is not initialized"), nil
	}
	task := call.Task
	if task == "" {
		task = plugin.Task(ctx)
	}
	if task == "" {
		return plugin.Deny("guard cannot evaluate a tool call without a current user request"), nil
	}

	arguments, err := structuredJSON(call.Arguments, maxArgumentsBytes)
	if err != nil {
		return plugin.Deny("guard cannot evaluate invalid tool arguments: " + err.Error()), nil
	}
	parameters, err := structuredJSON(call.Parameters, maxArgumentsBytes)
	if err != nil {
		return plugin.Deny("guard cannot evaluate invalid tool parameters: " + err.Error()), nil
	}
	conversation, err := structuredJSON(call.Context, maxContextBytes)
	if err != nil {
		return plugin.Deny("guard cannot evaluate invalid conversation context: " + err.Error()), nil
	}

	questions := map[string]plugin.Question{"relevant": relevanceQuestion}
	if call.Name == shellToolName {
		if _, err := shellCommand(call.Arguments); err != nil {
			return plugin.Deny("guard cannot evaluate shell command: " + err.Error()), nil
		}
		questions["file_bypass"] = fileBypassQuestion
	}

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
		p.log.Debug("guard denied after evaluation failure", "tool", call.Name, "error", err)
		return plugin.Deny("guard evaluation failed: " + err.Error()), nil
	}
	if err := verdict(answers, call.Name == shellToolName); err != nil {
		p.log.Debug("guard denied", "tool", call.Name, "error", err)
		return plugin.Deny(err.Error()), nil
	}
	return plugin.Allow(), nil
}

func shellCommand(raw json.RawMessage) (string, error) {
	value, err := structuredJSON(raw, maxArgumentsBytes)
	if err != nil {
		return "", err
	}
	args, ok := value.(map[string]any)
	if !ok {
		return "", errors.New("shell arguments must be an object")
	}
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return "", errors.New("shell command is missing")
	}
	return command, nil
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
