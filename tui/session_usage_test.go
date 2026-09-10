package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
)

func TestStatusBarSessionTotalsAndContext(t *testing.T) {
	line := statusBar{
		UseCWDStatus: true,
		Model:        llm.Model{ContextWindow: 1000},
		ContextUsage: llm.Usage{InputTokens: 100, OutputTokens: 20},
		SessionUsage: llm.Usage{InputTokens: 5000, OutputTokens: 2000, CachedTokens: 3000, Cost: llm.Cost{Available: true, Total: 1.25}},
	}.Lines(120)[0]
	for _, want := range []string{"↑5.0K ↓2.0K R3.0K", "12.0%/1.0K", "$1.25"} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing %q in %q", want, line)
		}
	}
}

func TestSessionUsageSnapshotAccumulates(t *testing.T) {
	model := &appModel{sessionUsage: llm.Usage{InputTokens: 100, OutputTokens: 10, CachedTokens: 40, Cost: llm.Cost{Available: true, Total: 1}}}
	for range 2 {
		_, err := model.handleConversationEvent(runtime.AssistantMessageEnded{Message: llm.AssistantMessage{Usage: llm.Usage{InputTokens: 200, OutputTokens: 20, CachedTokens: 80, Cost: llm.Cost{Available: true, Total: .5}}}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
	}
	got := model.sessionUsage
	if got.InputTokens != 500 || got.OutputTokens != 50 || got.CachedTokens != 200 || got.Cost.Total != 2 {
		t.Fatalf("snapshot = %+v", got)
	}
	if model.usage.InputTokens != 200 || model.usage.OutputTokens != 20 {
		t.Fatalf("context = %+v", model.usage)
	}
}
