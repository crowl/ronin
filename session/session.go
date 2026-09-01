package session

import (
	"context"
	"time"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/llm"
)

const Version = 1

// Store persists sessions as an append-only event journal plus mutable
// per-session metadata. Load and Latest reconstruct the effective message
// history from the journal.
type Store interface {
	Create(ctx context.Context, workingDir string, metadata Metadata) (Session, error)
	Append(ctx context.Context, sessionID string, event Event) error
	UpdateMetadata(ctx context.Context, sessionID string, metadata Metadata) error
	Load(ctx context.Context, sessionID string) (Session, []llm.Message, bool, error)
	Latest(ctx context.Context, workingDir string) (Session, []llm.Message, bool, error)
	List(ctx context.Context, workingDir string) ([]Ref, error)
	Delete(ctx context.Context, sessionID string) error
	Clear(ctx context.Context, workingDir string) error
}

// ForkStore atomically creates a child session with its initial journal event.
type ForkStore interface {
	Fork(ctx context.Context, parentID string, metadata Metadata, event Event) (Session, error)
}

// ModelSwitchStore atomically records a model transition and updates metadata.
type ModelSwitchStore interface {
	SwitchModel(ctx context.Context, sessionID string, metadata Metadata, event Event) error
}

// EventType distinguishes journal entries.
type EventType string

const (
	// EventMessage carries a single conversation message.
	EventMessage EventType = "message"
	// EventContextReset carries the effective message set that replaces prior
	// history after an operation such as rewind or fork.
	EventContextReset EventType = "context_reset"
	// EventModelChanged records a model transition without changing messages.
	EventModelChanged EventType = "model_changed"
	// EventCompaction carries the effective message set that replaces prior
	// history when reconstructing context.
	EventCompaction EventType = "compaction"
)

// Event is a single append-only journal entry for a session.
type Event struct {
	Seq            int64
	Type           EventType
	CreatedAt      time.Time
	Message        llm.Message
	Compacted      []llm.Message
	ResetReason    string
	PreviousModel  config.Model
	Model          config.Model
	ReasoningLevel string
}

// Reconstruct rebuilds the effective message history from an ordered journal.
// A compaction event resets the accumulated context to its effective set.
func Reconstruct(events []Event) []llm.Message {
	var messages []llm.Message
	for _, event := range events {
		switch event.Type {
		case EventMessage:
			if event.Message != nil {
				messages = append(messages, event.Message)
			}
		case EventCompaction, EventContextReset:
			messages = append([]llm.Message(nil), event.Compacted...)
		case EventModelChanged:
			// Model changes do not affect effective message history.
		}
	}
	return messages
}

type Metadata struct {
	Title          string
	Model          config.Model
	ReasoningLevel string
}

type Ref struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	ParentID  string    `json:"parent_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Session struct {
	Version        int
	ID             string
	Title          string
	WorkingDir     string
	ParentID       string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Model          config.Model
	ReasoningLevel string
	Cost           llm.SessionCost
}
