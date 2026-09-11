package runtime

import "github.com/crowl/ronin/llm"

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

func (c *Conversation) historyBefore(point RewindPoint) []llm.Message {
	target, _ := historyMessage(c.messages[point.MessageIndex])
	retained := retainedHistory(c.session.History)
	for i, message := range retained {
		ref, _ := historyMessage(message)
		if ref == target {
			return append([]llm.Message(nil), retained[:i]...)
		}
	}
	// Old or incomplete journals cannot safely establish a larger lineage.
	return append([]llm.Message(nil), c.messages[:point.MessageIndex]...)
}
