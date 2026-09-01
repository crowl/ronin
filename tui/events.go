package tui

import (
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/tui/internal/terminal"
	"github.com/crowl/ronin/workflow"
)

type terminalKeyRead struct{ Key terminal.Key }

type terminalReadFailed struct{ Err error }

type terminalResized struct{}

type workingTick struct{}

type renderRequested struct{}

type conversationEventReceived struct{ Event runtime.Event }

type conversationErrorReceived struct{ Err error }

type conversationPromptDone struct{}

type conversationCompactionDone struct{ Err error }

type mcpActivationDone struct {
	Item      menuItem
	Activated bool
	Err       error
}

type workflowEventReceived struct{ Event workflow.Event }

type workflowDone struct{ Err error }

// event is a sealed interface to mark all application events
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
func (workflowEventReceived) event()      {}
func (workflowDone) event()               {}
