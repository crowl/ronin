package fs

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
)

type sessionJSON struct {
	Version        int           `json:"version"`
	ID             string        `json:"id"`
	Title          string        `json:"title"`
	WorkingDir     string        `json:"working_dir"`
	ParentID       string        `json:"parent_id,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
	Model          config.Model  `json:"model"`
	ReasoningLevel string        `json:"reasoning_level"`
	Messages       []messageJSON `json:"messages"`
}

type messageJSON struct {
	Type                    string      `json:"type"`
	Timestamp               time.Time   `json:"timestamp"`
	Text                    string      `json:"text,omitempty"`
	Blocks                  []blockJSON `json:"blocks,omitempty"`
	StopReason              string      `json:"stop_reason,omitempty"`
	Usage                   llm.Usage   `json:"usage,omitempty"`
	ToolName                string      `json:"tool_name,omitempty"`
	ToolCallID              string      `json:"tool_call_id,omitempty"`
	ToolOutput              string      `json:"tool_output,omitempty"`
	RawToolOutput           string      `json:"raw_tool_output,omitempty"`
	ToolOutputWasSummarized bool        `json:"tool_output_was_summarized,omitempty"`
	Error                   string      `json:"error,omitempty"`
}

type blockJSON struct {
	Type             string          `json:"type"`
	Text             string          `json:"text,omitempty"`
	Signature        string          `json:"signature,omitempty"`
	ID               string          `json:"id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
}

func encode(s session.Session) ([]byte, error) {
	encoded := sessionJSON{
		Version:        session.Version,
		ID:             s.ID,
		Title:          s.Title,
		WorkingDir:     s.WorkingDir,
		ParentID:       s.ParentID,
		CreatedAt:      s.CreatedAt,
		UpdatedAt:      s.UpdatedAt,
		Model:          s.Model,
		ReasoningLevel: s.ReasoningLevel,
		Messages:       make([]messageJSON, 0, len(s.Messages)),
	}

	for i, message := range s.Messages {
		encodedMessage, err := encodeMessage(message)
		if err != nil {
			return nil, fmt.Errorf("encode message %d: %w", i, err)
		}
		encoded.Messages = append(encoded.Messages, encodedMessage)
	}

	data, err := json.MarshalIndent(encoded, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func decode(data []byte) (session.Session, error) {
	var encoded sessionJSON
	if err := json.Unmarshal(data, &encoded); err != nil {
		return session.Session{}, err
	}
	if encoded.Version != session.Version {
		return session.Session{}, fmt.Errorf("unsupported session version %d", encoded.Version)
	}

	s := session.Session{
		Version:        encoded.Version,
		ID:             encoded.ID,
		Title:          encoded.Title,
		WorkingDir:     encoded.WorkingDir,
		ParentID:       encoded.ParentID,
		CreatedAt:      encoded.CreatedAt,
		UpdatedAt:      encoded.UpdatedAt,
		Model:          encoded.Model,
		ReasoningLevel: encoded.ReasoningLevel,
		Messages:       make([]llm.Message, 0, len(encoded.Messages)),
	}

	for i, message := range encoded.Messages {
		decodedMessage, err := decodeMessage(message)
		if err != nil {
			return session.Session{}, fmt.Errorf("decode message %d: %w", i, err)
		}
		s.Messages = append(s.Messages, decodedMessage)
	}

	return s, nil
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
