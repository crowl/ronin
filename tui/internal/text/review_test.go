package text

import "testing"

func TestNarrowTabsAndCells(t *testing.T) {
	for width := 1; width <= 3; width++ {
		lines := Wrap("", "\tX", width)
		if len(lines) > 10 || len(lines) == 0 {
			t.Fatalf("wrap=%v", lines)
		}
	}
	if VisibleLen("界e\u0301") != 3 {
		t.Fatal("incorrect cell width")
	}
	if CutVisible("\x1b[31mhello\x1b[0m", 2) != 7 {
		t.Fatal("cut counted ANSI bytes")
	}
	if Truncate("界界", 3) != "界…" {
		t.Fatal("truncation not cell aware")
	}
	if Truncate("text", -1) != "" {
		t.Fatal("negative width")
	}
}
