package runtime

import (
	"github.com/crowl/ronin/session"
)

func (c *Conversation) addHistoryTool() {
	// The runtime owns this name; caller-provided tools cannot widen its scope.
	t := &historyTool{conversation: c}
	for i, existing := range c.toolDefs {
		if existing.Name() == t.Name() {
			c.toolDefs[i] = t
			c.toolByName[t.Name()] = t
			return
		}
	}
	c.toolDefs = append(c.toolDefs, t)
	c.toolByName[t.Name()] = t
}

func (c *Conversation) historyBefore(point RewindPoint) []session.Message {
	target, _ := session.HistoryMessage(c.messages[point.MessageIndex])
	retained := session.RetainedHistory(c.session.History)
	for i, message := range retained {
		ref, _ := session.HistoryMessage(message)
		if ref == target {
			return append([]session.Message(nil), retained[:i]...)
		}
	}
	// Old or incomplete journals cannot safely establish a larger lineage.
	return append([]session.Message(nil), c.messages[:point.MessageIndex]...)
}
