package runtime

import (
	"context"
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
	for _, message := range session.RetainedHistory(t.conversation.session.History) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Retrieval results must not recursively archive copies of prior history.
		if output, ok := message.(llm.ToolOutputMessage); ok && output.ToolName == t.Name() {
			continue
		}
		reference, content := session.HistoryMessage(message)
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
