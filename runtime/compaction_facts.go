package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
)

func compactionToolFacts(name, output string) string {
	switch name {
	case "shell", "edit_file", "write_file":
	default:
		return ""
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(output), &fields) != nil {
		return ""
	}
	var b strings.Builder
	for _, key := range []string{"command", "path", "exit_code", "success", "error", "timed_out", "changed", "created"} {
		if value, ok := fields[key]; ok {
			fmt.Fprintf(&b, "  %s=%s\n", key, compactOneLine(string(value), 1000))
		}
	}
	// Test diagnostics may be buried in the middle of otherwise repetitive logs.
	for _, key := range []string{"stdout", "stderr"} {
		var text string
		if json.Unmarshal(fields[key], &text) != nil {
			continue
		}
		count := 0
		for _, line := range strings.Split(text, "\n") {
			lower := strings.ToLower(line)
			if !strings.Contains(lower, "fail") && !strings.Contains(lower, "error:") && !strings.Contains(lower, "panic:") {
				continue
			}
			fmt.Fprintf(&b, "  diagnostic: %s\n", compactOneLine(line, 500))
			count++
			if count == 8 {
				b.WriteString("  [additional diagnostics omitted; retrieve source]\n")
				break
			}
		}
	}
	return b.String()
}
