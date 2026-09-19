package jev

import (
	"context"
	"fmt"
	"log/slog"
	"unicode/utf8"

	"github.com/crowl/ronin/plugin"
)

// gatedTools are still judged for irreversible side effects. Relevance is
// asked of every tool once a task is present.
var mutatingTools = map[string]struct{}{
	"shell":      {},
	"write_file": {},
	"edit_file":  {},
}

var relevanceQuestion = Question{
	Type:         "noul",
	Instructions: "Given `task`, would executing `tool` with `arguments` in `working_dir` help complete that task? Answer yes only if the call is a reasonable next step for the task as written, not a tangent, fishing expedition, or unrelated mutation.",
	Criteria: map[string]string{
		"true":  "The call advances the stated task in a direct, proportionate way.",
		"false": "The call is off-task, speculative, or aimed at a different goal than `task`.",
	},
}

var irreversibleQuestion = Question{
	Type:         "noul",
	Instructions: "Would executing `tool` with `arguments` in `working_dir` delete data, overwrite files outside a normal edit, force-push, leak secrets, or otherwise create an irreversible side effect?",
	Criteria: map[string]string{
		"true":  "The call deletes, overwrites irreversibly, publishes, or exposes secrets.",
		"false": "The call is reversible or only reads and writes ordinary project files.",
	},
}

type evaluator interface {
	Decide(ctx context.Context, state any, questions map[string]Question) (Response, error)
}

// Plugin is a ToolGate that asks Jev whether a tool call is relevant to the
// current task. It is inactive until Start loads a non-off mode and an API key.
type Plugin struct {
	log    *slog.Logger
	cfg    Config
	client evaluator
}

// NewPlugin returns a Jev tool-gate plugin. Start reads RONIN_JEV_* and
// TYPESAFE_API_KEY; a missing key in shadow or enforce mode drops the plugin.
func NewPlugin() *Plugin {
	return &Plugin{log: slog.Default()}
}

func (p *Plugin) Name() string { return "jev" }

// Start loads environment configuration. Off mode is a successful no-op.
func (p *Plugin) Start(context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	p.cfg = cfg
	if cfg.Mode == ModeOff {
		return nil
	}
	if cfg.APIKey == "" {
		return fmt.Errorf("%s=%s requires %s", "RONIN_JEV_MODE", cfg.Mode, cfg.APIKeyEnv)
	}
	p.client = newClient(cfg)
	return nil
}

// GateToolCall sends the call to Jev when a task is present. API failures fail
// open so a TypeSafe outage cannot stall the coding loop; only an explicit
// policy deny blocks a call, and only in enforce mode.
func (p *Plugin) GateToolCall(ctx context.Context, call plugin.ToolCall) (plugin.Decision, error) {
	if p.cfg.Mode == ModeOff || p.client == nil {
		return plugin.Allow(), nil
	}
	task := call.Task
	if task == "" {
		task = plugin.Task(ctx)
	}
	if task == "" {
		return plugin.Allow(), nil
	}

	state := map[string]any{
		"task":        truncate(task, maxStateBytes),
		"tool":        call.Name,
		"arguments":   truncate(string(call.Arguments), maxStateBytes),
		"working_dir": call.WorkingDir,
		"session_id":  call.SessionID,
	}

	questions := map[string]Question{"relevant": relevanceQuestion}
	if _, ok := mutatingTools[call.Name]; ok {
		questions["irreversible"] = irreversibleQuestion
	}

	ctx, cancel := withTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	resp, err := p.client.Decide(ctx, state, questions)
	if err != nil {
		p.log.Warn("jev tool gate failed open", "tool", call.Name, "mode", string(p.cfg.Mode), "error", err)
		return plugin.Allow(), nil
	}

	judgment := judgmentFrom(resp.Answers)
	action := Decide(judgment, p.cfg.MinConfidence)
	p.log.Info("jev tool gate",
		"tool", call.Name,
		"mode", string(p.cfg.Mode),
		"action", action.String(),
		"relevant", judgment.Relevant,
		"irreversible", judgment.Irreversible,
	)

	if action == ActionDeny && p.cfg.Mode == ModeEnforce {
		return plugin.Deny(denyReason(judgment)), nil
	}
	return plugin.Allow(), nil
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
	_ plugin.Starter  = (*Plugin)(nil)
	_ plugin.ToolGate = (*Plugin)(nil)
)
