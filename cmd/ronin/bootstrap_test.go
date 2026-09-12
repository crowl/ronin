package main

import (
	"bytes"
	"errors"
	"flag"
	"reflect"
	"strings"
	"testing"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/llm"
)

func TestParseFlags(t *testing.T) {
	t.Run("collects flags and positional arguments", func(t *testing.T) {
		opts, err := parseFlags([]string{
			"-resume", "-prompt", "hello", "-working_dir", "/repo",
			"-model", "openai:gpt", "-reasoning", "high",
			"-context-file", "a.md", "-context-file", "b.md",
			"-skill", "git", "-mcp", "docs", "-mcp", "all",
			"run", "flow.lua", "input",
		}, &bytes.Buffer{})
		if err != nil {
			t.Fatalf("parseFlags() error = %v", err)
		}
		want := cliOptions{
			resume:         true,
			prompt:         "hello",
			workingDir:     "/repo",
			model:          "openai:gpt",
			reasoningLevel: "high",
			contextFiles:   []string{"a.md", "b.md"},
			skills:         []string{"git"},
			mcp:            []string{"docs", "all"},
			args:           []string{"run", "flow.lua", "input"},
		}
		if !reflect.DeepEqual(opts, want) {
			t.Fatalf("parseFlags() = %#v, want %#v", opts, want)
		}
	})

	t.Run("defaults working directory to the current directory", func(t *testing.T) {
		opts, err := parseFlags(nil, &bytes.Buffer{})
		if err != nil {
			t.Fatalf("parseFlags() error = %v", err)
		}
		if opts.workingDir != "." || opts.version || len(opts.args) != 0 {
			t.Fatalf("parseFlags() = %#v", opts)
		}
	})

	t.Run("reports help without treating it as a failure", func(t *testing.T) {
		var usage bytes.Buffer
		_, err := parseFlags([]string{"-h"}, &usage)
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("parseFlags(-h) error = %v, want flag.ErrHelp", err)
		}
		if !strings.Contains(usage.String(), "-working_dir") {
			t.Fatalf("usage output missing flags:\n%s", usage.String())
		}
	})

	t.Run("rejects unknown flags", func(t *testing.T) {
		var usage bytes.Buffer
		_, err := parseFlags([]string{"-bogus"}, &usage)
		if err == nil || errors.Is(err, flag.ErrHelp) {
			t.Fatalf("parseFlags(-bogus) error = %v, want parse failure", err)
		}
		if usage.Len() == 0 {
			t.Fatal("parse failure did not write usage")
		}
	})
}

func TestSelectModel(t *testing.T) {
	configured := llm.Model{Provider: "select-test", Name: "configured", ContextWindow: 100_000, SupportedReasoning: llm.NewReasoningSet(llm.ReasoningLevelOff, llm.ReasoningLevelLow)}
	override := llm.Model{Provider: "select-test", Name: "override", ContextWindow: 100_000, SupportedReasoning: llm.NewReasoningSet(llm.ReasoningLevelOff, llm.ReasoningLevelHigh)}
	for _, model := range []llm.Model{configured, override} {
		if err := llm.RegisterModel(model, func(llm.ReasoningLevel) (llm.ModelClient, error) {
			return nil, errors.New("unused")
		}); err != nil {
			t.Fatalf("RegisterModel(%s) error = %v", model, err)
		}
	}
	settings := config.Settings{
		Model:          config.Model{Provider: configured.Provider, Name: configured.Name},
		ReasoningLevel: string(llm.ReasoningLevelLow),
		MaxTurns:       7,
	}

	t.Run("uses configured model without flags", func(t *testing.T) {
		sel, err := selectModel(settings, "", "")
		if err != nil {
			t.Fatalf("selectModel() error = %v", err)
		}
		if sel.model != configured || sel.level != llm.ReasoningLevelLow {
			t.Fatalf("selectModel() = %s/%s, want configured/low", sel.model, sel.level)
		}
		if sel.modelOverridden || sel.reasoningOverridden {
			t.Fatal("selectModel() reported overrides without flags")
		}
		if sel.settings.MaxTurns != 7 {
			t.Fatalf("settings not preserved: %#v", sel.settings)
		}
	})

	t.Run("flags override configuration and are recorded", func(t *testing.T) {
		sel, err := selectModel(settings, "select-test:override", "high")
		if err != nil {
			t.Fatalf("selectModel() error = %v", err)
		}
		if sel.model != override || sel.level != llm.ReasoningLevelHigh {
			t.Fatalf("selectModel() = %s/%s, want override/high", sel.model, sel.level)
		}
		if !sel.modelOverridden || !sel.reasoningOverridden {
			t.Fatal("selectModel() did not record overrides")
		}
		if sel.settings.Model != (config.Model{Provider: "select-test", Name: "override"}) || sel.settings.ReasoningLevel != "high" {
			t.Fatalf("settings not updated with overrides: %#v", sel.settings)
		}
	})

	t.Run("rejects malformed model flag", func(t *testing.T) {
		if _, err := selectModel(settings, "no-colon", ""); err == nil || !strings.Contains(err.Error(), "invalid -model") {
			t.Fatalf("selectModel() error = %v, want invalid -model", err)
		}
	})

	t.Run("rejects unsupported reasoning level for the selected model", func(t *testing.T) {
		if _, err := selectModel(settings, "", "high"); err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("selectModel() error = %v, want unsupported reasoning", err)
		}
	})

	t.Run("rejects unknown model", func(t *testing.T) {
		if _, err := selectModel(settings, "select-test:missing", ""); err == nil || !strings.Contains(err.Error(), "unknown model") {
			t.Fatalf("selectModel() error = %v, want unknown model", err)
		}
	})
}

func TestStartMCP(t *testing.T) {
	servers := map[string]config.MCPServer{"docs": {Command: "unused"}}

	t.Run("no selection yields an empty registry", func(t *testing.T) {
		registry, err := startMCP(t.Context(), t.TempDir(), servers, nil)
		if err != nil {
			t.Fatalf("startMCP() error = %v", err)
		}
		if len(registry.Tools()) != 0 || len(registry.Instructions()) != 0 {
			t.Fatal("empty registry exposed tools or instructions")
		}
		if err := registry.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	t.Run("invalid selection fails before connecting", func(t *testing.T) {
		_, err := startMCP(t.Context(), t.TempDir(), servers, []string{"missing"})
		if err == nil || !strings.Contains(err.Error(), "invalid MCP selection") || !strings.Contains(err.Error(), `"missing"`) {
			t.Fatalf("startMCP() error = %v", err)
		}
	})
}

func TestCloseInto(t *testing.T) {
	t.Run("keeps a nil error when close succeeds", func(t *testing.T) {
		var err error
		closeInto(&err, "resource", fakeCloser{})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	})

	t.Run("reports a close failure after success", func(t *testing.T) {
		var err error
		closeInto(&err, "resource", fakeCloser{err: errors.New("boom")})
		if err == nil || !strings.Contains(err.Error(), "failed to close resource: boom") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("preserves the primary failure", func(t *testing.T) {
		primary := errors.New("primary")
		err := primary
		closeInto(&err, "resource", fakeCloser{err: errors.New("boom")})
		if !errors.Is(err, primary) || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("err = %v, want both failures", err)
		}
	})
}

type fakeCloser struct{ err error }

func (f fakeCloser) Close() error { return f.err }
