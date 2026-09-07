package runtime

import (
	"fmt"
	"github.com/crowl/ronin/session"
)

// DisplayHistory returns local shell records and effective conversation messages.
// Rewinds and compaction retain the local execution audit; forks start a new one.
// This history must never be used to construct model requests.
func (c *Conversation) DisplayHistory() []session.Event {
	var history []session.Event
	for _, event := range c.session.History {
		switch event.Type {
		case session.EventMessage, session.EventShellCommand, session.EventShellOutput, session.EventShellStatus:
			history = append(history, event)
		case session.EventCompaction, session.EventContextReset:
			kept := history[:0]
			for _, old := range history {
				if old.Type != session.EventMessage {
					kept = append(kept, old)
				}
			}
			history = kept
			for _, message := range event.Compacted {
				history = append(history, session.Event{Type: session.EventMessage, Message: message})
			}
		}
	}
	if len(c.session.History) == 0 {
		for _, message := range c.messages {
			history = append(history, session.Event{Type: session.EventMessage, Message: message})
		}
	}
	return history
}

// RecordShellEvent persists local history without mutating model context.
// Like Prompt, it is not safe for concurrent use on a Conversation.
func (c *Conversation) RecordShellEvent(event session.Event) error {
	switch event.Type {
	case session.EventShellCommand, session.EventShellOutput, session.EventShellStatus:
	default:
		return fmt.Errorf("not a shell history event: %s", event.Type)
	}
	if _, _, err := session.EncodeEvent(event); err != nil {
		return err
	}
	event.CreatedAt = c.now()
	if c.sessionStore != nil && c.session.ID != "" {
		ctx, cancel := detachedPersistenceContext()
		defer cancel()
		if err := c.sessionStore.Append(ctx, c.session.ID, event); err != nil {
			return fmt.Errorf("save shell history: %w", err)
		}
		c.session.UpdatedAt = event.CreatedAt.UTC()
	}
	c.session.History = append(c.session.History, event)
	return nil
}
