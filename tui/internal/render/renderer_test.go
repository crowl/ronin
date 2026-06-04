package render_test

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/crowl/ronin/tui/internal/render"
	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/tui/internal/text"
)

func TestRenderer(t *testing.T) {
	t.Run("first render does not clear screen", func(t *testing.T) {
		term := newVirtualTerminal(20, 12)
		renderer := newRenderer(t, term)

		if err := renderer.Render(render.Request{Lines: []string{"one", "two"}, Width: 20, Height: 12}); err != nil {
			t.Fatalf("render initial frame: %v", err)
		}

		output := term.lastWrite()
		if strings.Contains(output, terminal.ClearScreen) || strings.Contains(output, terminal.MoveToTop) {
			t.Fatalf("first render should not clear or home the terminal: %q", output)
		}
		if !strings.Contains(text.StripANSI(output), "one"+terminal.CRLF+"two") {
			t.Fatalf("first render did not write all lines: %q", output)
		}
	})

	t.Run("appends only new lines", func(t *testing.T) {
		term := newVirtualTerminal(20, 12)
		renderer := newRenderer(t, term)

		if err := renderer.Render(render.Request{Lines: []string{"one", "two"}, Width: 20, Height: 12}); err != nil {
			t.Fatalf("render initial frame: %v", err)
		}
		if err := renderer.Render(render.Request{Lines: []string{"one", "two", "three"}, Width: 20, Height: 12}); err != nil {
			t.Fatalf("render appended frame: %v", err)
		}

		output := text.StripANSI(term.lastWrite())
		if strings.Contains(output, "one") || strings.Contains(output, "two") {
			t.Fatalf("old lines were reprinted during append: %q", term.lastWrite())
		}
		if !strings.Contains(output, "three") {
			t.Fatalf("new line was not appended: %q", term.lastWrite())
		}
	})

	t.Run("patches streaming line without appending duplicate", func(t *testing.T) {
		term := newVirtualTerminal(40, 12)
		renderer := newRenderer(t, term)

		if err := renderer.Render(render.Request{Lines: []string{"hello", "Hello! How can I", "editor"}, Width: 40, Height: 12}); err != nil {
			t.Fatalf("render partial stream: %v", err)
		}
		if err := renderer.Render(render.Request{Lines: []string{"hello", "Hello! How can I help?", "editor"}, Width: 40, Height: 12}); err != nil {
			t.Fatalf("render updated stream: %v", err)
		}

		output := text.StripANSI(term.lastWrite())
		if strings.Contains(output, "hello") || strings.Contains(output, "Hello! How can I"+terminal.CRLF) {
			t.Fatalf("unchanged/stale lines were reprinted: %q", term.lastWrite())
		}
		if !strings.Contains(term.lastWrite(), terminal.ClearLine+"Hello! How can I help?") {
			t.Fatalf("changed streaming line was not patched: %q", term.lastWrite())
		}

		assertViewportContainsInOrder(t, term.viewport(), []string{"hello", "Hello! How can I help?", "editor"})
	})

	t.Run("patches middle line without shifting", func(t *testing.T) {
		term := newVirtualTerminal(40, 10)
		renderer := newRenderer(t, term)

		if err := renderer.Render(render.Request{Lines: []string{"Header", "Working...", "Footer"}, Width: 40, Height: 10}); err != nil {
			t.Fatalf("render initial frame: %v", err)
		}
		if err := renderer.Render(render.Request{Lines: []string{"Header", "Working.", "Footer"}, Width: 40, Height: 10}); err != nil {
			t.Fatalf("render updated frame: %v", err)
		}

		assertViewportContainsInOrder(t, term.viewport(), []string{"Header", "Working.", "Footer"})
	})

	t.Run("clears stale rows when line count shrinks", func(t *testing.T) {
		term := newVirtualTerminal(40, 12)
		renderer := newRenderer(t, term)

		if err := renderer.Render(render.Request{Lines: []string{"editor", "menu one", "menu two", "status"}, Width: 40, Height: 12}); err != nil {
			t.Fatalf("render menu open: %v", err)
		}
		if err := renderer.Render(render.Request{Lines: []string{"editor", "status"}, Width: 40, Height: 12}); err != nil {
			t.Fatalf("render menu closed: %v", err)
		}

		view := strings.Join(term.viewport(), "\n")
		if strings.Contains(view, "menu one") || strings.Contains(view, "menu two") {
			t.Fatalf("stale menu rows remained visible:\n%s", view)
		}
		assertViewportContainsInOrder(t, term.viewport(), []string{"editor", "status"})
	})

	t.Run("tool elapsed changes to took while appending", func(t *testing.T) {
		term := newVirtualTerminal(40, 6)
		renderer := newRenderer(t, term)

		if err := renderer.Render(render.Request{
			Lines:  []string{"", " read_file llm/model.go", "", " Elapsed 0.0s", "", " editor", " status"},
			Width:  40,
			Height: 6,
		}); err != nil {
			t.Fatalf("render running tool frame: %v", err)
		}
		if err := renderer.Render(render.Request{
			Lines:  []string{"", " read_file llm/model.go", "", " Took 0.0s", "", " I will read llm/registry.go next.", "", " editor", " status"},
			Width:  40,
			Height: 6,
		}); err != nil {
			t.Fatalf("render completed tool plus assistant frame: %v", err)
		}

		view := strings.Join(term.viewport(), "\n")
		if strings.Contains(view, "Elapsed 0.0s") {
			t.Fatalf("stale elapsed line remained visible:\n%s", view)
		}
		if !strings.Contains(view, "Took 0.0s") {
			t.Fatalf("took line missing:\n%s", view)
		}
		if !strings.Contains(view, "I will read llm/registry.go next.") {
			t.Fatalf("assistant line missing:\n%s", view)
		}
	})

	t.Run("append only preserves natural scrollback", func(t *testing.T) {
		term := newVirtualTerminal(20, 5)
		renderer := newRenderer(t, term)

		var lines []string
		for i := range 10 {
			lines = append(lines, "Line "+strconv.Itoa(i))
			if err := renderer.Render(render.Request{Lines: lines, Width: 20, Height: 5}); err != nil {
				t.Fatalf("render line %d: %v", i, err)
			}
		}

		assertViewportEquals(t, term.viewport(), []string{"Line 5", "Line 6", "Line 7", "Line 8", "Line 9"})
	})

	t.Run("first changed above viewport full redraws", func(t *testing.T) {
		term := newVirtualTerminal(20, 5)
		renderer := newRenderer(t, term)

		lines := make([]string, 20)
		for i := range lines {
			lines[i] = "Line " + strconv.Itoa(i)
		}
		if err := renderer.Render(render.Request{Lines: lines, Width: 20, Height: 5}); err != nil {
			t.Fatalf("render initial frame: %v", err)
		}

		redraws := renderer.FullRedraws()
		lines[0] = "Changed 0"
		if err := renderer.Render(render.Request{Lines: lines, Width: 20, Height: 5}); err != nil {
			t.Fatalf("render changed frame: %v", err)
		}
		if renderer.FullRedraws() <= redraws {
			t.Fatalf("expected full redraw when first changed line is above viewport")
		}
	})

	t.Run("noop writes nothing", func(t *testing.T) {
		term := newVirtualTerminal(20, 12)
		renderer := newRenderer(t, term)

		if err := renderer.Render(render.Request{Lines: []string{"one", "two"}, Width: 20, Height: 12}); err != nil {
			t.Fatalf("render initial frame: %v", err)
		}
		writes := len(term.writes)
		if err := renderer.Render(render.Request{Lines: []string{"one", "two"}, Width: 20, Height: 12}); err != nil {
			t.Fatalf("render unchanged frame: %v", err)
		}
		if len(term.writes) != writes {
			t.Fatalf("noop render wrote output: %q", term.lastWrite())
		}
	})

	t.Run("force clears and redraws", func(t *testing.T) {
		term := newVirtualTerminal(20, 12)
		renderer := newRenderer(t, term)

		if err := renderer.Render(render.Request{Lines: []string{"one"}, Width: 20, Height: 12}); err != nil {
			t.Fatalf("render initial frame: %v", err)
		}
		if err := renderer.Render(render.Request{Lines: []string{"one"}, Width: 20, Height: 12, Force: true}); err != nil {
			t.Fatalf("render forced frame: %v", err)
		}

		output := term.lastWrite()
		if !strings.Contains(output, terminal.ClearScreen+terminal.MoveToTop) {
			t.Fatalf("forced render did not clear and home terminal: %q", output)
		}
	})
}

