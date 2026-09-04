package tui

import (
	"strings"
	"testing"

	"github.com/crowl/ronin/tui/internal/text"
)

func TestEditorCellLayout(t *testing.T) {
	for _, tc := range []struct {
		name, input   string
		cursor, width int
	}{
		{"ascii boundary", "abcdef", 3, 6},
		{"end at boundary", "abc", 3, 6},
		{"wide", "界界x", 2, 7},
		{"accent", "e\u0301xy", 2, 5},
		{"tab", "a\tb", 2, 7},
		{"narrow tab", "\tx", 1, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := editorPresenter{Text: []rune(tc.input), Cursor: tc.cursor}.Lines(tc.width)
			cursorCount := 0
			for _, line := range lines {
				cursorCount += strings.Count(line, "\x1b[7m")
				if strings.ContainsRune(line, '\t') {
					t.Fatal("unexpanded tab")
				}
				if text.VisibleLen(line) > tc.width {
					t.Fatalf("line exceeds width: %q (%d)", line, text.VisibleLen(line))
				}
			}
			if cursorCount != 1 {
				t.Fatalf("cursor count=%d lines=%q", cursorCount, lines)
			}
		})
	}
}
