package runtime

import (
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
)

func (c *Conversation) modelMessages() ([]llm.Message, error) {
	messages, err := session.ModelMessages(c.messages)
	if err != nil {
		return nil, err
	}
	return llm.ProjectMessagesForProvider(messages, c.Model().Provider), nil
}
