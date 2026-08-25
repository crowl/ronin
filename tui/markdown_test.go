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
		{name: "bold", input: "This is **bold** text", width: 80, want: []string{"This is \x1b[1mbold\x1b[0m text"}},
		{name: "italic", input: "This is *italic* text", width: 80, want: []string{"This is \x1b[3mitalic\x1b[0m text"}},
		{name: "inline code", input: "Use `foo` here", width: 80, want: []string{"Use \x1b[1mfoo\x1b[0m here"}},
		{name: "heading", input: "## Title", width: 80, want: []string{"\x1b[1m## Title\x1b[0m"}},
		{name: "lists", input: "- item with multiple words\n1. ordered item", width: 80, want: []string{"- item with multiple words", "1. ordered item"}},
		{name: "horizontal rule", input: "---", width: 10, want: []string{""}},
		{name: "blockquote", input: "> quoted text", width: 80, want: []string{"\x1b[3m│ quoted text\x1b[0m"}},
		{name: "link", input: "See [docs](https://example.com)", width: 80, want: []string{"See docs (https://example.com)"}},
		{name: "blank lines", input: "first\n\nsecond", width: 80, want: []string{"first", "", "second"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := markdownLines(tt.input, tt.width, defaultTextStyles())
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("markdown lines\ngot:  %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}
