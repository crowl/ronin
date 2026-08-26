package runtime_test

import (
	"strings"
	"testing"

	"github.com/crowl/ronin/runtime"
)

func TestBuildSystemPromptGuidesGoExploration(t *testing.T) {
	prompt, err := runtime.BuildSystemPrompt(runtime.SystemPromptInput{CWD: "/work"})
	if err != nil {
		t.Fatalf("BuildSystemPrompt() error = %v", err)
	}

	for _, expected := range []string{
		"## Go Exploration",
		"Use `outline_package` first when a package is unfamiliar",
		"Use `find_symbol` when you know",
		"returned `file`, `start_line`, and `end_line` with `read_file`",
		"text search for references, interface implementations, callers, callees, literals, textual patterns, generated files",
		"Prefer targeted range reads over whole-file reads",
		"fall back to text search",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("system prompt does not contain %q:\n%s", expected, prompt)
		}
	}

	if strings.Contains(prompt, "go_navigation") {
		t.Errorf("system prompt contains removed go_navigation tool:\n%s", prompt)
	}
}
