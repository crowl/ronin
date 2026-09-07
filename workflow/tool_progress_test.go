package workflow

import (
	"strings"
	"testing"

	"github.com/crowl/ronin/tool"
)

func TestWorkflowToolProgressIsStatusOnly(t *testing.T) {
	for _, event := range []AgentEvent{
		AgentThinkingDelta{Text: "private"}, AgentTextDelta{Text: "private"},
		AgentToolStarted{Title: "private"}, AgentToolOutput{Artifact: tool.TextArtifact{Text: "private"}},
		AgentToolFailed{Error: "private"},
	} {
		if artifact := workflowEventArtifact(AgentEventReceived{Invocation: 1, Event: event}); artifact != nil {
			t.Fatalf("transcript emitted as tool progress: %#v", artifact)
		}
	}
	for _, tc := range []struct {
		event Event
		want  string
	}{
		{AgentStarted{Invocation: 1, Request: AgentRequest{Name: "Planning", Prompt: "private"}}, "Planning started"},
		{AgentStarted{Invocation: 2}, "Agent 2 started"},
		{AgentFinished{Invocation: 1, Text: "private"}, "Agent 1 finished"},
		{AgentFinished{Invocation: 1, Error: "broken"}, "Agent 1 failed: broken"},
		{AgentFinished{Invocation: 1, Cancelled: true}, "Agent 1 cancelled"},
	} {
		artifact, ok := workflowEventArtifact(tc.event).(tool.TextArtifact)
		if !ok || !strings.Contains(artifact.Text, tc.want) || strings.Contains(artifact.Text, "private") {
			t.Fatalf("artifact = %#v, want %q without transcript", artifact, tc.want)
		}
	}
}
