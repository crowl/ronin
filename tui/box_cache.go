package tui

import (
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
	Theme         Theme
	Kind          string
	Width         int
	ToolsExpanded bool
	ToolCallID    string
	Title         string
	Text          string
	StartedAt     int64
	EndedAt       int64
	ElapsedBucket int64
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

func (c *boxLineCache) Lines(boxes []box, width int, theme Theme, toolsExpanded bool, now time.Time) []string {
	if len(c.entries) > len(boxes) {
		c.entries = c.entries[:len(boxes)]
	}
	for len(c.entries) < len(boxes) {
		c.entries = append(c.entries, boxLineCacheEntry{})
	}

	var lines []string
	for i, box := range boxes {
		if i > 0 && needsBlankLineBeforeBox(boxes[i-1], box) {
			lines = append(lines, "")
		}

		signature := boxSignature(box, width, theme, toolsExpanded, now)
		entry := c.entries[i]
		if entry.signature == signature && entry.lines != nil {
			lines = append(lines, entry.lines...)
			continue
		}

		rendered := renderBoxLinesAt(box, width, theme, toolsExpanded, now)
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

func boxSignature(block box, width int, theme Theme, toolsExpanded bool, now time.Time) boxLineSignature {
	signature := boxLineSignature{
		Theme:         theme,
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
		signature.Text = toolCallSignatureText(typedBlock)
		signature.StartedAt = typedBlock.StartedAt.UnixNano()
		signature.EndedAt = typedBlock.EndedAt.UnixNano()
		if typedBlock.EndedAt.IsZero() {
			duration := now.Sub(typedBlock.StartedAt)
			if duration < 0 {
				duration = 0
			}
			signature.ElapsedBucket = int64((duration + 50*time.Millisecond) / (100 * time.Millisecond))
		}
	case systemMessageBox:
		signature.Kind = "system"
		signature.Text = typedBlock.Text
	case errorMessageBox:
		signature.Kind = "error"
		signature.Text = typedBlock.Text
	}

	return signature
}
