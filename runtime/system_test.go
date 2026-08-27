package runtime_test

import (
	"strings"
	"testing"

	"github.com/crowl/ronin/runtime"
)

func TestBuildSystemPromptIsLanguageNeutral(t *testing.T) {
	prompt, err := runtime.BuildSystemPrompt(runtime.SystemPromptInput{CWD: "/work"})
	if err != nil {
		t.Fatalf("BuildSystemPrompt() error = %v", err)
	}
	if !strings.Contains(prompt, "Current working directory: /work") {
		t.Fatalf("system prompt does not contain working directory:\n%s", prompt)
	}
	for _, unexpected := range []string{"Go Exploration", "gopls", "outline_package", "find_symbol"} {
		if strings.Contains(prompt, unexpected) {
			t.Errorf("system prompt contains language-specific guidance %q:\n%s", unexpected, prompt)
		}
	}
}

func TestBuildSystemPromptIncludesGenericMCPInstructions(t *testing.T) {
	prompt, err := runtime.BuildSystemPrompt(runtime.SystemPromptInput{
		CWD: "/work",
		MCPInstructions: []runtime.MCPInstruction{
			{Server: "knowledge", Content: "Search before answering.", Tools: []string{"search", "fetch"}},
		},
	})
	if err != nil {
		t.Fatalf("BuildSystemPrompt() error = %v", err)
	}
	for _, expected := range []string{
		"# MCP Server Instructions",
		"MCP tools are namespaced as `<server>__<tool>`.",
		"## knowledge",
		"`knowledge__search`",
		"`knowledge__fetch`",
		"Search before answering.",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("system prompt does not contain %q:\n%s", expected, prompt)
		}
	}
}
