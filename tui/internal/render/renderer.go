package render

import (
	"fmt"
	"strings"

	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/tui/internal/text"
)

type Terminal interface {
	Write(data string) error
	Size() (terminal.Size, error)
}

func New(term Terminal) (*Renderer, error) {
	if term == nil {
		return nil, fmt.Errorf("terminal is required")
	}
	return &Renderer{term: term}, nil
}

type Renderer struct {
	term Terminal

	previousLines []string
	renderLines   []string
	previousW     int
	previousH     int

	// cursorRow is the logical row at the end of the rendered content.
	cursorRow int

	// hardwareCursorRow is the logical row where the terminal cursor is believed
	// to be after last write. It may differ from cursorRow after partial patches.
	hardwareCursorRow int

	// previousViewportTop is the logical row that appeared at the top of the
	// terminal viewport after the previous render.
	previousViewportTop int

	// maxLinesRendered tracks the high-water mark for shrink handling.
	maxLinesRendered int

	// fullRedraws is useful for tests and debugging.
	fullRedraws int
}

func (r *Renderer) FullRedraws() int {
	return r.fullRedraws
}

type Request struct {
	Lines  []string
	Width  int
	Height int
	Force  bool
}

func (r *Renderer) Render(req Request) error {
	lines := normalizeLines(req.Lines, req.Width, true, r.renderLines)
	r.renderLines = lines

	widthChanged := r.previousW != 0 && r.previousW != req.Width
	heightChanged := r.previousH != 0 && r.previousH != req.Height

	if req.Force || widthChanged || heightChanged {
		return r.fullRender(lines, req, true)
	}

	if len(r.previousLines) == 0 {
		return r.fullRender(lines, req, false)
	}

	previousBufferLength := req.Height
	if r.previousH > 0 {
		previousBufferLength = r.previousViewportTop + r.previousH
	}

	prevViewportTop := r.previousViewportTop
	if heightChanged {
		prevViewportTop = max(0, previousBufferLength-req.Height)
	}
	viewportTop := prevViewportTop
	hardwareCursorRow := r.hardwareCursorRow

	computeLineDiff := func(targetRow int) int {
		currentScreenRow := hardwareCursorRow - prevViewportTop
		targetScreenRow := targetRow - viewportTop
		return targetScreenRow - currentScreenRow
	}

	firstChanged, lastChanged := changedRange(r.previousLines, lines)
	appendedLines := len(lines) > len(r.previousLines)
	if appendedLines {
		if firstChanged == -1 {
			firstChanged = len(r.previousLines)
		}
		lastChanged = len(lines) - 1
	}
	appendStart := appendedLines && firstChanged == len(r.previousLines) && firstChanged > 0

	if firstChanged == -1 {
		r.previousH = req.Height
		r.previousViewportTop = prevViewportTop
		return nil
	}

	if firstChanged >= len(lines) {
		return r.renderDeletedLines(lines, req, prevViewportTop, computeLineDiff)
	}

	if firstChanged < prevViewportTop {
		return r.fullRender(lines, req, true)
	}

	var b strings.Builder
	b.WriteString(terminal.SyncBegin + terminal.AutoWrapDisable)

	prevViewportBottom := prevViewportTop + req.Height - 1
	moveTargetRow := firstChanged
	if appendStart {
		moveTargetRow = firstChanged - 1
	}

	if moveTargetRow > prevViewportBottom {
		currentScreenRow := max(hardwareCursorRow-prevViewportTop, 0)
		if currentScreenRow >= req.Height {
			currentScreenRow = req.Height - 1
		}

		moveToBottom := req.Height - 1 - currentScreenRow
		if moveToBottom > 0 {
			b.WriteString(terminal.MoveDown(moveToBottom))
		}

		scroll := moveTargetRow - prevViewportBottom
		for range scroll {
			b.WriteString(terminal.CRLF)
		}
		prevViewportTop += scroll
		viewportTop += scroll
		hardwareCursorRow = moveTargetRow
	}

	lineDiff := computeLineDiff(moveTargetRow)
	if lineDiff > 0 {
		b.WriteString(terminal.MoveDown(lineDiff))
	} else if lineDiff < 0 {
		b.WriteString(terminal.MoveUp(-lineDiff))
	}

	if appendStart {
		b.WriteString(terminal.CRLF)
	} else {
		b.WriteString(terminal.CarriageReturn)
	}

	renderEnd := min(lastChanged, len(lines)-1)
	for i := firstChanged; i <= renderEnd; i++ {
		if i > firstChanged {
			b.WriteString(terminal.CRLF)
		}
		b.WriteString(terminal.ClearLine)
		b.WriteString(lines[i])
		b.WriteString(terminal.SGRReset)
	}

	finalCursorRow := renderEnd
	if len(r.previousLines) > len(lines) {
		if renderEnd < len(lines)-1 {
			moveDown := len(lines) - 1 - renderEnd
			b.WriteString(terminal.MoveDown(moveDown))
			finalCursorRow = len(lines) - 1
		}

		extraLines := len(r.previousLines) - len(lines)
		for range extraLines {
			b.WriteString(terminal.CRLF)
			b.WriteString(terminal.ClearLine)
		}
		if extraLines > 0 {
			b.WriteString(terminal.MoveUp(extraLines))
		}
	}

	b.WriteString(terminal.AutoWrapEnable + terminal.SyncEnd)

	if err := r.term.Write(b.String()); err != nil {
		return err
	}

	r.storeState(lines, req, prevViewportTop, finalCursorRow)
	return nil
}

