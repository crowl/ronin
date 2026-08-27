package tui

import (
	"fmt"
	"time"

	"github.com/crowl/ronin/tui/internal/text"
)

func renderWorkflowBoxLines(workflow workflowBox, width int, toolsExpanded bool, now time.Time) []string {
	lines, _ := renderWorkflowBoxLinesState(workflow, width, toolsExpanded, now)
	return lines
}

func renderWorkflowBoxLinesState(workflow workflowBox, width int, toolsExpanded bool, now time.Time) ([]string, bool) {
	if width < 1 {
		width = 1
	}
	lines := make([]string, 0, maxWorkflowVisualLines)
	appendLine := func(line string) bool {
		if len(lines) >= maxWorkflowVisualLines {
			return false
		}
		lines = append(lines, line)
		return true
	}

	title := "% " + workflow.Name
	if workflow.Status != "" {
		title += " (" + workflow.Status + ")"
	}
	appendLine(strongStyle.apply(text.Truncate(title, width)))
	if workflow.Input != "" {
		appendWorkflowWrappedLine(&lines, "  Input: ", workflow.Input, width, maxWorkflowSummaryLines, mutedStyle.apply)
	}

	summaryLines := boundedWorkflowWrap("  Summary: ", workflow.Summary, width, maxWorkflowSummaryLines)
	footerLines := 1
	timelineLimit := max(min(maxWorkflowTimelineLines, maxWorkflowVisualLines-len(lines)-footerLines-len(summaryLines)), 1)
	timelineStart := len(lines)
	truncatedNotice := workflow.TimelineTruncated
	for _, entry := range workflow.Entries {
		timelineLines := len(lines) - timelineStart
		if timelineLines >= timelineLimit {
			truncatedNotice = true
			break
		}
		if !appendWorkflowWrappedLine(&lines, "  ", entry.Text, width, timelineLimit-timelineLines, mutedStyle.apply) {
			truncatedNotice = true
			break
		}
		if toolsExpanded && entry.Detail != "" {
			timelineLines = len(lines) - timelineStart
			if !appendWorkflowWrappedLine(&lines, "    ", entry.Detail, width, timelineLimit-timelineLines, mutedStyle.apply) {
				truncatedNotice = true
			}
		}
		if truncatedNotice {
			break
		}
		if toolsExpanded {
			for _, artifact := range entry.Artifacts {
				timelineLines = len(lines) - timelineStart
				artifactLines, more := toolArtifactLinesBounded(artifact, width, timelineLimit-timelineLines)
				for _, line := range artifactLines {
					if !appendLine(line) {
						more = true
						break
					}
				}
				if more {
					truncatedNotice = true
					break
				}
			}
		}
		if truncatedNotice {
			break
		}
	}

	if truncatedNotice {
		activity := "  ... workflow output truncated"
		if workflow.LatestActivity != "" {
			activity = "  " + workflow.LatestActivity + " (workflow output truncated)"
		}
		if len(lines)-timelineStart >= timelineLimit && len(lines) > timelineStart {
			lines = lines[:len(lines)-1]
		}
		appendLine(mutedStyle.apply(text.Truncate(activity, width)))
	} else if workflow.LatestActivity != "" {
		if len(lines)-timelineStart >= timelineLimit && len(lines) > timelineStart {
			lines = lines[:len(lines)-1]
		}
		appendLine(mutedStyle.apply(text.Truncate("  "+workflow.LatestActivity, width)))
	}

	for _, summary := range summaryLines {
		if len(lines) >= maxWorkflowVisualLines-footerLines {
			break
		}
		appendLine(mutedStyle.apply(summary))
	}
	endedAt := workflow.EndedAt
	label := "Elapsed"
	if endedAt.IsZero() {
		endedAt = now
	} else {
		label = "Took"
	}
	appendLine(mutedStyle.apply(text.Truncate(fmt.Sprintf("  %s %.1fs", label, max(0, endedAt.Sub(workflow.StartedAt).Seconds())), width)))
	return lines, truncatedNotice
}

func appendWorkflowWrappedLine(lines *[]string, prefix, value string, width, limit int, render func(string) string) bool {
	if limit <= 0 {
		return false
	}
	initial := len(*lines)
	return forEachBoundedWrappedLine(prefix, value, width, limit, func(line string, _ int) bool {
		if len(*lines)-initial >= limit || len(*lines) >= maxWorkflowVisualLines {
			return false
		}
		*lines = append(*lines, render(line))
		return true
	})
}

func boundedWorkflowWrap(prefix, value string, width, limit int) []string {
	if value == "" || limit <= 0 {
		return nil
	}
	lines := make([]string, 0, min(limit, 8))
	complete := forEachBoundedWrappedLine(prefix, value, width, limit, func(line string, _ int) bool {
		if len(lines) >= limit {
			return false
		}
		lines = append(lines, line)
		return true
	})
	if complete {
		return lines
	}
	if len(lines) >= limit {
		lines = lines[:limit-1]
	}
	return append(lines, "  ... summary truncated")
}
