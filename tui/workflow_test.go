package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/workflow"
)

func TestWorkflowInputMode(t *testing.T) {
	model, err := newAppModel([]Command{InvokeWorkflow{Workflow: workflow.Workflow{Name: "implement"}}})
	if err != nil {
		t.Fatalf("newAppModel() error = %v", err)
	}
	model.enterWorkflowInput(workflow.Workflow{Name: "implement", Path: "/workflow.lua"})
	if got := model.editorLabel(); got != "workflow implement input" {
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
	model, err := newAppModel([]Command{Exit{}})
	if err != nil {
		t.Fatalf("newAppModel() error = %v", err)
	}
	item := workflow.Workflow{Name: "implement"}
	model.startWorkflow(item, "build it")
	model.handleWorkflowEvent(workflow.Log{Text: "Planning"}, time.Now())
	model.handleWorkflowEvent(workflow.AgentStarted{Invocation: 1, Request: workflow.AgentRequest{Prompt: "secret detail"}}, time.Now())
	model.handleWorkflowEvent(workflow.Finished{Result: workflow.Result{Name: "implement", Status: workflow.StatusCompleted, Summary: "done"}}, time.Now())

	box := model.boxes[len(model.boxes)-1].(workflowBox)
	collapsed := strings.Join(renderBoxLinesAt(box, 80, false, time.Now()), "\n")
	if !strings.Contains(collapsed, "Planning") || !strings.Contains(collapsed, "done") || strings.Contains(collapsed, "secret detail") {
		t.Fatalf("collapsed timeline = %q", collapsed)
	}
	expanded := strings.Join(renderBoxLinesAt(box, 80, true, time.Now()), "\n")
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

func TestWorkflowTimelineReservationsAndWidthCap(t *testing.T) {
	box := workflowBox{
		Name: "large", Status: string(workflow.StatusCompleted), Summary: strings.Repeat("summary ", 1000),
		Entries: []workflowEntry{{Text: strings.Repeat("timeline ", 10000)}}, TimelineTruncated: true,
		LatestActivity: "Agent 1 finished", StartedAt: time.Now(),
	}
	for _, width := range []int{1, 7, 80, 200} {
		lines := renderWorkflowBoxLines(box, width, true, time.Now())
		if len(lines) > maxWorkflowVisualLines {
			t.Fatalf("width %d rendered %d lines", width, len(lines))
		}
		if len(boundedWorkflowWrap(" Summary: ", box.Summary, width, maxWorkflowSummaryLines)) > maxWorkflowSummaryLines {
			t.Fatalf("width %d summary exceeded reservation", width)
		}
	}
}

func TestWorkflowTimelineHasOneTruncationNotice(t *testing.T) {
	box := workflowBox{Name: "large", TimelineTruncated: true, LatestActivity: "Agent 1 finished", StartedAt: time.Now()}
	lines := renderWorkflowBoxLines(box, 80, true, time.Now())
	plain := textWithoutANSI(lines)
	if strings.Count(plain, "workflow output truncated") != 1 {
		t.Fatalf("truncation notices = %d in %q", strings.Count(plain, "workflow output truncated"), plain)
	}
}

func BenchmarkBoundedArtifactRendering(b *testing.B) {
	artifact := tool.TextArtifact{Text: strings.Repeat("long line ", 100000)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
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
		if len(got) > limit {
			t.Fatalf("limit %d: got %d bytes", limit, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("limit %d: invalid UTF-8 %q", limit, got)
		}
	}
	if got := truncateWorkflowText(value, 32); strings.Contains(got, value) {
		t.Fatal("truncated text retained the original value")
	}
}

func TestWorkflowArtifactContentBounds(t *testing.T) {
	content := strings.Repeat("界", 100)
	artifacts := []tool.Artifact{
		tool.TextArtifact{Text: content},
		tool.ShellStreamArtifact{Content: content},
		tool.FileArtifact{Content: content},
		tool.FileRangeArtifact{Content: content},
		tool.UnifiedDiffArtifact{Diff: content},
		tool.FileMetadataArtifact{Path: strings.Repeat("path", 100), FileID: content},
	}
	for _, artifact := range artifacts {
		bounded, used, truncated := boundWorkflowArtifact(artifact, 40)
		if !truncated || used > 40 || len(workflowArtifactContent(bounded)) > 40 || !utf8.ValidString(workflowArtifactContent(bounded)) {
			t.Fatalf("artifact was not bounded: %#v, used=%d truncated=%t", bounded, used, truncated)
		}
	}
}

func TestWorkflowArtifactFieldsAreBoundThroughEvents(t *testing.T) {
	artifacts := []tool.Artifact{
		tool.TextArtifact{Text: strings.Repeat("text", 400000)},
		tool.ShellStreamArtifact{Content: strings.Repeat("shell", 400000)},
		tool.FileArtifact{Path: strings.Repeat("file-path", 400000), Content: strings.Repeat("file", 400000)},
		tool.FileRangeArtifact{Path: strings.Repeat("range-path", 400000), Content: strings.Repeat("range", 400000)},
		tool.UnifiedDiffArtifact{Path: strings.Repeat("diff-path", 400000), Diff: strings.Repeat("diff", 400000)},
	}
	for _, artifact := range artifacts {
		model := mustWorkflowModel(t)
		model.startWorkflow(workflow.Workflow{Name: "bounded"}, "input")
		model.handleWorkflowEvent(workflow.AgentEventReceived{Invocation: 1, Event: workflow.AgentToolOutput{Artifact: artifact}}, time.Now())
		box := model.boxes[len(model.boxes)-1].(workflowBox)
		if box.TimelineBytes > workflowTimelineCapacity(box) {
			t.Fatalf("%T retained %d timeline bytes, capacity %d", artifact, box.TimelineBytes, workflowTimelineCapacity(box))
		}
		for _, entry := range box.Entries {
			for _, retained := range entry.Artifacts {
				switch retained := retained.(type) {
				case tool.FileArtifact:
					if retained.Path != "" || len(retained.Content) > maxWorkflowDisplayBytes {
						t.Fatalf("file artifact retained path/content: path=%d content=%d", len(retained.Path), len(retained.Content))
					}
				case tool.FileRangeArtifact:
					if retained.Path != "" || len(retained.Content) > maxWorkflowDisplayBytes {
						t.Fatalf("file range retained path/content: path=%d content=%d", len(retained.Path), len(retained.Content))
					}
				case tool.UnifiedDiffArtifact:
					if retained.Path != "" || len(retained.Diff) > maxWorkflowDisplayBytes {
						t.Fatalf("diff retained path/content: path=%d content=%d", len(retained.Path), len(retained.Diff))
					}
				}
			}
		}
	}

	model := mustWorkflowModel(t)
	model.startWorkflow(workflow.Workflow{Name: "metadata"}, "input")
	model.handleWorkflowEvent(workflow.AgentEventReceived{Invocation: 1, Event: workflow.AgentToolOutput{Artifact: tool.FileMetadataArtifact{Path: strings.Repeat("oversized-path", 200000), FileID: "id"}}}, time.Now())
	box := model.boxes[len(model.boxes)-1].(workflowBox)
	metadata := box.Entries[0].Artifacts[0].(tool.FileMetadataArtifact)
	if metadata.Path != "" || box.TimelineBytes > workflowTimelineCapacity(box) {
		t.Fatalf("metadata artifact retained non-rendered path or exceeded budget: %#v, bytes=%d", metadata, box.TimelineBytes)
	}
}

func TestWorkflowVisualExhaustionDropsDetailedEvents(t *testing.T) {
	model := mustWorkflowModel(t)
	model.startWorkflow(workflow.Workflow{Name: "narrow"}, "input")
	for i := range maxWorkflowTimelineLines + 10 {
		model.handleWorkflowEvent(workflow.Log{Text: fmt.Sprintf("event %d", i)}, time.Now())
	}
	if _, err := model.lines(1, &fakeConversation{}, time.Now()); err != nil {
		t.Fatalf("lines() error = %v", err)
	}
	box := model.boxes[len(model.boxes)-1].(workflowBox)
	if !box.TimelineTruncated {
		t.Fatal("narrow rendering did not mark visual exhaustion")
	}
	entries := len(box.Entries)
	model.handleWorkflowEvent(workflow.Log{Text: "debug: " + strings.Repeat("detail", 10000)}, time.Now())
	model.handleWorkflowEvent(workflow.AgentFinished{Invocation: 1}, time.Now())
	box = model.boxes[len(model.boxes)-1].(workflowBox)
	if len(box.Entries) != entries || strings.Contains(box.LatestActivity, "debug:") || !strings.Contains(box.LatestActivity, "finished") {
		t.Fatalf("post-exhaustion timeline = entries %d, latest %q", len(box.Entries), box.LatestActivity)
	}
}
func TestWorkflowTimelineBoundsLifecycleAndRendering(t *testing.T) {
	model := mustWorkflowModel(t)
	model.startWorkflow(workflow.Workflow{Name: "large"}, "input")
	model.handleWorkflowEvent(workflow.AgentEventReceived{Invocation: 1, Event: workflow.AgentToolOutput{Artifact: tool.FileArtifact{Content: strings.Repeat("line content 123456789\\n", 100000)}}}, time.Now())

	box := model.boxes[len(model.boxes)-1].(workflowBox)
	if box.TimelineBytes > workflowTimelineCapacity(box) || !box.TimelineTruncated {
		t.Fatalf("timeline bytes=%d capacity=%d truncated=%t", box.TimelineBytes, workflowTimelineCapacity(box), box.TimelineTruncated)
	}
	entries := len(box.Entries)
	model.handleWorkflowEvent(workflow.AgentStarted{Invocation: 2}, time.Now())
	model.handleWorkflowEvent(workflow.AgentFinished{Invocation: 2, Text: strings.Repeat("response", 100000)}, time.Now())
	box = model.boxes[len(model.boxes)-1].(workflowBox)
	if len(box.Entries) != entries || !strings.Contains(box.LatestActivity, "finished") {
		t.Fatalf("post-truncation lifecycle: entries=%d latest=%q", len(box.Entries), box.LatestActivity)
	}
	model.handleWorkflowEvent(workflow.Finished{Result: workflow.Result{Status: workflow.StatusFailed, Summary: strings.Repeat("summary ", 100000)}}, time.Now())
	box = model.boxes[len(model.boxes)-1].(workflowBox)
	if box.Status != string(workflow.StatusFailed) || len(box.Summary) > maxWorkflowSummaryBytes || !utf8.ValidString(box.Summary) {
		t.Fatalf("final workflow state: status=%q summary bytes=%d", box.Status, len(box.Summary))
	}
	lines := renderWorkflowBoxLines(box, 80, true, time.Now())
	if len(lines) > maxWorkflowVisualLines {
		t.Fatalf("rendered %d lines", len(lines))
	}
	if !strings.Contains(textWithoutANSI(lines), string(workflow.StatusFailed)) {
		t.Fatal("final status missing from rendered workflow")
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
	return strings.Join(plainLines(lines), "\\n")
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