func (r *Renderer) renderDeletedLines(lines []string, req Request, prevViewportTop int, computeLineDiff func(int) int) error {
	if len(r.previousLines) <= len(lines) {
		r.storeState(lines, req, prevViewportTop, max(0, len(lines)-1))
		return nil
	}

	targetRow := max(0, len(lines)-1)
	if targetRow < prevViewportTop {
		return r.fullRender(lines, req, true)
	}

	extraLines := len(r.previousLines) - len(lines)
	if extraLines > req.Height {
		return r.fullRender(lines, req, true)
	}

	var b strings.Builder
	b.WriteString(terminal.SyncBegin + terminal.AutoWrapDisable)

	lineDiff := computeLineDiff(targetRow)
	if lineDiff > 0 {
		b.WriteString(terminal.MoveDown(lineDiff))
	} else if lineDiff < 0 {
		b.WriteString(terminal.MoveUp(-lineDiff))
	}
	b.WriteString(terminal.CarriageReturn)

	if extraLines > 0 {
		b.WriteString(terminal.MoveDown(1))
	}
	for i := range extraLines {
		b.WriteString(terminal.CarriageReturn + terminal.ClearLine)
		if i < extraLines-1 {
			b.WriteString(terminal.MoveDown(1))
		}
	}
	if extraLines > 0 {
		b.WriteString(terminal.MoveUp(extraLines))
	}

	b.WriteString(terminal.AutoWrapEnable + terminal.SyncEnd)

	if err := r.term.Write(b.String()); err != nil {
		return err
	}

	r.storeState(lines, req, prevViewportTop, targetRow)
	return nil
}

func (r *Renderer) fullRender(lines []string, req Request, clear bool) error {
	var b strings.Builder
	b.Grow(renderedLinesSize(lines) + len(terminal.SyncBegin+terminal.AutoWrapDisable+terminal.AutoWrapEnable+terminal.SyncEnd+terminal.ClearScreen+terminal.MoveToTop))
	b.WriteString(terminal.SyncBegin + terminal.AutoWrapDisable)
	if clear {
		b.WriteString(terminal.ClearScreen + terminal.MoveToTop)
	}
	writeLines(&b, lines)
	b.WriteString(terminal.AutoWrapEnable + terminal.SyncEnd)

	if err := r.term.Write(b.String()); err != nil {
		return err
	}

	r.storeLines(lines)
	r.previousW = req.Width
	r.previousH = req.Height
	r.cursorRow = max(0, len(lines)-1)
	r.hardwareCursorRow = r.cursorRow
	r.maxLinesRendered = max(r.maxLinesRendered, len(lines))
	if clear {
		r.maxLinesRendered = len(lines)
	}
	bufferLength := max(req.Height, len(lines))
	r.previousViewportTop = viewportTopFor(bufferLength, req.Height)
	r.fullRedraws++
	return nil
}

func (r *Renderer) storeState(lines []string, req Request, previousViewportTop int, hardwareCursorRow int) {
	r.storeLines(lines)
	r.previousW = req.Width
	r.previousH = req.Height
	r.cursorRow = max(0, len(lines)-1)
	r.hardwareCursorRow = max(0, hardwareCursorRow)
	r.maxLinesRendered = max(r.maxLinesRendered, len(lines))
	r.previousViewportTop = max(previousViewportTop, r.hardwareCursorRow-req.Height+1)
}

func (r *Renderer) storeLines(lines []string) {
	r.previousLines = append(r.previousLines[:0], lines...)
}

func viewportTopFor(lineCount int, height int) int {
	if height <= 0 || lineCount <= height {
		return 0
	}
	return lineCount - height
}

func writeLines(b *strings.Builder, lines []string) {
	for i, line := range lines {
		if i > 0 {
			b.WriteString(terminal.CRLF)
		}
		b.WriteString(line)
		b.WriteString(terminal.SGRReset)
	}
}

func renderedLinesSize(lines []string) int {
	if len(lines) == 0 {
		return 0
	}

	size := len(terminal.CRLF) * (len(lines) - 1)
	size += len(terminal.SGRReset) * len(lines)
	for _, line := range lines {
		size += len(line)
	}
	return size
}

func changedRange(old []string, next []string) (int, int) {
	limit := min(len(old), len(next))
	first := 0
	for first < limit && old[first] == next[first] {
		first++
	}

	if first == limit {
		if len(old) == len(next) {
			return -1, -1
		}
		return first, max(len(old), len(next)) - 1
	}

	if len(old) != len(next) {
		return first, max(len(old), len(next)) - 1
	}

	oldLast := len(old) - 1
	nextLast := len(next) - 1
	for oldLast >= first && nextLast >= first && old[oldLast] == next[nextLast] {
		oldLast--
		nextLast--
	}

	return first, max(oldLast, nextLast)
}

func normalizeLines(lines []string, width int, ensureNonEmpty bool, out []string) []string {
	out = out[:0]
	for _, line := range lines {
		line = strings.ReplaceAll(line, "\n", " ")
		if text.VisibleLen(line) > width {
			line = text.Truncate(line, width)
		}
		out = append(out, line)
	}
	if len(out) == 0 && ensureNonEmpty {
		out = append(out, "")
	}
	return out
}
