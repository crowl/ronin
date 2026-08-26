package tui

import (
	"strings"
	"testing"
)

func TestMinimalConversationRendering(t *testing.T) {
	lines := boxesPresenter{
		Boxes: []box{
			userMessageBox{Text: "Explain this function."},
			assistantMessageBox{Text: "It validates **input**."},
			assistantThinkingBox{Text: "Checking edge cases."},
			systemMessageBox{Text: "New session started"},
			errorMessageBox{Text: "Operation canceled"},
		},
	}.Lines(80)

	plain := plainLines(lines)
	if got := strings.Join(plain, "\n"); !strings.Contains(got, " > Explain this function.") ||
		!strings.Contains(got, " • New session started") || !strings.Contains(got, " ! Operation canceled") {
		t.Fatalf("markers missing from render:\n%s", got)
	}
	thinkingStyle := thinkingTextStyles().normal.start()
	if !strings.Contains(lines[4], thinkingStyle) {
		t.Fatalf("thinking text is not muted and italic: %q", lines[4])
	}
	for _, index := range []int{0, 2, 6} {
		if strings.Contains(lines[index], mutedStyle.start()) {
			t.Fatalf("message at line %d is unexpectedly muted: %q", index, lines[index])
		}
	}
	if !strings.Contains(lines[len(lines)-1], errorStyle.start()) {
		t.Fatalf("error text is not red: %q", lines[len(lines)-1])
	}
}

func TestRenderToolCallUsesToolMarker(t *testing.T) {
	box := toolCallBox{Title: "go test ./..."}
	lines := renderBoxLinesAt(box, 24, false, box.StartedAt)
	plain := plainLines(lines)
	if plain[0] != " • go test ./..." {
		t.Fatalf("tool title = %q", plain[0])
	}
	if strings.Contains(lines[0], mutedStyle.start()) {
		t.Fatalf("tool title is unexpectedly muted: %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], mutedStyle.start()) {
		t.Fatalf("tool duration is not muted: %q", lines[len(lines)-1])
	}
	if plain[len(plain)-1] != "   Elapsed 0.0s" {
		t.Fatalf("tool duration = %q", plain[len(plain)-1])
	}
}

func TestRenderWorkflowUsesMutedContent(t *testing.T) {
	box := workflowBox{
		Name:    "implement",
		Input:   "build it",
		Summary: "done",
		Entries: []workflowEntry{{Text: "Agent started"}},
	}
	lines := renderBoxLinesAt(box, 80, false, box.StartedAt)

	if strings.Contains(lines[0], mutedStyle.start()) {
		t.Fatalf("workflow title is unexpectedly muted: %q", lines[0])
	}
	for i, line := range lines[1:] {
		if !strings.Contains(line, mutedStyle.start()) {
			t.Fatalf("workflow content at line %d is not muted: %q", i+1, line)
		}
	}
}

func TestWorkingIndicatorUsesConversationPadding(t *testing.T) {
	lines := workingIndicator{Frame: 3}.Lines(80)
	if got := lines[0]; got != " Working..." {
		t.Fatalf("working indicator = %q", got)
	}
}

func TestPendingSteeringPresenterUsesQueuedToolMarker(t *testing.T) {
	lines := pendingSteeringPresenter{Text: "Refine the tests"}.Lines(80)
	if got := plainLines(lines)[0]; got != "• (queued) Refine the tests" {
		t.Fatalf("queued steering message = %q", got)
	}
}

func TestEditorPresenterUsesPromptMarkerAndReverseCursor(t *testing.T) {
	lines := editorPresenter{Text: []rune("abc"), Cursor: 1}.Lines(80)
	if got := plainLines(lines)[0]; got != " > abc" {
		t.Fatalf("editor line = %q", got)
	}
	if !strings.Contains(lines[0], cursorStyle.start()) {
		t.Fatalf("editor cursor is not reversed: %q", lines[0])
	}
}
