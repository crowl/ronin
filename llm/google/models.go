package google

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/crowl/ronin/llm"
)

const provider = "google"

var models = []llm.Model{
	{Provider: provider, Name: "gemini-3.1-pro-preview", ContextWindow: 1048576},
	{Provider: provider, Name: "gemini-3.1-pro-preview-customtools", ContextWindow: 1048576},
	{Provider: provider, Name: "gemini-3.1-flash-lite", ContextWindow: 1048576},
	{Provider: provider, Name: "gemini-3.5-flash", ContextWindow: 1048576},
	{Provider: provider, Name: "gemini-3.7-flash", ContextWindow: 1048576},
	{Provider: provider, Name: "gemma-4-12b-it", ContextWindow: 256000},
	{Provider: provider, Name: "gemma-4-26b-a4b-it", ContextWindow: 256000},
	{Provider: provider, Name: "gemma-4-31b-it", ContextWindow: 256000},
}

// Setup registers the Gemini models using the default Interactions API root.
func Setup(apiKey string) error {
	return registerModels(func(model llm.Model, level llm.ReasoningLevel) (llm.ModelClient, error) {
		newLLM, err := NewLLM(LLMConfig{
			APIKey:         apiKey,
			Model:          model,
			ReasoningLevel: level,
		})
		if err != nil {
			return nil, fmt.Errorf("create gemini llm: %w", err)
		}
		return newLLM, nil
	})
}

// SetupWithBaseURL registers the Gemini models using baseURL as the API root.
func SetupWithBaseURL(apiKey, baseURL string) error {
	root, err := validateBaseURL(baseURL)
	if err != nil {
		return err
	}
	return registerModels(func(model llm.Model, level llm.ReasoningLevel) (llm.ModelClient, error) {
		newLLM, err := NewLLM(LLMConfig{
			BaseURL:        root,
			APIKey:         apiKey,
			Model:          model,
			ReasoningLevel: level,
		})
		if err != nil {
			return nil, fmt.Errorf("create gemini llm: %w", err)
		}
		return newLLM, nil
	})
}

// SetupModels registers models using baseURL as the Gemini API root.
func SetupModels(apiKey, baseURL string, models []llm.Model) error {
	root, err := validateBaseURL(baseURL)
	if err != nil {
		return err
	}
	return registerModelList(models, func(model llm.Model, level llm.ReasoningLevel) (llm.ModelClient, error) {
		newLLM, err := NewLLM(LLMConfig{
			BaseURL:        root,
			APIKey:         apiKey,
			Model:          model,
			ReasoningLevel: level,
		})
		if err != nil {
			return nil, fmt.Errorf("create gemini llm: %w", err)
		}
		return newLLM, nil
	})
}

func validateBaseURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid GEMINI_BASE_URL: %w", err)
	}
	if !parsed.IsAbs() {
		return "", fmt.Errorf("invalid GEMINI_BASE_URL: URL must be absolute")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("invalid GEMINI_BASE_URL: URL scheme must be http or https")
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("invalid GEMINI_BASE_URL: URL must include a host")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", fmt.Errorf("invalid GEMINI_BASE_URL: URL must not contain a query string")
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" || strings.Contains(baseURL, "#") {
		return "", fmt.Errorf("invalid GEMINI_BASE_URL: URL must not contain a fragment")
	}
	return strings.TrimRight(baseURL, "/"), nil
}

func registerModels(newClient func(llm.Model, llm.ReasoningLevel) (llm.ModelClient, error)) error {
	return registerModelList(models, newClient)
}

func registerModelList(modelList []llm.Model, newClient func(llm.Model, llm.ReasoningLevel) (llm.ModelClient, error)) error {
	for _, model := range modelList {
		registeredModel := model
		if err := llm.RegisterModel(registeredModel, func(level llm.ReasoningLevel) (llm.ModelClient, error) {
			return newClient(registeredModel, level)
		}); err != nil {
			return fmt.Errorf("failed to setup model: %w", err)
		}
	}
	return nil
}
