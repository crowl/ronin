package llm_test

import (
	"testing"

	"github.com/crowl/ronin/llm"
)

func TestProjectMessagesForProvider(t *testing.T) {
	messages := []llm.Message{
		llm.AssistantMessage{Blocks: []llm.AssistantBlock{
			llm.ThinkingBlock{Text: "anthropic thought", Signature: "a", Provider: "anthropic"},
			llm.RedactedThinkingBlock{Data: "opaque", Provider: "anthropic"},
			llm.ThinkingBlock{Text: "legacy", Signature: "unknown"},
			llm.TextBlock{Text: "answer"},
			llm.ToolCallBlock{ID: "call", Name: "shell", ThoughtSignature: "g", ThoughtProvider: "google"},
		}},
		llm.AssistantMessage{Blocks: []llm.AssistantBlock{
			llm.ThinkingBlock{Text: "only foreign", Provider: "anthropic"},
		}},
	}

	got := llm.ProjectMessagesForProvider(messages, "google")
	if len(got) != 1 {
		t.Fatalf("ProjectMessagesForProvider() length = %d, want 1", len(got))
	}
	assistant := got[0].(llm.AssistantMessage)
	if len(assistant.Blocks) != 2 {
		t.Fatalf("projected blocks = %#v, want text and tool call", assistant.Blocks)
	}
	toolCall := assistant.Blocks[1].(llm.ToolCallBlock)
	if toolCall.ThoughtSignature != "g" || toolCall.ThoughtProvider != "google" {
		t.Fatalf("matching thought signature = %#v", toolCall)
	}
	original := messages[0].(llm.AssistantMessage)
	if len(original.Blocks) != 5 {
		t.Fatal("projection mutated original messages")
	}

	anthropic := llm.ProjectMessagesForProvider(messages[:1], "anthropic")[0].(llm.AssistantMessage)
	if len(anthropic.Blocks) != 4 {
		t.Fatalf("anthropic blocks = %#v", anthropic.Blocks)
	}
	foreignTool := anthropic.Blocks[3].(llm.ToolCallBlock)
	if foreignTool.ThoughtSignature != "" || foreignTool.ThoughtProvider != "" {
		t.Fatalf("foreign thought signature was not cleared: %#v", foreignTool)
	}
}
