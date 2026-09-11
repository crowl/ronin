package runtime

import (
	"github.com/crowl/ronin/session"
	"strings"
	"testing"

	"github.com/crowl/ronin/llm"
)

func TestRepeatedCompactionPreservesSummaryWithoutRecursiveFacts(t *testing.T) {
	const fact = "Keep the migration rollback instructions"
	client := &fakeStructuredModelClient{raw: validCompactionSummary(strings.Repeat("background ", 100) + fact)}
	compactor := &DefaultCompactor{}
	messages, err := compactor.Compact(t.Context(), client, makeCompactionMessages(14))
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		messages = append(messages, llm.UserMessage{Text: "continue"})
		messages, err = compactor.Compact(t.Context(), client, messages)
		if err != nil {
			t.Fatal(err)
		}
	}

	prompt := client.lastRequest.Messages[0].(llm.UserMessage).Text
	if !strings.Contains(prompt, fact) {
		t.Fatal("second compaction lost the previous summary's late fact")
	}
	compacted := messages[0].(session.ContextSummary).Text
	if len(compacted) > maxCompactionFactSheetBytes {
		t.Fatalf("repeatedly compacted context length = %d, want bounded output", len(compacted))
	}
	if strings.Count(compacted, "<compacted_context>") != 1 {
		t.Fatalf("compacted context contains recursively nested contexts:\n%s", compacted)
	}
	if strings.Count(compacted, compactionFactsHeading) != 1 {
		t.Fatalf("compacted context contains recursively nested facts:\n%s", compacted)
	}
	preservedFacts := compacted[strings.Index(compacted, compactionFactsHeading):]
	if strings.Contains(preservedFacts, fact) {
		t.Fatal("new compacted context copied the previous summary into deterministic facts")
	}
}

func TestOversizedPreviousCompactionRecoversWithBoundedSummary(t *testing.T) {
	const (
		headFact = "HEAD_FACT"
		tailFact = "TAIL_FACT"
	)
	client := &fakeStructuredModelClient{raw: validCompactionSummary("goal")}
	compactor := &DefaultCompactor{}
	previous := "<compacted_context>\n" + compactedContextPreamble + "\n\n# Current Goal\n" + headFact + strings.Repeat("x", maxCompactionFactSheetBytes) + tailFact + "\n</compacted_context>"
	messages := makeCompactionMessages(14)
	messages[0] = session.ContextSummary{Text: previous}

	compacted, err := compactor.Compact(t.Context(), client, messages)
	if err != nil {
		t.Fatalf("Compact() error = %v, want oversized context recovery", err)
	}
	prompt := client.lastRequest.Messages[0].(llm.UserMessage).Text
	for _, want := range []string{headFact, tailFact, "previous compacted context truncated"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("compaction prompt missing %q", want)
		}
	}
	if len(prompt) > maxCompactionFactSheetBytes+len(compactionSummaryPromptTemplateText) {
		t.Fatalf("compaction prompt length = %d, want bounded input", len(prompt))
	}
	if strings.Contains(compacted[0].(session.ContextSummary).Text, headFact) {
		t.Fatal("recovered compacted context copied the legacy summary into deterministic facts")
	}
}

func TestExtractPreviousCompactionSummaryExcludesPreservedFacts(t *testing.T) {
	text := `<compacted_context>
This is a compacted record of prior conversation state. Treat it as authoritative unless contradicted by newer messages.

# Current Goal
Keep this summary

# Recovery
- Resume here

# Automatically Preserved Facts
Message count being compacted: 100
- 001 previous compacted context:
<compacted_context>recursive history</compacted_context>
</compacted_context>`

	got := extractPreviousCompactionSummary(text)
	if !strings.Contains(got, "Keep this summary") || !strings.Contains(got, "Resume here") {
		t.Fatalf("summary lost relevant state:\n%s", got)
	}
	if strings.Contains(got, "Automatically Preserved Facts") || strings.Contains(got, "recursive history") {
		t.Fatalf("summary retained recursively embedded facts:\n%s", got)
	}
}
