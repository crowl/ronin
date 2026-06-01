package tui

import (
	"github.com/crowl/ronin/agent"
	"github.com/crowl/ronin/llm"
)

type StartNewConversation struct{}

type CompactConversation struct{}

type SwitchModel struct{ Model llm.Model }

type SwitchReasoningLevel struct{ Level llm.ReasoningLevel }

type InvokeSkill struct{ Skill agent.Skill }

type Exit struct{}

// Command is a sealed interface to mark all application commands
type Command interface{ command() }

func (StartNewConversation) command() {}
func (CompactConversation) command()  {}
func (SwitchModel) command()          {}
func (SwitchReasoningLevel) command() {}
func (InvokeSkill) command()          {}
func (Exit) command()                 {}
