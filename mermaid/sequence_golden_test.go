package mermaid

import "testing"

func TestSequenceGolden(t *testing.T) {
	got, err := Render("sequenceDiagram\nA->>B: Request\nB-->>A: Response", 80)
	if err != nil {
		t.Fatal(err)
	}
	want := `  ┌───┐       ┌───┐
  │ A │       │ B │
  └───┘       └───┘
    ┆           ┆
    ┆           ┆
    ┆  Request  ┆
    ┆──────────▶┆
    ┆           ┆
    ┆ Response  ┆
    ┆◀┄┄┄┄┄┄┄┄┄┄┆
    ┆           ┆
  ┌───┐       ┌───┐
  │ A │       │ B │
  └───┘       └───┘`
	if got != want {
		t.Fatalf("sequence golden mismatch:\n%s", got)
	}
}

func TestSequenceNestedGolden(t *testing.T) {
	got, err := Render("sequenceDiagram\nparticipant A\nalt yes\nopt cached\nA->>A: hit\nend\nelse no\nend", 80)
	if err != nil {
		t.Fatal(err)
	}
	want := `      ┌───┐
      │ A │
      └───┘
        ┆
        ┆
┌────────────────────────┐
│ alt yes                │
│       ┆                │
│ ┌────────────────────┐ │
│ │ opt cached         │ │
│ │     ┆              │ │
│ │     ┆ hit          │ │
│ │     ┆────┐         │ │
│ │     ┆    │         │ │
│ │     ┆◀───┘         │ │
│ │     ┆              │ │
│ └────────────────────┘ │
│       ┆                │
├┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┤
│ else no                │
│       ┆                │
└────────────────────────┘
        ┆
      ┌───┐
      │ A │
      └───┘`
	if got != want {
		t.Fatalf("nested golden mismatch:\n%s", got)
	}
}
