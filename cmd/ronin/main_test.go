package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/internal/agenttools"
	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/anthropic"
	"github.com/crowl/ronin/llm/openai"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
)

func TestAgentToolFactory(t *testing.T) {
	factory := agenttools.NewFactory()
	mcpTools := []runtime.Tool{fakePromptTool{name: "mcp__tool"}}

	t.Run("read-only agent receives navigation and read_file", func(t *testing.T) {
		tools := factory.New(t.TempDir(), true, false, mcpTools)
		if got := toolNames(tools); !reflect.DeepEqual(got, []string{"read_file", "code_map", "code_find"}) {
			t.Fatalf("tool names = %v, want [read_file code_map code_find]", got)
		}
	})

	t.Run("writable agent receives default and MCP tools", func(t *testing.T) {
		tools := factory.New(t.TempDir(), false, false, mcpTools)
		want := []string{"code_map", "code_find", "read_file", "edit_file", "write_file", "shell", "mcp__tool"}
		if got := toolNames(tools); !reflect.DeepEqual(got, want) {
			t.Fatalf("tool names = %v, want %v", got, want)
		}
	})

	t.Run("managed writable agent has file tools without shell", func(t *testing.T) {
		tools := factory.New(t.TempDir(), false, true, nil)
		want := []string{"code_map", "code_find", "read_file", "edit_file", "write_file"}
		if got := toolNames(tools); !reflect.DeepEqual(got, want) {
			t.Fatalf("tool names = %v, want %v", got, want)
		}
	})

	t.Run("managed read-only agent receives navigation and read_file", func(t *testing.T) {
		tools := factory.New(t.TempDir(), true, true, nil)
		if got := toolNames(tools); !reflect.DeepEqual(got, []string{"read_file", "code_map", "code_find"}) {
			t.Fatalf("tool names = %v, want [read_file code_map code_find]", got)
		}
	})
}

func toolNames(tools []runtime.Tool) []string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name()
	}
	return names
}

func TestBuiltInPlugins(t *testing.T) {
	t.Run("requires API key when Jev is enabled", func(t *testing.T) {
		t.Setenv("TYPESAFE_API_KEY", "")
		plugins, err := builtInPlugins(false)
		if err == nil || !strings.Contains(err.Error(), "TYPESAFE_API_KEY") {
			t.Fatalf("builtInPlugins(false) error = %v, want missing key error", err)
		}
		if plugins != nil {
			t.Fatalf("builtInPlugins(false) plugins = %#v, want nil", plugins)
		}
	})

	t.Run("enables Jev by default", func(t *testing.T) {
		t.Setenv("TYPESAFE_API_KEY", " test-key ")
		plugins, err := builtInPlugins(false)
		if err != nil {
			t.Fatalf("builtInPlugins(false) error = %v", err)
		}
		if len(plugins) != 2 || plugins[0].Name() != "opentelemetry" || plugins[1].Name() != "guard" {
			t.Fatalf("builtInPlugins(false) = %#v", plugins)
		}
	})

	t.Run("disables Jev without requiring an API key", func(t *testing.T) {
		t.Setenv("TYPESAFE_API_KEY", "")
		plugins, err := builtInPlugins(true)
		if err != nil {
			t.Fatalf("builtInPlugins(true) error = %v", err)
		}
		if len(plugins) != 1 || plugins[0].Name() != "opentelemetry" {
			t.Fatalf("builtInPlugins(true) = %#v", plugins)
		}
	})
}

