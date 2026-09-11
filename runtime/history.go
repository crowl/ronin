package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
)

// historyTool reads only model-visible messages in the active journal. It never
// follows ParentID or exposes shell, metadata, usage, or raw persistence events.
type historyTool struct{ conversation *Conversation }
type historyArgs struct {
	Query     string `json:"query,omitempty" jsonschema:"Case-insensitive text to find. Empty lists messages."`
	Reference string `json:"reference,omitempty" jsonschema:"Exact history reference from a compacted summary."`
	Offset    int    `json:"offset,omitempty" jsonschema:"Byte offset within a referenced message; defaults to zero."`
	Skip      int    `json:"skip,omitempty" jsonschema:"Number of matching messages to skip for search pagination; defaults to zero."`
	Limit     int    `json:"limit,omitempty" jsonschema:"Maximum matching messages, 1..20; defaults to 5."`
}
type historyEntry struct {
	Reference  string `json:"reference"`
	Content    string `json:"content"`
	NextOffset int    `json:"next_offset,omitempty"`
}
type historyResult struct {
	NextSkip  int            `json:"next_skip,omitempty"`
	Entries   []historyEntry `json:"entries"`
	Truncated bool           `json:"truncated"`
}

func (*historyTool) Name() string { return "conversation_history" }
func (*historyTool) Description() string {
	return "Recover prior model-visible messages omitted by compaction. Restricted to retained current-session history; excludes local shell commands and discarded history. Use exact references and offsets to read long messages."
}
func (*historyTool) Parameters() *jsonschema.Schema { return jsonschema.FromType[historyArgs]() }
func (t *historyTool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	var args historyArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if args.Limit == 0 {
		args.Limit = 5
	}
	if args.Skip < 0 || args.Limit < 1 || args.Limit > 20 || args.Offset < 0 || (args.Offset > 0 && args.Reference == "") {
		return nil, fmt.Errorf("invalid history limit or offset")
	}
	result := historyResult{Entries: []historyEntry{}}
	budget := 32 * 1024
	matched := 0
	for _, message := range retainedHistory(t.conversation.session.History) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Retrieval results must not recursively archive copies of prior history.
		if output, ok := message.(llm.ToolOutputMessage); ok && output.ToolName == t.Name() {
			continue
		}
		reference, content := historyMessage(message)
		if args.Reference != "" && args.Reference != reference {
			continue
		}
		if args.Query != "" && !strings.Contains(strings.ToLower(content), strings.ToLower(args.Query)) {
			continue
		}
		matched++
		if matched <= args.Skip {
			continue
		}
		if len(result.Entries) >= args.Limit || budget < 4 {
			result.NextSkip = args.Skip + len(result.Entries)
			result.Truncated = true
			break
		}
		if args.Offset > len(content) {
			return nil, fmt.Errorf("offset exceeds message length")
		}
		if args.Offset < len(content) && !utf8.RuneStart(content[args.Offset]) {
			return nil, fmt.Errorf("offset must be a UTF-8 boundary")
		}
		end := min(len(content), args.Offset+min(8192, budget))
		for end < len(content) && !utf8.RuneStart(content[end]) {
			end--
		}
		entry := historyEntry{Reference: reference, Content: content[args.Offset:end]}
		budget -= len(entry.Content)
		if end < len(content) {
			entry.NextOffset = end
			result.Truncated = true
		}
		result.Entries = append(result.Entries, entry)
	}
	return result, nil
}

func historyMessage(message llm.Message) (string, string) {
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
	digest := sha256.Sum256(data)
	return "history:" + hex.EncodeToString(digest[:16]), string(data)
}

func retainedHistory(events []session.Event) []llm.Message {
	var messages []llm.Message
	seen := map[string]bool{}
	add := func(message llm.Message) {
		if message == nil {
			return
		}
		ref, _ := historyMessage(message)
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		messages = append(messages, message)
	}
	for _, event := range events {
		switch event.Type {
		case session.EventMessage:
			add(event.Message)
		case session.EventCompaction:
			for _, message := range event.Compacted {
				add(message)
			}
		case session.EventContextReset:
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
