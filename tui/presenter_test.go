package tui

import "testing"

func TestRenderEditorPresenterSegmentStylesText(t *testing.T) {
	theme := Theme{
		Text: TextTheme{
			Normal: Style{FG: "white"},
		},
		UI: UITheme{
			EditorCursor: Style{Reverse: true},
		},
	}

	segment := editorSegment{text: []rune("abc"), startColumn: 0, endColumn: 3}

	got := rendereditorPresenterSegment(" ", segment, 0, false, theme)
	want := " " + "\x1b[37mabc\x1b[0m"
	if got != want {
		t.Fatalf("editor text style\ngot:  %q\nwant: %q", got, want)
	}
}

func TestRenderEditorPresenterSegmentStylesCursorWithTextColor(t *testing.T) {
	theme := Theme{
		Text: TextTheme{
			Normal: Style{FG: "white"},
		},
		UI: UITheme{
			EditorCursor: Style{Reverse: true},
		},
	}

	segment := editorSegment{text: []rune("abc"), startColumn: 0, endColumn: 3}

	got := rendereditorPresenterSegment(" ", segment, 1, true, theme)
	want := " " + "\x1b[37ma\x1b[0m" + "\x1b[7;37mb\x1b[0m" + "\x1b[37mc\x1b[0m"
	if got != want {
		t.Fatalf("editor cursor style\ngot:  %q\nwant: %q", got, want)
	}
}