func TestVersion(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })

	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "development", value: "dev", want: "ronin dev\n"},
		{name: "release", value: "v0.1.0", want: "ronin v0.1.0\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			version = test.value
			var output bytes.Buffer
			if err := writeVersion(&output); err != nil {
				t.Fatalf("writeVersion() error = %v", err)
			}
			if got := output.String(); got != test.want {
				t.Errorf("writeVersion() = %q, want %q", got, test.want)
			}
		})
	}
}
func TestSetupProvidersDefaultOpenAIConfiguration(t *testing.T) {
	if scenario := os.Getenv("RONIN_SETUP_PROVIDERS_DEFAULT_SCENARIO"); scenario != "" {
		for _, name := range []string{"OPENAI_API_KEY", "GEMINI_API_KEY", "ANTHROPIC_API_KEY", "XAI_API_KEY"} {
			t.Setenv(name, "")
		}
		if err := os.Unsetenv("OPENAI_BASE_URL"); err != nil {
			t.Fatalf("unset OPENAI_BASE_URL: %v", err)
		}
		if err := os.Unsetenv("GEMINI_BASE_URL"); err != nil {
			t.Fatalf("unset GEMINI_BASE_URL: %v", err)
		}
		if err := os.Unsetenv("ANTHROPIC_BASE_URL"); err != nil {
			t.Fatalf("unset ANTHROPIC_BASE_URL: %v", err)
		}
		if err := os.Unsetenv("XAI_BASE_URL"); err != nil {
			t.Fatalf("unset XAI_BASE_URL: %v", err)
		}

		switch scenario {
		case "no-credentials":
			if err := setupProviders(); err != nil {
				t.Fatalf("setupProviders() error = %v", err)
			}
			if len(llm.Models()) != 0 {
				t.Fatalf("registered models = %v, want none", llm.Models())
			}
			return
		case "api-key":
			t.Setenv("OPENAI_API_KEY", "api-key")
		default:
			t.Fatalf("unknown scenario %q", scenario)
		}

		if err := setupProviders(); err != nil {
			t.Fatalf("setupProviders() error = %v", err)
		}
		client, err := llm.LoadModelClient(openai.Gpt56Sol, llm.ReasoningLevelOff)
		if err != nil {
			t.Fatalf("LoadModelClient() error = %v", err)
		}
		apiKey := reflect.ValueOf(client).Elem().FieldByName("apiKey").String()
		if apiKey != "api-key" {
			t.Errorf("configured API key = %q, want api-key", apiKey)
		}
		return
	}

	for _, scenario := range []string{"no-credentials", "api-key"} {
		t.Run(scenario, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestSetupProvidersDefaultOpenAIConfiguration$")
			cmd.Env = append(os.Environ(), "RONIN_SETUP_PROVIDERS_DEFAULT_SCENARIO="+scenario)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("setupProviders subprocess failed: %v\n%s", err, output)
			}
		})
	}
}

func TestSetupProvidersCustomOpenAIConfiguration(t *testing.T) {
	for _, name := range []string{"GEMINI_API_KEY", "ANTHROPIC_API_KEY", "XAI_API_KEY"} {
		t.Setenv(name, "")
	}
	for _, name := range []string{"GEMINI_BASE_URL", "ANTHROPIC_BASE_URL", "XAI_BASE_URL"} {
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}

	t.Run("missing API key", func(t *testing.T) {
		t.Setenv("OPENAI_BASE_URL", "https://proxy.example.com/v1")
		t.Setenv("OPENAI_API_KEY", "")
		err := setupProviders()
		if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") || !strings.Contains(err.Error(), "OPENAI_BASE_URL") {
			t.Fatalf("setupProviders() error = %v, want OPENAI_API_KEY requirement for OPENAI_BASE_URL", err)
		}
	})

	t.Run("empty base URL", func(t *testing.T) {
		t.Setenv("OPENAI_BASE_URL", "")
		t.Setenv("OPENAI_API_KEY", "test-key")
		err := setupProviders()
		if err == nil || !strings.Contains(err.Error(), "OPENAI_BASE_URL") {
			t.Fatalf("setupProviders() error = %v, want OPENAI_BASE_URL validation error", err)
		}
	})

	t.Run("invalid base URL", func(t *testing.T) {
		t.Setenv("OPENAI_BASE_URL", "ftp://proxy.example.com/v1")
		t.Setenv("OPENAI_API_KEY", "test-key")
		err := setupProviders()
		if err == nil || !strings.Contains(err.Error(), "OPENAI_BASE_URL") {
			t.Fatalf("setupProviders() error = %v, want OPENAI_BASE_URL validation error", err)
		}
	})

	t.Run("custom URL", func(t *testing.T) {
		var gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{}}\n\n"))
		}))
		defer server.Close()

		t.Setenv("OPENAI_BASE_URL", server.URL+"/v1/")
		t.Setenv("OPENAI_API_KEY", "test-key")
		if err := setupProviders(); err != nil {
			t.Fatalf("setupProviders() error = %v", err)
		}

		client, err := llm.LoadModelClient(openai.Gpt56Sol, llm.ReasoningLevelOff)
		if err != nil {
			t.Fatalf("LoadModelClient() error = %v", err)
		}
		events, errs := client.PredictNext(context.Background(), llm.PredictNextRequest{})
		for range events {
		}
		if err := <-errs; err != nil {
			t.Fatalf("PredictNext() error = %v", err)
		}
		if gotPath != "/v1/responses" {
			t.Errorf("request path = %q, want /v1/responses", gotPath)
		}
	})
}

