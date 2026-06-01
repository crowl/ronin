package google

import (
	"fmt"

	"github.com/crowl/ronin/llm"
)

const provider = "google"

func Setup(apiKey string) error {
	models := []llm.Model{
		{Provider: provider, Name: "gemini-3.1-pro-preview", ContextLimit: 1048576},
		{Provider: provider, Name: "gemini-3.1-pro-preview-customtools", ContextLimit: 1048576},
		{Provider: provider, Name: "gemini-3.1-flash-lite", ContextLimit: 1048576},
		{Provider: provider, Name: "gemini-3.5-flash", ContextLimit: 1048576},
		{Provider: provider, Name: "gemma-4-26b-a4b-it", ContextLimit: 256000},
		{Provider: provider, Name: "gemma-4-31b-it", ContextLimit: 256000},
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
				return nil, fmt.Errorf("create gemini llm: %w", err)
			}
			return newLLM, nil
		}); err != nil {
			return fmt.Errorf("failed to setup model: %w", err)
		}
	}
	return nil
}
