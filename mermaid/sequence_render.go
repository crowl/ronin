package mermaid

import (
	"fmt"
	"strings"
)

// Sequence diagrams have fixed participant columns and chronological event rows.
// Frames span all participants: nesting changes the frame inset, not message order.
func renderSequence(s sequence, maxWidth int) (string, error) {
	n := len(s.participants)
	widths := make([]int, n)
	pitch := 8
	for i, p := range s.participants {
		widths[i], _ = labelWidth(p.label)
		widths[i] += 4
		pitch = max(pitch, widths[i]+2)
	}
	leftExtra, rightExtra := 0, 0
	captionWidth := 0
	for _, e := range s.events {
		w, _ := labelWidth(e.label)
		switch e.kind {
		case "message":
			if e.from == e.to {
				pitch = max(pitch, w+6)
				if e.from == n-1 {
					rightExtra = max(rightExtra, w+5)
				}
			} else {
				distance := e.to - e.from
				if distance < 0 {
					distance = -distance
				}
				pitch = max(pitch, (w+4+distance-1)/distance)
			}
		case "note-left":
			pitch = max(pitch, w+7)
			if e.from == 0 {
				leftExtra = max(leftExtra, w+6)
			}
		case "note-right":
			pitch = max(pitch, w+7)
			if e.from == n-1 {
				rightExtra = max(rightExtra, w+6)
			}
		case "note-over":
			pitch = max(pitch, w+6)
			if e.from == 0 || e.to == 0 {
				leftExtra = max(leftExtra, (w+5)/2)
			}
			if e.from == n-1 || e.to == n-1 {
				rightExtra = max(rightExtra, (w+5)/2)
			}
		case "alt", "opt", "loop", "else":
			captionWidth = max(captionWidth, w+len(e.kind)+5)
		}
	}
	inset := s.depth*2 + 2
	centers := make([]int, n)
	centers[0] = inset + max(widths[0]/2, leftExtra)
	for i := 1; i < n; i++ {
		centers[i] = centers[i-1] + pitch
	}
	width := centers[n-1] + max((widths[n-1]+1)/2, rightExtra) + inset + 1
	width = max(width, captionWidth+2*inset)
	height := 5
	for _, e := range s.events {
		height += sequenceEventHeight(e)
	}
	height += 3
	if width > maxWidth {
		return "", fmt.Errorf("mermaid: sequence width %d exceeds available width %d", width, maxWidth)
	}
	if width > 512 || height > 512 || width*height > 65536 {
		return "", fmt.Errorf("mermaid: sequence canvas limit exceeded")
	}
	c := canvas{w: width, h: height, cells: make([]cell, width*height)}
	for i, p := range s.participants {
		x := centers[i]
		c.sequenceBox(x-widths[i]/2, 0, widths[i], p.label)
		for y := 3; y < height-3; y++ {
			c.put(point{x, y}, "┆")
		}
		c.sequenceBox(x-widths[i]/2, height-3, widths[i], p.label)
	}
	type frame struct{ left, right, top int }
	frames := []frame{}
	y := 5
	for _, e := range s.events {
		w, _ := labelWidth(e.label)
		switch e.kind {
		case "alt", "opt", "loop":
			f := frame{e.depth * 2, width - 1 - e.depth*2, y}
			c.horizontal(f.left, f.right, y, "─")
			c.put(point{f.left, y}, "┌")
			c.put(point{f.right, y}, "┐")
			c.horizontal(f.left+1, f.right-1, y+1, " ")
			c.text(point{f.left + 2, y + 1}, e.kind+" "+e.label)
			frames = append(frames, f)
		case "else":
			f := frames[len(frames)-1]
			c.horizontal(f.left, f.right, y, "┄")
			c.put(point{f.left, y}, "├")
			c.put(point{f.right, y}, "┤")
			caption := "else"
			if e.label != "" {
				caption += " " + e.label
			}
			c.horizontal(f.left+1, f.right-1, y+1, " ")
			c.text(point{f.left + 2, y + 1}, caption)
		case "end":
			f := frames[len(frames)-1]
			frames = frames[:len(frames)-1]
			c.horizontal(f.left, f.right, y, "─")
			c.put(point{f.left, y}, "└")
			c.put(point{f.right, y}, "┘")
			for row := f.top + 1; row < y; row++ {
				// Preserve the else separator's junctions.
				for _, x := range []int{f.left, f.right} {
					v := c.cells[c.index(point{x, row})].text
					if v != "├" && v != "┤" {
						c.put(point{x, row}, "│")
					}
				}
			}
		case "message":
			from, to := centers[e.from], centers[e.to]
			line := "─"
			if e.dashed {
				line = "┄"
			}
			if from == to {
				right := from + max(4, w+2)
				c.text(point{from + 2, y}, e.label)
				c.horizontal(from+1, right, y+1, line)
				c.put(point{right, y + 1}, "┐")
				c.put(point{right, y + 2}, "│")
				c.horizontal(from+1, right, y+3, line)
				c.put(point{right, y + 3}, "┘")
				c.put(point{from + 1, y + 3}, "◀")
			} else {
				left, right := min(from, to), max(from, to)
				c.text(point{left + 1 + (right-left-1-w)/2, y}, e.label)
				c.horizontal(left+1, right-1, y+1, line)
				if to > from {
					c.put(point{to - 1, y + 1}, "▶")
				} else {
					c.put(point{to + 1, y + 1}, "◀")
				}
			}
		case "note-left", "note-right", "note-over":
			a, b := centers[e.from], centers[e.to]
			boxWidth := w + 4
			x := (a + b - boxWidth) / 2
			if e.kind == "note-left" {
				x = a - boxWidth - 2
			}
			if e.kind == "note-right" {
				x = a + 2
			}
			if e.kind == "note-over" && a != b {
				x = min(a, b) - 2
				boxWidth = max(boxWidth, max(a, b)-x+3)
			}
			c.sequenceBox(x, y, boxWidth, e.label)
		}
		y += sequenceEventHeight(e)
	}
	lines := make([]string, height)
	for row := 0; row < height; row++ {
		var b strings.Builder
		for x := 0; x < width; x++ {
			v := c.cells[row*width+x]
			if v.blocked {
				b.WriteString(v.text)
			} else {
				b.WriteByte(' ')
			}
		}
		lines[row] = strings.TrimRight(b.String(), " ")
	}
	return strings.Join(lines, "\n"), nil
}
func sequenceEventHeight(e sequenceEvent) int {
	switch e.kind {
	case "message":
		if e.from == e.to {
			return 5
		}
		return 3
	case "note-left", "note-right", "note-over":
		return 4
	case "end":
		return 2
	default:
		return 3
	}
}
func (c *canvas) horizontal(left, right, y int, glyph string) {
	for x := left; x <= right; x++ {
		c.put(point{x, y}, glyph)
	}
}
func (c *canvas) sequenceBox(x, y, width int, label string) {
	for row := y; row < y+3; row++ {
		for col := x; col < x+width; col++ {
			c.put(point{col, row}, " ")
		}
	}
	c.horizontal(x+1, x+width-2, y, "─")
	c.horizontal(x+1, x+width-2, y+2, "─")
	c.put(point{x, y}, "┌")
	c.put(point{x + width - 1, y}, "┐")
	c.put(point{x, y + 2}, "└")
	c.put(point{x + width - 1, y + 2}, "┘")
	c.put(point{x, y + 1}, "│")
	c.put(point{x + width - 1, y + 1}, "│")
	w, _ := labelWidth(label)
	c.text(point{x + (width-w)/2, y + 1}, label)
}
