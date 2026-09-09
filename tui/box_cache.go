package tui

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
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
		signature.Text = fmt.Sprintf("%q:%q:%q:%q:%d", typedBlock.Name, typedBlock.Input, typedBlock.Status, typedBlock.Summary, typedBlock.Completed)
		digest := sha256.New()
		_, _ = fmt.Fprintf(digest, "%q;", typedBlock.LatestActivity)
		for _, steps := range [][]workflowStep{typedBlock.Active, typedBlock.Recent} {
			for _, step := range steps {
				_, _ = fmt.Fprintf(digest, "%d:%q:%q:%q:%d:%d;", step.Invocation, step.Name, step.Status, step.Error, step.StartedAt.UnixNano(), step.EndedAt.UnixNano())
			}
		}
		copy(signature.WorkflowDigest[:], digest.Sum(nil))
	case systemMessageBox:
		signature.Kind = "system"
		signature.Text = typedBlock.Text
	case shellOutputBox:
		signature.Kind = "shell-output"
		signature.Text = fmt.Sprintf("%q:%q:%t:%t:%q:%q", typedBlock.Stdout, typedBlock.Stderr, typedBlock.StdoutTruncated, typedBlock.StderrTruncated, typedBlock.Notice, typedBlock.Error)
	case errorMessageBox:
		signature.Kind = "error"
		signature.Text = typedBlock.Text
	}

	return signature
}
