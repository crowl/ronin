package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"unicode/utf8"

	"github.com/crowl/ronin/plugin"
)

var relevanceQuestion = Question{
	Type:         "noul",
	Instructions: "Given the current request, recent conversation, and proposed tool call, would executing this call help complete the request? Answer yes only if it is a reasonable next step, not a tangent or fishing expedition.",
	Criteria: map[string]string{
		"true":  "The call advances the current request in a direct, proportionate way.",
		"false": "The call is off-task, speculative, or aimed at a different goal.",
	},
}

var irreversibleQuestion = Question{
	Type:         "noul",
	Instructions: "Would executing the proposed tool call delete data, overwrite files outside a normal edit, publish or deploy, force-push, leak secrets, or otherwise create an irreversible side effect?",
	Criteria: map[string]string{
		"true":  "The call deletes, overwrites irreversibly, publishes, deploys, or exposes secrets.",
		"false": "The call is read-only, reversible, or only writes ordinary project files.",
	},
}

type evaluator interface {
	Decide(ctx context.Context, state any, questions map[string]Question) (Response, error)
}

// Plugin is a strict ToolGate that asks Jev whether each tool call is relevant
// and reversible. Registering it enables enforcement; failures deny the call.
type Plugin struct {
	log    *slog.Logger
	cfg    config
	client evaluator
}

// NewPlugin returns an enforcing Jev tool gate for apiKey.
func NewPlugin(apiKey string) *Plugin {
	if apiKey == "" {
		panic("jev: API key is required")
	}
	cfg := config{
		Endpoint:      defaultEndpoint,
		Model:         defaultModel,
		APIKey:        apiKey,
		Timeout:       defaultTimeout,
		MinConfidence: defaultMinConfidence,
	}
	return newPlugin(cfg, newClient(cfg))
}

func newPlugin(cfg config, client evaluator) *Plugin {
	// Discard by default. slog.Default writes to stderr and corrupts the TUI.
	return &Plugin{log: slog.New(slog.NewTextHandler(io.Discard, nil)), cfg: cfg, client: client}
}

func (p *Plugin) Name() string { return "jev" }

// GateToolCall denies calls that Jev rejects or cannot evaluate safely.
func (p *Plugin) GateToolCall(ctx context.Context, call plugin.ToolCall) (plugin.Decision, error) {
	if p.client == nil {
		return plugin.Deny("Jev tool gate is not initialized"), nil
	}
	task := call.Task
	if task == "" {
		task = plugin.Task(ctx)
	}
	if task == "" {
		return plugin.Deny("Jev cannot evaluate a tool call without a current user request"), nil
	}

	arguments, err := structuredJSON(call.Arguments, maxArgumentsBytes)
	if err != nil {
		return plugin.Deny("Jev cannot evaluate invalid tool arguments: " + err.Error()), nil
	}
	parameters, err := structuredJSON(call.Parameters, maxArgumentsBytes)
	if err != nil {
		return plugin.Deny("Jev cannot evaluate invalid tool parameters: " + err.Error()), nil
	}
	conversation, err := structuredJSON(call.Context, maxContextBytes)
	if err != nil {
		return plugin.Deny("Jev cannot evaluate invalid conversation context: " + err.Error()), nil
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
	questions := map[string]Question{
		"relevant":     relevanceQuestion,
		"irreversible": irreversibleQuestion,
	}

	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	resp, err := p.client.Decide(ctx, state, questions)
	if err != nil {
		p.log.Debug("jev tool gate denied after evaluation failure", "tool", call.Name, "error", err)
		return plugin.Deny("Jev evaluation failed: " + err.Error()), nil
	}

	judgment, err := judgmentFrom(resp.Answers)
	if err != nil {
		p.log.Debug("jev tool gate denied malformed response", "tool", call.Name, "error", err)
		return plugin.Deny("Jev returned an invalid response: " + err.Error()), nil
	}
	action := Decide(judgment, p.cfg.MinConfidence)
	p.log.Debug("jev tool gate", "tool", call.Name, "action", action.String(), "relevant", judgment.Relevant, "irreversible", judgment.Irreversible)
	if action == ActionDeny {
		return plugin.Deny(denyReason(judgment)), nil
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
