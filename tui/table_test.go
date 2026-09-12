package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/crowl/ronin/tui/internal/text"
)

func TestMarkdownTableGolden(t *testing.T) {
	input := "| Name | Count |\n| :--- | ---: |\n| Alice | 12 |\n| Bob | 3 |"
	want := `┌───────┬───────┐
│ Name  │ Count │
├───────┼───────┤
│ Alice │    12 │
│ Bob   │     3 │
└───────┴───────┘`
	got := text.StripANSI(strings.Join(markdownLines(input, 80, defaultTextStyles()), "\n"))
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
func TestMarkdownTableStacked(t *testing.T) {
	input := "| Name | |\n| --- | --- |\n| Alice | 12 |\n| Bob | 3 |"
	got := text.StripANSI(strings.Join(markdownLines(input, 12, defaultTextStyles()), "\n"))
	want := "Name: Alice\nColumn 2: 12\n\nName: Bob\nColumn 2: 3"
	if got != want {
		t.Fatalf("got %q; want %q", got, want)
	}
}
func TestTableRow(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  []string
	}{
		{"| A | B |", []string{"A", "B"}},
		{"A | B", []string{"A", "B"}},
		{`| A\|B | C |`, []string{"A|B", "C"}},
		{"| `A\\|B` | C |", []string{"`A|B`", "C"}},
		{`| A\\| B |`, []string{`A\\`, "B"}},
		{"||B||", []string{"", "B", ""}},
	} {
		got, ok := tableRow(tt.input)
		if !ok || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q: got %#v want %#v", tt.input, got, tt.want)
		}
	}
}
func TestMarkdownTableWrapping(t *testing.T) {
	input := "| Name | Description |\n| :---: | --- |\n| **你好** | *a long explanation* with `code` |\n| Café | [docs](url) |"
	for _, width := range []int{1, 2, 8, 12, 13, 18, 30, 80} {
		t.Run(string(rune('A'+width)), func(t *testing.T) {
			lines := markdownLines(input, width, defaultTextStyles())
			for _, line := range lines {
				if text.VisibleLen(line) > width {
					t.Fatalf("width %d overflow: %q", width, line)
				}
			}
			got := text.StripANSI(strings.Join(lines, "\n"))
			if strings.Contains(got, "**") || strings.Contains(got, "[docs]") {
				t.Fatalf("unrendered inline syntax: %s", got)
			}
		})
	}
}
func TestWrapTableCellStyles(t *testing.T) {
	got := wrapTableCell("\x1b[1malpha beta gamma\x1b[0m", 5)
	want := []string{"\x1b[1malpha\x1b[0m", "\x1b[1mbeta\x1b[0m", "\x1b[1mgamma\x1b[0m\x1b[0m"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}
func TestMarkdownTableFallback(t *testing.T) {
	for _, input := range []string{
		"A | B\n-- | ---\n1 | 2",
		"A | B\n--- | --- | ---\n1 | 2",
		"A | B\n--- | ---\n1 | 2 | 3",
		"A | B", "A | B\n--- | --",
		"```\nA | B\n--- | ---\n1 | 2\n```",
	} {
		got := text.StripANSI(strings.Join(markdownLines(input, 80, defaultTextStyles()), "\n"))
		if strings.Contains(got, "┌") {
			t.Fatalf("unexpected table: %q", got)
		}
	}
}
func TestTableParsing(t *testing.T) {
	input := []string{"A | B", "--- | :---:", "| one |", "", "after"}
	table, end, ok := parseTable(input, 0)
	if !ok || end != 2 || table.rows[1][1] != "" || table.align[1] != 'c' {
		t.Fatalf("bad table: %+v %d %v", table, end, ok)
	}
	input = []string{"| Header |", "| --- |", "after"}
	if _, end, ok := parseTable(input, 0); !ok || end != 1 {
		t.Fatal("header-only table not recognized")
	}
}
func TestTableLimits(t *testing.T) {
	for _, input := range []string{
		strings.Repeat("x |", maxTableColumns+1) + "\n" + strings.Repeat("--- |", maxTableColumns+1),
		"A | B\n--- | ---\n" + strings.Repeat("1 | 2\n", maxTableRows),
		"A | B\n--- | ---\n" + strings.Repeat("x", maxTableBytes) + " | y",
	} {
		if _, _, ok := parseTable(strings.Split(input, "\n"), 0); ok {
			t.Fatal("accepted oversized table")
		}
	}
}
func FuzzMarkdownTable(f *testing.F) {
	for _, body := range []string{"one | two", "**bold** | `code`", "你好 | Café", `a\|b | c`} {
		f.Add(body, 30)
	}
	f.Fuzz(func(t *testing.T, body string, width int) {
		width = 1 + int(uint(width)%120)
		if len(body) > maxTableBytes {
			return
		}
		table, _, ok := parseTable(strings.Split("A | B\n--- | ---\n"+body, "\n"), 0)
		if !ok {
			return
		}
		for _, line := range tableLines(table, width, defaultTextStyles()) {
			if text.VisibleLen(line) > width {
				t.Fatalf("overflow at %d: %q", width, line)
			}
		}
	})
}
