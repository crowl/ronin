package text

import (
	"strings"

	"github.com/crowl/ronin/tui/internal/terminal"
)

const (
	textTabWidth = 4

	textTab               = '\t'
	textSquareBracketOpen = '['
	textAt                = '@'
	textTilde             = '~'

	textEllipsis = "…"
	textSpace    = " "
)

func Wrap(prefix string, text string, width int) []string {
	if width <= 0 {
		return []string{prefix + text}
	}
	available := max(width-VisibleLen(prefix), 1)
	if text == "" {
		return []string{prefix}
	}
	var lines []string
	paragraphs := SplitLines(text)
	for paragraphIndex, paragraph := range paragraphs {
		if paragraphIndex > 0 && paragraph == "" {
			lines = append(lines, prefix)
			continue
		}
		for VisibleLen(paragraph) > available {
			cut := CutVisible(paragraph, available)
			spaceCut := strings.LastIndex(paragraph[:cut], textSpace)
			if spaceCut > 0 {
				cut = spaceCut
			}
			lines = append(lines, prefix+strings.TrimRight(paragraph[:cut], textSpace))
			paragraph = strings.TrimLeft(paragraph[cut:], textSpace)
		}
		lines = append(lines, prefix+paragraph)
	}
	return lines
}

func Fill(v string, width int) string {
	visible := VisibleLen(v)
	if visible < width {
		return v + strings.Repeat(" ", width-visible)
	}
	if visible > width {
		return Truncate(v, width)
	}
	return v
}

func SplitLines(text string) []string {
	return strings.Split(strings.ReplaceAll(text, terminal.CRLF, terminal.LineFeed), terminal.LineFeed)
}

func Truncate(s string, width int) string {
	if VisibleLen(s) <= width {
		return s
	}
	if width <= 1 {
		return string([]rune(StripANSI(s))[:width])
	}
	plain := StripANSI(s)
	runes := []rune(plain)
	if len(runes) <= width {
		return plain
	}
	return string(runes[:width-1]) + textEllipsis
}

func VisibleLen(s string) int {
	visible := 0
	inTerminalEscape := false
	inCSI := false
	for _, r := range s {
		if inTerminalEscape {
			if !inCSI && r == textSquareBracketOpen {
				inCSI = true
				continue
			}
			if inCSI {
				if r >= textAt && r <= textTilde {
					inTerminalEscape = false
					inCSI = false
				}
				continue
			}
			inTerminalEscape = false
			continue
		}
		if r == terminal.EscapeRune {
			inTerminalEscape = true
			continue
		}
		if r == textTab {
			visible += textTabWidth - (visible % textTabWidth)
			continue
		}
		visible++
	}
	return visible
}

func CutVisible(s string, width int) int {
	if width <= 0 {
		return 0
	}
	visible := 0
	for i, r := range s {
		if r == terminal.EscapeRune {
			continue
		}
		if r == textTab {
			visible += textTabWidth - (visible % textTabWidth)
		} else {
			visible++
		}
		if visible > width {
			return i
		}
	}
	return len(s)
}

func StripANSI(s string) string {
	if !strings.ContainsRune(s, terminal.EscapeRune) {
		return s
	}

	var b strings.Builder
	inTerminalEscape := false
	inCSI := false
	for _, r := range s {
		if inTerminalEscape {
			if !inCSI && r == textSquareBracketOpen {
				inCSI = true
				continue
			}
			if inCSI {
				if r >= textAt && r <= textTilde {
					inTerminalEscape = false
					inCSI = false
				}
				continue
			}
			inTerminalEscape = false
			continue
		}
		if r == terminal.EscapeRune {
			inTerminalEscape = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func ExpandTabs(s string) string {
	expandedTab := strings.Repeat(" ", textTabWidth)
	return strings.ReplaceAll(s, string(textTab), expandedTab)
}
