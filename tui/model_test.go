package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/tui/internal/text"
)

func TestAppModel(t *testing.T) {
	t.Run("ctrl c exits when idle", func(t *testing.T) {
		model := newTestModel(t)

		update, err := model.handleKey(terminal.Key{Type: terminal.KeyCtrlC})
		if err != nil {
			t.Fatalf("handle key: %v", err)
		}
		if _, ok := update.Action.(exitAction); !ok {
			t.Fatalf("action\ngot:  %T\nwant: %T", update.Action, exitAction{})
		}
	})

	t.Run("ctrl c cancels when working", func(t *testing.T) {
		model := newTestModel(t)
		model.working = true

		update, err := model.handleKey(terminal.Key{Type: terminal.KeyCtrlC})
		if err != nil {
			t.Fatalf("handle key: %v", err)
		}
		if !update.Render {
			t.Fatalf("cancel did not request render")
		}
		if _, ok := update.Action.(cancelPromptAction); !ok {
			t.Fatalf("action\ngot:  %T\nwant: %T", update.Action, cancelPromptAction{})
		}
		if len(model.boxes) != 1 {
			t.Fatalf("box count\ngot:  %d\nwant: 1", len(model.boxes))
		}
		box, ok := model.boxes[0].(errorMessageBox)
		if !ok || box.Text != "Operation cancelled" {
			t.Fatalf("cancel box\ngot:  %#v\nwant: Operation cancelled", model.boxes[0])
		}
	})

	t.Run("submit prompt returns submit action", func(t *testing.T) {
		model := newTestModel(t)

		if _, err := model.handleKey(terminal.Key{Type: terminal.KeyRune, Rune: 'h'}); err != nil {
			t.Fatalf("type h: %v", err)
		}
		if _, err := model.handleKey(terminal.Key{Type: terminal.KeyRune, Rune: 'i'}); err != nil {
			t.Fatalf("type i: %v", err)
		}
		update, err := model.handleKey(terminal.Key{Type: terminal.KeyEnter})
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
		action, ok := update.Action.(submitPromptAction)
		if !ok {
			t.Fatalf("action\ngot:  %T\nwant: %T", update.Action, submitPromptAction{})
		}
		if action.Prompt != "hi" {
			t.Fatalf("prompt\ngot:  %q\nwant: hi", action.Prompt)
		}
	})

	t.Run("menu selection returns command action", func(t *testing.T) {
		model := newTestModel(t)

		if _, err := model.handleKey(terminal.Key{Type: terminal.KeyRune, Rune: '/'}); err != nil {
			t.Fatalf("open menu: %v", err)
		}
		update, err := model.handleKey(terminal.Key{Type: terminal.KeyEnter})
		if err != nil {
			t.Fatalf("select menu: %v", err)
		}
		action, ok := update.Action.(runCommandAction)
		if !ok {
			t.Fatalf("action\ngot:  %T\nwant: %T", update.Action, runCommandAction{})
		}
		if _, ok := action.Command.(StartNewConversation); !ok {
			t.Fatalf("command\ngot:  %T\nwant: %T", action.Command, StartNewConversation{})
		}
	})

	t.Run("compaction shows Compacting indicator", func(t *testing.T) {
		model := newTestModel(t)
		model.startCompaction(menuItem{Value: "/compact"})

		if !model.working || model.workingLabel != "Compacting" {
			t.Fatalf("working=%v label=%q, want Compacting", model.working, model.workingLabel)
		}

		lines, err := model.lines(80, &fakeAgent{}, time.Now())
		if err != nil {
			t.Fatalf("lines: %v", err)
		}
		if !renderedLinesContain(stripLines(lines), "Compacting") {
			t.Fatalf("Compacting indicator not rendered: %#v", lines)
		}
		if renderedLinesContain(stripLines(lines), "Working") {
			t.Fatalf("unexpected Working indicator: %#v", lines)
		}
	})

	t.Run("pending steering prompt renders above indicator", func(t *testing.T) {
		model := newTestModel(t)
		model.startPrompt("do the thing")
		model.steeringPrompt = "then this"

		lines, err := model.lines(80, &fakeAgent{}, time.Now())
		if err != nil {
			t.Fatalf("lines: %v", err)
		}
		stripped := stripLines(lines)
		steeringIdx := lineIndexContaining(stripped, "then this")
		indicatorIdx := lineIndexContaining(stripped, "Working")
		if steeringIdx < 0 {
			t.Fatalf("steering prompt not rendered: %#v", lines)
		}
		if indicatorIdx < 0 {
			t.Fatalf("working indicator not rendered: %#v", lines)
		}
		if steeringIdx >= indicatorIdx {
			t.Fatalf("steering prompt (%d) should render above indicator (%d)", steeringIdx, indicatorIdx)
		}
	})

	t.Run("working tick increments indicator only while working", func(t *testing.T) {
		model := newTestModel(t)

		update := model.tickWorking()
		if update.Render || model.indicatorFrame != 0 {
			t.Fatalf("idle tick changed model: update=%#v frame=%d", update, model.indicatorFrame)
		}

		model.working = true
		update = model.tickWorking()
		if !update.Render || model.indicatorFrame != 1 {
			t.Fatalf("working tick\nupdate: %#v\nframe: %d", update, model.indicatorFrame)
		}
	})
}

func stripLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = text.StripANSI(line)
	}
	return out
}

func lineIndexContaining(lines []string, value string) int {
	for i, line := range lines {
		if strings.Contains(line, value) {
			return i
		}
	}
	return -1
}

func newTestModel(t *testing.T) *appModel {
	t.Helper()

	model, err := newAppModel([]Command{
		StartNewConversation{},
		CompactConversation{},
		Exit{},
	}, DefaultTheme())
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	return model
}
