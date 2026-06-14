package tui

import (
	"reflect"
	"testing"
)

func TestRenderAssistantThinkingBoxBoldUsesThinkingTextColor(t *testing.T) {
	theme := Theme{
		Text: TextTheme{
			Normal: Style{FG: "white"},
			Strong: Style{FG: "red", Bold: true},
		},
		Box: BoxTheme{
			AssistantThinking: BoxStyle{
				Container: Style{FG: "color-8"},
				Body:      Style{FG: "color-8", Italic: true},
				Strong:    Style{FG: "color-8", Bold: true},
			},
		},
	}

	got := renderBoxLines(assistantThinkingBox{Text: "think **bold** now"}, 80, theme, false)
	want := []string{
		"\x1b[3;38;5;8m                                                                                \x1b[0m",
		"\x1b[3;38;5;8m think \x1b[1;3;38;5;8mbold\x1b[3;38;5;8m now\x1b[0m",
		"\x1b[3;38;5;8m                                                                                \x1b[0m",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("thinking box bold style\ngot:  %#v\nwant: %#v", got, want)
	}
}

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
