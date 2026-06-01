package openai

import (
	"fmt"

	"github.com/crowl/ronin/llm"
)

const provider = "openai"

var (
	Gpt55    = llm.Model{Provider: provider, Name: "gpt-5.5", ContextLimit: 272000}
	Gpt55Pro = llm.Model{Provider: provider, Name: "gpt-5.5-pro", ContextLimit: 1050000}
)

func Setup(apiKey string) error {
	models := []llm.Model{
		Gpt55,
		Gpt55Pro,
	}
	for _, model := range models {
		registeredModel := model
		if err := llm.Register(registeredModel, func(level llm.ReasoningLevel) (llm.Assistant, error) {
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
