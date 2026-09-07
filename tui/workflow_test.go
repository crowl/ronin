package tui

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/tui/internal/text"
	"github.com/crowl/ronin/workflow"
)

func TestWorkflowInputMode(t *testing.T) {
	model := mustWorkflowModel(t)
	model.enterWorkflowInput(workflow.Workflow{Name: "implement", Path: "/workflow.lua"})
	if got := model.editorLabel(); got != "workflow implement input" {
		t.Fatalf("editorLabel() = %q", got)
	}
	if _, err := model.handleKey(terminal.Key{Type: terminal.KeyRune, Rune: 'x'}); err != nil {
		t.Fatal(err)
	}
	update, err := model.handleKey(terminal.Key{Type: terminal.KeyEnter})
	if err != nil {
		t.Fatal(err)
	}
	action, ok := update.Action.(runWorkflowAction)
	if !ok || action.Workflow.Name != "implement" || action.Input != "x" || model.workflowInput != nil {
		t.Fatalf("submit action = %#v, input = %#v", update.Action, model.workflowInput)
	}
	model.enterWorkflowInput(workflow.Workflow{Name: "review"})
	if _, err := model.handleKey(terminal.Key{Type: terminal.KeyEscape}); err != nil {
		t.Fatal(err)
	}
	if model.workflowInput != nil {
		t.Fatal("escape did not exit workflow input mode")
	}
}

func TestWorkflowConcurrentStepStatus(t *testing.T) {
	model := mustWorkflowModel(t)
	model.startWorkflow(workflow.Workflow{Name: "developing"}, "build it")
	now := time.Now()
	start := func(id int, name string) {
		model.handleWorkflowEvent(workflow.AgentStarted{Invocation: id, Request: workflow.AgentRequest{Name: name, Prompt: "secret prompt"}}, now)
	}
	start(1, "Implementing: alpha (cycle 1)")
	start(2, "Reviewing: beta (cycle 1)")
	start(2, "duplicate")
	box := model.boxes[0].(workflowBox)
	if len(box.Active) != 2 {
		t.Fatalf("active = %#v", box.Active)
	}
	lines := renderWorkflowBoxLines(box, 100, false, now.Add(2*time.Second))
	plain := textWithoutANSI(lines)
	for _, want := range []string{"2 active", "Implementing: alpha (cycle 1)", "Reviewing: beta (cycle 1)", "2.0s"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in %q", want, plain)
		}
	}
	if !strings.Contains(lines[1], strongStyle.start()) || !strings.Contains(lines[2], strongStyle.start()) {
		t.Fatalf("active steps are not prominent: %q", lines)
	}
	model.handleWorkflowEvent(workflow.AgentFinished{Invocation: 2, Text: "secret report"}, now.Add(3*time.Second))
	start(3, "Reviewing: beta (cycle 2)")
	box = model.boxes[0].(workflowBox)
	if len(box.Active) != 2 || box.Active[0].Invocation != 1 || box.Active[1].Invocation != 3 || box.Completed != 1 || box.Recent[0].Status != "completed" {
		t.Fatalf("out-of-order completion/retry = %#v", box)
	}
	plain = textWithoutANSI(renderWorkflowBoxLines(box, 100, true, now.Add(8*time.Second)))
	if strings.Contains(plain, "secret") || !strings.Contains(plain, "3.0s") || !strings.Contains(plain, "cycle 2") {
		t.Fatalf("expanded status = %q", plain)
	}
	if strings.Index(plain, "cycle 2") > strings.Index(plain, "completed") {
		t.Fatalf("completed history precedes active steps: %q", plain)
	}
}

func TestWorkflowDiscardsAgentTranscripts(t *testing.T) {
	model := mustWorkflowModel(t)
	model.startWorkflow(workflow.Workflow{Name: "quiet"}, "input")
	now := time.Now()
	model.handleWorkflowEvent(workflow.AgentStarted{Invocation: 1}, now)
	secret := strings.Repeat("secret", 100000)
	for _, event := range []workflow.AgentEvent{
		workflow.AgentThinkingDelta{Text: secret},
		workflow.AgentTextDelta{Text: secret},
		workflow.AgentToolStarted{Title: secret},
		workflow.AgentToolOutput{Artifact: tool.FileArtifact{Path: secret, Content: secret}},
		workflow.AgentToolFailed{Error: secret},
		workflow.AgentToolEnded{},
	} {
		if update := model.handleWorkflowEvent(workflow.AgentEventReceived{Invocation: 1, Event: event}, now); update.Render {
			t.Fatal("transcript event requested a render")
		}
	}
	box := model.boxes[0].(workflowBox)
	if box.Active[0].Name != "Agent 1" || len(box.Recent) != 0 || box.LatestActivity != "" {
		t.Fatalf("transcript changed status: %#v", box)
	}
	for _, expanded := range []bool{false, true} {
		if got := textWithoutANSI(renderWorkflowBoxLines(box, 80, expanded, now)); strings.Contains(got, "secret") {
			t.Fatalf("transcript leaked: %q", got)
		}
	}
}

