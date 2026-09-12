package tui

import (
	"strings"
	"testing"

	"github.com/crowl/ronin/mermaid"
	"github.com/crowl/ronin/tui/internal/text"
)

func TestMarkdownMermaid(t *testing.T) {
	source := "graph TD; A --> B"
	want, err := mermaid.Render(source, 80)
	if err != nil {
		t.Fatal(err)
	}
	for _, fence := range []string{"```", "~~~~"} {
		input := "before\n" + fence + "mermaid\n" + source + "\n" + fence + "\nafter"
		got := text.StripANSI(strings.Join(markdownLines(input, 80, defaultTextStyles()), "\n"))
		if got != "before\n"+want+"\nafter" {
			t.Fatalf("unexpected diagram:\n%s", got)
		}
	}
}
func TestMarkdownMermaidFallback(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		width       int
	}{
		{"streaming", "```mermaid\ngraph TD; A --> B", 80},
		{"unsupported", "```mermaid\nsequenceDiagram\n```", 80},
		{"invalid", "```mermaid\ngraph TD; A -->\n```", 80},
		{"too wide", "```mermaid\ngraph LR; A --> B\n```", 12},
		{"wrong closing marker", "```mermaid\ngraph TD; A\n~~~", 80},
		{"short closing fence", "````mermaid\ngraph TD; A\n```", 80},
		{"ordinary code", "```go\ngraph TD; A\n```", 80},
		{"nested fence", "~~~~text\n```mermaid\ngraph TD; A\n```\n~~~~", 80},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := text.StripANSI(strings.Join(markdownLines(tt.input, tt.width, defaultTextStyles()), "\n"))
			if strings.Contains(got, "┌") || !strings.Contains(got, "```") && !strings.Contains(got, "~~~~") {
				t.Fatalf("expected source fallback, got %q", got)
			}
		})
	}
}
func TestMarkdownMermaidResize(t *testing.T) {
	input := "```mermaid\ngraph LR; A --> B\n```"
	narrow := text.StripANSI(strings.Join(markdownLines(input, 12, defaultTextStyles()), "\n"))
	wide := text.StripANSI(strings.Join(markdownLines(input, 80, defaultTextStyles()), "\n"))
	if !strings.Contains(narrow, "```mermaid") || strings.Contains(wide, "```mermaid") || !strings.Contains(wide, "▶") {
		t.Fatal("resize did not switch between source and diagram")
	}
}
