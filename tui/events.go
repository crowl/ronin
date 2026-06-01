package tui

import (
	"github.com/crowl/ronin/agent"
	"github.com/crowl/ronin/tui/internal/terminal"
)

type terminalKeyRead struct{ Key terminal.Key }

type terminalReadFailed struct{ Err error }

type terminalResized struct{}

type workingTick struct{}

type renderRequested struct{}

type agentEventReceived struct{ Event agent.Event }

type agentErrorReceived struct{ Err error }

type agentPromptDone struct{}

// event is a sealed interface to mark all application events
type event interface{ event() }

func (terminalKeyRead) event()    {}
func (terminalReadFailed) event() {}
func (terminalResized) event()    {}
func (workingTick) event()        {}
func (renderRequested) event()    {}
func (agentEventReceived) event() {}
func (agentErrorReceived) event() {}
func (agentPromptDone) event()    {}
