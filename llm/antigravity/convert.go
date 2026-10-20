package antigravity

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crowl/ronin/llm"
)

// convertMessages projects the transcript onto Gemini contents. Consecutive
// messages with the same role are merged, because the backend expects one
// content per conversational turn rather than one per transcript entry.
func convertMessages(messages []llm.Message) ([]content, error) {
	var turns conversation
	for _, message := range messages {
		switch typed := message.(type) {
		case llm.UserMessage:
			turns.append(roleUser, part{Text: typed.Text})

		case llm.AssistantMessage:
			parts, err := assistantParts(typed)
			if err != nil {
				return nil, err
			}
			turns.append(roleModel, parts...)

		case llm.ToolOutputMessage:
			turns.append(roleUser, part{FunctionResponse: &functionResponse{
				ID:       typed.ToolCallID,
				Name:     typed.ToolName,
				Response: map[string]any{"output": typed.ToolOutput},
			}})

		case llm.ToolErrorMessage:
			turns.append(roleUser, part{FunctionResponse: &functionResponse{
				ID:       typed.ToolCallID,
				Name:     typed.ToolName,
				Response: map[string]any{"error": errorText(typed.Error)},
			}})

		case llm.ErrorMessage:
			turns.append(roleUser, part{Text: "error: " + errorText(typed.Error)})

		default:
			return nil, fmt.Errorf("unsupported message %T", message)
		}
	}
	return turns.contents, nil
}

func assistantParts(message llm.AssistantMessage) ([]part, error) {
	parts := make([]part, 0, len(message.Blocks))
	for _, block := range message.Blocks {
		switch typed := block.(type) {
		case llm.TextBlock:
			if typed.Text != "" {
				parts = append(parts, part{Text: typed.Text})
			}

		case llm.ThinkingBlock:
			// Gemini 3 requires the thought signature back on the next turn,
			// so a signature-only block is still worth sending.
			if typed.Text == "" && typed.Signature == "" {
				continue
			}
			parts = append(parts, part{Text: typed.Text, Thought: true, ThoughtSignature: typed.Signature})

		case llm.RedactedThinkingBlock:
			continue

		case llm.ToolCallBlock:
			arguments, err := normalizeArguments(typed.Arguments)
			if err != nil {
				return nil, fmt.Errorf("tool call %q arguments: %w", typed.ID, err)
			}
			parts = append(parts, part{
				ThoughtSignature: typed.ThoughtSignature,
				FunctionCall:     &functionCall{ID: typed.ID, Name: typed.Name, Args: arguments},
			})

		default:
			return nil, fmt.Errorf("unsupported assistant block %T", block)
		}
	}
	return parts, nil
}

func normalizeArguments(arguments json.RawMessage) (json.RawMessage, error) {
	if len(arguments) == 0 || strings.TrimSpace(string(arguments)) == "" {
		return json.RawMessage("{}"), nil
	}
	if !json.Valid(arguments) {
		return nil, fmt.Errorf("invalid JSON")
	}
	return arguments, nil
}

const (
	roleUser  = "user"
	roleModel = "model"
)

type conversation struct {
	contents []content
}

func (c *conversation) append(role string, parts ...part) {
	if len(parts) == 0 {
		return
	}
	if last := len(c.contents) - 1; last >= 0 && c.contents[last].Role == role {
		c.contents[last].Parts = append(c.contents[last].Parts, parts...)
		return
	}
	c.contents = append(c.contents, content{Role: role, Parts: parts})
}
