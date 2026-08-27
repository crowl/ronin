package anthropic

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/crowl/ronin/llm"
)

const provider = "anthropic"

var (
	ClaudeHaiku45 = llm.Model{Provider: provider, Name: "claude-haiku-4-5", ContextWindow: 1000000}
	ClaudeSonnet5 = llm.Model{Provider: provider, Name: "claude-sonnet-5", ContextWindow: 1000000}
	ClaudeOpus5   = llm.Model{Provider: provider, Name: "claude-opus-5", ContextWindow: 1000000}

	models = []llm.Model{
		ClaudeHaiku45,
		ClaudeSonnet5,
		ClaudeOpus5,
	}
)

func Setup(apiKey string) error {
	return registerModels(func(model llm.Model, level llm.ReasoningLevel) (llm.ModelClient, error) {
		newLLM, err := NewLLM(LLMConfig{
			APIKey:         apiKey,
			Model:          model,
			ReasoningLevel: level,
		})
		if err != nil {
			return nil, fmt.Errorf("create anthropic llm: %w", err)
		}
		return newLLM, nil
	})
}

// SetupWithBaseURL registers the Anthropic models using baseURL as the API root.
func SetupWithBaseURL(apiKey, baseURL string) error {
	root, err := validateBaseURL(baseURL)
	if err != nil {
		return err
	}
	endpoint := root + "/messages"
	return registerModels(func(model llm.Model, level llm.ReasoningLevel) (llm.ModelClient, error) {
		newLLM, err := NewLLM(LLMConfig{
			BaseURL:        endpoint,
			APIKey:         apiKey,
			Model:          model,
			ReasoningLevel: level,
		})
		if err != nil {
			return nil, fmt.Errorf("create anthropic llm: %w", err)
		}
		return newLLM, nil
	})
}

func validateBaseURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid ANTHROPIC_BASE_URL: %w", err)
	}
	if !parsed.IsAbs() {
		return "", fmt.Errorf("invalid ANTHROPIC_BASE_URL: URL must be absolute")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("invalid ANTHROPIC_BASE_URL: URL scheme must be http or https")
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("invalid ANTHROPIC_BASE_URL: URL must include a host")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", fmt.Errorf("invalid ANTHROPIC_BASE_URL: URL must not contain a query string")
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" || strings.Contains(baseURL, "#") {
		return "", fmt.Errorf("invalid ANTHROPIC_BASE_URL: URL must not contain a fragment")
	}
	return strings.TrimRight(baseURL, "/"), nil
}

func registerModels(newClient func(llm.Model, llm.ReasoningLevel) (llm.ModelClient, error)) error {
	for _, model := range models {
		registeredModel := model
		if err := llm.RegisterModel(registeredModel, func(level llm.ReasoningLevel) (llm.ModelClient, error) {
			return newClient(registeredModel, level)
		}); err != nil {
			return fmt.Errorf("failed to setup model: %w", err)
		}
	}
	return nil
}
