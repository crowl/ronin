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
		"Use `go_navigation` for semantic references, interface implementations, callers, and callees",
		"Select the target by symbol name or exact 1-based file position",
		"text search for literals, textual patterns, generated files",
		"Prefer targeted range reads over whole-file reads",
		"fall back to text search",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("system prompt does not contain %q:\n%s", expected, prompt)
		}
	}
}
