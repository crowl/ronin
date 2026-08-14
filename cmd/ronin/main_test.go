package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
)

func TestParseWorkflowCommand(t *testing.T) {
	t.Run("not a workflow command", func(t *testing.T) {
		got, workflowMode, err := parseWorkflowCommand([]string{"other"}, strings.NewReader("unused"))
		if err != nil {
			t.Fatalf("parseWorkflowCommand() error = %v", err)
		}
		if workflowMode {
			t.Fatal("workflowMode = true, want false")
		}
		if got != (workflowCommand{}) {
			t.Fatalf("command = %#v, want zero value", got)
		}
	})

	t.Run("no input does not read stdin", func(t *testing.T) {
		stdin := &failingReader{err: errors.New("stdin should not be read")}
		got, workflowMode, err := parseWorkflowCommand([]string{"run", "workflow.lua"}, stdin)
		if err != nil {
			t.Fatalf("parseWorkflowCommand() error = %v", err)
		}
		if !workflowMode {
			t.Fatal("workflowMode = false, want true")
		}
		if got.Script != "workflow.lua" || got.Input != "" {
			t.Fatalf("command = %#v, want script and empty input", got)
		}
	})

	t.Run("inline input", func(t *testing.T) {
		got, _, err := parseWorkflowCommand([]string{"run", "workflow.lua", "implement it"}, strings.NewReader("unused"))
		if err != nil {
			t.Fatalf("parseWorkflowCommand() error = %v", err)
		}
		if got.Input != "implement it" {
			t.Fatalf("Input = %q, want implement it", got.Input)
		}
	})

	t.Run("double dash allows input beginning with dash", func(t *testing.T) {
		for _, want := range []string{"-requirement", "-"} {
			got, _, err := parseWorkflowCommand([]string{"run", "workflow.lua", "--", want}, strings.NewReader("unused"))
			if err != nil {
				t.Fatalf("parseWorkflowCommand() error = %v", err)
			}
			if got.Input != want {
				t.Fatalf("Input = %q, want %q", got.Input, want)
			}
		}
	})

	t.Run("explicit stdin", func(t *testing.T) {
		got, _, err := parseWorkflowCommand([]string{"run", "workflow.lua", "-"}, strings.NewReader("from stdin"))
		if err != nil {
			t.Fatalf("parseWorkflowCommand() error = %v", err)
		}
		if got.Input != "from stdin" {
			t.Fatalf("Input = %q, want from stdin", got.Input)
		}
	})

	t.Run("explicit stdin read errors", func(t *testing.T) {
		wantErr := errors.New("read failed")
		_, _, err := parseWorkflowCommand([]string{"run", "workflow.lua", "-"}, &failingReader{err: wantErr})
		if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "read workflow stdin") {
			t.Fatalf("parseWorkflowCommand() error = %v, want wrapped stdin error", err)
		}

		_, _, err = parseWorkflowCommand([]string{"run", "workflow.lua", "-"}, nil)
		if err == nil || !strings.Contains(err.Error(), "input is unavailable") {
			t.Fatalf("parseWorkflowCommand() nil stdin error = %v", err)
		}
	})

	t.Run("input file option forms", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "requirement.md")
		if err := os.WriteFile(path, []byte("from file"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		for name, args := range map[string][]string{
			"separate": {"run", "workflow.lua", "--input", path},
			"equals":   {"run", "workflow.lua", "--input=" + path},
		} {
			t.Run(name, func(t *testing.T) {
				got, _, err := parseWorkflowCommand(args, strings.NewReader("unused"))
				if err != nil {
					t.Fatalf("parseWorkflowCommand() error = %v", err)
				}
				if got.Input != "from file" {
					t.Fatalf("Input = %q, want from file", got.Input)
				}
			})
		}
	})

	t.Run("relative input file uses process working directory", func(t *testing.T) {
		oldWorkingDir, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd() error = %v", err)
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "requirement.md"), []byte("relative file"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatalf("Chdir() error = %v", err)
		}
		t.Cleanup(func() { _ = os.Chdir(oldWorkingDir) })

		got, _, err := parseWorkflowCommand([]string{"run", "workflow.lua", "--input", "requirement.md"}, strings.NewReader("unused"))
		if err != nil {
			t.Fatalf("parseWorkflowCommand() error = %v", err)
		}
		if got.Input != "relative file" {
			t.Fatalf("Input = %q, want relative file", got.Input)
		}
	})

	t.Run("rejects invalid arguments", func(t *testing.T) {
		cases := []struct {
			name    string
			args    []string
			wantErr string
		}{
			{name: "missing script", args: []string{"run"}, wantErr: "usage:"},
			{name: "empty script", args: []string{"run", ""}, wantErr: "usage:"},
			{name: "missing input after separator", args: []string{"run", "workflow.lua", "--"}, wantErr: "unexpected workflow argument"},
			{name: "separator after inline", args: []string{"run", "workflow.lua", "one", "--"}, wantErr: "unexpected workflow argument"},
			{name: "missing input path", args: []string{"run", "workflow.lua", "--input"}, wantErr: "requires a file path"},
			{name: "option as input path", args: []string{"run", "workflow.lua", "--input", "--unknown"}, wantErr: "requires a file path"},
			{name: "empty input path", args: []string{"run", "workflow.lua", "--input="}, wantErr: "requires a file path"},
			{name: "unknown option", args: []string{"run", "workflow.lua", "--unknown"}, wantErr: "unknown workflow option"},
			{name: "extra inline", args: []string{"run", "workflow.lua", "one", "two"}, wantErr: "unexpected workflow argument"},
			{name: "inline and file", args: []string{"run", "workflow.lua", "one", "--input", "req.md"}, wantErr: "sources conflict"},
			{name: "stdin and inline", args: []string{"run", "workflow.lua", "-", "one"}, wantErr: "sources conflict"},
			{name: "file and stdin", args: []string{"run", "workflow.lua", "--input", "req.md", "-"}, wantErr: "sources conflict"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, workflowMode, err := parseWorkflowCommand(tc.args, strings.NewReader("unused"))
				if !workflowMode {
					t.Fatal("workflowMode = false, want true")
				}
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseWorkflowCommand() error = %v, want %q", err, tc.wantErr)
				}
			})
		}
	})

	t.Run("rejects file errors", func(t *testing.T) {
		for name, path := range map[string]string{
			"missing":   filepath.Join(t.TempDir(), "missing.md"),
			"directory": t.TempDir(),
		} {
			t.Run(name, func(t *testing.T) {
				_, _, err := parseWorkflowCommand([]string{"run", "workflow.lua", "--input", path}, strings.NewReader("unused"))
				if err == nil {
					t.Fatal("parseWorkflowCommand() error = nil, want file error")
				}
			})
		}
	})

	t.Run("input size limit", func(t *testing.T) {
		exact := strings.Repeat("a", int(maxWorkflowInputBytes))
		got, _, err := parseWorkflowCommand([]string{"run", "workflow.lua", "-"}, strings.NewReader(exact))
		if err != nil {
			t.Fatalf("parseWorkflowCommand() exact stdin limit error = %v", err)
		}
		if got.Input != exact {
			t.Fatalf("Input length = %d, want %d", len(got.Input), len(exact))
		}

		exactPath := filepath.Join(t.TempDir(), "exact.md")
		if err := os.WriteFile(exactPath, []byte(exact), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		got, _, err = parseWorkflowCommand([]string{"run", "workflow.lua", "--input", exactPath}, strings.NewReader("unused"))
		if err != nil {
			t.Fatalf("parseWorkflowCommand() exact file limit error = %v", err)
		}
		if got.Input != exact {
			t.Fatalf("file Input length = %d, want %d", len(got.Input), len(exact))
		}

		_, _, err = parseWorkflowCommand([]string{"run", "workflow.lua", "-"}, strings.NewReader(exact+"a"))
		if err == nil || !strings.Contains(err.Error(), "exceeds the 1 MiB limit") {
			t.Fatalf("parseWorkflowCommand() oversized stdin error = %v", err)
		}

		path := filepath.Join(t.TempDir(), "large.md")
		if err := os.WriteFile(path, []byte(exact+"a"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		_, _, err = parseWorkflowCommand([]string{"run", "workflow.lua", "--input", path}, strings.NewReader("unused"))
		if err == nil || !strings.Contains(err.Error(), "exceeds the 1 MiB limit") {
			t.Fatalf("parseWorkflowCommand() oversized file error = %v", err)
		}
	})
}

func TestRunWorkflow(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "workflow.lua")
	if err := os.WriteFile(path, []byte(`ronin.log(ronin.input)`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	var output strings.Builder
	if err := runWorkflow(t.Context(), path, t.TempDir(), "workflow requirement", nil, &output); err != nil {
		t.Fatalf("runWorkflow() error = %v", err)
	}
	if got, want := output.String(), "workflow requirement\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
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

func TestStartupSession(t *testing.T) {
	metadata := session.Metadata{
		Model:          config.Model{Provider: "openai", Name: "gpt-5.5"},
		ReasoningLevel: "medium",
	}
	workingDir := "/workspace"

	t.Run("fresh startup creates a session without loading active session", func(t *testing.T) {
		created := session.Session{ID: "created", WorkingDir: workingDir}
		store := &fakeStartupSessionStore{created: created}

		got, err := startupSession(store, workingDir, metadata, false)
		if err != nil {
			t.Fatalf("startupSession() error = %v", err)
		}
		if got.ID != created.ID {
			t.Fatalf("startupSession() ID = %q, want %q", got.ID, created.ID)
		}
		if store.loadActiveCalls != 0 {
			t.Fatalf("LoadActive calls = %d, want 0", store.loadActiveCalls)
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

		got, err := startupSession(store, workingDir, metadata, true)
		if err != nil {
			t.Fatalf("startupSession() error = %v", err)
		}
		if got.ID != loaded.ID {
			t.Fatalf("startupSession() ID = %q, want %q", got.ID, loaded.ID)
		}
		if store.loadActiveCalls != 1 {
			t.Fatalf("LoadActive calls = %d, want 1", store.loadActiveCalls)
		}
		if store.loadedWorkingDir != workingDir {
			t.Fatalf("LoadActive workingDir = %q, want %q", store.loadedWorkingDir, workingDir)
		}
		if store.createCalls != 0 {
			t.Fatalf("Create calls = %d, want 0", store.createCalls)
		}
	})

	t.Run("resume startup creates session when active session is missing", func(t *testing.T) {
		created := session.Session{ID: "created", WorkingDir: workingDir}
		store := &fakeStartupSessionStore{created: created}

		got, err := startupSession(store, workingDir, metadata, true)
		if err != nil {
			t.Fatalf("startupSession() error = %v", err)
		}
		if got.ID != created.ID {
			t.Fatalf("startupSession() ID = %q, want %q", got.ID, created.ID)
		}
		if store.loadActiveCalls != 1 {
			t.Fatalf("LoadActive calls = %d, want 1", store.loadActiveCalls)
		}
		if store.createCalls != 1 {
			t.Fatalf("Create calls = %d, want 1", store.createCalls)
		}
	})

	t.Run("returns load error", func(t *testing.T) {
		wantErr := errors.New("read workspace")
		store := &fakeStartupSessionStore{loadErr: wantErr}

		_, err := startupSession(store, workingDir, metadata, true)
		if err == nil || !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "failed to load session") {
			t.Fatalf("startupSession() error = %v, want load error wrapping %v", err, wantErr)
		}
	})

	t.Run("returns create error", func(t *testing.T) {
		wantErr := errors.New("write session")
		store := &fakeStartupSessionStore{createErr: wantErr}

		_, err := startupSession(store, workingDir, metadata, false)
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

type failingReader struct {
	err error
}

func (r *failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

type fakeStartupSessionStore struct {
	loaded   session.Session
	loadedOK bool
	loadErr  error

	created   session.Session
	createErr error

	loadActiveCalls int
	createCalls     int

	loadedWorkingDir  string
	createdWorkingDir string
	createdMetadata   session.Metadata
}

func (s *fakeStartupSessionStore) LoadActive(workingDir string) (session.Session, bool, error) {
	s.loadActiveCalls++
	s.loadedWorkingDir = workingDir
	return s.loaded, s.loadedOK, s.loadErr
}

func (s *fakeStartupSessionStore) Load(string) (session.Session, bool, error) {
	return session.Session{}, false, nil
}

func (s *fakeStartupSessionStore) Create(workingDir string, metadata session.Metadata) (session.Session, error) {
	s.createCalls++
	s.createdWorkingDir = workingDir
	s.createdMetadata = metadata
	return s.created, s.createErr
}

func (s *fakeStartupSessionStore) Save(session.Session) error {
	return nil
}

func (s *fakeStartupSessionStore) List(string) ([]session.Ref, error) {
	return nil, nil
}

func (s *fakeStartupSessionStore) Delete(string) error {
	return nil
}

func (s *fakeStartupSessionStore) Clear(string) error {
	return nil
}

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