func TestSetupProvidersCustomProviderConfiguration(t *testing.T) {
	if scenario := os.Getenv("RONIN_SETUP_PROVIDERS_PROVIDER_SCENARIO"); scenario != "" {
		for _, name := range []string{
			"OPENAI_API_KEY", "OPENAI_BASE_URL",
			"GEMINI_API_KEY", "GEMINI_BASE_URL",
			"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL",
			"XAI_API_KEY", "XAI_BASE_URL",
		} {
			if err := os.Unsetenv(name); err != nil {
				t.Fatalf("unset %s: %v", name, err)
			}
		}
		t.Setenv("HOME", t.TempDir())

		switch scenario {
		case "gemini-missing-key":
			t.Setenv("GEMINI_BASE_URL", "https://proxy.example.com/v1beta")
		case "anthropic-missing-key":
			t.Setenv("ANTHROPIC_BASE_URL", "https://proxy.example.com/v1")
		case "xai-missing-key":
			t.Setenv("XAI_BASE_URL", "https://proxy.example.com/v1")
		case "gemini-empty-url":
			t.Setenv("GEMINI_API_KEY", "key")
			t.Setenv("GEMINI_BASE_URL", "")
		case "anthropic-empty-url":
			t.Setenv("ANTHROPIC_API_KEY", "key")
			t.Setenv("ANTHROPIC_BASE_URL", "")
		case "xai-empty-url":
			t.Setenv("XAI_API_KEY", "key")
			t.Setenv("XAI_BASE_URL", "")
		case "gemini-invalid-url":
			t.Setenv("GEMINI_API_KEY", "key")
			t.Setenv("GEMINI_BASE_URL", "ftp://proxy.example.com/v1beta")
		case "anthropic-invalid-url":
			t.Setenv("ANTHROPIC_API_KEY", "key")
			t.Setenv("ANTHROPIC_BASE_URL", "ftp://proxy.example.com/v1")
		case "xai-invalid-url":
			t.Setenv("XAI_API_KEY", "key")
			t.Setenv("XAI_BASE_URL", "ftp://proxy.example.com/v1")
		case "gemini-custom":
			t.Setenv("GEMINI_API_KEY", "key")
			t.Setenv("GEMINI_BASE_URL", "https://proxy.example.com/v1beta///")
		case "anthropic-custom":
			t.Setenv("ANTHROPIC_API_KEY", "key")
			t.Setenv("ANTHROPIC_BASE_URL", "https://proxy.example.com/v1///")
		case "xai-custom":
			t.Setenv("XAI_API_KEY", "key")
			t.Setenv("XAI_BASE_URL", "https://proxy.example.com/v1///")
		default:
			t.Fatalf("unknown scenario %q", scenario)
		}

		err := setupProviders()
		switch scenario {
		case "gemini-missing-key", "anthropic-missing-key", "xai-missing-key", "gemini-empty-url", "anthropic-empty-url", "xai-empty-url", "gemini-invalid-url", "anthropic-invalid-url", "xai-invalid-url":
			if err == nil {
				t.Fatalf("setupProviders() error = nil, want failure")
			}
			if !strings.Contains(err.Error(), "BASE_URL") {
				t.Fatalf("setupProviders() error = %v, want base URL context", err)
			}
			if strings.Contains(err.Error(), "key") {
				t.Fatalf("setupProviders() error exposes API key: %v", err)
			}
		case "gemini-custom":
			if err != nil {
				t.Fatalf("setupProviders() error = %v", err)
			}
			client, err := llm.LoadModelClient(llm.Model{Provider: "google", Name: "gemini-3.1-flash-lite", ContextWindow: 1048576}, llm.ReasoningLevelOff)
			if err != nil {
				t.Fatalf("load Gemini model: %v", err)
			}
			if got := reflect.ValueOf(client).Elem().FieldByName("baseURL").String(); got != "https://proxy.example.com/v1beta" {
				t.Fatalf("Gemini base URL = %q", got)
			}
		case "anthropic-custom":
			if err != nil {
				t.Fatalf("setupProviders() error = %v", err)
			}
			client, err := llm.LoadModelClient(anthropic.ClaudeHaiku45, llm.ReasoningLevelOff)
			if err != nil {
				t.Fatalf("load Anthropic model: %v", err)
			}
			if got := reflect.ValueOf(client).Elem().FieldByName("baseURL").String(); got != "https://proxy.example.com/v1/messages" {
				t.Fatalf("Anthropic base URL = %q", got)
			}
		case "xai-custom":
			if err != nil {
				t.Fatalf("setupProviders() error = %v", err)
			}
			client, err := llm.LoadModelClient(llm.Model{Provider: "xai", Name: "grok-4.6", ContextWindow: 500_000}, llm.ReasoningLevelOff)
			if err != nil {
				t.Fatalf("load xAI model: %v", err)
			}
			if got := reflect.ValueOf(client).Elem().FieldByName("baseURL").String(); got != "https://proxy.example.com/v1/responses" {
				t.Fatalf("xAI base URL = %q", got)
			}
		}
		return
	}

	for _, scenario := range []string{
		"gemini-missing-key", "anthropic-missing-key", "xai-missing-key",
		"gemini-empty-url", "anthropic-empty-url", "xai-empty-url",
		"gemini-invalid-url", "anthropic-invalid-url", "xai-invalid-url",
		"gemini-custom", "anthropic-custom", "xai-custom",
	} {
		t.Run(scenario, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestSetupProvidersCustomProviderConfiguration$")
			cmd.Env = append(os.Environ(), "RONIN_SETUP_PROVIDERS_PROVIDER_SCENARIO="+scenario)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("setupProviders subprocess failed: %v\n%s", err, output)
			}
		})
	}
}

