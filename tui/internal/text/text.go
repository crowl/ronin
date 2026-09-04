package text

import (
	"strings"
	"unicode"
	"unicode/utf8"

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
	text = ExpandTabs(text)
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
			if cut == 0 {
				_, cut = utf8.DecodeRuneInString(paragraph)
			}
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
	if width <= 0 {
		return ""
	}
	if VisibleLen(s) <= width {
		return s
	}
	plain := ExpandTabs(StripANSI(s))
	if width == 1 {
		return plain[:CutVisible(plain, 1)]
	}
	return plain[:CutVisible(plain, width-1)] + textEllipsis
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
		visible += runeCells(r)
	}
	return visible
}

func CutVisible(s string, width int) int {
	if width <= 0 {
		return 0
	}
	visible := 0
	inEscape, inCSI := false, false
	for i, r := range s {
		if inEscape {
			if !inCSI && r == '[' {
				inCSI = true
				continue
			}
			if inCSI {
				if r >= '@' && r <= '~' {
					inEscape = false
					inCSI = false
				}
				continue
			}
			inEscape = false
			continue
		}
		if r == terminal.EscapeRune {
			inEscape = true
			continue
		}
		if r == textTab {
			visible += textTabWidth - (visible % textTabWidth)
		} else {
			visible += runeCells(r)
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

func runeCells(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == 0x200d {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a || r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe10 && r <= 0xfe19 || r >= 0xfe30 && r <= 0xfe6f || r >= 0xff00 && r <= 0xff60 || r >= 0xffe0 && r <= 0xffe6 || r >= 0x1f300 && r <= 0x1faff || r >= 0x20000 && r <= 0x3ffff) {
		return 2
	}
	return 1
}

func ExpandTabs(s string) string {
	var b strings.Builder
	column := 0
	for _, r := range s {
		if r == '\t' {
			n := textTabWidth - column%textTabWidth
			b.WriteString(strings.Repeat(" ", n))
			column += n
		} else {
			b.WriteRune(r)
			if r == '\n' {
				column = 0
			} else {
				column += runeCells(r)
			}
		}
	}
	return b.String()
}
