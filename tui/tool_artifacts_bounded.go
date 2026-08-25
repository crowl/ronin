package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tui/internal/text"
)

func toolArtifactLinesBounded(artifact tool.Artifact, width, limit int) ([]string, bool) {
	if limit <= 0 {
		return nil, artifactHasContent(artifact)
	}
	if typed, ok := artifact.(tool.FileMetadataArtifact); ok {
		metadata := "Already in context (" + typed.FileID + ")"
		return wrappedToolArtifactTextBounded(metadata, width, limit)
	}
	content := text.ExpandTabs(workflowArtifactContent(artifact))
	if _, ok := artifact.(tool.TextArtifact); ok {
		return wrappedToolArtifactTextBounded(content, width, limit)
	}
	if _, ok := artifact.(tool.ShellStreamArtifact); ok {
		return wrappedToolArtifactTextBounded(content, width, limit)
	}
	if content == "" {
		return toolArtifactLinesBoundedValue(artifact, width, limit)
	}

	diff := false
	startLine := 1
	switch typed := artifact.(type) {
	case tool.FileRangeArtifact:
		startLine = typed.StartLine
		if startLine < 1 {
			startLine = 1
		}
	case tool.UnifiedDiffArtifact:
		diff = true
	}

	var rendered []string
	oldLine, newLine := 0, 0
	inHunk := false
	lineNumber := 0
	more := false
	forEachArtifactLine(content, func(raw string) bool {
		line := raw
		kind := toolArtifactLineBody
		var old, new int
		if diff {
			if parsedOld, parsedNew, ok := parseUnifiedDiffHunkHeader(line); ok {
				oldLine, newLine, inHunk = parsedOld, parsedNew, true
				kind = toolArtifactLineMuted
			} else {
				switch {
				case strings.HasPrefix(line, "diff --git"):
					oldLine, newLine, inHunk, kind = 0, 0, false, toolArtifactLineMuted
				case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, `\`) || !inHunk:
					kind = toolArtifactLineMuted
				case strings.HasPrefix(line, "+"):
					if newLine < 1 {
						newLine = 1
					}
					new, kind = newLine, toolArtifactLineDiffAdded
					newLine++
				case strings.HasPrefix(line, "-"):
					if oldLine < 1 {
						oldLine = 1
					}
					old, kind = oldLine, toolArtifactLineDiffRemoved
					oldLine++
				default:
					if oldLine < 1 {
						oldLine = 1
					}
					if newLine < 1 {
						newLine = 1
					}
					old, new = oldLine, newLine
					oldLine++
					newLine++
				}
			}
		} else {
			lineNumber++
			new = startLine + lineNumber - 1
		}
		complete := appendBoundedArtifactLine(&rendered, toolArtifactLine{OldLine: old, NewLine: new, Text: line, Kind: kind}, width, limit)
		if !complete {
			more = true
		}
		return complete
	})
	return rendered, more
}

func artifactHasContent(artifact tool.Artifact) bool {
	return workflowArtifactContent(artifact) != ""
}

func toolArtifactLinesBoundedValue(artifact tool.Artifact, width, limit int) ([]string, bool) {
	return wrappedToolArtifactTextBounded(fmt.Sprintf("%#v", artifact), width, limit)
}

func forEachArtifactLine(content string, visit func(string) bool) {
	start := 0
	for start <= len(content) {
		end := strings.IndexByte(content[start:], '\n')
		if end < 0 {
			visit(strings.TrimSuffix(content[start:], "\r"))
			return
		}
		end += start
		if !visit(strings.TrimSuffix(content[start:end], "\r")) {
			return
		}
		start = end + 1
		if start == len(content) {
			visit("")
			return
		}
	}
}

func appendBoundedArtifactLine(rendered *[]string, line toolArtifactLine, width, limit int) bool {
	prefix := toolArtifactLinePrefix(line, line.OldLine > 0 && line.NewLine == 0 || line.Kind == toolArtifactLineDiffAdded || line.Kind == toolArtifactLineDiffRemoved || line.Kind == toolArtifactLineMuted && line.OldLine == 0 && line.NewLine == 0)
	available := width - text.VisibleLen(prefix)
	if available < 1 {
		available = 1
	}
	return forEachBoundedWrappedLine("", line.Text, available, limit-len(*rendered), func(value string, index int) bool {
		linePrefix := prefix
		if index > 0 {
			linePrefix = strings.Repeat(" ", text.VisibleLen(prefix))
		}
		*rendered = append(*rendered, styleToolArtifactLine(linePrefix, value, line.Kind, width))
		return true
	})
}

func wrappedToolArtifactTextBounded(value string, width, limit int) ([]string, bool) {
	value = text.ExpandTabs(value)
	var rendered []string
	more := false
	forEachArtifactLine(value, func(line string) bool {
		complete := forEachBoundedWrappedLine("  ", line, width, limit-len(rendered), func(value string, _ int) bool {
			rendered = append(rendered, text.StripANSI(value))
			return true
		})
		if !complete {
			more = true
		}
		return complete
	})
	return rendered, more
}

func forEachBoundedWrappedLine(prefix, value string, width, limit int, visit func(string, int) bool) bool {
	if limit <= 0 {
		return false
	}
	start := 0
	emitted := 0
	paragraphVisit := func(line string, index int) bool {
		if emitted >= limit || !visit(line, index) {
			return false
		}
		emitted++
		return true
	}
	for start <= len(value) {
		end := strings.IndexByte(value[start:], '\n')
		if end < 0 {
			end = len(value)
		} else {
			end += start
		}
		line := strings.TrimSuffix(value[start:end], "\r")
		if !forEachBoundedWrappedParagraph(prefix, line, width, limit-emitted, paragraphVisit) {
			return false
		}
		if end == len(value) {
			return true
		}
		start = end + 1
		if start == len(value) {
			return forEachBoundedWrappedParagraph(prefix, "", width, limit-emitted, paragraphVisit)
		}
	}
	return true
}

func forEachBoundedWrappedParagraph(prefix, value string, width, limit int, visit func(string, int) bool) bool {
	available := width - text.VisibleLen(prefix)
	if available < 1 {
		available = 1
	}
	if value == "" {
		return visit(prefix, 0)
	}

	lineIndex := 0
	for len(value) > 0 {
		if lineIndex >= limit {
			return false
		}
		cut := text.CutVisible(value, available)
		if cut == len(value) {
			cut = len(value)
		}
		if cut < len(value) {
			if space := strings.LastIndex(value[:cut], " "); space > 0 {
				cut = space
			}
			if cut == 0 {
				_, size := utf8.DecodeRuneInString(value)
				cut = size
			}
		}
		part := strings.TrimRight(value[:cut], " ")
		if !visit(prefix+part, lineIndex) {
			return false
		}
		lineIndex++
		value = strings.TrimLeft(value[cut:], " ")
	}
	return true
}
