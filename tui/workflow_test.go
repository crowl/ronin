package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/workflow"
)

func TestWorkflowInputMode(t *testing.T) {
	model, err := newAppModel([]Command{InvokeWorkflow{Workflow: workflow.Workflow{Name: "implement"}}}, DefaultTheme())
	if err != nil {
		t.Fatalf("newAppModel() error = %v", err)
	}
	model.enterWorkflowInput(workflow.Workflow{Name: "implement", Path: "/workflow.lua"})
	if got := model.editorLabel(); got != "Workflow input: implement" {
		t.Fatalf("editorLabel() = %q", got)
	}

	if _, err := model.handleKey(terminal.Key{Type: terminal.KeyRune, Rune: 'x'}); err != nil {
		t.Fatalf("handleKey() error = %v", err)
	}
	update, err := model.handleKey(terminal.Key{Type: terminal.KeyEnter})
	if err != nil {
		t.Fatalf("submit error = %v", err)
	}
	action, ok := update.Action.(runWorkflowAction)
	if !ok || action.Workflow.Name != "implement" || action.Input != "x" {
		t.Fatalf("submit action = %#v", update.Action)
	}
	if model.workflowInput != nil {
		t.Fatal("workflow input mode remained active")
	}

	model.enterWorkflowInput(workflow.Workflow{Name: "review"})
	if _, err := model.handleKey(terminal.Key{Type: terminal.KeyEscape}); err != nil {
		t.Fatalf("escape error = %v", err)
	}
	if model.workflowInput != nil {
		t.Fatal("escape did not exit workflow input mode")
	}
}

func TestWorkflowTimeline(t *testing.T) {
	model, err := newAppModel([]Command{Exit{}}, DefaultTheme())
	if err != nil {
		t.Fatalf("newAppModel() error = %v", err)
	}
	item := workflow.Workflow{Name: "implement"}
	model.startWorkflow(item, "build it")
	model.handleWorkflowEvent(workflow.Log{Text: "Planning"}, time.Now())
	model.handleWorkflowEvent(workflow.AgentStarted{Invocation: 1, Request: workflow.AgentRequest{Prompt: "secret detail"}}, time.Now())
	model.handleWorkflowEvent(workflow.Finished{Result: workflow.Result{Name: "implement", Status: workflow.StatusCompleted, Summary: "done"}}, time.Now())

	box := model.boxes[len(model.boxes)-1].(workflowBox)
	collapsed := strings.Join(renderBoxLinesAt(box, 80, DefaultTheme(), false, time.Now()), "\n")
	if !strings.Contains(collapsed, "Planning") || !strings.Contains(collapsed, "done") || strings.Contains(collapsed, "secret detail") {
		t.Fatalf("collapsed timeline = %q", collapsed)
	}
	expanded := strings.Join(renderBoxLinesAt(box, 80, DefaultTheme(), true, time.Now()), "\n")
	if !strings.Contains(expanded, "secret detail") {
		t.Fatalf("expanded timeline = %q", expanded)
	}

	model = mustWorkflowModel(t)
	model.populateInitialBoxes(&fakeConversation{messages: []llm.Message{llm.WorkflowResultMessage{Name: "review", Input: "check", Status: llm.WorkflowStatusCompleted, Summary: "approved"}}})
	if _, ok := model.boxes[0].(workflowBox); !ok {
		t.Fatalf("resumed box = %T", model.boxes[0])
	}
}

func TestWorkflowTimelineCoalescesDeltas(t *testing.T) {
	entries := appendWorkflowEntry(nil, workflowEntry{Text: "Agent 1 response", Detail: "one"})
	entries = appendWorkflowEntry(entries, workflowEntry{Text: "Agent 1 response", Detail: " two"})
	if len(entries) != 1 || entries[0].Detail != "one two" {
		t.Fatalf("entries = %#v", entries)
	}
	entries = appendWorkflowEntry(entries, workflowEntry{Text: "Agent 1 tool output", Artifacts: []tool.Artifact{tool.TextArtifact{Text: "old"}}})
	entries = appendWorkflowEntry(entries, workflowEntry{Text: "Agent 1 tool output", Artifacts: []tool.Artifact{tool.TextArtifact{Text: "new"}}})
	if len(entries[1].Artifacts) != 1 || entries[1].Artifacts[0].(tool.TextArtifact).Text != "new" {
		t.Fatalf("tool artifacts = %#v, want latest only", entries[1].Artifacts)
	}
}

func TestCancelledWorkflowDropsQueuedPrompt(t *testing.T) {
	model := mustWorkflowModel(t)
	model.startWorkflow(workflow.Workflow{Name: "implement"}, "input")
	model.steeringPrompt = "do this next"
	model.handleWorkflowEvent(workflow.Finished{Result: workflow.Result{Name: "implement", Status: workflow.StatusCancelled, Summary: "cancelled"}}, time.Now())
	update := model.finishWorkflow(nil)
	if model.steeringPrompt != "" {
		t.Fatalf("steering prompt = %q, want empty", model.steeringPrompt)
	}
	if _, ok := update.Action.(submitPromptAction); ok {
		t.Fatalf("finish action = %#v, should not submit queued prompt", update.Action)
	}
}

func mustWorkflowModel(t *testing.T) *appModel {
	t.Helper()
	model, err := newAppModel([]Command{Exit{}}, DefaultTheme())
	if err != nil {
		t.Fatalf("newAppModel() error = %v", err)
	}
	return model
}
