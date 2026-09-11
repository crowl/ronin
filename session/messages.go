package session

import (
	"fmt"
	"github.com/crowl/ronin/llm"
	"time"
)

// Message is an entry in the application transcript. Ordinary model messages
// implement this marker too, without depending on session storage or policies.
type Message interface{ TranscriptMessage() }

// ContextSummary is generated context, never a user-authored rewind point.
type ContextSummary struct {
	Timestamp time.Time
	Text      string
}

func (ContextSummary) TranscriptMessage() {}

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

func (WorkflowResultMessage) TranscriptMessage() {}
func (m WorkflowResultMessage) Text() string {
	return "Workflow " + m.Name + " " + string(m.Status) + ".\nInput:\n" + m.Input + "\nSummary:\n" + m.Summary
}

// ModelMessages lowers application entries to provider-neutral input. Unknown
// entries fail explicitly instead of silently disappearing from model context.
func ModelMessages(messages []Message) ([]llm.Message, error) {
	result := make([]llm.Message, 0, len(messages))
	for _, entry := range messages {
		var message llm.Message
		switch m := entry.(type) {
		case ContextSummary:
			message = llm.UserMessage{Timestamp: m.Timestamp, Text: m.Text}
		case WorkflowResultMessage:
			message = llm.UserMessage{Timestamp: m.Timestamp, Text: m.Text()}
		case llm.Message:
			message = m
		default:
			return nil, fmt.Errorf("unsupported transcript entry %T", entry)
		}
		result = append(result, message)
	}
	return result, nil
}
