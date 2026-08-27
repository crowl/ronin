package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crowl/ronin/config"
)

func TestDir(t *testing.T) {
	t.Run("honors XDG_CONFIG_HOME", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")

		dir, err := config.Dir()
		if err != nil {
			t.Fatalf("Dir() error = %v", err)
		}
		if want := filepath.Join("/custom/xdg", "ronin"); dir != want {
			t.Fatalf("Dir() = %q, want %q", dir, want)
		}
	})

	t.Run("falls back to HOME/.config", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "/home/user")

		dir, err := config.Dir()
		if err != nil {
			t.Fatalf("Dir() error = %v", err)
		}
		if want := filepath.Join("/home/user", ".config", "ronin"); dir != want {
			t.Fatalf("Dir() = %q, want %q", dir, want)
		}
	})

	t.Run("errors when XDG and HOME unset", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "")

		_, err := config.Dir()
		if err == nil || !strings.Contains(err.Error(), "config dir") {
			t.Fatalf("Dir() error = %v, want config dir error", err)
		}
	})
}

func TestDataDir(t *testing.T) {
	t.Run("honors XDG_DATA_HOME", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "/custom/data")

		dir, err := config.DataDir()
		if err != nil {
			t.Fatalf("DataDir() error = %v", err)
		}
		if want := filepath.Join("/custom/data", "ronin"); dir != want {
			t.Fatalf("DataDir() = %q, want %q", dir, want)
		}
	})

	t.Run("falls back to HOME local share", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "")
		t.Setenv("HOME", "/home/user")

		dir, err := config.DataDir()
		if err != nil {
			t.Fatalf("DataDir() error = %v", err)
		}
		if want := filepath.Join("/home/user", ".local", "share", "ronin"); dir != want {
			t.Fatalf("DataDir() = %q, want %q", dir, want)
		}
	})

	t.Run("errors when XDG and HOME unset", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "")
		t.Setenv("HOME", "")

		_, err := config.DataDir()
		if err == nil || !strings.Contains(err.Error(), "data dir") {
			t.Fatalf("DataDir() error = %v, want data dir error", err)
		}
	})
}

func TestEnsureDataDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", base)

	dir, err := config.EnsureDataDir()
	if err != nil {
		t.Fatalf("EnsureDataDir() error = %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("data directory stat = %#v, error = %v", info, err)
	}
}

func TestSkillsDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")

	dir, err := config.SkillsDir()
	if err != nil {
		t.Fatalf("SkillsDir() error = %v", err)
	}
	if want := filepath.Join("/custom/xdg", "ronin", "skills"); dir != want {
		t.Fatalf("SkillsDir() = %q, want %q", dir, want)
	}
}

func TestWorkflowsDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")

	dir, err := config.WorkflowsDir()
	if err != nil {
		t.Fatalf("WorkflowsDir() error = %v", err)
	}
	if want := filepath.Join("/custom/xdg", "ronin", "workflows"); dir != want {
		t.Fatalf("WorkflowsDir() = %q, want %q", dir, want)
	}
}

