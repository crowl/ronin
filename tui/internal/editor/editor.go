package editor

import (
	"unicode"

	"github.com/crowl/ronin/tui/internal/terminal"
)

func New() *Readline {
	return &Readline{
		text: make([]rune, 0, 2048),
	}
}

type Readline struct {
	text         []rune
	cursor       int
	history      []string
	historyIndex int
	draft        string
}

func (e *Readline) Text() []rune {
	return e.text
}

func (e *Readline) Cursor() int {
	return e.cursor
}

func (e *Readline) SetText(text string) {
	e.text = []rune(text)
	e.cursor = len(e.text)
}

func (e *Readline) Clear() {
	e.text = nil
	e.cursor = 0
	e.historyIndex = 0
	e.draft = ""
}

const editorLineFeed = '\n'

func (e *Readline) HandleKey(key terminal.Key) (Effect, error) {
	switch key.Type {
	case terminal.KeyRune:
		e.insertRune(key.Rune)
		return TextChange{Text: string(e.text)}, nil
	case terminal.KeyPaste:
		e.insertText(key.Text)
		return TextChange{Text: string(e.text)}, nil
	case terminal.KeyShiftEnter:
		e.insertRune(editorLineFeed)
		return TextChange{Text: string(e.text)}, nil
	case terminal.KeyBackspace:
		if e.cursor > 0 {
			e.text = append(e.text[:e.cursor-1], e.text[e.cursor:]...)
			e.cursor--
			return TextChange{Text: string(e.text)}, nil
		}
		return NoEffect{}, nil
	case terminal.KeyDelete:
		if e.deleteUnderCursor() {
			return TextChange{Text: string(e.text)}, nil
		}
		return NoEffect{}, nil
	case terminal.KeyArrowLeft, terminal.KeyCtrlB:
		if e.cursor > 0 {
			e.cursor--
			return CursorMove{Cursor: e.cursor}, nil
		}
		return NoEffect{}, nil
	case terminal.KeyArrowRight, terminal.KeyCtrlF:
		if e.cursor < len(e.text) {
			e.cursor++
			return CursorMove{Cursor: e.cursor}, nil
		}
		return NoEffect{}, nil
	case terminal.KeyHome:
		if e.cursor != 0 {
			e.cursor = 0
			return CursorMove{Cursor: e.cursor}, nil
		}
		return NoEffect{}, nil
	case terminal.KeyEnd:
		if e.cursor != len(e.text) {
			e.cursor = len(e.text)
			return CursorMove{Cursor: e.cursor}, nil
		}
		return NoEffect{}, nil
	case terminal.KeyCtrlA:
		lineStart := e.lineStart()
		if e.cursor != lineStart {
			e.cursor = lineStart
			return CursorMove{Cursor: e.cursor}, nil
		}
		return NoEffect{}, nil
	case terminal.KeyCtrlE:
		lineEnd := e.lineEnd()
		if e.cursor != lineEnd {
			e.cursor = lineEnd
			return CursorMove{Cursor: e.cursor}, nil
		}
		return NoEffect{}, nil
	case terminal.KeyArrowUp:
		if e.historyPrev() {
			return TextChange{Text: string(e.text)}, nil
		}
		return NoEffect{}, nil
	case terminal.KeyArrowDown:
		if e.historyNext() {
			return TextChange{Text: string(e.text)}, nil
		}
		return NoEffect{}, nil
	case terminal.KeyCtrlK:
		if e.deleteToLineEnd() {
			return TextChange{Text: string(e.text)}, nil
		}
		return NoEffect{}, nil
	case terminal.KeyCtrlU:
		if e.deleteToLineStart() {
			return TextChange{Text: string(e.text)}, nil
		}
		return NoEffect{}, nil
	case terminal.KeyCtrlW:
		if e.deletePreviousWord() {
			return TextChange{Text: string(e.text)}, nil
		}
		return NoEffect{}, nil
	case terminal.KeyEnter:
		prompt := string(e.text)
		if prompt == "" {
			return NoEffect{}, nil
		}
		e.addHistory(prompt)
		return SubmitPrompt{Prompt: prompt}, nil
	case terminal.KeyCtrlD:
		if e.deleteUnderCursor() {
			return TextChange{Text: string(e.text)}, nil
		}
		return NoEffect{}, nil
	default:
		return NoEffect{}, nil
	}
}

func (e *Readline) insertRune(r rune) {
	e.text = append(e.text, 0)
	copy(e.text[e.cursor+1:], e.text[e.cursor:])
	e.text[e.cursor] = r
	e.cursor++
	e.historyIndex = 0
}

func (e *Readline) insertText(text string) {
	runes := []rune(text)
	if len(runes) == 0 {
		return
	}
	next := make([]rune, 0, len(e.text)+len(runes))
	next = append(next, e.text[:e.cursor]...)
	next = append(next, runes...)
	next = append(next, e.text[e.cursor:]...)
	e.text = next
	e.cursor += len(runes)
	e.historyIndex = 0
}

func (e *Readline) deleteUnderCursor() bool {
	if e.cursor >= len(e.text) {
		return false
	}
	e.text = append(e.text[:e.cursor], e.text[e.cursor+1:]...)
	e.historyIndex = 0
	return true
}

func (e *Readline) deleteToLineStart() bool {
	lineStart := e.lineStart()
	if e.cursor == lineStart {
		return false
	}
	e.text = append(e.text[:lineStart], e.text[e.cursor:]...)
	e.cursor = lineStart
	e.historyIndex = 0
	return true
}

func (e *Readline) deleteToLineEnd() bool {
	lineEnd := e.lineEnd()
	if e.cursor == lineEnd {
		return false
	}
	e.text = append(e.text[:e.cursor], e.text[lineEnd:]...)
	e.historyIndex = 0
	return true
}

func (e *Readline) deletePreviousWord() bool {
	if e.cursor == 0 {
		return false
	}
	start := e.cursor
	for start > 0 && unicode.IsSpace(e.text[start-1]) {
		start--
	}
	for start > 0 && !unicode.IsSpace(e.text[start-1]) {
		start--
	}
	if start == e.cursor {
		return false
	}
	e.text = append(e.text[:start], e.text[e.cursor:]...)
	e.cursor = start
	e.historyIndex = 0
	return true
}

func (e *Readline) lineStart() int {
	for index := e.cursor - 1; index >= 0; index-- {
		if e.text[index] == editorLineFeed {
			return index + 1
		}
	}
	return 0
}

func (e *Readline) lineEnd() int {
	for index := e.cursor; index < len(e.text); index++ {
		if e.text[index] == editorLineFeed {
			return index
		}
	}
	return len(e.text)
}

func (e *Readline) addHistory(text string) {
	if text == "" {
		return
	}
	if len(e.history) > 0 && e.history[len(e.history)-1] == text {
		return
	}
	e.history = append(e.history, text)
}

func (e *Readline) historyPrev() bool {
	if len(e.history) == 0 {
		return false
	}
	if e.historyIndex == 0 {
		e.draft = string(e.text)
	}
	if e.historyIndex >= len(e.history) {
		return false
	}
	e.historyIndex++
	e.SetText(e.history[len(e.history)-e.historyIndex])
	return true
}

func (e *Readline) historyNext() bool {
	if e.historyIndex == 0 {
		return false
	}
	e.historyIndex--
	if e.historyIndex == 0 {
		e.SetText(e.draft)
		e.draft = ""
		return true
	}
	e.SetText(e.history[len(e.history)-e.historyIndex])
	return true
}
