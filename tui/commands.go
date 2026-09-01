package tui

import (
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/workflow"
)

type StartNewConversation struct{}

type RewindConversation struct{}

type ForkConversation struct{}

type rewindConversationAt struct {
	Point        runtime.RewindPoint
	RemovedTurns int
}

type forkConversationAt struct{ Point runtime.RewindPoint }

type CompactConversation struct{}

type SwitchModel struct{ Model llm.Model }

type SwitchReasoningLevel struct{ Level llm.ReasoningLevel }

type InvokeSkill struct{ Skill runtime.Skill }

type InvokeWorkflow struct{ Workflow workflow.Workflow }

type ActivateMCP struct{ Name string }

type Exit struct{}

// Command is a sealed interface to mark all application commands
type Command interface{ command() }

func (StartNewConversation) command() {}
func (RewindConversation) command()   {}
func (ForkConversation) command()     {}
func (rewindConversationAt) command() {}
func (forkConversationAt) command()   {}
func (CompactConversation) command()  {}
func (SwitchModel) command()          {}
func (SwitchReasoningLevel) command() {}
func (InvokeSkill) command()          {}
func (InvokeWorkflow) command()       {}
func (ActivateMCP) command()          {}
func (Exit) command()                 {}
