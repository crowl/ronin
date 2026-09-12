package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/tui/internal/text"
)

const (
	maxTableColumns   = 32
	maxTableRows      = 256
	maxTableBytes     = 64 * 1024
	minTableCellWidth = 3
)

type markdownTable struct {
	rows  [][]string
	align []byte
}

// tableRow splits unescaped pipes. As in GFM, pipes inside code spans must
// also be escaped. Only pipe escapes are consumed here; inline styling remains
// the responsibility of the existing Markdown renderer.
func tableRow(line string) ([]string, bool) {
	if len(line) > maxTableBytes {
		return nil, false
	}
	line = strings.TrimSpace(line)
	if line == "" || isCodeFence(line) {
		return nil, false
	}
	var cells []string
	var cell strings.Builder
	pipes := 0
	leading, trailing := false, false
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) {
			if line[i+1] == '|' {
				cell.WriteByte('|')
				i++
				trailing = false
				continue
			}
			// Preserve paired backslashes so an even run does not escape a pipe.
			if line[i+1] == '\\' {
				cell.WriteString("\\\\")
				i++
				trailing = false
				continue
			}
		}
		if line[i] == '|' {
			if i == 0 {
				leading = true
			}
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
			pipes++
			if pipes > maxTableColumns+1 {
				return nil, false
			}
			trailing = true
		} else {
			cell.WriteByte(line[i])
			trailing = false
		}
	}
	cells = append(cells, strings.TrimSpace(cell.String()))
	if leading {
		cells = cells[1:]
	}
	if trailing {
		cells = cells[:len(cells)-1]
	}
	return cells, pipes > 0 && len(cells) > 0
}

func parseTable(lines []string, start int) (markdownTable, int, bool) {
	if start+1 >= len(lines) {
		return markdownTable{}, start, false
	}
	header, ok := tableRow(lines[start])
	if !ok || len(header) > maxTableColumns {
		return markdownTable{}, start, false
	}
	delimiter, ok := tableRow(lines[start+1])
	if !ok || len(delimiter) != len(header) {
		return markdownTable{}, start, false
	}
	table := markdownTable{rows: [][]string{header}, align: make([]byte, len(header))}
	for i, d := range delimiter {
		left, right := strings.HasPrefix(d, ":"), strings.HasSuffix(d, ":")
		d = strings.TrimPrefix(d, ":")
		d = strings.TrimSuffix(d, ":")
		if len(d) < 3 || strings.Trim(d, "-") != "" {
			return markdownTable{}, start, false
		}
		if left && right {
			table.align[i] = 'c'
		} else if right {
			table.align[i] = 'r'
		}
	}
	size := len(lines[start]) + len(lines[start+1])
	end := start + 1
	for end+1 < len(lines) {
		if len(lines[end+1]) > maxTableBytes {
			return markdownTable{}, start, false
		}
		row, ok := tableRow(lines[end+1])
		if !ok {
			if strings.Contains(lines[end+1], "|") && !isCodeFence(strings.TrimSpace(lines[end+1])) {
				return markdownTable{}, start, false
			}
			break
		}
		if len(row) > len(header) || len(table.rows) >= maxTableRows {
			return markdownTable{}, start, false
		}
		size += len(lines[end+1])
		if size > maxTableBytes {
			return markdownTable{}, start, false
		}
		for len(row) < len(header) {
			row = append(row, "")
		}
		table.rows = append(table.rows, row)
		end++
	}
	if size > maxTableBytes {
		return markdownTable{}, start, false
	}
	return table, end, true
}

