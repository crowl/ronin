package mermaid

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	for _, source := range []string{
		"graph TD\nA --> B",
		"flowchart LR; A[Start] --> B(Work) --> C{Ready?}",
		"graph TD\nA --> B\nA --> C\nB --> D\nC --> D",
		"graph LR; A --> B; B --> A",
		"graph TD; A --> A",
		"graph TD; A; B; C",
		"graph TD; A -->|yes| B",
		"%% comment\nflowchart TB\nA[\"你好\"] --> B[Café] %% end",
	} {
		t.Run(source, func(t *testing.T) {
			got, err := Render(source, 200)
			if err != nil {
				t.Fatal(err)
			}
			again, err := Render(source, 200)
			if err != nil || again != got {
				t.Fatal("non-deterministic render")
			}
			t.Log("\n" + got)
			for _, line := range strings.Split(got, "\n") {
				w, err := labelWidth(line)
				if err != nil || w > 200 {
					t.Fatalf("invalid row %q: %v", line, err)
				}
			}
		})
	}
}
func TestGolden(t *testing.T) {
	got, err := Render("graph TD; A --> B", 80)
	if err != nil {
		t.Fatal(err)
	}
	want := "┌─────┐\n│     │\n│  A  │\n│     │\n└─────┘\n   │\n   │\n   │\n   │\n   ▼\n┌─────┐\n│     │\n│  B  │\n│     │\n└─────┘"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
func TestReject(t *testing.T) {
	for _, source := range []string{"", "sequenceDiagram", "graph RL; A --> B", "graph TD", "graph TD; subgraph X", "graph TD; A -.-> B", "graph TD; A & B --> C", "graph TD; A[broken", "graph TD; A((circle))", "graph TD; A[<br>]", "graph TD; A[\"\x1b[31m\"]", "graph TD; A[\"\u200d\"]", "graph TD; A[\"\u0301x\"]", "graph TD; A -->|yes", strings.Repeat("x", maxInput+1)} {
		if got, err := Render(source, 80); err == nil || got != "" {
			t.Errorf("accepted %q: %q %v", source, got, err)
		}
	}
	for _, width := range []int{-1, 0, 1, 6} {
		if _, err := Render("graph TD; A", width); err == nil {
			t.Errorf("accepted width %d", width)
		}
	}
	var source strings.Builder
	source.WriteString("graph TD;")
	for i := 0; i < maxNodes+1; i++ {
		source.WriteString("N" + strings.Repeat("a", i) + ";")
	}
	if _, err := Render(source.String(), 500); err == nil {
		t.Fatal("accepted too many nodes")
	}
}
func FuzzRender(f *testing.F) {
	for _, s := range []string{"graph TD; A --> B", "flowchart LR; A{yes?} -->|ok| B", "graph TD; A --> A", "graph TD; A[\"你好\"]"} {
		f.Add(s, 80)
	}
	f.Fuzz(func(t *testing.T, source string, width int) {
		got, err := Render(source, width)
		if err != nil {
			if got != "" {
				t.Fatal("partial output on error")
			}
			return
		}
		if got == "" {
			t.Fatal("empty successful render")
		}
		for _, line := range strings.Split(got, "\n") {
			w, err := labelWidth(line)
			if err != nil || w > width {
				t.Fatalf("invalid output row: %q", line)
			}
		}
	})
}
