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
	StopReasonFinished                   StopReason = "finished"
	StopReasonEndTurn                    StopReason = "end_turn"
	StopReasonMaxTokens                  StopReason = "max_tokens"
	StopReasonStopSequence               StopReason = "stop_sequence"
	StopReasonToolUse                    StopReason = "tool_use"
	StopReasonPauseTurn                  StopReason = "pause_turn"
	StopReasonRefusal                    StopReason = "refusal"
	StopReasonModelContextWindowExceeded StopReason = "model_context_window_exceeded"
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
	Provider  string
}

// RedactedThinkingBlock contains opaque Anthropic thinking data that must be
// returned unchanged when continuing a tool-use conversation.
type RedactedThinkingBlock struct {
	Data     string
	Provider string
}

type ToolCallBlock struct {
	ID               string
	Name             string
	Arguments        json.RawMessage
	ThoughtSignature string
	ThoughtProvider  string
}

type AssistantBlock interface{ assistantBlock() }

func (TextBlock) assistantBlock()             {}
func (ThinkingBlock) assistantBlock()         {}
func (RedactedThinkingBlock) assistantBlock() {}
func (ToolCallBlock) assistantBlock()         {}

type Usage struct {
	InputTokens      int
	OutputTokens     int
	CachedTokens     int
	CacheWriteTokens int
	TotalTokens      int
	Cost             Cost
}

type Cost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
	Total      float64
	Available  bool
}

func EstimateCost(model Model, usage Usage) Cost {
	uncachedInput := max(usage.InputTokens-usage.CachedTokens-usage.CacheWriteTokens, 0)
	cost := Cost{Available: true}
	if uncachedInput > 0 && !model.Pricing.HasInput || usage.OutputTokens > 0 && !model.Pricing.HasOutput || usage.CachedTokens > 0 && !model.Pricing.HasCacheRead || usage.CacheWriteTokens > 0 && !model.Pricing.HasCacheWrite {
		cost.Available = false
		return cost
	}
	const tokensPerMillion = 1_000_000
	cost.Input = float64(uncachedInput) * model.Pricing.Input / tokensPerMillion
	cost.Output = float64(usage.OutputTokens) * model.Pricing.Output / tokensPerMillion
	cost.CacheRead = float64(usage.CachedTokens) * model.Pricing.CacheRead / tokensPerMillion
	cost.CacheWrite = float64(usage.CacheWriteTokens) * model.Pricing.CacheWrite / tokensPerMillion
	cost.Total = cost.Input + cost.Output + cost.CacheRead + cost.CacheWrite
	return cost
}

type SessionCost struct {
	Total     float64
	Available bool
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

func ProjectMessagesForProvider(messages []Message, provider string) []Message {
	projected := make([]Message, 0, len(messages))
	for _, message := range messages {
		assistant, ok := message.(AssistantMessage)
		if !ok {
			projected = append(projected, message)
			continue
		}
		blocks := make([]AssistantBlock, 0, len(assistant.Blocks))
		for _, block := range assistant.Blocks {
			switch typed := block.(type) {
			case ThinkingBlock:
				if typed.Provider == provider && provider != "" {
					blocks = append(blocks, typed)
				}
			case RedactedThinkingBlock:
				if typed.Provider == provider && provider != "" {
					blocks = append(blocks, typed)
				}
			case ToolCallBlock:
				if typed.ThoughtProvider != provider || provider == "" {
					typed.ThoughtSignature = ""
					typed.ThoughtProvider = ""
				}
				blocks = append(blocks, typed)
			default:
				blocks = append(blocks, block)
			}
		}
		if len(blocks) == 0 {
			continue
		}
		assistant.Blocks = blocks
		projected = append(projected, assistant)
	}
	return projected
}

// Message is a sealed interface that marks all LLM messages
type Message interface{ message() }

func (UserMessage) message()           {}
func (AssistantMessage) message()      {}
func (WorkflowResultMessage) message() {}
func (ToolOutputMessage) message()     {}
func (ToolErrorMessage) message()      {}
func (ErrorMessage) message()          {}
