package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/crowl/ronin/tui/internal/text"
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

func TestRenderToolCallBoxWrapsLongTitleWithTitleStyle(t *testing.T) {
	theme := Theme{
		Box: BoxTheme{
			ToolCall: BoxStyle{
				Container: Style{FG: "white"},
				Title:     Style{FG: "red", Bold: true},
				Meta:      Style{FG: "blue"},
			},
		},
	}
	box := toolCallBox{Title: "$ go test ./tui -run TestRenderToolCallBox Wraps Long Title"}

	got := renderBoxLinesAt(box, 24, theme, false, box.StartedAt)
	wantTitleLines := []string{
		" $ go test ./tui -run",
		" TestRenderToolCallBox",
		" Wraps Long Title",
	}
	if len(got) < 1+len(wantTitleLines) {
		t.Fatalf("tool call lines too short\ngot: %#v", got)
	}

	var gotTitle string
	for i, want := range wantTitleLines {
		line := got[i+1]
		plain := strings.TrimRight(text.StripANSI(line), " ")
		if plain != want {
			t.Fatalf("wrapped title line %d\ngot:  %q\nwant: %q", i, plain, want)
		}
		if !strings.HasPrefix(line, "\x1b[1;31") {
			t.Fatalf("wrapped title line %d does not use title style\ngot: %q", i, line)
		}
		gotTitle += strings.ReplaceAll(strings.TrimSpace(plain), " ", "")
	}

	wantTitle := strings.ReplaceAll(box.Title, " ", "")
	if gotTitle != wantTitle {
		t.Fatalf("wrapped title content\ngot:  %q\nwant: %q", gotTitle, wantTitle)
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