func TestParseModelFlag(t *testing.T) {
	t.Run("parses provider and name", func(t *testing.T) {
		got, err := parseModelFlag("openai:gpt-5.5")
		if err != nil {
			t.Fatalf("parseModelFlag() error = %v", err)
		}
		want := config.Model{Provider: "openai", Name: "gpt-5.5"}
		if got != want {
			t.Fatalf("parseModelFlag() = %#v, want %#v", got, want)
		}
	})

	t.Run("splits on the first colon only", func(t *testing.T) {
		got, err := parseModelFlag("provider:name:with:colons")
		if err != nil {
			t.Fatalf("parseModelFlag() error = %v", err)
		}
		want := config.Model{Provider: "provider", Name: "name:with:colons"}
		if got != want {
			t.Fatalf("parseModelFlag() = %#v, want %#v", got, want)
		}
	})

	t.Run("rejects invalid input", func(t *testing.T) {
		cases := []struct {
			name    string
			value   string
			wantErr string
		}{
			{name: "no colon", value: "gpt-5.5", wantErr: "want format <provider>:<name>"},
			{name: "empty provider", value: ":gpt-5.5", wantErr: "must not be empty"},
			{name: "empty name", value: "openai:", wantErr: "must not be empty"},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := parseModelFlag(tc.value)
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseModelFlag(%q) error = %v, want error containing %q", tc.value, err, tc.wantErr)
				}
			})
		}
	})
}

