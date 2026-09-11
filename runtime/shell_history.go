package runtime

import (
	"fmt"
	"github.com/crowl/ronin/session"
)

// DisplayHistory returns local shell records and effective conversation messages.
// Rewinds and compaction retain the local execution audit; forks start a new one.
// This history must never be used to construct model requests.
func (c *Conversation) DisplayHistory() []session.Event {
	return session.DisplayHistory(c.session.History)
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
	ctx, cancel := detachedPersistenceContext()
	defer cancel()
	if err := c.appendEvent(ctx, event); err != nil {
		return fmt.Errorf("save shell history: %w", err)
	}
	return nil
}
