package editor

type NoEffect struct{}

type TextChange struct{ Text string }

type CursorMove struct{ Cursor int }

type SubmitPrompt struct{ Prompt string }

type Effect interface{ editorEffect() }

func (NoEffect) editorEffect()     {}
func (TextChange) editorEffect()   {}
func (CursorMove) editorEffect()   {}
func (SubmitPrompt) editorEffect() {}