func TestResolveSessionModel(t *testing.T) {
	sessionModel := llm.Model{Provider: "session-test", Name: "stored", ContextWindow: 272_000}
	overrideModel := llm.Model{Provider: "session-test", Name: "override", ContextWindow: 128_000}
	for _, model := range []llm.Model{sessionModel, overrideModel} {
		if err := llm.RegisterModel(model, func(llm.ReasoningLevel) (llm.ModelClient, error) {
			return nil, errors.New("unused")
		}); err != nil {
			t.Fatalf("RegisterModel(%s) error = %v", model, err)
		}
	}

	activeSession := session.Session{
		Model:          config.Model{Provider: sessionModel.Provider, Name: sessionModel.Name},
		ReasoningLevel: string(llm.ReasoningLevelHigh),
	}

	t.Run("uses stored session settings without explicit overrides", func(t *testing.T) {
		gotModel, gotLevel, err := resolveSessionModel(activeSession, overrideModel, llm.ReasoningLevelOff, false, false)
		if err != nil {
			t.Fatalf("resolveSessionModel() error = %v", err)
		}
		if gotModel != sessionModel {
			t.Fatalf("resolveSessionModel() model = %#v, want stored model %#v", gotModel, sessionModel)
		}
		if gotLevel != llm.ReasoningLevelHigh {
			t.Fatalf("resolveSessionModel() reasoning level = %q, want %q", gotLevel, llm.ReasoningLevelHigh)
		}
	})

	t.Run("explicit flags override stored session settings", func(t *testing.T) {
		gotModel, gotLevel, err := resolveSessionModel(activeSession, overrideModel, llm.ReasoningLevelLow, true, true)
		if err != nil {
			t.Fatalf("resolveSessionModel() error = %v", err)
		}
		if gotModel != overrideModel {
			t.Fatalf("resolveSessionModel() model = %#v, want override model %#v", gotModel, overrideModel)
		}
		if gotLevel != llm.ReasoningLevelLow {
			t.Fatalf("resolveSessionModel() reasoning level = %q, want %q", gotLevel, llm.ReasoningLevelLow)
		}
	})

	t.Run("overrides are independent", func(t *testing.T) {
		gotModel, gotLevel, err := resolveSessionModel(activeSession, overrideModel, llm.ReasoningLevelLow, true, false)
		if err != nil {
			t.Fatalf("resolveSessionModel() error = %v", err)
		}
		if gotModel != overrideModel || gotLevel != llm.ReasoningLevelHigh {
			t.Fatalf("resolveSessionModel() = %#v/%q, want override model and stored reasoning", gotModel, gotLevel)
		}
	})
}

