package acp

import (
	"context"
	"strings"
)

type slashCommand struct {
	Feedback string
	Execute  func(context.Context) error
}

func availableSlashCommands() []availableCommand {
	return []availableCommand{
		{Name: "compact", Description: "Compact the current conversation"},
		{Name: "new", Description: "Start a new conversation"},
	}
}

func lookupSlashCommand(conv Conversation, prompt string) (slashCommand, bool, string) {
	name, input, ok := parseSlashCommand(prompt)
	if !ok {
		return slashCommand{}, false, "unknown slash command"
	}

	switch name {
	case "compact":
		if input != "" {
			return slashCommand{}, false, "/compact does not accept input"
		}
		return slashCommand{
			Feedback: "Compacted the conversation.",
			Execute: func(ctx context.Context) error {
				return conv.CompactConversation(ctx)
			},
		}, true, ""
	case "new":
		if input != "" {
			return slashCommand{}, false, "/new does not accept input"
		}
		return slashCommand{
			Feedback: "Started a new conversation.",
			Execute: func(context.Context) error {
				return conv.NewConversation()
			},
		}, true, ""
	default:
		return slashCommand{}, false, "unknown slash command"
	}
}

func parseSlashCommand(prompt string) (string, string, bool) {
	prompt = strings.TrimSpace(prompt)
	if !strings.HasPrefix(prompt, "/") {
		return "", "", false
	}
	commandText := strings.TrimSpace(strings.TrimPrefix(prompt, "/"))
	if commandText == "" {
		return "", "", false
	}

	for i, r := range commandText {
		if isCommandSpace(r) {
			return commandText[:i], strings.TrimSpace(commandText[i:]), true
		}
	}
	return commandText, "", true
}

func isCommandSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}
