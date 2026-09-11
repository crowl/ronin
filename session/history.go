package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/crowl/ronin/llm"
)

// HistoryMessage returns a stable reference and retrievable content.
func HistoryMessage(message Message) (string, string) {
	data, err := json.Marshal(message)
	switch m := message.(type) {
	case llm.ToolErrorMessage:
		data, err = json.Marshal(struct{ Name, CallID, Error string }{m.ToolName, m.ToolCallID, fmt.Sprint(m.Error)})
	case llm.ErrorMessage:
		data, err = json.Marshal(struct{ Error string }{fmt.Sprint(m.Error)})
	}
	if err != nil {
		return "", ""
	}
	kind, err := messageKind(message)
	if err != nil {
		return "", ""
	}
	digest := sha256.Sum256(append([]byte(kind+"\x00"), data...))
	return "history:" + hex.EncodeToString(digest[:16]), string(data)
}

// RetainedHistory projects the model-visible archive, excluding discarded branches and local shell history.
func RetainedHistory(events []Event) []Message {
	var messages []Message
	seen := map[string]bool{}
	add := func(message Message) {
		if message == nil {
			return
		}
		ref, _ := HistoryMessage(message)
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		messages = append(messages, message)
	}
	for _, event := range events {
		switch event.Type {
		case EventMessage:
			add(event.Message)
		case EventCompaction:
			for _, message := range event.Compacted {
				add(message)
			}
		case EventContextReset:
			// Reset snapshots are authoritative. Never infer lineage from text or
			// timestamps: doing so could reintroduce explicitly discarded messages.
			messages = nil
			seen = map[string]bool{}
			for _, message := range event.RetainedHistory {
				add(message)
			}
			for _, message := range event.Compacted {
				add(message)
			}
		}
	}
	return messages
}
