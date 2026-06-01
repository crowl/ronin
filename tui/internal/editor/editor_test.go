package editor

import (
	"testing"

	"github.com/crowl/ronin/tui/internal/terminal"
)

func TestReadlineEditor(t *testing.T) {
	t.Run("inserts at cursor", func(t *testing.T) {
		editor := New()
		editor.SetText("ac")
		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyArrowLeft}); err != nil {
			t.Fatalf("move cursor left: %v", err)
		}
		fx, err := editor.HandleKey(terminal.Key{Type: terminal.KeyRune, Rune: 'b'})
		if err != nil {
			t.Fatalf("insert rune: %v", err)
		}

		textChange, ok := fx.(TextChange)
		if !ok {
			t.Fatalf("effect type\ngot:  %T\nwant: %T", fx, TextChange{})
		}
		if textChange.Text != "abc" || string(editor.Text()) != "abc" {
			t.Fatalf("editor text\neffect: %q\neditor: %q\nwant:   %q", textChange.Text, string(editor.Text()), "abc")
		}
		if editor.Cursor() != 2 {
			t.Fatalf("cursor\ngot:  %d\nwant: 2", editor.Cursor())
		}
	})

	t.Run("deletes around cursor", func(t *testing.T) {
		editor := New()
		editor.SetText("abc")
		for range 2 {
			if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyArrowLeft}); err != nil {
				t.Fatalf("move cursor left: %v", err)
			}
		}

		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyBackspace}); err != nil {
			t.Fatalf("backspace: %v", err)
		}
		if got, want := string(editor.Text()), "bc"; got != want {
			t.Fatalf("text after backspace\ngot:  %q\nwant: %q", got, want)
		}
		if editor.Cursor() != 0 {
			t.Fatalf("cursor after backspace\ngot:  %d\nwant: 0", editor.Cursor())
		}

		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyDelete}); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if got, want := string(editor.Text()), "c"; got != want {
			t.Fatalf("text after delete\ngot:  %q\nwant: %q", got, want)
		}
	})

	t.Run("line local movement and deletion", func(t *testing.T) {
		editor := New()
		editor.SetText("abc\ndef\nghi")
		for range 4 {
			if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyArrowLeft}); err != nil {
				t.Fatalf("move cursor left: %v", err)
			}
		}

		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyCtrlA}); err != nil {
			t.Fatalf("ctrl-a: %v", err)
		}
		if editor.Cursor() != 4 {
			t.Fatalf("cursor after ctrl-a\ngot:  %d\nwant: 4", editor.Cursor())
		}

		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyCtrlE}); err != nil {
			t.Fatalf("ctrl-e: %v", err)
		}
		if editor.Cursor() != 7 {
			t.Fatalf("cursor after ctrl-e\ngot:  %d\nwant: 7", editor.Cursor())
		}

		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyCtrlU}); err != nil {
			t.Fatalf("ctrl-u: %v", err)
		}
		if got, want := string(editor.Text()), "abc\n\nghi"; got != want {
			t.Fatalf("text after ctrl-u\ngot:  %q\nwant: %q", got, want)
		}

		editor.SetText("abc\ndef\nghi")
		for range 6 {
			if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyArrowLeft}); err != nil {
				t.Fatalf("move cursor left: %v", err)
			}
		}
		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyCtrlK}); err != nil {
			t.Fatalf("ctrl-k: %v", err)
		}
		if got, want := string(editor.Text()), "abc\nd\nghi"; got != want {
			t.Fatalf("text after ctrl-k\ngot:  %q\nwant: %q", got, want)
		}
	})

	t.Run("deletes previous word", func(t *testing.T) {
		editor := New()
		editor.SetText("one two  ")
		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyCtrlW}); err != nil {
			t.Fatalf("ctrl-w: %v", err)
		}
		if got, want := string(editor.Text()), "one "; got != want {
			t.Fatalf("text after ctrl-w\ngot:  %q\nwant: %q", got, want)
		}
		if editor.Cursor() != 4 {
			t.Fatalf("cursor after ctrl-w\ngot:  %d\nwant: 4", editor.Cursor())
		}
	})

	t.Run("history restores draft", func(t *testing.T) {
		editor := New()
		editor.SetText("first")
		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyEnter}); err != nil {
			t.Fatalf("submit first prompt: %v", err)
		}
		editor.Clear()
		editor.SetText("second")
		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyEnter}); err != nil {
			t.Fatalf("submit second prompt: %v", err)
		}
		editor.Clear()
		editor.SetText("draft")

		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyArrowUp}); err != nil {
			t.Fatalf("history up latest: %v", err)
		}
		if got, want := string(editor.Text()), "second"; got != want {
			t.Fatalf("latest history text\ngot:  %q\nwant: %q", got, want)
		}
		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyArrowUp}); err != nil {
			t.Fatalf("history up previous: %v", err)
		}
		if got, want := string(editor.Text()), "first"; got != want {
			t.Fatalf("previous history text\ngot:  %q\nwant: %q", got, want)
		}
		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyArrowDown}); err != nil {
			t.Fatalf("history down latest: %v", err)
		}
		if got, want := string(editor.Text()), "second"; got != want {
			t.Fatalf("history down text\ngot:  %q\nwant: %q", got, want)
		}
		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyArrowDown}); err != nil {
			t.Fatalf("history down draft: %v", err)
		}
		if got, want := string(editor.Text()), "draft"; got != want {
			t.Fatalf("draft text\ngot:  %q\nwant: %q", got, want)
		}
	})

	t.Run("submits non-empty prompt only", func(t *testing.T) {
		editor := New()
		fx, err := editor.HandleKey(terminal.Key{Type: terminal.KeyEnter})
		if err != nil {
			t.Fatalf("submit empty prompt: %v", err)
		}
		if _, ok := fx.(NoEffect); !ok {
			t.Fatalf("empty prompt effect\ngot:  %T\nwant: %T", fx, NoEffect{})
		}

		editor.SetText("prompt")
		fx, err = editor.HandleKey(terminal.Key{Type: terminal.KeyEnter})
		if err != nil {
			t.Fatalf("submit prompt: %v", err)
		}
		submit, ok := fx.(SubmitPrompt)
		if !ok {
			t.Fatalf("submit effect\ngot:  %T\nwant: %T", fx, SubmitPrompt{})
		}
		if submit.Prompt != "prompt" {
			t.Fatalf("submitted prompt\ngot:  %q\nwant: %q", submit.Prompt, "prompt")
		}
	})

	t.Run("shift enter sequence adds newline", func(t *testing.T) {
		keys := terminal.ParseKeys([]byte(terminal.KeyShiftEnterCSIu))
		if len(keys) != 1 || keys[0].Type != terminal.KeyShiftEnter {
			t.Fatalf("parse shift+enter: %#v", keys)
		}

		editor := New()
		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyRune, Rune: 'a'}); err != nil {
			t.Fatalf("type first rune: %v", err)
		}
		if _, err := editor.HandleKey(keys[0]); err != nil {
			t.Fatalf("type shift+enter: %v", err)
		}
		if _, err := editor.HandleKey(terminal.Key{Type: terminal.KeyRune, Rune: 'b'}); err != nil {
			t.Fatalf("type second rune: %v", err)
		}

		if got, want := string(editor.Text()), "a\nb"; got != want {
			t.Fatalf("editor text after shift+enter\ngot:  %q\nwant: %q", got, want)
		}
	})
}
