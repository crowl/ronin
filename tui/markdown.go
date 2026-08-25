package tui

import (
	"strings"
	"unicode"

	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/tui/internal/text"
)

func markdownLines(input string, width int, styles textStyles) []string {
	inputLines := text.SplitLines(input)

	var lines []string
	inCodeBlock := false
	for _, raw := range inputLines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			if len(lines) > 0 && lines[len(lines)-1] != "" {
				lines = append(lines, "")
			}
			continue
		}

		if isCodeFence(trimmed) {
			lines = append(lines, styledWrap("", raw, width, styles.muted, styles.normal)...)
			inCodeBlock = !inCodeBlock
			continue
		}

		if inCodeBlock {
			lines = append(lines, styledWrap("", raw, width, styles.code, styles.normal)...)
			continue
		}

		if isHorizontalRule(trimmed) {
			// ignore hrules
			continue
		}

		if isHeading(raw) {
			line := styleInline(raw, styles)
			lines = append(lines, styledWrap("", line, width, styles.strong, styles.normal)...)
			continue
		}

		if quote, ok := blockquoteText(raw); ok {
			line := styleInline(quote, styles)
			lines = append(lines, styledWrap("│ ", line, width, styles.emphasis, styles.normal)...)
			continue
		}

		if marker, body, ok := listItem(raw); ok {
			line := marker + styleInline(body, styles)
			lines = append(lines, styledWrap("", line, width, styles.normal, style{})...)
			continue
		}

		line := styleInline(raw, styles)
		lines = append(lines, styledWrap("", line, width, styles.normal, style{})...)
	}

	start := 0
	for start < len(lines) && strings.TrimSpace(text.StripANSI(lines[start])) == "" {
		start++
	}
	end := len(lines)
	for end > start && strings.TrimSpace(text.StripANSI(lines[end-1])) == "" {
		end--
	}
	if start == end {
		return []string{styles.normal.apply("")}
	}

	return lines[start:end]
}

func isCodeFence(trimmed string) bool {
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

func isHeading(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	return level > 0 && level <= 6 && level < len(trimmed) && trimmed[level] == ' '
}

func isHorizontalRule(trimmed string) bool {
	if len(trimmed) < 3 {
		return false
	}
	var marker rune
	count := 0
	for _, r := range trimmed {
		if r == ' ' || r == '\t' {
			continue
		}
		if r != '-' && r != '*' && r != '_' {
			return false
		}
		if marker == 0 {
			marker = r
		}
		if r != marker {
			return false
		}
		count++
	}
	return count >= 3
}

func blockquoteText(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, ">") {
		return "", false
	}
	quote := strings.TrimPrefix(trimmed, ">")
	quote = strings.TrimPrefix(quote, " ")
	return quote, true
}

func listItem(line string) (string, string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) >= 2 && (trimmed[0] == '-' || trimmed[0] == '*' || trimmed[0] == '+') && trimmed[1] == ' ' {
		return string(trimmed[:2]), trimmed[2:], true
	}

	index := 0
	for index < len(trimmed) {
		r := rune(trimmed[index])
		if !unicode.IsDigit(r) {
			break
		}
		index++
	}
	if index == 0 || index+1 >= len(trimmed) {
		return "", "", false
	}
	if trimmed[index] != '.' || trimmed[index+1] != ' ' {
		return "", "", false
	}
	return trimmed[:index+2], trimmed[index+2:], true
}

func styleInline(input string, styles textStyles) string {
	if strings.Contains(input, "](") {
		input = styleLinks(input)
	}
	if strings.Contains(input, "`") {
		input = styleDelimited(input, "`", styles.code, styles.normal)
	}
	if strings.Contains(input, "**") {
		input = styleDelimited(input, "**", styles.strong, styles.normal)
	}
	if strings.Contains(input, "*") {
		input = styleDelimited(input, "*", styles.emphasis, styles.normal)
	}
	return input
}

func styleLinks(input string) string {
	start := strings.Index(input, "[")
	if start == -1 {
		return input
	}

	var b strings.Builder
	b.Grow(len(input))
	for start != -1 {
		endLabel := strings.Index(input[start+1:], "](")
		if endLabel == -1 {
			b.WriteString(input)
			return b.String()
		}
		endLabel += start + 1
		urlStart := endLabel + 2
		urlEnd := strings.Index(input[urlStart:], ")")
		if urlEnd == -1 {
			b.WriteString(input)
			return b.String()
		}
		urlEnd += urlStart

		label := input[start+1 : endLabel]
		url := input[urlStart:urlEnd]
		b.WriteString(input[:start])
		if label == url {
			b.WriteString(label)
		} else {
			b.WriteString(label)
			b.WriteString(" (")
			b.WriteString(url)
			b.WriteString(")")
		}
		input = input[urlEnd+1:]
		start = strings.Index(input, "[")
	}
	b.WriteString(input)
	return b.String()
}

func styleDelimited(input string, marker string, valueStyle style, resume style) string {
	if marker == "" || valueStyle.empty() {
		return input
	}
	start := strings.Index(input, marker)
	if start == -1 {
		return input
	}

	styleStart := valueStyle.start()
	resumeStart := resume.start()
	var b strings.Builder
	b.Grow(len(input) + len(styleStart) + len(terminal.SGRReset))
	for start != -1 {
		end := strings.Index(input[start+len(marker):], marker)
		if end == -1 {
			b.WriteString(input)
			return b.String()
		}
		end += start + len(marker)
		b.WriteString(input[:start])
		b.WriteString(applyInlineStyleStart(input[start+len(marker):end], styleStart, resumeStart))
		input = input[end+len(marker):]
		start = strings.Index(input, marker)
	}
	b.WriteString(input)
	return b.String()
}

func styledWrap(prefix string, value string, width int, valueStyle style, resume style) []string {
	if valueStyle.empty() {
		return text.Wrap(prefix, value, width)
	}
	lines := text.Wrap(prefix, value, width)
	styleStart := valueStyle.start()
	resumeStart := resume.start()
	for i := range lines {
		lines[i] = applyInlineStyleStart(lines[i], styleStart, resumeStart)
	}
	return lines
}

func applyInlineStyle(v string, valueStyle style, resume style) string {
	if valueStyle.empty() {
		return v
	}
	return applyInlineStyleStart(v, valueStyle.start(), resume.start())
}

func applyInlineStyleStart(v string, styleStart string, resumeStart string) string {
	if styleStart == "" {
		return v
	}
	if resumeStart == "" {
		return styleStart + v + terminal.SGRReset
	}
	return styleStart + v + resumeStart
}
