package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
)

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

func (s *fakeStartupSessionStore) Create(workingDir string, metadata session.Metadata) (session.Session, error) {
	s.createCalls++
	s.createdWorkingDir = workingDir
	s.createdMetadata = metadata
	return s.created, s.createErr
}

func (s *fakeStartupSessionStore) Save(session.Session) error {
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
