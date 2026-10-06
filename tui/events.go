package tui

import (
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tool/shell"
	"github.com/crowl/ronin/tui/internal/terminal"
)

type terminalKeyRead struct{ Key terminal.Key }
type terminalReadFailed struct{ Err error }
type terminalResized struct{}
type workingTick struct{}
type renderRequested struct{}
type conversationEventReceived struct{ Event runtime.Event }
type conversationErrorReceived struct{ Err error }
type conversationPromptDone struct{ Cancelled bool }
type conversationCompactionDone struct{ Err error }
type mcpActivationDone struct {
	Item      menuItem
	Activated bool
	Err       error
}
type shellOutputReceived struct {
	Stream tool.ShellStream
	Text   string
}

func (shellOutputReceived) event() {}

type shellCommandDone struct {
	Command string
	Result  shell.Result
	Err     error
}

type event interface{ event() }

func (terminalKeyRead) event()            {}
func (terminalReadFailed) event()         {}
func (terminalResized) event()            {}
func (workingTick) event()                {}
func (renderRequested) event()            {}
func (conversationEventReceived) event()  {}
func (conversationErrorReceived) event()  {}
func (conversationPromptDone) event()     {}
func (conversationCompactionDone) event() {}
func (mcpActivationDone) event()          {}
func (shellCommandDone) event()           {}
