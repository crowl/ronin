package tui

import (
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
)

type StartNewConversation struct{}

type CompactConversation struct{}

type SwitchModel struct{ Model llm.Model }

type SwitchReasoningLevel struct{ Level llm.ReasoningLevel }

type SwitchTheme struct {
	Name  string
	Theme Theme
}

type InvokeSkill struct{ Skill runtime.Skill }

type Exit struct{}

// Command is a sealed interface to mark all application commands
type Command interface{ command() }

func (StartNewConversation) command() {}
func (CompactConversation) command()  {}
func (SwitchModel) command()          {}
func (SwitchReasoningLevel) command() {}
func (SwitchTheme) command()          {}
func (InvokeSkill) command()          {}
func (Exit) command()                 {}
