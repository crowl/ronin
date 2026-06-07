package openai

import (
	"fmt"

	"github.com/crowl/ronin/llm"
)

const provider = "openai"

var (
	Gpt55     = llm.Model{Provider: provider, Name: "gpt-5.5", ContextWindow: 272000}
	Gpt55Pro  = llm.Model{Provider: provider, Name: "gpt-5.5-pro", ContextWindow: 1_000_000}
	Gpt54     = llm.Model{Provider: provider, Name: "gpt-5.4", ContextWindow: 272000}
	Gpt54Mini = llm.Model{Provider: provider, Name: "gpt-5.4-mini", ContextWindow: 272000}
	Gpt54Nano = llm.Model{Provider: provider, Name: "gpt-5.4-nano", ContextWindow: 272000}
)

func Setup(apiKey string) error {
	models := []llm.Model{
		Gpt55,
		Gpt55Pro,
		Gpt54,
		Gpt54Mini,
		Gpt54Nano,
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
				return nil, fmt.Errorf("create openai llm: %w", err)
			}
			return newLLM, nil
		}); err != nil {
			return fmt.Errorf("failed to setup model: %w", err)
		}
	}
	return nil
}
