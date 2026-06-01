package tui

import (
	"reflect"
	"testing"
)

func TestMarkdownLines(t *testing.T) {
	tests := []struct {
		name  string
		input string
		width int
		want  []string
	}{
		{
			name:  "bold fence",
			input: "This is **bold** text",
			width: 80,
			want:  []string{" This is \x1b[38;5;1mbold\x1b[0m text"},
		},
		{
			name:  "italic fence",
			input: "This is *italic* text",
			width: 80,
			want:  []string{" This is \x1b[38;5;2mitalic\x1b[0m text"},
		},
		{
			name:  "inline code bold",
			input: "Use `foo` here",
			width: 80,
			want:  []string{" Use \x1b[38;5;3mfoo\x1b[0m here"},
		},
		{
			name:  "heading bold preserves text",
			input: "## Title",
			width: 80,
			want:  []string{"\x1b[38;5;1m ## Title\x1b[0m"},
		},
		{
			name:  "code block preserves fences",
			input: "```go\nfmt.Println(1)\n```",
			width: 80,
			want:  []string{"\x1b[38;5;3m ```go\x1b[0m", "\x1b[38;5;3m fmt.Println(1)\x1b[0m", "\x1b[38;5;3m ```\x1b[0m"},
		},
		{
			name:  "lists preserve text",
			input: "- item with multiple words\n1. ordered item",
			width: 80,
			want:  []string{" - item with multiple words", " 1. ordered item"},
		},
		{
			name:  "horizontal rule is ignored",
			input: "---",
			width: 10,
			want:  []string{" "},
		},
		{
			name:  "blockquote",
			input: "> quoted text",
			width: 80,
			want:  []string{"\x1b[38;5;2m │ quoted text\x1b[0m"},
		},
		{
			name:  "link",
			input: "See [docs](https://example.com)",
			width: 80,
			want:  []string{" See docs (https://example.com)"},
		},
		{
			name:  "blank lines are preserved between content",
			input: "first\n\nsecond",
			width: 80,
			want:  []string{" first", "", " second"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := markdownLines(tt.input, tt.width, fakeTextTheme())
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("markdown lines\ngot:  %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}

func fakeTextTheme() TextTheme {
	return TextTheme{
		Strong:   Style{FG: "color-1"},
		Emphasis: Style{FG: "color-2"},
		Code:     Style{FG: "color-3"},
	}
}
