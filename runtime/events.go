package runtime

import (
	"encoding/json"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/tool"
)

type PromptProcessingStarted struct{}

type ConversationTurnStarted struct {
	Turn int
}

type AssistantMessageStarted struct {
	Message llm.AssistantMessage
}

type AssistantThinkingDeltaReceived struct {
	Text string
}

type AssistantMessageDeltaReceived struct {
	Text string
}

type AssistantMessageEnded struct {
	Message llm.AssistantMessage
}

type ToolExecutionStarted struct {
	Tool          Tool
	CallID        string
	CallArguments json.RawMessage
	CallTitle     string
}

type ToolExecutionOutputDeltaReceived struct {
	Tool     Tool
	CallID   string
	Artifact tool.Artifact
}

type ToolExecutionResultReceived struct {
	Tool      Tool
	CallID    string
	Artifacts []tool.Artifact
}

type ToolExecutionFailed struct {
	Tool   Tool
	CallID string
	Error  error
}

type ToolExecutionEnded struct {
	Tool   Tool
	CallID string
}

type ConversationTurnEnded struct {
	Turn int
}

type PromptProcessingEnded struct{}

type PromptProcessingError struct {
	Error error
}

type SessionSaveFailed struct {
	Error error
}

// Event is a sealed interface to mark runtime events
type Event interface{ event() }

func (PromptProcessingStarted) event()          {}
func (ConversationTurnStarted) event()          {}
func (AssistantMessageStarted) event()          {}
func (AssistantMessageDeltaReceived) event()    {}
func (AssistantThinkingDeltaReceived) event()   {}
func (AssistantMessageEnded) event()            {}
func (ToolExecutionStarted) event()             {}
func (ToolExecutionOutputDeltaReceived) event() {}
func (ToolExecutionResultReceived) event()      {}
func (ToolExecutionFailed) event()              {}
func (ToolExecutionEnded) event()               {}
func (ConversationTurnEnded) event()            {}
func (PromptProcessingEnded) event()            {}
func (PromptProcessingError) event()            {}
func (SessionSaveFailed) event()                {}
