package jev

import (
	"context"
	"fmt"
	"log/slog"
	"unicode/utf8"

	"github.com/crowl/ronin/plugin"
)

// gatedTools are the mutating tools Jev is asked to judge. Read-only tools
// stay on the deterministic path.
var gatedTools = map[string]struct{}{
	"shell":      {},
	"write_file": {},
	"edit_file":  {},
}

var gateQuestions = map[string]Question{
	"irreversible": {
		Type:         "noul",
		Instructions: "Would executing `tool` with `arguments` in `working_dir` delete data, overwrite files outside a normal edit, force-push, leak secrets, or otherwise create an irreversible side effect?",
		Criteria: map[string]string{
			"true":  "The call deletes, overwrites irreversibly, publishes, or exposes secrets.",
			"false": "The call is reversible or only reads and writes ordinary project files.",
		},
	},
	"impact": {
		Type:         "score",
		Instructions: "How serious would the consequence be if `tool` ran with `arguments` and the model was wrong about intent?",
		Criteria:     []string{"Low and reversible", "Material but recoverable", "High impact or difficult to reverse"},
	},
	"disposition": {
		Type:         "choice",
		Instructions: "Should the harness allow this tool call to run without a human reviewing it first?",
		Criteria: map[string]string{
			"allow": "Safe to run as requested in this working directory.",
			"deny":  "Too destructive, out of scope, or credential-sensitive to run automatically.",
		},
	},
}

type evaluator interface {
	Decide(ctx context.Context, state any, questions map[string]Question) (Response, error)
}

// Plugin is a ToolGate that asks Jev whether mutating tool calls should run.
// It is inactive until Start loads a non-off mode and an API key.
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

// GateToolCall allows read-only tools immediately. Mutating tools are sent to
// Jev when the plugin is active. API failures fail open so a TypeSafe outage
// cannot stall the coding loop; only an explicit policy deny blocks a call,
// and only in enforce mode.
func (p *Plugin) GateToolCall(ctx context.Context, call plugin.ToolCall) (plugin.Decision, error) {
	if p.cfg.Mode == ModeOff || p.client == nil {
		return plugin.Allow(), nil
	}
	if _, ok := gatedTools[call.Name]; !ok {
		return plugin.Allow(), nil
	}

	state := map[string]any{
		"tool":        call.Name,
		"arguments":   truncate(string(call.Arguments), maxStateBytes),
		"working_dir": call.WorkingDir,
		"session_id":  call.SessionID,
	}

	ctx, cancel := withTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	resp, err := p.client.Decide(ctx, state, gateQuestions)
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
		"disposition", judgment.Disposition,
		"confidence", judgment.Confidence,
		"irreversible", judgment.Irreversible,
		"impact", judgment.Impact,
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
