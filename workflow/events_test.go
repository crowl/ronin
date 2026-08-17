package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/crowl/ronin/workflow"
)

func TestRun(t *testing.T) {
	t.Run("reports logs agents and successful result", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "implement.lua")
		if err := os.WriteFile(path, []byte(`
ronin.log("planning")
local result = ronin.run_agent({ prompt = ronin.input })
ronin.done(result.text)
`), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		var events []workflow.Event
		result := workflow.Run(t.Context(), workflow.Workflow{Name: "implement", Path: path}, t.TempDir(), "build it", func(_ context.Context, req workflow.AgentRequest) (workflow.AgentResult, error) {
			if req.Prompt != "build it" {
				t.Fatalf("Prompt = %q, want build it", req.Prompt)
			}
			if req.Progress != nil {
				req.Progress(workflow.AgentTextDelta{Text: "done"})
			}
			return workflow.AgentResult{Text: "implemented"}, nil
		}, func(event workflow.Event) {
			events = append(events, event)
		})

		if result.Status != workflow.StatusCompleted || result.Summary != "implemented" {
			t.Fatalf("Run() = %#v", result)
		}
		if len(events) != 6 {
			t.Fatalf("event count = %d, want 6: %#v", len(events), events)
		}
		if _, ok := events[0].(workflow.Started); !ok {
			t.Fatalf("event 0 = %T, want Started", events[0])
		}
		if _, ok := events[1].(workflow.Log); !ok {
			t.Fatalf("event 1 = %T, want Log", events[1])
		}
		if _, ok := events[2].(workflow.AgentStarted); !ok {
			t.Fatalf("event 2 = %T, want AgentStarted", events[2])
		}
		if _, ok := events[3].(workflow.AgentEventReceived); !ok {
			t.Fatalf("event 3 = %T, want AgentEventReceived", events[3])
		}
		if _, ok := events[4].(workflow.AgentFinished); !ok {
			t.Fatalf("event 4 = %T, want AgentFinished", events[4])
		}
		if _, ok := events[5].(workflow.Finished); !ok {
			t.Fatalf("event 5 = %T, want Finished", events[5])
		}
	})

	t.Run("reports failure and cancellation", func(t *testing.T) {
		failurePath := filepath.Join(t.TempDir(), "fail.lua")
		if err := os.WriteFile(failurePath, []byte(`ronin.fail("broken")`), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		failed := workflow.Run(t.Context(), workflow.Workflow{Name: "fail", Path: failurePath}, t.TempDir(), "", nil, nil)
		if failed.Status != workflow.StatusFailed || failed.Summary != "broken" {
			t.Fatalf("failed Run() = %#v", failed)
		}

		cancelPath := filepath.Join(t.TempDir(), "cancel.lua")
		if err := os.WriteFile(cancelPath, []byte(`ronin.run_agent({ prompt = "wait" })`), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		cancelled := workflow.Run(ctx, workflow.Workflow{Name: "cancel", Path: cancelPath}, t.TempDir(), "", func(ctx context.Context, _ workflow.AgentRequest) (workflow.AgentResult, error) {
			return workflow.AgentResult{}, ctx.Err()
		}, nil)
		if cancelled.Status != workflow.StatusCancelled {
			t.Fatalf("cancelled Run() = %#v", cancelled)
		}
	})
}
