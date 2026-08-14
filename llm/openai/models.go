package openai

import (
	"fmt"
	"net/http"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/openai/internal"
)

const provider = "openai"

var (
	Gpt56Sol   = llm.Model{Provider: provider, Name: "gpt-5.6-sol", ContextWindow: 272_000}
	Gpt56Terra = llm.Model{Provider: provider, Name: "gpt-5.6-terra", ContextWindow: 272_000}
	Gpt56Luna  = llm.Model{Provider: provider, Name: "gpt-5.6-luna", ContextWindow: 272_000}
	Gpt55      = llm.Model{Provider: provider, Name: "gpt-5.5", ContextWindow: 272_000}
	Gpt55Pro   = llm.Model{Provider: provider, Name: "gpt-5.5-pro", ContextWindow: 272_000}
	Gpt54      = llm.Model{Provider: provider, Name: "gpt-5.4", ContextWindow: 272_000}
	Gpt54Mini  = llm.Model{Provider: provider, Name: "gpt-5.4-mini", ContextWindow: 400_000}
	Gpt54Nano  = llm.Model{Provider: provider, Name: "gpt-5.4-nano", ContextWindow: 400_000}
)

func Setup(apiKey string) error {
	models := []llm.Model{
		Gpt56Sol,
		Gpt56Terra,
		Gpt56Luna,
		Gpt55,
		Gpt55Pro,
		Gpt54,
		Gpt54Mini,
		Gpt54Nano,
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
				return nil, fmt.Errorf("create openai llm: %w", err)
			}
			return newLLM, nil
		}); err != nil {
			return fmt.Errorf("failed to setup model: %w", err)
		}
	}
	return nil
}

// HasLocalOAuth checks if a valid auth.json is discoverable on the system.
func HasLocalOAuth() bool {
	am := internal.NewAuthManager(nil)
	path, _, err := am.FindAndLoadAuthFile()
	return err == nil && path != ""
}

// SetupOAuth registers the OpenAI models configured with the transparent OAuth proxy.
func SetupOAuth() error {
	models := []llm.Model{
		Gpt56Sol,
		Gpt56Terra,
		Gpt56Luna,
		Gpt55,
		Gpt55Pro,
		Gpt54,
		Gpt54Mini,
		Gpt54Nano,
	}
	for _, model := range models {
		registeredModel := model
		if err := llm.RegisterModel(registeredModel, func(level llm.ReasoningLevel) (llm.ModelClient, error) {
			newLLM, err := NewLLM(LLMConfig{
				APIKey:         "oauth-placeholder",
				Model:          registeredModel,
				ReasoningLevel: level,
				Client:         &http.Client{Transport: internal.NewOAuthTransport(nil)},
			})
			if err != nil {
				return nil, fmt.Errorf("create openai oauth llm: %w", err)
			}
			return newLLM, nil
		}); err != nil {
			return fmt.Errorf("failed to setup model: %w", err)
		}
	}
	return nil
}
