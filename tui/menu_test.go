package tui

import (
	"strings"
	"testing"

	"github.com/crowl/ronin/agent"
	"github.com/crowl/ronin/llm"

	"github.com/crowl/ronin/tui/internal/terminal"
)

func TestMenu(t *testing.T) {
	t.Run("rejects empty command list", func(t *testing.T) {
		_, err := newMenu(nil)
		if err == nil || !strings.Contains(err.Error(), "no commands provided") {
			t.Fatalf("error\ngot:  %v\nwant: contains %q", err, "no commands provided")
		}
	})

	t.Run("creates items for all command types", func(t *testing.T) {
		menu, err := newMenu([]Command{
			StartNewConversation{},
			CompactConversation{},
			SwitchModel{Model: llm.Model{Provider: "test", Name: "model"}},
			SwitchReasoningLevel{Level: llm.ReasoningLevelHigh},
			SwitchTheme{Name: "light", Theme: LightTheme()},
			InvokeSkill{Skill: agent.Skill{Name: "go", Description: "Go help"}},
			Exit{},
		})
		if err != nil {
			t.Fatalf("create menu: %v", err)
		}

		got := menu.Items()
		wantValues := []string{"/new", "/compact", "/model test:model", "/reasoning high", "/theme light", "/skill:go", "/exit"}
		if len(got) != len(wantValues) {
			t.Fatalf("item count\ngot:  %d %#v\nwant: %d", len(got), got, len(wantValues))
		}
		for i, want := range wantValues {
			if got[i].Value != want {
				t.Fatalf("item %d value\ngot:  %q\nwant: %q", i, got[i].Value, want)
			}
		}
	})

	t.Run("theme command item value and description", func(t *testing.T) {
		menu, err := newMenu([]Command{
			SwitchTheme{Name: "dark", Theme: DarkTheme()},
		})
		if err != nil {
			t.Fatalf("create menu: %v", err)
		}

		got := menu.Items()
		if len(got) != 1 {
			t.Fatalf("item count\ngot:  %d\nwant: 1", len(got))
		}
		if got[0].Value != "/theme dark" {
			t.Fatalf("item value\ngot:  %q\nwant: %q", got[0].Value, "/theme dark")
		}
		if got[0].Description != "switch theme to dark" {
			t.Fatalf("item description\ngot:  %q\nwant: %q", got[0].Description, "switch theme to dark")
		}
	})

	t.Run("query shows and hides menu", func(t *testing.T) {
		menu := newTestMenu(t)

		menu.SetQuery("/")
		if !menu.Shown() {
			t.Fatalf("slash query did not show menu")
		}

		menu.SetQuery("")
		if menu.Shown() {
			t.Fatalf("empty query did not hide menu")
		}
	})

	t.Run("selection wraps", func(t *testing.T) {
		menu := newTestMenu(t)
		menu.Show()

		if _, err := menu.HandleKey(terminal.Key{Type: terminal.KeyArrowUp}); err != nil {
			t.Fatalf("move selection up: %v", err)
		}
		if menu.SelectedIndex() != len(menu.Items())-1 {
			t.Fatalf("selection after wrap up\ngot:  %d\nwant: %d", menu.SelectedIndex(), len(menu.Items())-1)
		}
		if _, err := menu.HandleKey(terminal.Key{Type: terminal.KeyArrowDown}); err != nil {
			t.Fatalf("move selection down: %v", err)
		}
		if menu.SelectedIndex() != 0 {
			t.Fatalf("selection after wrap down\ngot:  %d\nwant: 0", menu.SelectedIndex())
		}
	})

	t.Run("selection resets when query filters items", func(t *testing.T) {
		menu, err := newMenu([]Command{
			StartNewConversation{},
			StartNewConversation{},
			CompactConversation{},
		})
		if err != nil {
			t.Fatalf("create menu: %v", err)
		}

		menu.Show()
		if _, err := menu.HandleKey(terminal.Key{Type: terminal.KeyArrowDown}); err != nil {
			t.Fatalf("move menu selection: %v", err)
		}
		if menu.SelectedIndex() != 1 {
			t.Fatalf("selected index after move\ngot:  %d\nwant: 1", menu.SelectedIndex())
		}

		menu.SetQuery("compact")

		if menu.SelectedIndex() != 0 {
			t.Fatalf("selected index after query filter\ngot:  %d\nwant: 0", menu.SelectedIndex())
		}
		if len(menu.Items()) != 1 {
			t.Fatalf("filtered item count\ngot:  %d\nwant: 1", len(menu.Items()))
		}
		fx, err := menu.HandleKey(terminal.Key{Type: terminal.KeyEnter})
		if err != nil {
			t.Fatalf("select filtered item: %v", err)
		}
		selected, ok := fx.(menuItemSelected)
		if !ok {
			t.Fatalf("effect type\ngot:  %T\nwant: %T", fx, menuItemSelected{})
		}
		if selected.MenuItem.Value != "/compact" {
			t.Fatalf("selected item\ngot:  %q\nwant: %q", selected.MenuItem.Value, "/compact")
		}
	})

	t.Run("preselects filtered selected item", func(t *testing.T) {
		menu, err := newMenu([]Command{
			StartNewConversation{},
			SwitchModel{Model: llm.Model{Provider: "test", Name: "a"}},
			StartNewConversation{},
			SwitchModel{Model: llm.Model{Provider: "test", Name: "b"}},
		})
		if err != nil {
			t.Fatalf("create menu: %v", err)
		}

		menu.Show()
		menu.SetQuery("/model")
		if _, err := menu.HandleKey(terminal.Key{Type: terminal.KeyArrowDown}); err != nil {
			t.Fatalf("move filtered selection: %v", err)
		}

		fx, err := menu.HandleKey(terminal.Key{Type: terminal.KeyTab})
		if err != nil {
			t.Fatalf("preselect filtered item: %v", err)
		}
		preselected, ok := fx.(menuItemPreSelected)
		if !ok {
			t.Fatalf("effect type\ngot:  %T\nwant: %T", fx, menuItemPreSelected{})
		}
		if preselected.MenuItem.Value != "/model test:b" {
			t.Fatalf("preselected item\ngot:  %q\nwant: %q", preselected.MenuItem.Value, "/model test:b")
		}
	})

	t.Run("fuzzy matching is case insensitive", func(t *testing.T) {
		menu, err := newMenu([]Command{
			StartNewConversation{},
			SwitchModel{Model: llm.Model{Provider: "openai", Name: "gpt"}},
			CompactConversation{},
		})
		if err != nil {
			t.Fatalf("create menu: %v", err)
		}

		menu.SetQuery("Mdl")
		items := menu.Items()
		if len(items) != 1 || items[0].Value != "/model openai:gpt" {
			t.Fatalf("filtered items\ngot:  %#v\nwant: one /model item", items)
		}
	})

	t.Run("selection with no filtered items returns specific error", func(t *testing.T) {
		menu := newTestMenu(t)
		menu.Show()
		menu.SetQuery("zzzz")

		_, err := menu.HandleKey(terminal.Key{Type: terminal.KeyEnter})
		if err == nil || !strings.Contains(err.Error(), "invalid selected index") {
			t.Fatalf("select error\ngot:  %v\nwant: contains %q", err, "invalid selected index")
		}
	})
}

func newTestMenu(t *testing.T) *menuState {
	t.Helper()

	menu, err := newMenu([]Command{
		StartNewConversation{},
		CompactConversation{},
		Exit{},
	})
	if err != nil {
		t.Fatalf("create menu: %v", err)
	}
	return menu
}
