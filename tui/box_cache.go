package tui

import (
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/crowl/ronin/tool"
)

type boxLineCache struct {
	entries []boxLineCacheEntry
}

type boxLineCacheEntry struct {
	signature boxLineSignature
	lines     []string
}

type boxLineSignature struct {
	Revision       uint64
	Truncated      bool
	Kind           string
	Width          int
	ToolsExpanded  bool
	ToolCallID     string
	Title          string
	Text           string
	WorkflowDigest [32]byte
	StartedAt      int64
	EndedAt        int64
	ElapsedBucket  int64
}

func toolCallSignatureText(box toolCallBox) string {
	var b strings.Builder
	for _, artifact := range box.Artifacts {
		_, _ = fmt.Fprintf(&b, "%#v\n", artifact)
	}
	if box.Error != "" {
		b.WriteString(box.Error)
	}
	return b.String()
}

func (c *boxLineCache) Lines(boxes []box, width int, toolsExpanded bool, now time.Time) []string {
	if len(c.entries) > len(boxes) {
		c.entries = c.entries[:len(boxes)]
	}
	for len(c.entries) < len(boxes) {
		c.entries = append(c.entries, boxLineCacheEntry{})
	}

	var lines []string
	for i, box := range boxes {
		if i > 0 {
			lines = append(lines, "")
		}

		signature := boxSignature(box, width, toolsExpanded, now)
		entry := c.entries[i]
		if entry.signature == signature && entry.lines != nil {
			lines = append(lines, entry.lines...)
			continue
		}

		rendered := renderBoxLinesAt(box, width, toolsExpanded, now)
		c.entries[i] = boxLineCacheEntry{
			signature: signature,
			lines:     append([]string(nil), rendered...),
		}
		lines = append(lines, rendered...)
	}

	return lines
}

func (c *boxLineCache) Reset() {
	c.entries = nil
}

func writeWorkflowArtifactSignature(w io.Writer, artifact tool.Artifact) {
	switch artifact := artifact.(type) {
	case tool.TextArtifact:
		_, _ = fmt.Fprintf(w, "text:%q;", artifact.Text)
	case tool.ShellStreamArtifact:
		_, _ = fmt.Fprintf(w, "shell:%q:%q;", artifact.Stream, artifact.Content)
	case tool.FileArtifact:
		_, _ = fmt.Fprintf(w, "file:%q:%q;", artifact.Path, artifact.Content)
	case tool.FileRangeArtifact:
		_, _ = fmt.Fprintf(w, "range:%q:%d:%d:%q;", artifact.Path, artifact.StartLine, artifact.EndLine, artifact.Content)
	case tool.FileMetadataArtifact:
		_, _ = fmt.Fprintf(w, "metadata:%q:%q;", artifact.Path, artifact.FileID)
	case tool.UnifiedDiffArtifact:
		_, _ = fmt.Fprintf(w, "diff:%q:%q;", artifact.Path, artifact.Diff)
	default:
		_, _ = fmt.Fprintf(w, "%T:%#v;", artifact, artifact)
	}
}

func boxSignature(block box, width int, toolsExpanded bool, now time.Time) boxLineSignature {
	signature := boxLineSignature{
		Width:         width,
		ToolsExpanded: toolsExpanded,
	}

	switch typedBlock := block.(type) {
	case userMessageBox:
		signature.Kind = "user"
		signature.Text = typedBlock.Text
	case assistantMessageBox:
		signature.Kind = "assistant"
		signature.Text = typedBlock.Text
	case assistantThinkingBox:
		signature.Kind = "thinking"
		signature.Text = typedBlock.Text
	case toolCallBox:
		signature.Kind = "tool"
		signature.ToolCallID = typedBlock.ToolCallID
		signature.Title = typedBlock.Title
		signature.Revision = typedBlock.Revision
		signature.Truncated = typedBlock.DisplayTruncated
		if typedBlock.Revision == 0 {
			signature.Text = toolCallSignatureText(typedBlock)
		} else {
			signature.Text = typedBlock.Error
		}
		signature.StartedAt = typedBlock.StartedAt.UnixNano()
		signature.EndedAt = typedBlock.EndedAt.UnixNano()
		if typedBlock.EndedAt.IsZero() {
			duration := max(now.Sub(typedBlock.StartedAt), 0)
			signature.ElapsedBucket = int64((duration + 50*time.Millisecond) / (100 * time.Millisecond))
		}
	case workflowBox:
		signature.Kind = "workflow"
		signature.StartedAt = typedBlock.StartedAt.UnixNano()
		signature.EndedAt = typedBlock.EndedAt.UnixNano()
		if typedBlock.EndedAt.IsZero() {
			duration := max(now.Sub(typedBlock.StartedAt), 0)
			signature.ElapsedBucket = int64((duration + 50*time.Millisecond) / (100 * time.Millisecond))
		}
		signature.Text = fmt.Sprintf("%s:%s:%s:%s:%d:%d:%t", typedBlock.Name, typedBlock.Input, typedBlock.Status, typedBlock.Summary, typedBlock.TimelineBytes, len(typedBlock.Entries), typedBlock.TimelineTruncated)
		digest := sha256.New()
		_, _ = digest.Write([]byte(typedBlock.LatestActivity))
		for _, entry := range typedBlock.Entries {
			_, _ = digest.Write([]byte(entry.Text))
			_, _ = digest.Write([]byte(entry.Detail))
			for _, artifact := range entry.Artifacts {
				writeWorkflowArtifactSignature(digest, artifact)
			}
		}
		copy(signature.WorkflowDigest[:], digest.Sum(nil))
	case systemMessageBox:
		signature.Kind = "system"
		signature.Text = typedBlock.Text
	case errorMessageBox:
		signature.Kind = "error"
		signature.Text = typedBlock.Text
	}

	return signature
}
