package google

import (
	"fmt"

	"github.com/crowl/ronin/llm"
)

const provider = "google"

func Setup(apiKey string) error {
	models := []llm.Model{
		{Provider: provider, Name: "gemini-3.1-pro-preview", ContextWindow: 1048576},
		{Provider: provider, Name: "gemini-3.1-pro-preview-customtools", ContextWindow: 1048576},
		{Provider: provider, Name: "gemini-3.1-flash-lite", ContextWindow: 1048576},
		{Provider: provider, Name: "gemini-3.5-flash", ContextWindow: 1048576},
		{Provider: provider, Name: "gemini-3.7-flash", ContextWindow: 1048576},
		{Provider: provider, Name: "gemma-4-12b-it", ContextWindow: 256000},
		{Provider: provider, Name: "gemma-4-26b-a4b-it", ContextWindow: 256000},
		{Provider: provider, Name: "gemma-4-31b-it", ContextWindow: 256000},
	}
	for _, model := range models {
		registeredModel := model
		if err := llm.RegisterModel(registeredModel, func(level llm.ReasoningLevel) (llm.ModelClient, error) {
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