func newRenderer(t *testing.T, term *virtualTerminal) *render.Renderer {
	t.Helper()

	renderer, err := render.New(term)
	if err != nil {
		t.Fatalf("create renderer: %v", err)
	}
	return renderer
}

type virtualTerminal struct {
	width  int
	height int
	row    int
	col    int
	lines  [][]rune
	writes []string
}

func newVirtualTerminal(width int, height int) *virtualTerminal {
	term := &virtualTerminal{width: width, height: height}
	for range height {
		term.lines = append(term.lines, blankRunes(width))
	}
	return term
}

func (t *virtualTerminal) Write(data string) error {
	t.writes = append(t.writes, data)
	for index := 0; index < len(data); {
		switch data[index] {
		case '\x1b':
			index = t.applyTerminalEscape(data, index)
		case '\r':
			t.col = 0
			index++
		case '\n':
			t.lineFeed()
			index++
		default:
			r, size := utf8.DecodeRuneInString(data[index:])
			if r == utf8.RuneError && size == 0 {
				return nil
			}
			if r >= ' ' {
				t.writeRune(r)
			}
			index += size
		}
	}
	return nil
}

func (t *virtualTerminal) Size() (terminal.Size, error) {
	return terminal.Size{Width: t.width, Height: t.height}, nil
}