func TestStartupSession(t *testing.T) {
	metadata := session.Metadata{
		Model:          config.Model{Provider: "openai", Name: "gpt-5.5"},
		ReasoningLevel: "medium",
	}
	workingDir := "/workspace"

	t.Run("fresh startup creates a session without loading active session", func(t *testing.T) {
		created := session.Session{ID: "created", WorkingDir: workingDir}
		store := &fakeStartupSessionStore{created: created}

		got, _, err := startupSession(t.Context(), store, workingDir, metadata, false)
		if err != nil {
			t.Fatalf("startupSession() error = %v", err)
		}
		if got.ID != created.ID {
			t.Fatalf("startupSession() ID = %q, want %q", got.ID, created.ID)
		}
		if store.latestCalls != 0 {
			t.Fatalf("Latest calls = %d, want 0", store.latestCalls)
		}
		if store.createCalls != 1 {
			t.Fatalf("Create calls = %d, want 1", store.createCalls)
		}
		if store.createdWorkingDir != workingDir {
			t.Fatalf("Create workingDir = %q, want %q", store.createdWorkingDir, workingDir)
		}
		if store.createdMetadata != metadata {
			t.Fatalf("Create metadata = %#v, want %#v", store.createdMetadata, metadata)
		}
	})

	t.Run("resume startup loads active session", func(t *testing.T) {
		loaded := session.Session{ID: "loaded", WorkingDir: workingDir}
		store := &fakeStartupSessionStore{loaded: loaded, loadedOK: true}

		got, _, err := startupSession(t.Context(), store, workingDir, metadata, true)
		if err != nil {
			t.Fatalf("startupSession() error = %v", err)
		}
		if got.ID != loaded.ID {
			t.Fatalf("startupSession() ID = %q, want %q", got.ID, loaded.ID)
		}
		if store.latestCalls != 1 {
			t.Fatalf("Latest calls = %d, want 1", store.latestCalls)
		}
		if store.latestWorkingDir != workingDir {
			t.Fatalf("Latest workingDir = %q, want %q", store.latestWorkingDir, workingDir)
		}
		if store.createCalls != 0 {
			t.Fatalf("Create calls = %d, want 0", store.createCalls)
		}
	})

	t.Run("resume startup creates session when active session is missing", func(t *testing.T) {
		created := session.Session{ID: "created", WorkingDir: workingDir}
		store := &fakeStartupSessionStore{created: created}

		got, _, err := startupSession(t.Context(), store, workingDir, metadata, true)
		if err != nil {
			t.Fatalf("startupSession() error = %v", err)
		}
		if got.ID != created.ID {
			t.Fatalf("startupSession() ID = %q, want %q", got.ID, created.ID)
		}
		if store.latestCalls != 1 {
			t.Fatalf("Latest calls = %d, want 1", store.latestCalls)
		}
		if store.createCalls != 1 {
			t.Fatalf("Create calls = %d, want 1", store.createCalls)
		}
	})

	t.Run("returns load error", func(t *testing.T) {
		wantErr := errors.New("read workspace")
		store := &fakeStartupSessionStore{loadErr: wantErr}

		_, _, err := startupSession(t.Context(), store, workingDir, metadata, true)
		if err == nil || !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "failed to load session") {
			t.Fatalf("startupSession() error = %v, want load error wrapping %v", err, wantErr)
		}
	})

	t.Run("returns create error", func(t *testing.T) {
		wantErr := errors.New("write session")
		store := &fakeStartupSessionStore{createErr: wantErr}

		_, _, err := startupSession(t.Context(), store, workingDir, metadata, false)
		if err == nil || !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "failed to create session") {
			t.Fatalf("startupSession() error = %v, want create error wrapping %v", err, wantErr)
		}
	})
}

