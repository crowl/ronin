package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/crowl/ronin/tool"
)

const workflowTruncatedSuffix = "\n... truncated"

func truncateWorkflowText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return strings.Clone(value)
	}

	suffix := workflowTruncatedSuffix
	if len(suffix) > limit {
		suffix = suffix[:limit]
	}
	prefixLimit := limit - len(suffix)
	for prefixLimit > 0 && !utf8.RuneStart(value[prefixLimit]) {
		prefixLimit--
	}
	return string([]byte(value[:prefixLimit])) + suffix
}

func truncateWorkflowPrefix(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return strings.Clone(value)
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return string([]byte(value[:end]))
}

func appendWorkflowText(value, incoming string, limit int) (string, bool) {
	if limit <= 0 {
		return "", value != "" || incoming != ""
	}
	if len(value) > limit {
		value = truncateWorkflowText(value, limit)
	}
	if incoming == "" {
		return strings.Clone(value), false
	}
	remaining := limit - len(value)
	if remaining <= 0 {
		return strings.Clone(value), true
	}
	bounded := truncateWorkflowPrefix(incoming, remaining)
	return value + bounded, len(bounded) < len(incoming)
}

func boundWorkflowText(value string, limit int) string {
	return truncateWorkflowText(value, limit)
}

func boundWorkflowSummary(value string) string {
	return truncateWorkflowText(value, maxWorkflowSummaryBytes)
}

func workflowArtifactContentBytes(artifact tool.Artifact) int {
	switch artifact := artifact.(type) {
	case tool.TextArtifact:
		return len(artifact.Text)
	case tool.ShellStreamArtifact:
		return len(artifact.Content)
	case tool.FileArtifact:
		return len(artifact.Content)
	case tool.FileRangeArtifact:
		return len(artifact.Content)
	case tool.UnifiedDiffArtifact:
		return len(artifact.Diff)
	case tool.FileMetadataArtifact:
		return len("Already in context (") + len(artifact.FileID) + len(")")
	default:
		return 0
	}
}
func workflowArtifactContent(artifact tool.Artifact) string {
	switch artifact := artifact.(type) {
	case tool.TextArtifact:
		return artifact.Text
	case tool.ShellStreamArtifact:
		return artifact.Content
	case tool.FileArtifact:
		return artifact.Content
	case tool.FileRangeArtifact:
		return artifact.Content
	case tool.UnifiedDiffArtifact:
		return artifact.Diff
	case tool.FileMetadataArtifact:
		return "Already in context (" + artifact.FileID + ")"
	default:
		return ""
	}
}

func boundWorkflowArtifact(artifact tool.Artifact, limit int) (tool.Artifact, int, bool) {
	if limit <= 0 {
		return tool.FileMetadataArtifact{}, 0, workflowArtifactContentBytes(artifact) > 0
	}
	if typed, ok := artifact.(tool.FileMetadataArtifact); ok {
		const prefix = "Already in context ("
		const suffix = ")"
		if limit < len(prefix)+len(suffix) {
			return tool.FileMetadataArtifact{}, 0, true
		}
		idLimit := limit - len(prefix) - len(suffix)
		boundedID := truncateWorkflowPrefix(typed.FileID, idLimit)
		used := len(prefix) + len(boundedID) + len(suffix)
		return tool.FileMetadataArtifact{FileID: boundedID}, used, used < len(prefix)+len(typed.FileID)+len(suffix)
	}
	content := workflowArtifactContent(artifact)
	if content == "" {
		switch artifact := artifact.(type) {
		case tool.FileArtifact:
			artifact.Path = ""
			return artifact, 0, false
		case tool.FileRangeArtifact:
			artifact.Path = ""
			return artifact, 0, false
		case tool.UnifiedDiffArtifact:
			artifact.Path = ""
			return artifact, 0, false
		}
		return artifact, 0, false
	}
	bounded := truncateWorkflowPrefix(content, limit)
	used := len(bounded)
	truncated := len(content) > used

	switch artifact := artifact.(type) {
	case tool.TextArtifact:
		artifact.Text = bounded
		return artifact, used, truncated
	case tool.ShellStreamArtifact:
		artifact.Content = bounded
		return artifact, used, truncated
	case tool.FileArtifact:
		artifact.Path = ""
		artifact.Content = bounded
		return artifact, used, truncated
	case tool.FileRangeArtifact:
		artifact.Path = ""
		artifact.Content = bounded
		return artifact, used, truncated
	case tool.UnifiedDiffArtifact:
		artifact.Path = ""
		artifact.Diff = bounded
		return artifact, used, truncated
	default:
		return artifact, 0, false
	}
}

