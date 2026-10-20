package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crowl/ronin/llm"
)

const providerLabel = "antigravity"

// streamState turns Gemini candidate deltas into ronin block events. Gemini
// streams parts rather than block boundaries, so a run of same-kind parts is
// folded into one block and the block is closed when the kind changes.
type streamState struct {
	provider string

	blockIndex  int
	open        *openBlock
	usage       llm.Usage
	toolEmitted bool
	callCounter int
	finish      string
	finished    bool
}

type openBlock struct {
	kind      llm.BlockKind
	index     int
	text      strings.Builder
	signature string
}

func (state *streamState) handleData(ctx context.Context, data string, events chan<- llm.PredictionEvent) error {
	if data == "" || data == "[DONE]" {
		return nil
	}
	var chunk streamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return fmt.Errorf("parse antigravity event: %w", err)
	}
	reply := chunk.reply()
	if usage := reply.UsageMetadata; usage != nil {
		state.usage = convertUsage(*usage)
	}
	for _, candidate := range reply.Candidates {
		for _, piece := range candidate.Content.Parts {
			if err := state.handlePart(ctx, piece, events); err != nil {
				return err
			}
		}
		if candidate.FinishReason != "" {
			state.finish = candidate.FinishReason
			state.finished = true
		}
	}
	return nil
}

func (state *streamState) handlePart(ctx context.Context, piece part, events chan<- llm.PredictionEvent) error {
	switch {
	case piece.FunctionCall != nil:
		return state.emitToolCall(ctx, piece, events)

	case piece.Thought:
		if err := state.openBlockOfKind(ctx, llm.BlockKindThinking, events); err != nil {
			return err
		}
		if piece.ThoughtSignature != "" {
			state.open.signature = piece.ThoughtSignature
		}
		if piece.Text == "" {
			return nil
		}
		state.open.text.WriteString(piece.Text)
		return sendEvent(ctx, events, llm.ThinkingDelta{Index: state.open.index, Text: piece.Text})

	case piece.Text != "":
		if err := state.openBlockOfKind(ctx, llm.BlockKindText, events); err != nil {
			return err
		}
		state.open.text.WriteString(piece.Text)
		return sendEvent(ctx, events, llm.TextDelta{Index: state.open.index, Text: piece.Text})
	}
	return nil
}

// emitToolCall sends a whole call in one go: Gemini delivers complete
// functionCall objects rather than argument fragments, so the block opens,
// carries a single arguments delta, and closes immediately.
func (state *streamState) emitToolCall(ctx context.Context, piece part, events chan<- llm.PredictionEvent) error {
	if err := state.closeBlock(ctx, events); err != nil {
		return err
	}
	call := piece.FunctionCall
	arguments, err := normalizeArguments(call.Args)
	if err != nil {
		return fmt.Errorf("antigravity function call %q arguments: %w", call.Name, err)
	}
	identifier := call.ID
	if identifier == "" {
		state.callCounter++
		identifier = fmt.Sprintf("%s_%d", call.Name, state.callCounter)
	}

	index := state.blockIndex
	state.blockIndex++
	state.toolEmitted = true
	if err := sendEvent(ctx, events, llm.BlockStarted{Index: index, Kind: llm.BlockKindToolCall}); err != nil {
		return err
	}
	if err := sendEvent(ctx, events, llm.ToolCallArgumentsDelta{Index: index, Arguments: string(arguments)}); err != nil {
		return err
	}
	return sendEvent(ctx, events, llm.BlockEnded{Index: index, Block: llm.ToolCallBlock{
		ID:               identifier,
		Name:             call.Name,
		Arguments:        arguments,
		ThoughtSignature: piece.ThoughtSignature,
		ThoughtProvider:  signatureProvider(state.provider, piece.ThoughtSignature),
	}})
}

func (state *streamState) openBlockOfKind(ctx context.Context, kind llm.BlockKind, events chan<- llm.PredictionEvent) error {
	if state.open != nil && state.open.kind == kind {
		return nil
	}
	if err := state.closeBlock(ctx, events); err != nil {
		return err
	}
	index := state.blockIndex
	state.blockIndex++
	state.open = &openBlock{kind: kind, index: index}
	return sendEvent(ctx, events, llm.BlockStarted{Index: index, Kind: kind})
}

func (state *streamState) closeBlock(ctx context.Context, events chan<- llm.PredictionEvent) error {
	open := state.open
	if open == nil {
		return nil
	}
	state.open = nil

	var block llm.AssistantBlock
	switch open.kind {
	case llm.BlockKindThinking:
		block = llm.ThinkingBlock{
			Text:      open.text.String(),
			Signature: open.signature,
			Provider:  signatureProvider(state.provider, open.signature),
		}
	default:
		block = llm.TextBlock{Text: open.text.String()}
	}
	return sendEvent(ctx, events, llm.BlockEnded{Index: open.index, Block: block})
}

// signatureProvider stamps the configured provider name so the transcript
// projection returns the signature to this provider and strips it from
// others. An unsigned block carries no provider.
func signatureProvider(provider, signature string) string {
	if signature == "" {
		return ""
	}
	return provider
}

func (state *streamState) usageWithCost(model llm.Model) llm.Usage {
	usage := state.usage
	usage.Cost = llm.EstimateCost(model, usage)
	return usage
}

func (state *streamState) stopReason() llm.StopReason {
	if state.toolEmitted {
		return llm.StopReasonToolUse
	}
	switch state.finish {
	case "MAX_TOKENS":
		return llm.StopReasonMaxTokens
	case "SAFETY", "PROHIBITED_CONTENT", "BLOCKLIST", "SPII", "IMAGE_SAFETY":
		return llm.StopReasonRefusal
	default:
		return llm.StopReasonFinished
	}
}