func TestRunPrompt(t *testing.T) {
	t.Run("writes assistant text with trailing newline", func(t *testing.T) {
		var output strings.Builder
		conv := fakePromptConversation{events: []runtime.Event{
			runtime.AssistantMessageDeltaReceived{Text: "hello"},
			runtime.AssistantMessageDeltaReceived{Text: " world"},
		}}

		if err := runPrompt(t.Context(), conv, "prompt", &output); err != nil {
			t.Fatalf("runPrompt() error = %v", err)
		}

		if got, want := output.String(), "hello world\n"; got != want {
			t.Fatalf("output\ngot:  %q\nwant: %q", got, want)
		}
	})

	t.Run("ignores tool events", func(t *testing.T) {
		var output strings.Builder
		toolErr := errors.New("exit status 1")
		conv := fakePromptConversation{events: []runtime.Event{
			runtime.AssistantMessageDeltaReceived{Text: "checking"},
			runtime.ToolExecutionStarted{Tool: fakePromptTool{name: "read_file"}},
			runtime.ToolExecutionFailed{Tool: fakePromptTool{name: "shell"}, Error: toolErr},
			runtime.AssistantMessageDeltaReceived{Text: "done"},
		}}

		if err := runPrompt(t.Context(), conv, "prompt", &output); err != nil {
			t.Fatalf("runPrompt() error = %v", err)
		}

		if got, want := output.String(), "checkingdone\n"; got != want {
			t.Fatalf("output\ngot:  %q\nwant: %q", got, want)
		}
	})

	t.Run("returns prompt error", func(t *testing.T) {
		var output strings.Builder
		wantErr := errors.New("conversation failed")
		conv := fakePromptConversation{err: wantErr}

		err := runPrompt(t.Context(), conv, "prompt", &output)
		if !errors.Is(err, wantErr) {
			t.Fatalf("runPrompt() error = %v, want %v", err, wantErr)
		}
	})
}

type fakeStartupSessionStore struct {
	loaded   session.Session
	messages []session.Message
	loadedOK bool
	loadErr  error

	created   session.Session
	createErr error

	latestCalls int
	createCalls int

	latestWorkingDir  string
	createdWorkingDir string
	createdMetadata   session.Metadata
}

func (s *fakeStartupSessionStore) Latest(_ context.Context, workingDir string) (session.Session, []session.Message, bool, error) {
	s.latestCalls++
	s.latestWorkingDir = workingDir
	return s.loaded, s.messages, s.loadedOK, s.loadErr
}

func (s *fakeStartupSessionStore) Load(context.Context, string) (session.Session, []session.Message, bool, error) {
	return session.Session{}, nil, false, nil
}

func (s *fakeStartupSessionStore) Create(_ context.Context, workingDir string, metadata session.Metadata) (session.Session, error) {
	s.createCalls++
	s.createdWorkingDir = workingDir
	s.createdMetadata = metadata
	return s.created, s.createErr
}

func (*fakeStartupSessionStore) Fork(context.Context, string, session.Metadata, session.Event) (session.Session, error) {
	return session.Session{}, errors.New("unexpected fork")
}
func (*fakeStartupSessionStore) SwitchModel(context.Context, string, session.Metadata, session.Event) error {
	return errors.New("unexpected model switch")
}

func (s *fakeStartupSessionStore) Append(context.Context, string, session.Event) error { return nil }
func (s *fakeStartupSessionStore) UpdateMetadata(context.Context, string, session.Metadata) error {
	return nil
}
func (s *fakeStartupSessionStore) List(context.Context, string) ([]session.Ref, error) {
	return nil, nil
}
func (s *fakeStartupSessionStore) Delete(context.Context, string) error { return nil }
func (s *fakeStartupSessionStore) Clear(context.Context, string) error  { return nil }

type fakePromptConversation struct {
	events []runtime.Event
	err    error
}

func (a fakePromptConversation) Prompt(context.Context, string) (<-chan runtime.Event, <-chan error) {
	events := make(chan runtime.Event, len(a.events))
	errs := make(chan error, 1)

	for _, event := range a.events {
		events <- event
	}
	close(events)

	if a.err != nil {
		errs <- a.err
	}
	close(errs)

	return events, errs
}

type fakePromptTool struct {
	name string
}

func (t fakePromptTool) Name() string {
	return t.name
}

func (fakePromptTool) Description() string {
	return ""
}

func (fakePromptTool) Parameters() *jsonschema.Schema {
	return nil
}

func (fakePromptTool) Call(context.Context, json.RawMessage) (any, error) {
	return nil, nil
}
