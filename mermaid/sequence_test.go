package mermaid

import (
	"fmt"
	"strings"
	"testing"
)

func TestSequenceRender(t *testing.T) {
	for _, source := range []string{
		"sequenceDiagram\nA->>B: Request\nB-->>A: Response",
		"sequenceDiagram\nparticipant A as Client\nparticipant B as Backend\nA->>B: Request\nB->>B: Work\nB-->>A: Result",
		"sequenceDiagram\nA->>A: Self",
		"sequenceDiagram\nparticipant A\nparticipant B\nNote left of A: Left\nNote right of B: Right\nNote over A: One\nNote over A,B: Both",
		"sequenceDiagram\nparticipant A\nparticipant B\nalt available\nloop retries\nA->>B: Try\nopt cached\nB-->>A: Cached\nend\nend\nelse unavailable\nB-->>A: Error\nend",
		"%% comment\nsequenceDiagram\nparticipant A as 客户\nA->>B: Café\nNote over A,B: 你好",
		"sequenceDiagram\nparticipant A",
		"sequenceDiagram\nA->>B: \nalt test\nelse\nend",
	} {
		t.Run(source, func(t *testing.T) {
			got, err := Render(source, 200)
			if err != nil {
				t.Fatal(err)
			}
			again, err := Render(source, 200)
			if err != nil || again != got {
				t.Fatal("not deterministic")
			}
			for _, line := range strings.Split(got, "\n") {
				w, err := labelWidth(line)
				if err != nil || w > 200 {
					t.Fatalf("invalid row %q: %v", line, err)
				}
			}
			t.Log("\n" + got)
		})
	}
}
func TestSequenceReject(t *testing.T) {
	for _, body := range []string{
		"", "actor A", "A->B: wrong arrow", "A->>+B: activation", "activate A", "participant", "participant A as", "participant A as ", "A->>B", "end", "else nope", "alt test\nA->>B: open", "opt test\nelse nope\nend", "loop test\nend extra", "par test", "Note over A,B,C: nope", "Note left of A,B: nope", "Note over : nope", "Note over A:", "A->>B: <br>", "A->>B: \x1b[31m", "A->>B: \u200d", "participant A; participant B",
	} {
		t.Run(body, func(t *testing.T) {
			if got, err := Render("sequenceDiagram\n"+body, 200); err == nil || got != "" {
				t.Fatalf("accepted %q: %s", body, got)
			}
		})
	}
}
func TestSequenceLimits(t *testing.T) {
	participants := "sequenceDiagram\n"
	for i := 0; i < maxParticipants+1; i++ {
		participants += fmt.Sprintf("participant P%d\n", i)
	}
	for _, source := range []string{
		participants,
		"sequenceDiagram\n" + strings.Repeat("A->>B: hi\n", maxSequenceEvents+1),
		"sequenceDiagram\n" + strings.Repeat("alt test\n", maxSequenceDepth+1) + "A->>B: hi\n" + strings.Repeat("end\n", maxSequenceDepth+1),
		"sequenceDiagram\nA->>B: " + strings.Repeat("x", maxLabel+1),
		"sequenceDiagram\n" + strings.Repeat("A->>A: hi\n", 110),
	} {
		if _, err := Render(source, 10000); err == nil {
			t.Fatal("accepted oversized sequence")
		}
	}
	if _, err := Render("sequenceDiagram\nA->>B: hi", 5); err == nil {
		t.Fatal("accepted too narrow viewport")
	}
}
func TestSequenceParser(t *testing.T) {
	s, err := parseSequence("sequenceDiagram\nA->>B: URL: value\nparticipant B as Server\nB-->>A: reply\nalt condition\nopt optional\nend\nelse other\nend")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.participants) != 2 || s.participants[0].id != "A" || s.participants[1].label != "Server" || s.depth != 2 {
		t.Fatalf("unexpected sequence: %+v", s)
	}
	if s.events[0].label != "URL: value" || !s.events[1].dashed || s.events[1].from != 1 || s.events[1].to != 0 {
		t.Fatal("lost message semantics")
	}
}
func FuzzSequenceRender(f *testing.F) {
	for _, body := range []string{"A->>B: hello", "participant A\nNote left of A: note", "A->>A: self", "alt condition\nA->>B: hi\nelse other\nB-->>A: bye\nend"} {
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body string) {
		got, err := Render("sequenceDiagram\n"+body, 512)
		if err != nil {
			if got != "" {
				t.Fatal("partial result")
			}
			return
		}
		for _, row := range strings.Split(got, "\n") {
			w, err := labelWidth(row)
			if err != nil || w > 512 {
				t.Fatalf("invalid output %q", row)
			}
		}
	})
}
