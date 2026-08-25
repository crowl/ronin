package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/tool"
)

func TestBoxLineCache(t *testing.T) {
	t.Run("reuses unchanged box lines", func(t *testing.T) {
		var cache boxLineCache
		boxes := []box{
			userMessageBox{Text: "hello"},
			assistantMessageBox{Text: "world"},
		}
		now := time.Unix(100, 0)

		first := cache.Lines(boxes, 80, false, now)
		userLines := cache.entries[0].lines
		assistantLines := cache.entries[1].lines

		second := cache.Lines(boxes, 80, false, now)

		if strings.Join(second, "\n") != strings.Join(first, "\n") {
			t.Fatalf("cached lines changed\nfirst:  %#v\nsecond: %#v", first, second)
		}
		if &cache.entries[0].lines[0] != &userLines[0] {
			t.Fatalf("user box rendered again")
		}
		if &cache.entries[1].lines[0] != &assistantLines[0] {
			t.Fatalf("assistant box rendered again")
		}
	})

	t.Run("rerenders changed streaming box only", func(t *testing.T) {
		var cache boxLineCache
		now := time.Unix(100, 0)

		cache.Lines([]box{
			userMessageBox{Text: "prompt"},
			assistantMessageBox{Text: "partial"},
		}, 80, false, now)
		userLines := cache.entries[0].lines
		assistantLines := cache.entries[1].lines

		cache.Lines([]box{
			userMessageBox{Text: "prompt"},
			assistantMessageBox{Text: "partial response"},
		}, 80, false, now)

		if &cache.entries[0].lines[0] != &userLines[0] {
			t.Fatalf("unchanged user box rendered again")
		}
		if &cache.entries[1].lines[0] == &assistantLines[0] {
			t.Fatalf("changed assistant box did not render again")
		}
	})

	t.Run("invalidates tool expansion", func(t *testing.T) {
		var cache boxLineCache
		now := time.Unix(100, 0)
		toolOutput := strings.Join([]string{
			"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve",
		}, "\n")
		boxes := []box{
			toolCallBox{
				ToolCallID: "call_1",
				Title:      "read_file file.go",
				Artifacts:  []tool.Artifact{tool.TextArtifact{Text: toolOutput}},
				StartedAt:  now.Add(-time.Second),
				EndedAt:    now,
			},
		}

		collapsed := cache.Lines(boxes, 80, false, now)
		toolLines := cache.entries[0].lines
		expanded := cache.Lines(boxes, 80, true, now)

		if strings.Join(collapsed, "\n") == strings.Join(expanded, "\n") {
			t.Fatalf("tool expansion did not change rendered output")
		}
		if &cache.entries[0].lines[0] == &toolLines[0] {
			t.Fatalf("tool box did not render again after expansion changed")
		}
	})

	t.Run("running workflow elapsed time invalidates cached lines", func(t *testing.T) {
		var cache boxLineCache
		startedAt := time.Unix(100, 0)
		boxes := []box{workflowBox{Name: "review", StartedAt: startedAt}}

		first := cache.Lines(boxes, 80, false, startedAt.Add(time.Second))
		cachedLines := cache.entries[0].lines
		second := cache.Lines(boxes, 80, false, startedAt.Add(2*time.Second))

		if strings.Join(first, "\n") == strings.Join(second, "\n") {
			t.Fatalf("elapsed time did not change rendered output")
		}
		if &cache.entries[0].lines[0] == &cachedLines[0] {
			t.Fatalf("running workflow did not render again as time elapsed")
		}
	})

}
