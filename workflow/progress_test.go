package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConcurrentNamedLifecycleEvents(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "concurrent.lua")
	if err := os.WriteFile(path, []byte(`
local first = ronin.start_agent({ name = "Implementing: alpha", prompt = "alpha" })
local second = ronin.start_agent({ name = "Reviewing: beta", prompt = "beta" })
local done = ronin.wait_any({ first, second })
if done.job == first then ronin.wait_any({ second }) else ronin.wait_any({ first }) end
ronin.done("done")
`), 0o600); err != nil {
		t.Fatal(err)
	}
	bothStarted := make(chan struct{})
	var events []Event
	active := make(map[int]string)
	peak := 0
	result := Run(t.Context(), Workflow{Name: "concurrent", Path: path}, t.TempDir(), "", func(ctx context.Context, req AgentRequest) (AgentResult, error) {
		select {
		case <-bothStarted:
			return AgentResult{Text: req.Prompt}, nil
		case <-ctx.Done():
			return AgentResult{}, ctx.Err()
		}
	}, func(event Event) {
		events = append(events, event)
		switch event := event.(type) {
		case AgentStarted:
			active[event.Invocation] = event.Request.Name
			peak = max(peak, len(active))
			if len(active) == 2 {
				close(bothStarted)
			}
		case AgentFinished:
			if active[event.Invocation] == "" {
				t.Errorf("completion without named start: %#v", event)
			}
			delete(active, event.Invocation)
		}
	})
	if result.Status != StatusCompleted || peak != 2 || len(active) != 0 {
		t.Fatalf("result = %#v, peak = %d, active = %v, events = %#v", result, peak, active, events)
	}
}

func TestAgentCancellationLifecycle(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cancel.lua")
	if err := os.WriteFile(path, []byte(`ronin.run_agent({name="Planning", prompt="wait"})`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var finished AgentFinished
	result := Run(ctx, Workflow{Name: "cancel", Path: path}, t.TempDir(), "", func(context.Context, AgentRequest) (AgentResult, error) {
		cancel()
		return AgentResult{}, errors.Join(errors.New("agent interrupted"), context.Canceled)
	}, func(event Event) {
		if event, ok := event.(AgentFinished); ok {
			finished = event
		}
	})
	if result.Status != StatusCancelled || !finished.Cancelled || !strings.Contains(finished.Error, "agent interrupted") {
		t.Fatalf("result = %#v, finished = %#v", result, finished)
	}
}
