package tui

import (
	"strings"
	"testing"

	"github.com/crowl/ronin/mermaid"
	"github.com/crowl/ronin/tui/internal/text"
)

func TestMarkdownSequence(t *testing.T) {
	source := "sequenceDiagram\nparticipant A as Client\nalt ready\nA->>B: Request\nelse unavailable\nB-->>A: Retry\nend"
	want, err := mermaid.Render(source, 100)
	if err != nil {
		t.Fatal(err)
	}
	input := "```mermaid\n" + source + "\n```"
	got := text.StripANSI(strings.Join(markdownLines(input, 100, defaultTextStyles()), "\n"))
	if got != want {
		t.Fatalf("sequence geometry changed:\n%s", got)
	}
	for _, tt := range []struct {
		name, input string
		width       int
	}{
		{"streaming", strings.TrimSuffix(input, "\n```"), 100},
		{"width", input, 12},
		{"unsupported", "```mermaid\nsequenceDiagram\nactor A\n```", 100},
		{"unclosed block", "```mermaid\nsequenceDiagram\nalt ready\nA->>B: hi\n```", 100},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := text.StripANSI(strings.Join(markdownLines(tt.input, tt.width, defaultTextStyles()), "\n"))
			if !strings.Contains(got, "```mermaid") || strings.Contains(got, "┌") {
				t.Fatalf("expected source fallback: %q", got)
			}
		})
	}
}