func TestLoad(t *testing.T) {
	t.Run("first run writes default and returns it", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", base)

		settings, err := config.Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if !reflect.DeepEqual(settings, defaultSettings()) {
			t.Fatalf("Load() = %#v, want %#v", settings, defaultSettings())
		}

		path := filepath.Join(base, "ronin", "config.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read written config: %v", err)
		}

		var written config.Settings
		if err := json.Unmarshal(data, &written); err != nil {
			t.Fatalf("written config is not valid JSON: %v", err)
		}
		if !reflect.DeepEqual(written, defaultSettings()) {
			t.Fatalf("written config = %#v, want %#v", written, defaultSettings())
		}
	})

	t.Run("parses an existing valid config", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", base)
		writeConfig(t, base, `{
			"model": {"provider": "anthropic", "name": "claude"},
			"reasoning_level": "high",
			"max_turns": 10
		}`)

		settings, err := config.Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}

		want := config.Settings{
			Model:                   config.Model{Provider: "anthropic", Name: "claude"},
			ReasoningLevel:          "high",
			MaxTurns:                10,
			ToolOutputSummarization: config.ToolOutputSummarization{},
		}
		if !reflect.DeepEqual(settings, want) {
			t.Fatalf("Load() = %#v, want %#v", settings, want)
		}
	})

	t.Run("default config round-trips", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", base)

		first, err := config.Load()
		if err != nil {
			t.Fatalf("first Load() error = %v", err)
		}
		second, err := config.Load()
		if err != nil {
			t.Fatalf("second Load() error = %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("round-trip mismatch: first = %#v, second = %#v", first, second)
		}
	})

	t.Run("rejects invalid configs", func(t *testing.T) {
		cases := []struct {
			name    string
			content string
			wantErr string
		}{
			{
				name:    "malformed JSON",
				content: `{ not json `,
				wantErr: "parse config",
			},
			{
				name:    "unknown field",
				content: `{"model": {"provider": "openai", "name": "gpt-5.5"}, "reasoning_level": "medium", "max_turns": 512, "theme": "dark"}`,
				wantErr: "parse config",
			},
			{
				name:    "empty provider",
				content: `{"model": {"provider": "", "name": "gpt-5.5"}, "reasoning_level": "medium", "max_turns": 512}`,
				wantErr: "model.provider",
			},
			{
				name:    "empty name",
				content: `{"model": {"provider": "openai", "name": ""}, "reasoning_level": "medium", "max_turns": 512}`,
				wantErr: "model.name",
			},
			{
				name:    "empty reasoning level",
				content: `{"model": {"provider": "openai", "name": "gpt-5.5"}, "reasoning_level": "", "max_turns": 512}`,
				wantErr: "reasoning_level",
			},
			{
				name:    "non-positive max turns",
				content: `{"model": {"provider": "openai", "name": "gpt-5.5"}, "reasoning_level": "medium", "max_turns": 0}`,
				wantErr: "max_turns",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				base := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", base)
				writeConfig(t, base, tc.content)

				_, err := config.Load()
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load() error = %v, want error containing %q", err, tc.wantErr)
				}
			})
		}
	})
}

func TestMCPConfiguration(t *testing.T) {
	t.Run("parses stdio server", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", base)
		writeConfig(t, base, `{
			"model": {"provider": "openai", "name": "gpt-5.5"},
			"reasoning_level": "medium",
			"max_turns": 512,
			"mcp_servers": {
				"gopls": {"command": "gopls", "args": ["mcp"], "env": {"GOWORK": "off"}}
			}
		}`)

		settings, err := config.Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		server := settings.MCPServers["gopls"]
		if server.Command != "gopls" || !reflect.DeepEqual(server.Args, []string{"mcp"}) || server.Env["GOWORK"] != "off" {
			t.Fatalf("MCP server = %#v", server)
		}
	})

	t.Run("rejects empty command", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", base)
		writeConfig(t, base, `{
			"model": {"provider": "openai", "name": "gpt-5.5"},
			"reasoning_level": "medium",
			"max_turns": 512,
			"mcp_servers": {"gopls": {"command": ""}}
		}`)

		_, err := config.Load()
		if err == nil || !strings.Contains(err.Error(), "mcp_servers.gopls.command") {
			t.Fatalf("Load() error = %v", err)
		}
	})
}

func defaultSettings() config.Settings {
	return config.Settings{
		Model:          config.Model{Provider: "openai", Name: "gpt-5.5"},
		ReasoningLevel: "medium",
		MaxTurns:       512,
		ToolOutputSummarization: config.ToolOutputSummarization{
			Enabled:          false,
			MinBytes:         16_000,
			MaxSummaryTokens: 1_000,
			SummarizeErrors:  false,
			ExcludedTools:    []string{"read_file", "edit_file", "write_file"},
		},
	}
}

func writeConfig(t *testing.T, base, content string) {
	t.Helper()
	dir := filepath.Join(base, "ronin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
