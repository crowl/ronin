package runtime

import (
	"context"

	"github.com/crowl/ronin/session"
)

// appendEvent publishes a journal event only after durable append succeeds.
// All conversation-local journal writes go through this boundary.
func (c *Conversation) appendEvent(ctx context.Context, event session.Event) error {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = c.now()
	}
	if c.sessionStore != nil && c.session.ID != "" {
		if err := c.sessionStore.Append(ctx, c.session.ID, event); err != nil {
			return err
		}
	}
	c.session.History = append(c.session.History, event)
	c.session.UpdatedAt = event.CreatedAt.UTC()
	return nil
}

// replaceContext commits a replacement before invalidating context-dependent state.
// RetainedHistory is deliberately supplied by the caller: compaction and rewind
// have different archive visibility rules.
func (c *Conversation) replaceContext(ctx context.Context, event session.Event) error {
	if err := c.appendEvent(ctx, event); err != nil {
		return err
	}
	c.messages = append([]session.Message(nil), event.Compacted...)
	c.resetToolContext()
	c.recalculateContextUsage()
	return nil
}
