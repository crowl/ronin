package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/crowl/ronin/tui/internal/text"
)

func renderWorkflowBoxLines(workflow workflowBox, width int, _ bool, now time.Time) []string {
	width = max(width, 1)
	lines := make([]string, 0, 20)
	appendLine := func(value string, render func(string) string) {
		if len(lines) < maxWorkflowVisualLines {
			lines = append(lines, render(text.Truncate(value, width)))
		}
	}
	status := workflow.Status
	if status == "" {
		status = "running"
	}
	appendLine(fmt.Sprintf("%% %s · %s · %d active · %d finished", workflowDisplayLine(workflow.Name), status, len(workflow.Active), workflow.Completed), strongStyle.apply)

	appendStep := func(step workflowStep) {
		end := step.EndedAt
		if end.IsZero() {
			end = now
		}
		render := mutedStyle.apply
		if step.Status == "running" {
			render = strongStyle.apply
		} else if step.Status == "failed" {
			render = errorStyle.apply
		}
		appendLine(fmt.Sprintf("  %-9s %s · %.1fs", step.Status, workflowDisplayLine(step.Name), max(0, end.Sub(step.StartedAt).Seconds())), render)
		if step.Error != "" {
			appendLine("    "+workflowDisplayLine(step.Error), errorStyle.apply)
		}
	}

	// Active steps are never displaced by logs, reports, or completed history.
	// Reserve space for an overflow notice and the elapsed footer.
	activeLimit := maxWorkflowVisualLines - 3
	for i, step := range workflow.Active {
		if i == activeLimit {
			appendLine(fmt.Sprintf("  ... %d more active steps", len(workflow.Active)-i), strongStyle.apply)
			break
		}
		appendStep(step)
	}
	if len(workflow.Active) < activeLimit {
		for i := len(workflow.Recent) - 1; i >= 0 && len(lines) < maxWorkflowVisualLines-10; i-- {
			appendStep(workflow.Recent[i])
		}
		if workflow.LatestActivity != "" {
			appendLine("  Update: "+workflowDisplayLine(workflow.LatestActivity), mutedStyle.apply)
		}
		if workflow.Input != "" {
			appendLine("  Input: "+workflowDisplayLine(workflow.Input), mutedStyle.apply)
		}
		for _, line := range boundedWorkflowWrap("  Summary: ", workflow.Summary, width, min(maxWorkflowSummaryLines, maxWorkflowVisualLines-len(lines)-1)) {
			appendLine(line, mutedStyle.apply)
		}
	}
	end := workflow.EndedAt
	label := "Took"
	if end.IsZero() {
		end = now
		label = "Elapsed"
	}
	appendLine(fmt.Sprintf("  %s %.1fs", label, max(0, end.Sub(workflow.StartedAt).Seconds())), mutedStyle.apply)
	return lines
}

func workflowDisplayLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
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
