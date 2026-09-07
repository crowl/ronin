package workflow

import (
	"context"
	"strings"
	"testing"
)

func TestAgentDisplayNameValidation(t *testing.T) {
	t.Parallel()
	for _, function := range []string{"run_agent", "start_agent"} {
		t.Run(function, func(t *testing.T) {
			_, err := runScriptWithAgent(t, `ronin.`+function+`({ name = {}, prompt = "test" })`, func(context.Context, AgentRequest) (AgentResult, error) {
				t.Error("invalid name reached agent")
				return AgentResult{}, nil
			})
			if err == nil || !strings.Contains(err.Error(), "name must be a string") {
				t.Fatalf("invalid name error = %v", err)
			}
		})
	}
}