func tableLines(table markdownTable, width int, styles textStyles) []string {
	if width <= 0 {
		return nil
	}
	widths := make([]int, len(table.align))
	rows := make([][]string, len(table.rows))
	for i, row := range table.rows {
		rows[i] = append([]string(nil), row...)
	}
	table.rows = rows
	for r, row := range table.rows {
		for col, value := range row {
			value = strings.Map(func(r rune) rune {
				if unicode.IsControl(r) && r != '\t' {
					return '\uFFFD'
				}
				return r
			}, value)
			value = text.ExpandTabs(styleInline(value, styles))
			table.rows[r][col] = value
			widths[col] = max(widths[col], text.VisibleLen(value), minTableCellWidth)
		}
	}
	budget := width - 3*len(widths) - 1
	if budget < minTableCellWidth*len(widths) {
		return stackedTableLines(table, width, styles)
	}
	total := 0
	for _, w := range widths {
		total += w
	}
	for total > budget {
		widest := 0
		for i := range widths {
			if widths[i] > widths[widest] {
				widest = i
			}
		}
		widths[widest]--
		total--
	}
	border := func(left, middle, right string) string {
		segments := make([]string, len(widths))
		for i, w := range widths {
			segments[i] = strings.Repeat("─", w+2)
		}
		return applyInlineStyle(left+strings.Join(segments, middle)+right, styles.muted, styles.normal)
	}
	output := []string{border("┌", "┬", "┐")}
	for r, row := range table.rows {
		wrapped := make([][]string, len(row))
		height := 1
		for col, value := range row {
			if r == 0 {
				value = applyInlineStyle(value, styles.strong, styles.normal)
			}
			wrapped[col] = wrapTableCell(value, widths[col])
			height = max(height, len(wrapped[col]))
		}
		for line := 0; line < height; line++ {
			var b strings.Builder
			b.WriteString(applyInlineStyle("│", styles.muted, styles.normal))
			for col := range row {
				value := ""
				if line < len(wrapped[col]) {
					value = wrapped[col][line]
				}
				padding := widths[col] - text.VisibleLen(value)
				left := 0
				if table.align[col] == 'r' {
					left = padding
				} else if table.align[col] == 'c' {
					left = padding / 2
				}
				b.WriteByte(' ')
				b.WriteString(strings.Repeat(" ", left))
				b.WriteString(value)
				b.WriteString(strings.Repeat(" ", padding-left))
				b.WriteByte(' ')
				b.WriteString(applyInlineStyle("│", styles.muted, styles.normal))
			}
			output = append(output, b.String())
		}
		if r == 0 {
			output = append(output, border("├", "┼", "┤"))
		}
	}
	return append(output, border("└", "┴", "┘"))
}

func stackedTableLines(table markdownTable, width int, styles textStyles) []string {
	var output []string
	rows := table.rows[1:]
	// A header-only table still has useful information to display.
	if len(rows) == 0 {
		rows = [][]string{make([]string, len(table.align))}
	}
	for r, row := range rows {
		if r > 0 {
			output = append(output, "")
		}
		for col, value := range row {
			header := table.rows[0][col]
			if strings.TrimSpace(text.StripANSI(header)) == "" {
				header = fmt.Sprintf("Column %d", col+1)
			}
			header = applyInlineStyle(header, styles.strong, styles.normal)
			output = append(output, wrapTableCell(header+": "+value, width)...)
		}
	}
	return output
}

// Wrap first, then replay SGR state on continuation lines. Every styled segment
// ends in a reset so padding, borders, and adjacent cells cannot inherit it.
func wrapTableCell(value string, width int) []string {
	// A single terminal cell cannot display a wide rune. At this extreme width
	// retain all other text and use a visible replacement instead of overflowing.
	if width == 1 {
		var b strings.Builder
		for _, r := range text.StripANSI(value) {
			if text.VisibleLen(string(r)) > 1 {
				b.WriteByte('?')
			} else {
				b.WriteRune(r)
			}
		}
		value = b.String()
	}
	lines := text.Wrap("", value, width)
	state := ""
	for i, line := range lines {
		prefix := state
		for at := 0; at < len(line); {
			start := strings.Index(line[at:], "\x1b[")
			if start < 0 {
				break
			}
			start += at
			end := start + 2
			for end < len(line) && !(line[end] >= '@' && line[end] <= '~') {
				end++
			}
			if end == len(line) {
				break
			}
			if line[end] == 'm' {
				sequence := line[start : end+1]
				if sequence == terminal.SGRReset || sequence == "\x1b[m" {
					state = ""
				} else {
					state += sequence
				}
			}
			at = end + 1
		}
		lines[i] = prefix + line
		if strings.Contains(lines[i], "\x1b[") {
			lines[i] += terminal.SGRReset
		}
	}
	return lines
}