func TestWorkflowTerminalStates(t *testing.T) {
	for _, status := range []workflow.Status{workflow.StatusCompleted, workflow.StatusFailed, workflow.StatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			model := mustWorkflowModel(t)
			model.startWorkflow(workflow.Workflow{Name: "test"}, "")
			now := time.Now()
			for id := 1; id <= 3; id++ {
				model.handleWorkflowEvent(workflow.AgentStarted{Invocation: id}, now)
			}
			model.handleWorkflowEvent(workflow.AgentFinished{Invocation: 1, Error: "concrete failure"}, now)
			model.handleWorkflowEvent(workflow.AgentFinished{Invocation: 2, Error: "context canceled", Cancelled: true}, now)
			box := model.boxes[0].(workflowBox)
			if box.Recent[0].Status != "failed" || box.Recent[1].Status != "cancelled" {
				t.Fatalf("step outcomes = %#v", box.Recent)
			}
			model.handleWorkflowEvent(workflow.Finished{Result: workflow.Result{Status: status, Summary: "final outcome"}}, now.Add(time.Second))
			model.handleWorkflowEvent(workflow.AgentStarted{Invocation: 4}, now)
			box = model.boxes[0].(workflowBox)
			if len(box.Active) != 0 || box.EndedAt.IsZero() || box.Status != string(status) || box.Completed != 3 {
				t.Fatalf("terminal state = %#v", box)
			}
			plain := textWithoutANSI(renderWorkflowBoxLines(box, 100, false, now))
			if strings.Contains(plain, "running") || !strings.Contains(plain, "concrete failure") || !strings.Contains(plain, "final outcome") {
				t.Fatalf("terminal display = %q", plain)
			}
		})
	}
}

func TestWorkflowHistoryAndRenderingBounds(t *testing.T) {
	model := mustWorkflowModel(t)
	model.startWorkflow(workflow.Workflow{Name: "bounded"}, "input")
	now := time.Now()
	for id := 1; id <= 100; id++ {
		model.handleWorkflowEvent(workflow.AgentStarted{Invocation: id, Request: workflow.AgentRequest{Name: strings.Repeat("界", 1000)}}, now)
		model.handleWorkflowEvent(workflow.AgentFinished{Invocation: id, Error: strings.Repeat("界", 1000)}, now)
	}
	model.handleWorkflowEvent(workflow.AgentStarted{Invocation: 101, Request: workflow.AgentRequest{Name: "Still running"}}, now)
	model.handleWorkflowEvent(workflow.Log{Text: strings.Repeat("log\n", 10000)}, now)
	box := model.boxes[0].(workflowBox)
	if len(box.Recent) != maxWorkflowRecentSteps || box.Completed != 100 || len(box.LatestActivity) > maxWorkflowLatestActivitySize {
		t.Fatalf("unbounded history: %#v", box)
	}
	for _, step := range box.Recent {
		if len(step.Name) > maxWorkflowNameSize || len(step.Error) > maxWorkflowStepErrorSize || !utf8.ValidString(step.Name) || !utf8.ValidString(step.Error) {
			t.Fatalf("unbounded step: %#v", step)
		}
	}
	for _, width := range []int{1, 7, 30, 80, 200} {
		lines := renderWorkflowBoxLines(box, width, true, now)
		if len(lines) > 20 {
			t.Fatalf("width %d: %d lines, want compact status", width, len(lines))
		}
		for _, line := range plainLines(lines) {
			if text.VisibleLen(line) > width {
				t.Fatalf("width %d: oversized line %q", width, line)
			}
		}
	}
	// A narrow render must not permanently suppress subsequent updates.
	if _, err := model.lines(1, &fakeConversation{}, now); err != nil {
		t.Fatal(err)
	}
	model.handleWorkflowEvent(workflow.Log{Text: "Latest update"}, now)
	box = model.boxes[0].(workflowBox)
	plain := textWithoutANSI(renderWorkflowBoxLines(box, 80, false, now))
	if !strings.Contains(plain, "Still running") || !strings.Contains(plain, "Latest update") {
		t.Fatalf("activity lost after narrow render: %q", plain)
	}
	for id := 102; id < maxWorkflowVisualLines+200; id++ {
		model.handleWorkflowEvent(workflow.AgentStarted{Invocation: id}, now)
	}
	box = model.boxes[0].(workflowBox)
	lines := renderWorkflowBoxLines(box, 80, false, now)
	if len(lines) > maxWorkflowVisualLines || !strings.Contains(textWithoutANSI(lines), "more active steps") || !strings.Contains(lines[len(lines)-1], "Elapsed") {
		t.Fatalf("active overflow: %d lines, footer %q", len(lines), lines[len(lines)-1])
	}
}

