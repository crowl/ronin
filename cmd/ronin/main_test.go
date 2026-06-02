package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crowl/ronin/agent"
	"github.com/crowl/ronin/jsonschema"
)

func TestRunPrompt(t *testing.T) {
	t.Run("writes assistant text with trailing newline", func(t *testing.T) {
		var output strings.Builder
		agt := fakePromptAgent{events: []agent.Event{
			agent.AssistantMessageDeltaReceived{Text: "hello"},
			agent.AssistantMessageDeltaReceived{Text: " world"},
		}}

		if err := runPrompt(t.Context(), agt, "prompt", &output); err != nil {
			t.Fatalf("runPrompt() error = %v", err)
		}

		if got, want := output.String(), "hello world\n"; got != want {
			t.Fatalf("output\ngot:  %q\nwant: %q", got, want)
		}
	})

	t.Run("writes compact tool status lines", func(t *testing.T) {
		var output strings.Builder
		agt := fakePromptAgent{events: []agent.Event{
			agent.AssistantMessageDeltaReceived{Text: "checking"},
			agent.ToolExecutionStarted{Tool: fakePromptTool{name: "read_file"}},
			agent.AssistantMessageDeltaReceived{Text: "done"},
		}}

		if err := runPrompt(t.Context(), agt, "prompt", &output); err != nil {
			t.Fatalf("runPrompt() error = %v", err)
		}

		if got, want := output.String(), "checking\n[tool] read_file\ndone\n"; got != want {
			t.Fatalf("output\ngot:  %q\nwant: %q", got, want)
		}
	})

	t.Run("falls back to tool name for status", func(t *testing.T) {
		var output strings.Builder
		agt := fakePromptAgent{events: []agent.Event{
			agent.ToolExecutionStarted{Tool: fakePromptTool{name: "shell"}},
		}}

		if err := runPrompt(t.Context(), agt, "prompt", &output); err != nil {
			t.Fatalf("runPrompt() error = %v", err)
		}

		if got, want := output.String(), "[tool] shell\n"; got != want {
			t.Fatalf("output\ngot:  %q\nwant: %q", got, want)
		}
	})

	t.Run("writes compact tool error lines", func(t *testing.T) {
		var output strings.Builder
		toolErr := errors.New("exit status 1")
		agt := fakePromptAgent{events: []agent.Event{
			agent.ToolExecutionFailed{Tool: fakePromptTool{name: "shell"}, Error: toolErr},
		}}

		if err := runPrompt(t.Context(), agt, "prompt", &output); err != nil {
			t.Fatalf("runPrompt() error = %v", err)
		}

		if got, want := output.String(), "[tool error] shell: exit status 1\n"; got != want {
			t.Fatalf("output\ngot:  %q\nwant: %q", got, want)
		}
	})

	t.Run("returns prompt error", func(t *testing.T) {
		var output strings.Builder
		wantErr := errors.New("agent failed")
		agt := fakePromptAgent{err: wantErr}

		err := runPrompt(t.Context(), agt, "prompt", &output)
		if !errors.Is(err, wantErr) {
			t.Fatalf("runPrompt() error = %v, want %v", err, wantErr)
		}
	})
}

type fakePromptAgent struct {
	events []agent.Event
	err    error
}

func (a fakePromptAgent) Prompt(context.Context, string) (<-chan agent.Event, <-chan error) {
	events := make(chan agent.Event, len(a.events))
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
