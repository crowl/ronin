package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
)

// Tool result pruning is a cheap, model-free complement to compaction. Large
// tool outputs from earlier user turns are replaced with short stubs while the
// journal keeps the originals, recoverable through conversation_history.
//
// Every prune rewrites prompt-cache prefixes from the first stubbed message
// onward, so pruning runs in batches only once enough stale bytes accumulate.
const (
	// pruneKeepUserTurns is how many most recent user prompts keep their tool
	// results intact; the model often edits what it read in the previous turn.
	pruneKeepUserTurns = 2
	// pruneMinResultBytes is the smallest tool output worth replacing.
	pruneMinResultBytes = 2 << 10
	// pruneMinTotalBytes and pruneMinContextPercent bound how much must be
	// reclaimable before a batch is worth a cache-prefix rewrite.
	pruneMinTotalBytes     = 64 << 10
	pruneMinContextPercent = 20
)

// prunedToolOutput is the model-visible stub for a removed tool result.
type prunedToolOutput struct {
	Pruned     bool   `json:"pruned"`
	Tool       string `json:"tool"`
	Bytes      int    `json:"bytes"`
	HistoryRef string `json:"history_ref"`
	Note       string `json:"note"`
}

const prunedToolOutputNote = "result removed from context to save space; re-run the tool for current state or pass history_ref to conversation_history"

func (c *Conversation) shouldPrune() bool {
	reclaimable := 0
	for _, index := range prunableToolResults(c.messages) {
		reclaimable += len(c.messages[index].(llm.ToolOutputMessage).ToolOutput)
	}
	if reclaimable == 0 {
		return false
	}
	threshold := max(pruneMinTotalBytes, compactionMessageBytes(c.messages)*pruneMinContextPercent/100)
	return reclaimable >= threshold
}

func (c *Conversation) pruneContext(ctx context.Context) error {
	pruned, changed := pruneToolResults(c.messages)
	if !changed {
		return nil
	}
	if err := c.replaceContext(ctx, session.Event{Type: session.EventCompaction, Compacted: pruned}); err != nil {
		return fmt.Errorf("save pruned session: %w", err)
	}
	// Reported input tokens describe the unpruned context; until the next
	// response, compaction decisions fall back to the byte estimate.
	c.contextUsage = llm.Usage{Cost: llm.Cost{Total: c.sessionCost.Total, Available: c.sessionCost.Available}}
	return nil
}

// pruneToolResults returns a copy of messages with eligible tool outputs
// replaced by stubs. changed reports whether any message was replaced.
func pruneToolResults(messages []session.Message) (pruned []session.Message, changed bool) {
	indices := prunableToolResults(messages)
	if len(indices) == 0 {
		return nil, false
	}
	pruned = append([]session.Message(nil), messages...)
	for _, index := range indices {
		output := messages[index].(llm.ToolOutputMessage)
		ref, _ := session.HistoryMessage(output)
		stub, err := json.Marshal(prunedToolOutput{
			Pruned:     true,
			Tool:       output.ToolName,
			Bytes:      len(output.ToolOutput),
			HistoryRef: ref,
			Note:       prunedToolOutputNote,
		})
		if err != nil {
			continue
		}
		output.ToolOutput = string(stub)
		pruned[index] = output
	}
	return pruned, true
}

// prunableToolResults lists indexes of large tool outputs that precede the
// most recent pruneKeepUserTurns user prompts. Stubs are already small and
// therefore never selected again.
func prunableToolResults(messages []session.Message) []int {
	boundary := -1
	seen := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if _, ok := messages[i].(llm.UserMessage); ok {
			seen++
			if seen == pruneKeepUserTurns {
				boundary = i
				break
			}
		}
	}
	var indices []int
	for i := range boundary {
		if output, ok := messages[i].(llm.ToolOutputMessage); ok && len(output.ToolOutput) >= pruneMinResultBytes {
			indices = append(indices, i)
		}
	}
	return indices
}
