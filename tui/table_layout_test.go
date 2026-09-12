package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/crowl/ronin/tui/internal/text"
)

func TestTableWrappedGolden(t *testing.T) {
	input := "A | B\n:---: | ---:\nabc def | 12"
	got := text.StripANSI(strings.Join(markdownLines(input, 13, defaultTextStyles()), "\n"))
	want := `┌─────┬─────┐
│  A  │   B │
├─────┼─────┤
│ abc │  12 │
│ def │     │
└─────┴─────┘`
	if got != want {
		t.Fatalf("unexpected wrapped table:\n%s", got)
	}
}
func TestTableStreamingAndResize(t *testing.T) {
	header := "Name | Value\n--- | ---"
	for _, input := range []string{header, header + "\nAlice |", header + "\nAlice | 42"} {
		got := strings.Join(markdownLines(input, 40, defaultTextStyles()), "\n")
		if !strings.Contains(got, "┌") {
			t.Fatalf("valid streaming table not rendered: %q", got)
		}
	}
	input := header + "\nAlice | 42"
	narrow := text.StripANSI(strings.Join(markdownLines(input, 12, defaultTextStyles()), "\n"))
	wide := text.StripANSI(strings.Join(markdownLines(input, 40, defaultTextStyles()), "\n"))
	if !strings.Contains(narrow, "Name: Alice") || !strings.Contains(wide, "┌") {
		t.Fatal("resize did not switch layout")
	}
}
func TestTableSurroundingText(t *testing.T) {
	input := "before\nA | B\n--- | ---\n1 | 2\n\nafter"
	got := text.StripANSI(strings.Join(markdownLines(input, 30, defaultTextStyles()), "\n"))
	if !strings.HasPrefix(got, "before\n┌") || !strings.HasSuffix(got, "\n\nafter") {
		t.Fatalf("lost surrounding text: %q", got)
	}
}
func TestTableDoesNotMutate(t *testing.T) {
	table, _, ok := parseTable([]string{"A | B", "--- | ---", "**bold** | value"}, 0)
	if !ok {
		t.Fatal("parse failed")
	}
	first := tableLines(table, 40, defaultTextStyles())
	second := tableLines(table, 40, defaultTextStyles())
	if !reflect.DeepEqual(first, second) || table.rows[1][0] != "**bold**" {
		t.Fatal("render changed source cells")
	}
}
func TestTableControls(t *testing.T) {
	table, _, ok := parseTable([]string{"A | B", "--- | ---", "\x1b[31mred | x"}, 0)
	if !ok {
		t.Fatal("parse failed")
	}
	got := strings.Join(tableLines(table, 40, defaultTextStyles()), "\n")
	if strings.Contains(got, "\x1b[31m") {
		t.Fatal("source ANSI escaped into output")
	}
}
