package tui

import (
	"testing"

	"github.com/crowl/ronin/tui/internal/terminal"
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
