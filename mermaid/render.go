package mermaid

import (
	"fmt"
	"strings"
)

type point struct{ x, y int }
type box struct{ x, y, w, h int }
type cell struct {
	text    string
	blocked bool
}
type canvas struct {
	w, h       int
	cells      []cell
	routeSteps int
}

func (c *canvas) index(p point) int     { return p.y*c.w + p.x }
func (c *canvas) inside(p point) bool   { return p.x >= 0 && p.y >= 0 && p.x < c.w && p.y < c.h }
func (c *canvas) put(p point, s string) { c.cells[c.index(p)] = cell{s, true} }
func (c *canvas) text(p point, s string) {
	previous := p
	for _, r := range s {
		w := runeWidth(r)
		if w == 0 {
			c.cells[c.index(previous)].text += string(r)
			continue
		}
		c.put(p, string(r))
		previous = p
		if w == 2 {
			c.put(point{p.x + 1, p.y}, "")
		}
		p.x += w
	}
}

// Back edges are excluded only from rank assignment, never from rendering.
// A DFS gives a deterministic acyclic orientation for the ranking constraints.
func ranks(g graph) []int {
	state := make([]int, len(g.nodes))
	rank := make([]int, len(g.nodes))
	forward := make([][]int, len(g.nodes))
	order := []int{}
	var visit func(int)
	visit = func(n int) {
		state[n] = 1
		for _, e := range g.edges {
			if e.from == n && state[e.to] != 1 {
				if state[e.to] == 0 {
					visit(e.to)
				}
				forward[n] = append(forward[n], e.to)
			}
		}
		state[n] = 2
		order = append(order, n)
	}
	for n := range g.nodes {
		if state[n] == 0 {
			visit(n)
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		n := order[i]
		for _, to := range forward[n] {
			rank[to] = max(rank[to], rank[n]+1)
		}
	}
	return rank
}

func render(g graph, maxWidth int) (string, error) {
	rank := ranks(g)
	bw, bh, labelSpace := 7, 5, 0
	for _, n := range g.nodes {
		w, err := labelWidth(n.label)
		if err != nil {
			return "", err
		}
		if w > maxLabel {
			return "", fmt.Errorf("mermaid: label too wide")
		}
		bw = max(bw, w+6)
	}
	for _, e := range g.edges {
		w, _ := labelWidth(e.label)
		labelSpace = max(labelSpace, w)
	}
	gapX, gapY := max(7, labelSpace+4), 5
	// Leave an outside corridor for cycles and self-loops.
	margin := 4
	counts := make(map[int]int)
	boxes := make([]box, len(g.nodes))
	width, height := 0, 0
	for i := range g.nodes {
		r := rank[i]
		slot := counts[r]
		counts[r]++
		x, y := margin+slot*(bw+gapX), margin+r*(bh+gapY)
		if g.horizontal {
			x, y = margin+r*(bw+gapX), margin+slot*(bh+gapY)
		}
		boxes[i] = box{x, y, bw, bh}
		width = max(width, x+bw+margin)
		height = max(height, y+bh+margin)
	}
	if width > 512 || height > 512 || width*height > 65536 {
		return "", fmt.Errorf("mermaid: canvas limit exceeded")
	}
	c := canvas{w: width, h: height, cells: make([]cell, width*height)}
	for i, b := range boxes {
		c.drawNode(b, g.nodes[i])
	}
	for _, e := range g.edges {
		path, ok := c.route(boxes[e.from], boxes[e.to], g.horizontal, e.label)
		if !ok {
			return "", fmt.Errorf("mermaid: cannot route edge %s --> %s", g.nodes[e.from].id, g.nodes[e.to].id)
		}
		c.drawPath(path)
		if e.label != "" && !c.edgeLabel(path, e.label, true) {
			return "", fmt.Errorf("mermaid: cannot place edge label")
		}
	}
	// Trim only the outside canvas, preserving diagram geometry and interior rows.
	left, right, top, bottom := width, 0, height, 0
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if c.cells[y*width+x].blocked {
				left = min(left, x)
				right = max(right, x+1)
				top = min(top, y)
				bottom = max(bottom, y+1)
			}
		}
	}
	if right-left > maxWidth {
		return "", fmt.Errorf("mermaid: diagram width %d exceeds available width %d", right-left, maxWidth)
	}
	lines := make([]string, 0, bottom-top)
	for y := top; y < bottom; y++ {
		var line strings.Builder
		for x := left; x < right; x++ {
			v := c.cells[y*width+x]
			if v.blocked {
				line.WriteString(v.text)
			} else {
				line.WriteByte(' ')
			}
		}
		lines = append(lines, strings.TrimRight(line.String(), " "))
	}
	return strings.Join(lines, "\n"), nil
}
func (c *canvas) drawNode(b box, n node) {
	// Reserve the full rectangle so routes cannot cross a label or node interior.
	for y := b.y; y < b.y+b.h; y++ {
		for x := b.x; x < b.x+b.w; x++ {
			c.put(point{x, y}, " ")
		}
	}
	corners := []string{"┌", "┐", "└", "┘"}
	if n.shape == '(' {
		corners = []string{"╭", "╮", "╰", "╯"}
	}
	// A compact six-sided decision outline retains room for readable labels.
	if n.shape == '{' {
		corners = []string{"╱", "╲", "╲", "╱"}
	}
	for x := b.x + 1; x < b.x+b.w-1; x++ {
		c.put(point{x, b.y}, "─")
		c.put(point{x, b.y + b.h - 1}, "─")
	}
	for y := b.y + 1; y < b.y+b.h-1; y++ {
		c.put(point{b.x, y}, "│")
		c.put(point{b.x + b.w - 1, y}, "│")
	}
	c.put(point{b.x, b.y}, corners[0])
	c.put(point{b.x + b.w - 1, b.y}, corners[1])
	c.put(point{b.x, b.y + b.h - 1}, corners[2])
	c.put(point{b.x + b.w - 1, b.y + b.h - 1}, corners[3])
	// Decisions use a compact diamond-like outline with sloping sides.
	if n.shape == '{' {
		for y := b.y; y < b.y+b.h; y++ {
			for x := b.x; x < b.x+b.w; x++ {
				c.put(point{x, y}, " ")
			}
		}
		for x := b.x + 2; x < b.x+b.w-2; x++ {
			c.put(point{x, b.y}, "─")
			c.put(point{x, b.y + 4}, "─")
		}
		c.put(point{b.x + 1, b.y + 1}, "╱")
		c.put(point{b.x + b.w - 2, b.y + 1}, "╲")
		c.put(point{b.x, b.y + 2}, "❮")
		c.put(point{b.x + b.w - 1, b.y + 2}, "❯")
		c.put(point{b.x + 1, b.y + 3}, "╲")
		c.put(point{b.x + b.w - 2, b.y + 3}, "╱")
	}
	w, _ := labelWidth(n.label)
	c.text(point{b.x + (b.w-w)/2, b.y + b.h/2}, n.label)
}
func ports(b box, horizontal bool) []point {
	bottom, top := point{b.x + b.w/2, b.y + b.h}, point{b.x + b.w/2, b.y - 1}
	right, left := point{b.x + b.w, b.y + b.h/2}, point{b.x - 1, b.y + b.h/2}
	if horizontal {
		return []point{right, left, bottom, top}
	}
	return []point{bottom, top, right, left}
}