func (t *virtualTerminal) lastWrite() string {
	if len(t.writes) == 0 {
		return ""
	}
	return t.writes[len(t.writes)-1]
}

func (t *virtualTerminal) viewport() []string {
	start := len(t.lines) - t.height
	if start < 0 {
		start = 0
	}
	out := make([]string, 0, t.height)
	for row := start; row < len(t.lines); row++ {
		out = append(out, strings.TrimRight(string(t.lines[row]), " "))
	}
	for len(out) < t.height {
		out = append([]string{""}, out...)
	}
	return out
}

func (t *virtualTerminal) applyTerminalEscape(data string, start int) int {
	if start+1 >= len(data) {
		return len(data)
	}
	if data[start+1] != '[' {
		return start + 2
	}

	end := start + 2
	for end < len(data) && !isAnsiFinalByte(rune(data[end])) {
		end++
	}
	if end >= len(data) {
		return len(data)
	}

	params := data[start+2 : end]
	command := data[end]
	t.applyCSI(params, command)
	return end + 1
}

func (t *virtualTerminal) applyCSI(params string, command byte) {
	switch command {
	case 'A':
		t.moveUp(parseCSIInt(params, 1))
	case 'B':
		t.moveDown(parseCSIInt(params, 1))
	case 'H':
		row, col := parseMoveTo(params)
		t.moveTo(row-1, col-1)
	case 'J':
		if params == "2" {
			t.clearScreen()
		}
	case 'K':
		if params == "2" {
			t.clearLine()
		}
	case 'h', 'l', 'm':
		return
	}
}

func (t *virtualTerminal) lineFeed() {
	if t.row >= len(t.lines)-1 {
		t.lines = append(t.lines, blankRunes(t.width))
		t.row = len(t.lines) - 1
		return
	}
	t.row++
}

func (t *virtualTerminal) moveUp(n int) {
	t.row -= n
	if t.row < 0 {
		t.row = 0
	}
}

func (t *virtualTerminal) moveDown(n int) {
	t.row += n
	if t.row >= len(t.lines) {
		t.row = len(t.lines) - 1
	}
}

func (t *virtualTerminal) moveTo(row int, col int) {
	if row < 0 {
		row = 0
	}
	if row >= len(t.lines) {
		row = len(t.lines) - 1
	}
	if col < 0 {
		col = 0
	}
	if col >= t.width {
		col = t.width - 1
	}
	t.row = row
	t.col = col
}

func (t *virtualTerminal) clearScreen() {
	start := len(t.lines) - t.height
	if start < 0 {
		start = 0
	}
	for row := start; row < len(t.lines); row++ {
		t.lines[row] = blankRunes(t.width)
	}
}

func (t *virtualTerminal) clearLine() {
	if t.row < 0 || t.row >= len(t.lines) {
		return
	}
	t.lines[t.row] = blankRunes(t.width)
}

func (t *virtualTerminal) writeRune(r rune) {
	if t.row < 0 || t.row >= len(t.lines) || t.col < 0 || t.col >= t.width {
		return
	}
	t.lines[t.row][t.col] = r
	t.col++
}

func blankRunes(width int) []rune {
	line := make([]rune, width)
	for i := range line {
		line[i] = ' '
	}
	return line
}

func assertViewportContainsInOrder(t *testing.T, viewport []string, want []string) {
	t.Helper()
	index := 0
	for _, line := range viewport {
		if index < len(want) && strings.Contains(line, want[index]) {
			index++
		}
	}
	if index != len(want) {
		t.Fatalf("viewport did not contain expected lines in order\nviewport: %#v\nwant:     %#v", viewport, want)
	}
}

func assertViewportEquals(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("viewport length\ngot:  %d %#v\nwant: %d %#v", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("viewport\ngot:  %#v\nwant: %#v", got, want)
		}
	}
}

func parseMoveTo(params string) (int, int) {
	if params == "" {
		return 1, 1
	}
	parts := strings.Split(params, ";")
	row := parseCSIInt(parts[0], 1)
	col := 1
	if len(parts) > 1 {
		col = parseCSIInt(parts[1], 1)
	}
	return row, col
}

func parseCSIInt(v string, fallback int) int {
	if v == "" {
		return fallback
	}
	if strings.HasPrefix(v, "?") {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func isAnsiFinalByte(r rune) bool {
	return r >= 0x40 && r <= 0x7e
}
