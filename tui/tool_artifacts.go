package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tui/internal/text"
)

type toolArtifactLineKind int

const (
	toolArtifactLineBody toolArtifactLineKind = iota
	toolArtifactLineMuted
	toolArtifactLineDiffAdded
	toolArtifactLineDiffRemoved
)

type toolArtifactLine struct {
	OldLine int
	NewLine int
	Text    string
	Kind    toolArtifactLineKind
}

func toolArtifactLines(artifact tool.Artifact, width int) []string {
	switch typedArtifact := artifact.(type) {
	case tool.TextArtifact:
		return wrappedToolArtifactText(typedArtifact.Text, width)
	case tool.ShellStreamArtifact:
		return wrappedToolArtifactText(typedArtifact.Content, width)
	case tool.FileArtifact:
		return renderToolArtifactLines(numberedToolArtifactLines(typedArtifact.Content, 1), width, false)
	case tool.FileRangeArtifact:
		startLine := max(typedArtifact.StartLine, 1)
		return renderToolArtifactLines(numberedToolArtifactLines(typedArtifact.Content, startLine), width, false)
	case tool.FileMetadataArtifact:
		return wrappedToolArtifactText(fmt.Sprintf("Already in context (%s)", typedArtifact.FileID), width)
	case tool.UnifiedDiffArtifact:
		return renderToolArtifactLines(diffToolArtifactLines(typedArtifact.Diff), width, true)
	default:
		return wrappedToolArtifactText(fmt.Sprintf("%#v", artifact), width)
	}
}

func wrappedToolArtifactText(value string, width int) []string {
	var lines []string
	for _, line := range text.Wrap("  ", text.ExpandTabs(value), width) {
		lines = append(lines, mutedStyle.apply(text.StripANSI(line)))
	}
	return lines
}

func numberedToolArtifactLines(content string, startLine int) []toolArtifactLine {
	if startLine < 1 {
		startLine = 1
	}
	contentLines := text.SplitLines(text.ExpandTabs(content))
	lines := make([]toolArtifactLine, 0, len(contentLines))
	for i, line := range contentLines {
		lines = append(lines, toolArtifactLine{
			NewLine: startLine + i,
			Text:    line,
			Kind:    toolArtifactLineBody,
		})
	}
	return lines
}

var unifiedDiffHunkHeaderPattern = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

func diffToolArtifactLines(content string) []toolArtifactLine {
	var oldLine int
	var newLine int
	inHunk := false
	lines := text.SplitLines(text.ExpandTabs(content))
	out := make([]toolArtifactLine, 0, len(lines))
	for _, line := range lines {
		if parsedOld, parsedNew, ok := parseUnifiedDiffHunkHeader(line); ok {
			oldLine = parsedOld
			newLine = parsedNew
			inHunk = true
			out = append(out, toolArtifactLine{Text: line, Kind: toolArtifactLineMuted})
			continue
		}

		switch {
		case strings.HasPrefix(line, "diff --git"):
			oldLine = 0
			newLine = 0
			inHunk = false
			out = append(out, toolArtifactLine{Text: line, Kind: toolArtifactLineMuted})
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			out = append(out, toolArtifactLine{Text: line, Kind: toolArtifactLineMuted})
		case strings.HasPrefix(line, `\`):
			out = append(out, toolArtifactLine{Text: line, Kind: toolArtifactLineMuted})
		case !inHunk:
			out = append(out, toolArtifactLine{Text: line, Kind: toolArtifactLineMuted})
		case strings.HasPrefix(line, "+"):
			if newLine < 1 {
				newLine = 1
			}
			out = append(out, toolArtifactLine{NewLine: newLine, Text: line, Kind: toolArtifactLineDiffAdded})
			newLine++
		case strings.HasPrefix(line, "-"):
			if oldLine < 1 {
				oldLine = 1
			}
			out = append(out, toolArtifactLine{OldLine: oldLine, Text: line, Kind: toolArtifactLineDiffRemoved})
			oldLine++
		default:
			if oldLine < 1 {
				oldLine = 1
			}
			if newLine < 1 {
				newLine = 1
			}
			out = append(out, toolArtifactLine{OldLine: oldLine, NewLine: newLine, Text: line, Kind: toolArtifactLineBody})
			oldLine++
			newLine++
		}
	}
	return out
}

func parseUnifiedDiffHunkHeader(line string) (int, int, bool) {
	matches := unifiedDiffHunkHeaderPattern.FindStringSubmatch(line)
	if len(matches) != 3 {
		return 0, 0, false
	}
	oldLine, err := strconv.Atoi(matches[1])
	if err != nil || oldLine < 1 {
		oldLine = 1
	}
	newLine, err := strconv.Atoi(matches[2])
	if err != nil || newLine < 1 {
		newLine = 1
	}
	return oldLine, newLine, true
}

func renderToolArtifactLines(lines []toolArtifactLine, width int, diff bool) []string {
	var rendered []string
	for _, line := range lines {
		prefix := toolArtifactLinePrefix(line, diff)
		available := max(width-text.VisibleLen(prefix), 1)

		wrapped := text.Wrap("", line.Text, available)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}

		for i, wrappedLine := range wrapped {
			linePrefix := prefix
			if i > 0 {
				linePrefix = strings.Repeat(" ", text.VisibleLen(prefix))
			}
			rendered = append(rendered, styleToolArtifactLine(linePrefix, wrappedLine, line.Kind, width))
		}
	}
	return rendered
}

func toolArtifactLinePrefix(line toolArtifactLine, diff bool) string {
	oldLine := "    "
	if line.OldLine > 0 {
		oldLine = fmt.Sprintf("%4d", line.OldLine)
	}
	newLine := "    "
	if line.NewLine > 0 {
		newLine = fmt.Sprintf("%4d", line.NewLine)
	}

	if diff || line.OldLine > 0 {
		return "  " + oldLine + " " + newLine + " "
	}
	if line.NewLine > 0 {
		return "  " + newLine + " "
	}
	return "  "
}

func styleToolArtifactLine(prefix string, value string, kind toolArtifactLineKind, _ int) string {
	line := prefix + value
	switch kind {
	case toolArtifactLineDiffAdded:
		return addedStyle.apply(line)
	case toolArtifactLineDiffRemoved:
		return removedStyle.apply(line)
	default:
		return mutedStyle.apply(line)
	}
}
