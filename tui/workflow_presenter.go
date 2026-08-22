package tui

import (
	"fmt"
	"time"

	"github.com/crowl/ronin/tui/internal/text"
)

func renderWorkflowBoxLines(workflow workflowBox, width int, theme Theme, toolsExpanded bool, now time.Time) []string {
	lines, _ := renderWorkflowBoxLinesState(workflow, width, theme, toolsExpanded, now)
	return lines
}

func renderWorkflowBoxLinesState(workflow workflowBox, width int, theme Theme, toolsExpanded bool, now time.Time) ([]string, bool) {
	if width < 1 {
		width = 1
	}
	boxStyle := theme.Box.ToolCall
	lines := make([]string, 0, maxWorkflowVisualLines)
	appendLine := func(line string) bool {
		if len(lines) >= maxWorkflowVisualLines {
			return false
		}
		lines = append(lines, line)
		return true
	}

	appendLine(boxStyle.ApplyBody(text.Fill("", width)))
	title := "Workflow: " + workflow.Name
	if workflow.Status != "" {
		title += " (" + workflow.Status + ")"
	}
	appendLine(boxStyle.ApplyTitle(text.Fill(title, width)))
	appendLine(boxStyle.ApplyMuted(text.Fill(" Input: "+workflow.Input, width)))

	summaryLines := boundedWorkflowWrap(" Summary: ", workflow.Summary, width, maxWorkflowSummaryLines)
	footerLines := 2
	timelineLimit := min(maxWorkflowTimelineLines, maxWorkflowVisualLines-3-footerLines-len(summaryLines))
	if timelineLimit < 1 {
		timelineLimit = 1
	}
	timelineLines := 0
	truncatedNotice := workflow.TimelineTruncated
	for _, entry := range workflow.Entries {
		if timelineLines >= timelineLimit {
			truncatedNotice = true
			break
		}
		if !appendWorkflowWrappedLine(&lines, " ", entry.Text, width, timelineLimit-timelineLines, func(value string) string {
			return boxStyle.ApplyBody(text.Fill(value, width))
		}) {
			truncatedNotice = true
			break
		}
		timelineLines = len(lines) - 3
		if toolsExpanded && entry.Detail != "" {
			if !appendWorkflowWrappedLine(&lines, "   ", entry.Detail, width, timelineLimit-timelineLines, func(value string) string {
				return boxStyle.ApplyMuted(text.Fill(value, width))
			}) {
				truncatedNotice = true
			}
			timelineLines = len(lines) - 3
		}
		if truncatedNotice {
			break
		}
		if toolsExpanded {
			for _, artifact := range entry.Artifacts {
				artifactLines, artifactMore := toolArtifactLinesBounded(artifact, boxStyle, width, timelineLimit-timelineLines)
				for _, artifactLine := range artifactLines {
					if !appendLine(artifactLine) {
						artifactMore = true
						break
					}
					timelineLines++
				}
				if artifactMore {
					truncatedNotice = true
					break
				}
			}
		}
		if truncatedNotice {
			break
		}
	}

	// Keep the latest lifecycle event visible, but combine it with the one
	// notice rather than adding a second timeline row after the budget.
	if truncatedNotice {
		activity := " ... workflow output truncated"
		if workflow.LatestActivity != "" {
			activity = " " + workflow.LatestActivity + " (workflow output truncated)"
		}
		if timelineLines >= timelineLimit && timelineLines > 0 {
			lines = lines[:len(lines)-1]
			timelineLines--
		}
		if appendLine(boxStyle.ApplyMuted(text.Fill(activity, width))) {
			timelineLines++
		}
	} else if workflow.LatestActivity != "" {
		if timelineLines >= timelineLimit && timelineLines > 0 {
			lines = lines[:len(lines)-1]
			timelineLines--
		}
		if appendLine(boxStyle.ApplyMuted(text.Fill(" "+workflow.LatestActivity, width))) {
			timelineLines++
		}
	}

	for _, summary := range summaryLines {
		if len(lines) >= maxWorkflowVisualLines-footerLines {
			break
		}
		appendLine(boxStyle.ApplyBody(text.Fill(summary, width)))
	}
	endedAt := workflow.EndedAt
	label := " Elapsed"
	if endedAt.IsZero() {
		endedAt = now
	} else {
		label = " Took"
	}
	appendLine(boxStyle.ApplyMeta(text.Fill(fmt.Sprintf("%s %.1fs", label, max(0, endedAt.Sub(workflow.StartedAt).Seconds())), width)))
	appendLine(boxStyle.ApplyBody(text.Fill("", width)))
	return lines, truncatedNotice
}

func appendWorkflowWrappedLine(lines *[]string, prefix, value string, width, limit int, style func(string) string) bool {
	if limit <= 0 {
		return false
	}
	initial := len(*lines)
	return forEachBoundedWrappedLine(prefix, value, width, limit, func(line string, _ int) bool {
		if len(*lines)-initial >= limit || len(*lines) >= maxWorkflowVisualLines {
			return false
		}
		*lines = append(*lines, style(line))
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
	return append(lines, " ... summary truncated")
}