func TestWorkflowCacheTracksStepChangesAndElapsedTime(t *testing.T) {
	now := time.Now()
	item := workflowBox{Name: "test", StartedAt: now, Active: []workflowStep{{Invocation: 1, Name: "Planning", Status: "running", StartedAt: now}}}
	var cache boxLineCache
	first := textWithoutANSI(cache.Lines([]box{item}, 100, false, now))
	later := textWithoutANSI(cache.Lines([]box{item}, 100, false, now.Add(time.Second)))
	if first == later || !strings.Contains(later, "1.0s") {
		t.Fatalf("elapsed display did not update: %q", later)
	}
	item.Active[0].Name = "Reviewing"
	changed := textWithoutANSI(cache.Lines([]box{item}, 100, false, now.Add(time.Second)))
	if changed == later || !strings.Contains(changed, "Reviewing") {
		t.Fatalf("step name did not invalidate cache: %q", changed)
	}
}

func TestWorkflowResumedSummary(t *testing.T) {
	model := mustWorkflowModel(t)
	model.populateInitialBoxes(&fakeConversation{messages: []llm.Message{llm.WorkflowResultMessage{Name: "review", Input: "check", Status: llm.WorkflowStatusCompleted, Summary: "approved", Timestamp: time.Now()}}})
	box, ok := model.boxes[0].(workflowBox)
	if !ok || box.Summary != "approved" || len(box.Active) != 0 || box.EndedAt.IsZero() {
		t.Fatalf("resumed workflow = %#v", model.boxes[0])
	}
}

func TestBoundedArtifactRenderingStopsWithinWrappedLineBudget(t *testing.T) {
	artifact := tool.TextArtifact{Text: strings.Repeat("界", 100000)}
	lines, more := toolArtifactLinesBounded(artifact, 10, 7)
	if len(lines) != 7 || !more {
		t.Fatalf("rendered %d lines, more=%t; want 7 bounded lines", len(lines), more)
	}
}

func TestBoundedArtifactRenderingPreservesFileMetadata(t *testing.T) {
	artifact := tool.FileMetadataArtifact{Path: "file.txt", FileID: "abc123"}
	lines, more := toolArtifactLinesBounded(artifact, 80, 10)
	if more || !strings.Contains(textWithoutANSI(lines), "Already in context (abc123)") {
		t.Fatalf("metadata rendering = %q, more=%t", textWithoutANSI(lines), more)
	}
}

func BenchmarkBoundedArtifactRendering(b *testing.B) {
	artifact := tool.TextArtifact{Text: strings.Repeat("long line ", 100000)}
	b.ResetTimer()
	for b.Loop() {
		lines, _ := toolArtifactLinesBounded(artifact, 80, 20)
		if len(lines) > 20 {
			b.Fatal("bounded artifact rendering exceeded limit")
		}
	}
}

func TestWorkflowTextTruncationFitsLimit(t *testing.T) {
	value := strings.Repeat("界", 100)
	for limit := 1; limit <= 32; limit++ {
		got := truncateWorkflowText(value, limit)
		if len(got) > limit || !utf8.ValidString(got) {
			t.Fatalf("limit %d: invalid bounded text %q", limit, got)
		}
	}
}

func TestWorkflowArtifactContentBounds(t *testing.T) {
	content := strings.Repeat("界", 100)
	for _, artifact := range []tool.Artifact{
		tool.TextArtifact{Text: content}, tool.ShellStreamArtifact{Content: content},
		tool.FileArtifact{Content: content}, tool.FileRangeArtifact{Content: content},
		tool.UnifiedDiffArtifact{Diff: content}, tool.FileMetadataArtifact{Path: content, FileID: content},
	} {
		bounded, used, truncated := boundWorkflowArtifact(artifact, 40)
		if !truncated || used > 40 || len(workflowArtifactContent(bounded)) > 40 || !utf8.ValidString(workflowArtifactContent(bounded)) {
			t.Fatalf("artifact was not bounded: %#v, used=%d truncated=%t", bounded, used, truncated)
		}
	}
}

func TestWorkflowSummaryIsBoundBeforePersistence(t *testing.T) {
	conversation := &fakeConversation{}
	model := mustWorkflowModel(t)
	events := make(chan event, 1)
	app := &app{conversation: conversation, events: events, model: model, workflowRunner: testWorkflowRunner{}}
	app.runWorkflow(context.Background(), workflow.Workflow{Name: "large"}, "input")
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-events:
			if _, ok := event.(workflowDone); ok {
				if len(conversation.recordedWorkflowResult.Summary) > maxWorkflowSummaryBytes || !utf8.ValidString(conversation.recordedWorkflowResult.Summary) {
					t.Fatalf("persisted summary bytes=%d", len(conversation.recordedWorkflowResult.Summary))
				}
				return
			}
		case <-deadline:
			t.Fatal("workflow did not finish")
		}
	}
}

func textWithoutANSI(lines []string) string {
	return strings.Join(plainLines(lines), "\n")
}

type testWorkflowRunner struct{}

func (testWorkflowRunner) Run(context.Context, workflow.Workflow, string, func(workflow.Event)) workflow.Result {
	return workflow.Result{Status: workflow.StatusCompleted, Summary: strings.Repeat("summary ", 100000)}
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
	model, err := newAppModel([]Command{Exit{}})
	if err != nil {
		t.Fatalf("newAppModel() error = %v", err)
	}
	return model
}
