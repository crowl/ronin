package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/tool"
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
	if got := strings.Join(plain, "\n"); !strings.Contains(got, " › Explain this function.") ||
		!strings.Contains(got, " • New session started") || !strings.Contains(got, " ! Operation canceled") {
		t.Fatalf("markers missing from render:\n%s", got)
	}
	thinkingStyle := thinkingTextStyles().normal.start()
	if !strings.Contains(lines[6], thinkingStyle) {
		t.Fatalf("thinking text is not muted and italic: %q", lines[6])
	}
	for _, index := range []int{1, 4, 8} {
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

func TestRenderToolCallHidesOutputUntilExpanded(t *testing.T) {
	startedAt := time.Unix(100, 0)
	box := toolCallBox{
		Title:     "shell",
		StartedAt: startedAt,
		EndedAt:   startedAt.Add(time.Second),
	}
	box.addDisplayArtifact(tool.TextArtifact{Text: "one\ntwo\nthree"})

	collapsed := plainLines(renderBoxLinesAt(box, 80, false, startedAt))
	if len(collapsed) != 2 {
		t.Fatalf("collapsed tool call = %#v, want title and duration only", collapsed)
	}
	if collapsed[0] != " • shell" || collapsed[1] != "   Took 1.0s" {
		t.Fatalf("collapsed tool call = %#v", collapsed)
	}

	expanded := plainLines(renderBoxLinesAt(box, 80, true, startedAt))
	if got := strings.Join(expanded, "\n"); !strings.Contains(got, "one") ||
		!strings.Contains(got, "two") || !strings.Contains(got, "three") {
		t.Fatalf("expanded tool call did not render output:\n%s", got)
	}
}

func TestRenderToolCallBoundsExpandedOutput(t *testing.T) {
	startedAt := time.Unix(100, 0)
	box := toolCallBox{Title: "shell", StartedAt: startedAt, EndedAt: startedAt}
	values := make([]string, 0, maxToolOutputLines*2)
	for i := range cap(values) {
		values = append(values, fmt.Sprintf("line-%d", i))
	}
	box.addDisplayArtifact(tool.TextArtifact{Text: strings.Join(values, "\n")})

	expanded := plainLines(renderBoxLinesAt(box, 80, true, startedAt))
	// Title, bounded output, truncation notice and duration.
	if len(expanded) != maxToolOutputLines+3 {
		t.Fatalf("expanded tool call = %#v, want %d output lines", expanded, maxToolOutputLines)
	}
	if notice := expanded[len(expanded)-2]; notice != "   ... (more output)" {
		t.Fatalf("truncation notice = %q", notice)
	}
	if strings.Contains(strings.Join(expanded, "\n"), "line-"+fmt.Sprint(maxToolOutputLines)) {
		t.Fatalf("expanded tool call rendered beyond the bound:\n%s", strings.Join(expanded, "\n"))
	}
}

func TestRenderToolCallAlwaysShowsError(t *testing.T) {
	startedAt := time.Unix(100, 0)
	box := toolCallBox{
		Title:     "shell",
		Error:     "exit status 1",
		StartedAt: startedAt,
		EndedAt:   startedAt,
	}
	box.addDisplayArtifact(tool.TextArtifact{Text: "hidden output"})

	for _, expanded := range []bool{false, true} {
		lines := renderBoxLinesAt(box, 80, expanded, startedAt)
		plain := strings.Join(plainLines(lines), "\n")
		if !strings.Contains(plain, "exit status 1") {
			t.Fatalf("tool error missing (expanded=%v):\n%s", expanded, plain)
		}
		if strings.Contains(plain, "hidden output") != expanded {
			t.Fatalf("tool output visibility (expanded=%v):\n%s", expanded, plain)
		}
	}
}

func TestWorkingIndicatorUsesConversationPadding(t *testing.T) {
	lines := workingIndicator{Frame: 3}.Lines(80)
	if got := lines[0]; got != " Working..." {
		t.Fatalf("working indicator = %q", got)
	}
}

func TestPendingSteeringPresenterUsesQueuedPromptMarker(t *testing.T) {
	lines := pendingSteeringPresenter{Text: "Refine the tests"}.Lines(80)
	if got := plainLines(lines)[0]; got != " » (queued) Refine the tests" {
		t.Fatalf("queued steering message = %q", got)
	}
}

func TestUserMessageUsesPromptMarker(t *testing.T) {
	lines := renderBoxLines(userMessageBox{Text: "Explain this function."}, 80, false)
	if got := plainLines(lines)[1]; got != editorPrefix+"Explain this function." {
		t.Fatalf("user message = %q, want editor prompt prefix %q", got, editorPrefix)
	}
}

func TestUserMessageUsesMutedBorders(t *testing.T) {
	lines := renderBoxLines(userMessageBox{Text: "one two three"}, 10, false)
	if len(lines) != 5 {
		t.Fatalf("user message lines = %#v, want borders around three wrapped lines", plainLines(lines))
	}
	border := strings.Repeat("─", 10)
	plain := plainLines(lines)
	if plain[0] != border || plain[len(plain)-1] != border {
		t.Fatalf("user message borders = %q, %q; want %q", plain[0], plain[len(plain)-1], border)
	}
	for _, index := range []int{0, len(lines) - 1} {
		if !strings.Contains(lines[index], mutedStyle.start()) {
			t.Fatalf("user message border is not muted: %q", lines[index])
		}
	}
	if strings.Contains(lines[1], mutedStyle.start()) {
		t.Fatalf("user message content is unexpectedly muted: %q", lines[1])
	}
}

func TestEditorUsesMutedBorders(t *testing.T) {
	lines := editorPresenter{}.Lines(32)
	if len(lines) != 3 {
		t.Fatalf("editor lines = %#v, want borders around the editor", plainLines(lines))
	}
	border := strings.Repeat("─", 32)
	plain := plainLines(lines)
	if plain[0] != border || plain[len(plain)-1] != border {
		t.Fatalf("editor borders = %q, %q; want %q", plain[0], plain[len(plain)-1], border)
	}
	for _, index := range []int{0, len(lines) - 1} {
		if !strings.Contains(lines[index], mutedStyle.start()) {
			t.Fatalf("editor border is not muted: %q", lines[index])
		}
	}
}

func TestEditorPresenterUsesPromptMarkerAndReverseCursor(t *testing.T) {
	lines := editorPresenter{Text: []rune("abc"), Cursor: 1}.Lines(80)
	if got := plainLines(lines)[1]; got != " › abc" {
		t.Fatalf("editor line = %q", got)
	}
	if !strings.Contains(lines[1], cursorStyle.start()) {
		t.Fatalf("editor cursor is not reversed: %q", lines[1])
	}
}