var steps = []point{{0, 1}, {1, 0}, {0, -1}, {-1, 0}}

func (c *canvas) route(from, to box, horizontal bool, label string) ([]point, bool) {
	starts, ends := ports(from, horizontal), ports(to, horizontal)
	// Prefer the incoming side opposite the normal outgoing side.
	ends[0], ends[1] = ends[1], ends[0]
	var best []point
	for _, s := range starts {
		for _, end := range ends {
			if s == end || c.cells[c.index(s)].blocked || c.cells[c.index(end)].blocked {
				continue
			}
			prev := make([]int, len(c.cells))
			for i := range prev {
				prev[i] = -1
			}
			si, ei := c.index(s), c.index(end)
			prev[si] = si
			queue := []int{si}
			for head := 0; head < len(queue) && prev[ei] < 0; head++ {
				c.routeSteps++
				if c.routeSteps > 2_000_000 {
					return nil, false
				}
				idx := queue[head]
				p := point{idx % c.w, idx / c.w}
				for _, d := range steps {
					q := point{p.x + d.x, p.y + d.y}
					if !c.inside(q) {
						continue
					}
					qi := c.index(q)
					if prev[qi] >= 0 || c.cells[qi].blocked {
						continue
					}
					if idx == si && !outward(from, s, q) || qi == ei && !outward(to, end, p) {
						continue
					}
					prev[qi] = idx
					queue = append(queue, qi)
				}
			}
			if prev[ei] < 0 {
				continue
			}
			path := []point{}
			for idx := ei; ; idx = prev[idx] {
				path = append(path, point{idx % c.w, idx / c.w})
				if idx == si {
					break
				}
			}
			for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
				path[i], path[j] = path[j], path[i]
			}
			// Endpoint travel must be perpendicular to the node border, so the arrow
			// really points at its destination rather than running alongside it.
			if len(path) < 2 || !outward(from, s, path[1]) || !outward(to, end, path[len(path)-2]) {
				continue
			}
			if label != "" && !c.edgeLabel(path, label, false) {
				continue
			}
			if best == nil || len(path) < len(best) {
				best = path
			}
		}
	}
	return best, best != nil
}
func outward(b box, p, next point) bool {
	switch {
	case p.x < b.x:
		return next.x < p.x
	case p.x >= b.x+b.w:
		return next.x > p.x
	case p.y < b.y:
		return next.y < p.y
	default:
		return next.y > p.y
	}
}
func (c *canvas) drawPath(path []point) {
	for i, p := range path {
		glyph := "│"
		if i == len(path)-1 {
			prev := path[i-1]
			switch {
			case p.x > prev.x:
				glyph = "▶"
			case p.x < prev.x:
				glyph = "◀"
			case p.y > prev.y:
				glyph = "▼"
			default:
				glyph = "▲"
			}
		} else if i == 0 {
			if path[1].y == p.y {
				glyph = "─"
			}
		} else {
			a, b := path[i-1], path[i+1]
			switch {
			case a.y == b.y:
				glyph = "─"
			case a.x == b.x:
				glyph = "│"
			default:
				up := a.y < p.y || b.y < p.y
				left := a.x < p.x || b.x < p.x
				switch {
				case up && left:
					glyph = "┘"
				case up:
					glyph = "└"
				case left:
					glyph = "┐"
				default:
					glyph = "┌"
				}
			}
		}
		c.put(p, glyph)
	}
}
func (c *canvas) edgeLabel(path []point, label string, draw bool) bool {
	w, _ := labelWidth(label)
	occupied := make(map[point]bool, len(path))
	for _, p := range path {
		occupied[p] = true
	}
	for i := 1; i < len(path)-1; i++ {
		p := path[i]
		candidates := []point{{p.x + 2, p.y}, {p.x - w/2, p.y - 1}}
		for _, start := range candidates {
			ok := true
			for x := start.x - 1; x <= start.x+w; x++ {
				q := point{x, start.y}
				if !c.inside(q) || occupied[q] || c.cells[c.index(q)].blocked {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			if draw {
				c.text(start, label)
				c.put(point{start.x - 1, start.y}, " ")
				c.put(point{start.x + w, start.y}, " ")
			}
			return true
		}
	}
	return false
}
