package anthropic

import (
	"fmt"

	"github.com/crowl/ronin/llm"
)

const provider = "anthropic"

var (
	ClaudeHaiku45  = llm.Model{Provider: provider, Name: "claude-haiku-4-5", ContextWindow: 1000000}
	ClaudeSonnet46 = llm.Model{Provider: provider, Name: "claude-sonnet-4-6", ContextWindow: 1000000}
	ClaudeOpus48   = llm.Model{Provider: provider, Name: "claude-opus-4-8", ContextWindow: 1000000}
)

func Setup(apiKey string) error {
	models := []llm.Model{
		ClaudeHaiku45,
		ClaudeSonnet46,
		ClaudeOpus48,
	}
	for _, model := range models {
		registeredModel := model
		if err := llm.RegisterModel(registeredModel, func(level llm.ReasoningLevel) (llm.Assistant, error) {
			newLLM, err := NewLLM(LLMConfig{
				APIKey:         apiKey,
				Model:          registeredModel,
				ReasoningLevel: level,
			})
			if err != nil {
				return nil, fmt.Errorf("create anthropic llm: %w", err)
			}
			return newLLM, nil
		}); err != nil {
			return fmt.Errorf("failed to setup model: %w", err)
		}
	}
	return nil
}
