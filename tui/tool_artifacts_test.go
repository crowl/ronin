package tui

import (
	"strings"
	"testing"

	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/tui/internal/text"
)

func TestToolArtifactLines(t *testing.T) {
	t.Run("file artifact starts at line one", func(t *testing.T) {
		lines := toolArtifactLines(tool.FileArtifact{Path: "main.go", Content: "package main\nfunc main() {}"}, DefaultTheme().Box.ToolCall, 80)

		plain := plainLines(lines)
		if len(plain) != 2 {
			t.Fatalf("line count\ngot:  %#v\nwant: 2 lines", plain)
		}
		if !strings.Contains(plain[0], "1") || !strings.Contains(plain[0], "package main") {
			t.Fatalf("first line\ngot:  %q\nwant: line number 1 and content", plain[0])
		}
		if !strings.Contains(plain[1], "2") || !strings.Contains(plain[1], "func main() {}") {
			t.Fatalf("second line\ngot:  %q\nwant: line number 2 and content", plain[1])
		}
	})

	t.Run("file range uses start line", func(t *testing.T) {
		lines := toolArtifactLines(tool.FileRangeArtifact{Path: "main.go", Content: "one\ntwo", StartLine: 12, EndLine: 13}, DefaultTheme().Box.ToolCall, 80)

		plain := plainLines(lines)
		if !strings.Contains(plain[0], "12") || !strings.Contains(plain[0], "one") {
			t.Fatalf("first range line\ngot:  %q\nwant: line 12", plain[0])
		}
		if !strings.Contains(plain[1], "13") || !strings.Contains(plain[1], "two") {
			t.Fatalf("second range line\ngot:  %q\nwant: line 13", plain[1])
		}
	})

	t.Run("file range falls back to line one", func(t *testing.T) {
		lines := toolArtifactLines(tool.FileRangeArtifact{Path: "main.go", Content: "one", StartLine: 0}, DefaultTheme().Box.ToolCall, 80)

		plain := plainLines(lines)
		if !strings.Contains(plain[0], "1") || !strings.Contains(plain[0], "one") {
			t.Fatalf("fallback line\ngot:  %q\nwant: line 1", plain[0])
		}
	})

	t.Run("unified diff omits separator pipe", func(t *testing.T) {
		lines := toolArtifactLines(tool.UnifiedDiffArtifact{Path: "main.go", Diff: "@@ -1,2 +1,2 @@\n old\n-removed\n+added\n"}, DefaultTheme().Box.ToolCall, 80)

		plain := plainLines(lines)
		joined := strings.Join(plain, "\n")
		if strings.Contains(joined, "|") {
			t.Fatalf("diff lines contain separator pipe: %#v", plain)
		}
		if !strings.Contains(joined, "1") || !strings.Contains(joined, "old") {
			t.Fatalf("diff missing context line number/content: %#v", plain)
		}
		if !strings.Contains(joined, "removed") || !strings.Contains(joined, "added") {
			t.Fatalf("diff missing changed content: %#v", plain)
		}
	})

	t.Run("unified diff aligns content column", func(t *testing.T) {
		lines := toolArtifactLines(tool.UnifiedDiffArtifact{Path: "main.go", Diff: "--- a/main.go\n+++ b/main.go\n@@ -9,2 +9,2 @@\n old\n-removed\n+added"}, DefaultTheme().Box.ToolCall, 80)

		plain := plainLines(lines)
		contents := []string{"---", "+++", "@@", " old", "-removed", "+added"}
		var contentColumn int
		for i, line := range plain {
			index := strings.Index(line, contents[i])
			if index < 0 {
				t.Fatalf("line %d missing content %q: %q", i, contents[i], line)
			}
			if i == 0 {
				contentColumn = index
				continue
			}
			if index != contentColumn {
				t.Fatalf("content column mismatch on line %d\ngot:  %d\nwant: %d\nlines: %#v", i, index, contentColumn, plain)
			}
		}
	})

	t.Run("unified diff metadata does not consume line numbers", func(t *testing.T) {
		lines := toolArtifactLines(tool.UnifiedDiffArtifact{Path: "main.go", Diff: strings.Join([]string{
			"diff --git a/main.go b/main.go",
			"index 1111111..2222222 100644",
			"--- a/main.go",
			"+++ b/main.go",
			"@@ -4,2 +4,2 @@",
			" old",
			"-removed",
			"+added",
			`\ No newline at end of file`,
		}, "\n")}, DefaultTheme().Box.ToolCall, 80)

		plain := plainLines(lines)
		if len(plain) != 9 {
			t.Fatalf("line count\ngot:  %d\nwant: 9\nlines: %#v", len(plain), plain)
		}
		for _, index := range []int{0, 1, 2, 3, 4, 8} {
			if strings.TrimSpace(plain[index][:10]) != "" {
				t.Fatalf("metadata line %d has line numbers: %q", index, plain[index])
			}
		}
		if !strings.Contains(plain[5], "   4    4  old") {
			t.Fatalf("context line has wrong line numbers\ngot:  %q\nwant old/new line 4", plain[5])
		}
		if !strings.Contains(plain[6], "   5      -removed") {
			t.Fatalf("removed line has wrong line numbers\ngot:  %q\nwant old line 5 only", plain[6])
		}
		if !strings.Contains(plain[7], "        5 +added") {
			t.Fatalf("added line has wrong line numbers\ngot:  %q\nwant new line 5 only", plain[7])
		}
	})

	t.Run("file artifact background spans full width", func(t *testing.T) {
		boxStyle := DefaultTheme().Box.ToolCall
		lines := toolArtifactLines(tool.FileArtifact{Path: "main.go", Content: "one"}, boxStyle, 20)

		if len(lines) != 1 {
			t.Fatalf("line count\ngot:  %d\nwant: 1", len(lines))
		}
		if text.VisibleLen(lines[0]) != 20 {
			t.Fatalf("visible width\ngot:  %d\nwant: 20\nline: %q", text.VisibleLen(lines[0]), lines[0])
		}
		if !strings.HasSuffix(lines[0], strings.Repeat(" ", 11)+terminal.SGRReset) {
			t.Fatalf("line padding is not inside final styled segment: %q", lines[0])
		}
		if strings.HasSuffix(lines[0], terminal.SGRReset+strings.Repeat(" ", 11)) {
			t.Fatalf("line has raw unstyled padding after reset: %q", lines[0])
		}
	})

	t.Run("line number prefix is muted", func(t *testing.T) {
		boxStyle := DefaultTheme().Box.ToolCall
		lines := toolArtifactLines(tool.FileArtifact{Path: "main.go", Content: "one"}, boxStyle, 80)

		mutedStart := boxStyle.Container.Merge(boxStyle.Muted).Start()
		if mutedStart == "" {
			t.Fatalf("test requires muted style start")
		}
		if !strings.HasPrefix(lines[0], mutedStart) {
			t.Fatalf("line number prefix is not muted\ngot:  %q\nwant prefix: %q", lines[0], mutedStart)
		}
	})
}

func plainLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = text.StripANSI(line)
	}
	return out
}
