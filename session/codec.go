package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crowl/ronin/llm"
)

type messageJSON struct {
	Type                    string      `json:"type"`
	Timestamp               time.Time   `json:"timestamp"`
	Text                    string      `json:"text,omitempty"`
	Blocks                  []blockJSON `json:"blocks,omitempty"`
	StopReason              string      `json:"stop_reason,omitempty"`
	Usage                   llm.Usage   `json:"usage"`
	ToolName                string      `json:"tool_name,omitempty"`
	Name                    string      `json:"name,omitempty"`
	ToolCallID              string      `json:"tool_call_id,omitempty"`
	ToolOutput              string      `json:"tool_output,omitempty"`
	RawToolOutput           string      `json:"raw_tool_output,omitempty"`
	ToolOutputWasSummarized bool        `json:"tool_output_was_summarized,omitempty"`
	Status                  string      `json:"status,omitempty"`
	Summary                 string      `json:"summary,omitempty"`
	Input                   string      `json:"input,omitempty"`
	Error                   string      `json:"error,omitempty"`
}

type blockJSON struct {
	Type             string          `json:"type"`
	Text             string          `json:"text,omitempty"`
	Signature        string          `json:"signature,omitempty"`
	Data             string          `json:"data,omitempty"`
	ID               string          `json:"id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
}

// encodeEvent serializes an event into its journal type tag and payload bytes.
// The type tag is stored in the session_events type column: the message kind
// for message events, or "compaction" for compaction events.
func encodeEvent(event Event) (string, []byte, error) {
	switch event.Type {
	case EventCompaction:
		payload, err := marshalMessages(event.Compacted)
		if err != nil {
			return "", nil, err
		}
		return string(EventCompaction), payload, nil
	case EventMessage:
		kind, err := messageKind(event.Message)
		if err != nil {
			return "", nil, err
		}
		payload, err := marshalMessage(event.Message)
		if err != nil {
			return "", nil, err
		}
		return kind, payload, nil
	default:
		return "", nil, fmt.Errorf("unsupported event type %q", event.Type)
	}
}

// decodeEvent reconstructs an event from its stored type tag and payload.
func decodeEvent(eventType string, payload []byte) (Event, error) {
	if eventType == string(EventCompaction) {
		messages, err := unmarshalMessages(payload)
		if err != nil {
			return Event{}, err
		}
		return Event{Type: EventCompaction, Compacted: messages}, nil
	}
	if !isMessageEventType(eventType) {
		return Event{}, fmt.Errorf("unsupported event type %q", eventType)
	}
	message, err := unmarshalMessage(payload)
	if err != nil {
		return Event{}, err
	}
	kind, err := messageKind(message)
	if err != nil {
		return Event{}, err
	}
	if kind != eventType {
		return Event{}, fmt.Errorf("event type %q does not match payload type %q", eventType, kind)
	}
	return Event{Type: EventMessage, Message: message}, nil
}

func isMessageEventType(eventType string) bool {
	switch eventType {
	case "user_message", "assistant_message", "tool_result", "tool_error", "error", "workflow_result":
		return true
	default:
		return false
	}
}

// messageKind returns the journal event type column value for a message.
func messageKind(message llm.Message) (string, error) {
	switch message.(type) {
	case llm.UserMessage:
		return "user_message", nil
	case llm.AssistantMessage:
		return "assistant_message", nil
	case llm.ToolOutputMessage:
		return "tool_result", nil
	case llm.ToolErrorMessage:
		return "tool_error", nil
	case llm.ErrorMessage:
		return "error", nil
	case llm.WorkflowResultMessage:
		return "workflow_result", nil
	default:
		return "", fmt.Errorf("unsupported message type %T", message)
	}
}

// marshalMessage encodes a single message as an event payload.
func marshalMessage(message llm.Message) ([]byte, error) {
	encoded, err := encodeMessage(message)
	if err != nil {
		return nil, err
	}
	return json.Marshal(encoded)
}

// unmarshalMessage decodes a single-message event payload.
func unmarshalMessage(data []byte) (llm.Message, error) {
	var encoded messageJSON
	if err := json.Unmarshal(data, &encoded); err != nil {
		return nil, err
	}
	return decodeMessage(encoded)
}

// marshalMessages encodes a compaction event payload.
func marshalMessages(messages []llm.Message) ([]byte, error) {
	encoded := make([]messageJSON, 0, len(messages))
	for i, message := range messages {
		encodedMessage, err := encodeMessage(message)
		if err != nil {
			return nil, fmt.Errorf("encode message %d: %w", i, err)
		}
		encoded = append(encoded, encodedMessage)
	}
	return json.Marshal(encoded)
}

// unmarshalMessages decodes a compaction event payload.
func unmarshalMessages(data []byte) ([]llm.Message, error) {
	var encoded []messageJSON
	if err := json.Unmarshal(data, &encoded); err != nil {
		return nil, err
	}
	messages := make([]llm.Message, 0, len(encoded))
	for i, message := range encoded {
		decoded, err := decodeMessage(message)
		if err != nil {
			return nil, fmt.Errorf("decode message %d: %w", i, err)
		}
		messages = append(messages, decoded)
	}
	return messages, nil
}

func encodeMessage(message llm.Message) (messageJSON, error) {
	switch m := message.(type) {
	case llm.UserMessage:
		return messageJSON{Type: "user", Timestamp: m.Timestamp, Text: m.Text}, nil
	case llm.AssistantMessage:
		encoded := messageJSON{
			Type:       "assistant",
			Timestamp:  m.Timestamp,
			Blocks:     make([]blockJSON, 0, len(m.Blocks)),
			StopReason: string(m.StopReason),
			Usage:      m.Usage,
		}
		for i, block := range m.Blocks {
			encodedBlock, err := encodeBlock(block)
			if err != nil {
				return messageJSON{}, fmt.Errorf("encode block %d: %w", i, err)
			}
			encoded.Blocks = append(encoded.Blocks, encodedBlock)
		}
		return encoded, nil
	case llm.WorkflowResultMessage:
		return messageJSON{Type: "workflow_result", Timestamp: m.Timestamp, Name: m.Name, Input: m.Input, Status: string(m.Status), Summary: m.Summary}, nil
	case llm.ToolOutputMessage:
		return messageJSON{Type: "tool_output", Timestamp: m.Timestamp, ToolName: m.ToolName, ToolCallID: m.ToolCallID, ToolOutput: m.ToolOutput, RawToolOutput: m.RawToolOutput, ToolOutputWasSummarized: m.ToolOutputWasSummarized}, nil
	case llm.ToolErrorMessage:
		return messageJSON{Type: "tool_error", Timestamp: m.Timestamp, ToolName: m.ToolName, ToolCallID: m.ToolCallID, Error: errorText(m.Error)}, nil
	case llm.ErrorMessage:
		return messageJSON{Type: "error", Timestamp: m.Timestamp, Error: errorText(m.Error)}, nil
	default:
		return messageJSON{}, fmt.Errorf("unsupported message type %T", message)
	}
}

func decodeMessage(encoded messageJSON) (llm.Message, error) {
	switch encoded.Type {
	case "user":
		return llm.UserMessage{Timestamp: encoded.Timestamp, Text: encoded.Text}, nil
	case "assistant":
		message := llm.AssistantMessage{
			Timestamp:  encoded.Timestamp,
			Blocks:     make([]llm.AssistantBlock, 0, len(encoded.Blocks)),
			StopReason: llm.StopReason(encoded.StopReason),
			Usage:      encoded.Usage,
		}
		for i, block := range encoded.Blocks {
			decodedBlock, err := decodeBlock(block)
			if err != nil {
				return nil, fmt.Errorf("decode block %d: %w", i, err)
			}
			message.Blocks = append(message.Blocks, decodedBlock)
		}
		return message, nil
	case "workflow_result":
		return llm.WorkflowResultMessage{Timestamp: encoded.Timestamp, Name: encoded.Name, Input: encoded.Input, Status: llm.WorkflowStatus(encoded.Status), Summary: encoded.Summary}, nil
	case "tool_output":
		return llm.ToolOutputMessage{Timestamp: encoded.Timestamp, ToolName: encoded.ToolName, ToolCallID: encoded.ToolCallID, ToolOutput: encoded.ToolOutput, RawToolOutput: encoded.RawToolOutput, ToolOutputWasSummarized: encoded.ToolOutputWasSummarized}, nil
	case "tool_error":
		return llm.ToolErrorMessage{Timestamp: encoded.Timestamp, ToolName: encoded.ToolName, ToolCallID: encoded.ToolCallID, Error: errors.New(encoded.Error)}, nil
	case "error":
		return llm.ErrorMessage{Timestamp: encoded.Timestamp, Error: errors.New(encoded.Error)}, nil
	default:
		return nil, fmt.Errorf("unsupported message type %q", encoded.Type)
	}
}

func encodeBlock(block llm.AssistantBlock) (blockJSON, error) {
	switch b := block.(type) {
	case llm.TextBlock:
		return blockJSON{Type: "text", Text: b.Text}, nil
	case llm.ThinkingBlock:
		return blockJSON{Type: "thinking", Text: b.Text, Signature: b.Signature}, nil
	case llm.RedactedThinkingBlock:
		return blockJSON{Type: "redacted_thinking", Data: b.Data}, nil
	case llm.ToolCallBlock:
		return blockJSON{Type: "tool_call", ID: b.ID, Name: b.Name, Arguments: cloneRawMessage(b.Arguments), ThoughtSignature: b.ThoughtSignature}, nil
	default:
		return blockJSON{}, fmt.Errorf("unsupported assistant block type %T", block)
	}
}

func decodeBlock(encoded blockJSON) (llm.AssistantBlock, error) {
	switch encoded.Type {
	case "text":
		return llm.TextBlock{Text: encoded.Text}, nil
	case "thinking":
		return llm.ThinkingBlock{Text: encoded.Text, Signature: encoded.Signature}, nil
	case "redacted_thinking":
		return llm.RedactedThinkingBlock{Data: encoded.Data}, nil
	case "tool_call":
		return llm.ToolCallBlock{ID: encoded.ID, Name: encoded.Name, Arguments: cloneRawMessage(encoded.Arguments), ThoughtSignature: encoded.ThoughtSignature}, nil
	default:
		return nil, fmt.Errorf("unsupported assistant block type %q", encoded.Type)
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
