package session

import (
	"context"
	"fmt"
	"strings"
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
	// EventUsage records auxiliary model costs without adding model context.
	EventUsage EventType = "usage"
	// EventMessage carries a single conversation message.
	EventMessage EventType = "message"
	// EventContextReset carries the effective message set that replaces prior
	// history after an operation such as rewind or fork.
	EventContextReset EventType = "context_reset"
	// EventModelChanged records a model transition without changing messages.
	EventModelChanged EventType = "model_changed"
	// EventCompaction carries the effective message set that replaces prior
	// history when reconstructing context.
	EventCompaction   EventType = "compaction"
	EventShellCommand EventType = "shell_command"
	EventShellOutput  EventType = "shell_output"
	EventShellStatus  EventType = "shell_status"
)

// Event is a single append-only journal entry for a session.
type Event struct {
	Usage     *llm.StructuredUsage
	Seq       int64
	Type      EventType
	CreatedAt time.Time
	Message   llm.Message
	Compacted []llm.Message
	// RetainedHistory is the explicit model-visible archive carried across a
	// rewind or fork. Nil on older events means only Compacted is retained.
	RetainedHistory []llm.Message
	ResetReason     string
	PreviousModel   config.Model
	Model           config.Model
	ReasoningLevel  string
	ShellCommand    *ShellCommandEntry
	ShellOutput     *ShellOutputEntry
	ShellStatus     *ShellStatusEntry
}

type ShellStream string

const (
	ShellStdout ShellStream = "stdout"
	ShellStderr ShellStream = "stderr"
)

type ShellStatus string

const (
	ShellSucceeded   ShellStatus = "succeeded"
	ShellFailed      ShellStatus = "failed"
	ShellCanceled    ShellStatus = "canceled"
	ShellStartFailed ShellStatus = "start_failed"
)

type ShellCommandEntry struct{ ID, Command, WorkingDir string }
type ShellOutputEntry struct {
	ID        string
	Stream    ShellStream
	Text      string
	Truncated bool
}
type ShellStatusEntry struct {
	ID            string
	Status        ShellStatus
	ExitCode      int
	HasExitCode   bool
	Error         string
	DurationMS    int64
	CleanupFailed bool
}

const (
	MaxShellCommandBytes = 128 * 1024
	MaxShellOutputBytes  = 128 * 1024
	MaxShellErrorBytes   = 16 * 1024
)

func (e Event) validateShell() error {
	switch e.Type {
	case EventShellCommand:
		if e.ShellCommand == nil || e.ShellCommand.ID == "" || e.ShellCommand.Command == "" {
			return fmt.Errorf("invalid shell command event")
		}
		if len(e.ShellCommand.Command) > MaxShellCommandBytes || strings.IndexByte(e.ShellCommand.Command, 0) >= 0 {
			return fmt.Errorf("shell command exceeds limits")
		}
	case EventShellOutput:
		if e.ShellOutput == nil || e.ShellOutput.ID == "" || (e.ShellOutput.Stream != ShellStdout && e.ShellOutput.Stream != ShellStderr) {
			return fmt.Errorf("invalid shell output event")
		}
		if len(e.ShellOutput.Text) > MaxShellOutputBytes {
			return fmt.Errorf("shell output exceeds limits")
		}
	case EventShellStatus:
		if e.ShellStatus == nil || e.ShellStatus.ID == "" {
			return fmt.Errorf("invalid shell status event")
		}
		if e.ShellStatus.Status != ShellSucceeded && e.ShellStatus.Status != ShellFailed && e.ShellStatus.Status != ShellCanceled && e.ShellStatus.Status != ShellStartFailed {
			return fmt.Errorf("invalid shell status")
		}
		if len(e.ShellStatus.Error) > MaxShellErrorBytes {
			return fmt.Errorf("shell error exceeds limits")
		}
	}
	return nil
}

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
		case EventShellCommand, EventShellOutput, EventShellStatus:
			// Local shell history is never model context.
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
	// History is the display journal, separate from model messages.
	History []Event
}
