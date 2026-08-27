package llm

import (
	"encoding/json"
	"strings"
	"time"
)

type UserMessage struct {
	Timestamp time.Time
	Text      string
}

type StopReason string

const (
	StopReasonToolUse  StopReason = "tool_use"
	StopReasonFinished StopReason = "finished"
)

type AssistantMessage struct {
	Timestamp  time.Time
	Blocks     []AssistantBlock
	StopReason StopReason
	Usage      Usage
}

type TextBlock struct {
	Text string
}

type ThinkingBlock struct {
	Text      string
	Signature string
}

// RedactedThinkingBlock contains opaque Anthropic thinking data that must be
// returned unchanged when continuing a tool-use conversation.
type RedactedThinkingBlock struct {
	Data string
}

type ToolCallBlock struct {
	ID               string
	Name             string
	Arguments        json.RawMessage
	ThoughtSignature string
}

type AssistantBlock interface{ assistantBlock() }

func (TextBlock) assistantBlock()             {}
func (ThinkingBlock) assistantBlock()         {}
func (RedactedThinkingBlock) assistantBlock() {}
func (ToolCallBlock) assistantBlock()         {}

type Usage struct {
	InputTokens  int
	OutputTokens int
	CachedTokens int
	TotalTokens  int
	Cost         Cost
}

type Cost struct {
	InputTokens      float64
	OutputTokens     float64
	CacheReadTokens  float64
	CacheWriteTokens float64
	TotalTokens      float64
}

func (m AssistantMessage) Text() string {
	var text strings.Builder
	for _, block := range m.Blocks {
		switch b := block.(type) {
		case TextBlock:
			text.WriteString(b.Text)
		}
	}
	return text.String()
}

func (m AssistantMessage) Thinking() string {
	var text strings.Builder
	for _, block := range m.Blocks {
		switch b := block.(type) {
		case ThinkingBlock:
			text.WriteString(b.Text)
		}
	}
	return text.String()
}

type WorkflowStatus string

const (
	WorkflowStatusCompleted WorkflowStatus = "completed"
	WorkflowStatusFailed    WorkflowStatus = "failed"
	WorkflowStatusCancelled WorkflowStatus = "cancelled"
)

type WorkflowResultMessage struct {
	Timestamp time.Time
	Name      string
	Input     string
	Status    WorkflowStatus
	Summary   string
}

func (m WorkflowResultMessage) Text() string {
	return "Workflow " + m.Name + " " + string(m.Status) + ".\nInput:\n" + m.Input + "\nSummary:\n" + m.Summary
}

type ToolOutputMessage struct {
	Timestamp               time.Time
	ToolName                string
	ToolCallID              string
	ToolOutput              string
	RawToolOutput           string
	ToolOutputWasSummarized bool
}

type ToolErrorMessage struct {
	Timestamp  time.Time
	ToolName   string
	ToolCallID string
	Error      error
}

type ErrorMessage struct {
	Timestamp time.Time
	Error     error
}

// Message is a sealed interface that marks all LLM messages
type Message interface{ message() }

func (UserMessage) message()           {}
func (AssistantMessage) message()      {}
func (WorkflowResultMessage) message() {}
func (ToolOutputMessage) message()     {}
func (ToolErrorMessage) message()      {}
func (ErrorMessage) message()          {}