func workflowEntryContentBytes(entry workflowEntry) int {
	used := len(entry.Text) + len(entry.Detail)
	for _, artifact := range entry.Artifacts {
		used += workflowArtifactContentBytes(artifact)
	}
	return used
}

func boundedWorkflowEntry(entry workflowEntry, limit int) (workflowEntry, int, bool) {
	originalBytes := workflowEntryContentBytes(entry)
	if limit <= 0 {
		return workflowEntry{}, 0, originalBytes > 0
	}

	entry.Text = truncateWorkflowPrefix(entry.Text, min(maxWorkflowTextSize, limit))
	used := len(entry.Text)
	remaining := limit - used
	entry.Detail = truncateWorkflowPrefix(entry.Detail, min(maxWorkflowDetailSize, remaining))
	used += len(entry.Detail)
	remaining = limit - used
	artifactsToBound := entry.Artifacts
	entry.Artifacts = nil

	if len(artifactsToBound) > 0 && remaining > 0 {
		artifacts := make([]tool.Artifact, 0, len(artifactsToBound))
		for _, artifact := range artifactsToBound {
			bounded, artifactBytes, _ := boundWorkflowArtifact(artifact, remaining)
			if artifactBytes > 0 || workflowArtifactContentBytes(artifact) == 0 {
				artifacts = append(artifacts, bounded)
			}
			used += artifactBytes
			remaining -= artifactBytes
			if remaining == 0 {
				break
			}
		}
		entry.Artifacts = artifacts
	}

	used = workflowEntryContentBytes(entry)
	return entry, used, originalBytes > used
}

func appendWorkflowEntryBounded(entries []workflowEntry, entry workflowEntry, limit int) ([]workflowEntry, int, bool) {
	if len(entries) > 0 && entries[len(entries)-1].Text == entry.Text {
		previous := entries[len(entries)-1]
		before := workflowEntryContentBytes(previous)
		aggregateLimit := before + max(0, limit)
		coalesced := workflowEntry{Text: previous.Text, Artifacts: entry.Artifacts, Lifecycle: previous.Lifecycle || entry.Lifecycle}
		detailTruncated := false
		coalesced.Detail, detailTruncated = appendWorkflowText(previous.Detail, entry.Detail, min(maxWorkflowDetailSize, aggregateLimit-len(coalesced.Text)))
		bounded, after, truncated := boundedWorkflowEntry(coalesced, aggregateLimit)
		entries[len(entries)-1] = bounded
		return entries, after - before, detailTruncated || truncated || workflowEntryContentBytes(entry) > workflowEntryContentBytes(bounded)
	}

	entry, used, truncated := boundedWorkflowEntry(entry, limit)
	if used == 0 && truncated {
		return entries, 0, true
	}
	if len(entries) >= maxWorkflowEntries {
		removed := workflowEntryContentBytes(entries[0])
		copy(entries, entries[1:])
		entries[len(entries)-1] = entry
		return entries, used - removed, true
	}
	return append(entries, entry), used, truncated
}

func appendWorkflowEntry(entries []workflowEntry, entry workflowEntry) []workflowEntry {
	entries, _, _ = appendWorkflowEntryBounded(entries, entry, maxWorkflowDisplayBytes)
	return entries
}
