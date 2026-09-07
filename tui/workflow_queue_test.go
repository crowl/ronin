package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/workflow"
)

type transcriptWorkflowRunner struct{}

func (transcriptWorkflowRunner) Run(_ context.Context, _ workflow.Workflow, _ string, emit func(workflow.Event)) workflow.Result {
	emit(workflow.AgentStarted{Invocation: 1, Request: workflow.AgentRequest{Name: "Planning", Prompt: "secret prompt", System: "secret system"}})
	for _, event := range []workflow.AgentEvent{
		workflow.AgentThinkingDelta{Text: "secret thinking"},
		workflow.AgentTextDelta{Text: "secret response"},
		workflow.AgentToolOutput{Artifact: tool.TextArtifact{Text: "secret tool output"}},
	} {
		emit(workflow.AgentEventReceived{Invocation: 1, Event: event})
	}
	emit(workflow.AgentFinished{Invocation: 1, Text: "secret report"})
	result := workflow.Result{Status: workflow.StatusCompleted, Summary: "done"}
	emit(workflow.Finished{Result: result})
	return result
}

func TestWorkflowQueueDoesNotRetainTranscripts(t *testing.T) {
	events := make(chan event, 10)
	app := &app{conversation: &fakeConversation{}, events: events, model: mustWorkflowModel(t), workflowRunner: transcriptWorkflowRunner{}}
	app.runWorkflow(t.Context(), workflow.Workflow{Name: "test"}, "input")
	deadline := time.After(time.Second)
	count := 0
	for {
		select {
		case event := <-events:
			if _, ok := event.(workflowDone); ok {
				if count != 3 {
					t.Fatalf("queued %d workflow events, want only start, finish, result", count)
				}
				return
			}
			received, ok := event.(workflowEventReceived)
			if !ok {
				continue
			}
			count++
			switch progress := received.Event.(type) {
			case workflow.AgentStarted:
				if progress.Request.Name != "Planning" || progress.Request.Prompt != "" || progress.Request.System != "" {
					t.Fatalf("queued request = %#v", progress.Request)
				}
			case workflow.AgentFinished:
				if strings.Contains(progress.Text, "secret") {
					t.Fatal("queued agent report")
				}
			case workflow.AgentEventReceived:
				t.Fatal("queued agent transcript")
			}
		case <-deadline:
			t.Fatal("workflow did not finish")
		}
	}
}
