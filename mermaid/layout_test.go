package mermaid

import (
	"strings"
	"testing"
)

func TestCycleGolden(t *testing.T) {
	got, err := Render("graph LR; A --> B; B --> A", 80)
	if err != nil {
		t.Fatal(err)
	}
	want := "┌─────┐       ┌─────┐\n│     │       │     │\n│  A  │──────▶│  B  │\n│     │       │     │\n└─────┘       └─────┘\n   ▲             │\n   └─────────────┘"
	if got != want {
		t.Fatalf("unexpected cycle:\n%s", got)
	}
}
func TestBranchConnections(t *testing.T) {
	got, err := Render("graph TD; A --> B; A --> C; B --> D; C --> D", 80)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, r := range got {
		if strings.ContainsRune("▶◀▼▲", r) {
			count++
		}
	}
	if count != 4 {
		t.Fatalf("lost edges:\n%s", got)
	}
	for _, label := range []string{"A", "B", "C", "D"} {
		if strings.Count(got, label) != 1 {
			t.Fatalf("lost or duplicated node %s", label)
		}
	}
}
func TestLabelRedeclaration(t *testing.T) {
	got, err := Render("graph TD; A --> B; A[Renamed]; B[Target]", 80)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Renamed") || !strings.Contains(got, "Target") {
		t.Fatal(got)
	}
}
func TestEdgeLimit(t *testing.T) {
	if _, err := Render("graph TD;"+strings.Repeat("A --> B;", maxEdges+1), 80); err == nil {
		t.Fatal("accepted too many edges")
	}
}
func TestCanvasLimit(t *testing.T) {
	var source strings.Builder
	source.WriteString("graph LR;")
	for i := 0; i < maxNodes; i++ {
		if i > 0 {
			source.WriteString(" --> ")
		}
		source.WriteString("N" + strings.Repeat("a", i))
	}
	if _, err := Render(source.String(), 10000); err == nil {
		t.Fatal("accepted oversized canvas")
	}
}
func TestRoutingBudget(t *testing.T) {
	c := canvas{w: 20, h: 20, cells: make([]cell, 400), routeSteps: 2_000_000}
	if _, ok := c.route(box{4, 4, 7, 5}, box{4, 14, 7, 5}, false, ""); ok {
		t.Fatal("ignored routing budget")
	}
}
