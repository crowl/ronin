package xai

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/openai"
)

const (
	provider       = "xai"
	defaultBaseURL = "https://api.x.ai/v1/responses"
)

var (
	Grok46      = llm.Model{Provider: provider, Name: "grok-4.6", ContextWindow: 500_000}
	GrokBuild01 = llm.Model{Provider: provider, Name: "grok-build-0.1", ContextWindow: 256_000}

	models = []llm.Model{
		Grok46,
		GrokBuild01,
	}
)

// Setup registers the xAI models using the default Responses API endpoint.
func Setup(apiKey string) error {
	if apiKey == "" {
		return errors.New("XAI_API_KEY is required")
	}
	return registerModels(apiKey, defaultBaseURL)
}

// SetupWithBaseURL registers the xAI models using baseURL as the API root.
func SetupWithBaseURL(apiKey, baseURL string) error {
	if apiKey == "" {
		return errors.New("XAI_API_KEY is required")
	}
	endpoint, err := responsesEndpoint(baseURL)
	if err != nil {
		return err
	}
	return registerModels(apiKey, endpoint)
}

func responsesEndpoint(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid XAI_BASE_URL: %w", err)
	}
	if !parsed.IsAbs() {
		return "", errors.New("invalid XAI_BASE_URL: URL must be absolute")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", errors.New("invalid XAI_BASE_URL: URL scheme must be http or https")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("invalid XAI_BASE_URL: URL must include a host")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", errors.New("invalid XAI_BASE_URL: URL must not contain a query string")
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" || strings.Contains(baseURL, "#") {
		return "", errors.New("invalid XAI_BASE_URL: URL must not contain a fragment")
	}
	return strings.TrimRight(baseURL, "/") + "/responses", nil
}

func registerModels(apiKey, endpoint string) error {
	for _, model := range models {
		registeredModel := model
		if err := llm.RegisterModel(registeredModel, func(level llm.ReasoningLevel) (llm.ModelClient, error) {
			client, err := openai.NewLLM(openai.LLMConfig{
				BaseURL:        endpoint,
				APIKey:         apiKey,
				Model:          registeredModel,
				ReasoningLevel: level,
			})
			if err != nil {
				return nil, fmt.Errorf("create xAI llm: %w", err)
			}
			return client, nil
		}); err != nil {
			return fmt.Errorf("failed to setup model: %w", err)
		}
	}
	return nil
}
