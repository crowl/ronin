package runtime

import (
	"github.com/crowl/ronin/llm"
	"strings"
	"testing"
)

func TestRepeatedCompactionPreservesSummary(t *testing.T) {
	const fact = "Keep the migration rollback instructions"
	client := &fakeStructuredModelClient{raw: validCompactionSummary(strings.Repeat("background ", 100) + fact)}
	compactor, err := NewDefaultCompactor(DefaultCompactorConfig{ModelClient: client})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := compactor.Compact(t.Context(), makeCompactionMessages(14))
	if err != nil {
		t.Fatal(err)
	}
	messages = append(messages, llm.UserMessage{Text: "continue"})
	_, err = compactor.Compact(t.Context(), messages)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.lastRequest.Messages[0].(llm.UserMessage).Text, fact) {
		t.Fatal("second compaction lost the previous summary's late fact")
	}
}

func TestOversizedPreviousCompactionFailsExplicitly(t *testing.T) {
	client := &fakeStructuredModelClient{raw: validCompactionSummary("goal")}
	compactor, err := NewDefaultCompactor(DefaultCompactorConfig{ModelClient: client})
	if err != nil {
		t.Fatal(err)
	}
	messages := makeCompactionMessages(14)
	messages[0] = llm.UserMessage{Text: "<compacted_context>" + strings.Repeat("x", maxCompactionFactSheetBytes) + "</compacted_context>"}
	_, err = compactor.Compact(t.Context(), messages)
	if err == nil || !strings.Contains(err.Error(), "previous compacted context exceeds") {
		t.Fatalf("error = %v", err)
	}
}
