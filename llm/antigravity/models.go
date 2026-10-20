package antigravity

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/crowl/ronin/llm"
)

const provider = "antigravity"

// models is the conservative fallback catalog. Antigravity advertises models
// per account entitlement and per client version, so the authoritative list
// comes from the provider configuration rather than from here.
var models = []llm.Model{
	{Provider: provider, Name: "gemini-3.1-pro", ContextWindow: 1048576},
	{Provider: provider, Name: "gemini-3.8-flash", ContextWindow: 1048576},
}

// Setup registers the Antigravity models against the default Cloud Code
// Assist root.
func Setup(apiKey string) error {
	return registerModelList(models, newClientFactory(apiKey, ""))
}

// SetupWithBaseURL registers the Antigravity models using baseURL as the API
// root.
func SetupWithBaseURL(apiKey, baseURL string) error {
	root, err := validateBaseURL(baseURL)
	if err != nil {
		return err
	}
	return registerModelList(models, newClientFactory(apiKey, root))
}

// SetupModels registers modelList using baseURL as the Cloud Code Assist root.
func SetupModels(apiKey, baseURL string, modelList []llm.Model) error {
	root, err := validateBaseURL(baseURL)
	if err != nil {
		return err
	}
	return registerModelList(modelList, newClientFactory(apiKey, root))
}

func newClientFactory(apiKey, baseURL string) func(llm.Model, llm.ReasoningLevel) (llm.ModelClient, error) {
	return func(model llm.Model, level llm.ReasoningLevel) (llm.ModelClient, error) {
		newLLM, err := NewLLM(LLMConfig{
			BaseURL:        baseURL,
			APIKey:         apiKey,
			Model:          model,
			ReasoningLevel: level,
		})
		if err != nil {
			return nil, fmt.Errorf("create antigravity llm: %w", err)
		}
		return newLLM, nil
	}
}

func validateBaseURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid ANTIGRAVITY_BASE_URL: %w", err)
	}
	if !parsed.IsAbs() {
		return "", fmt.Errorf("invalid ANTIGRAVITY_BASE_URL: URL must be absolute")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("invalid ANTIGRAVITY_BASE_URL: URL scheme must be http or https")
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("invalid ANTIGRAVITY_BASE_URL: URL must include a host")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", fmt.Errorf("invalid ANTIGRAVITY_BASE_URL: URL must not contain a query string")
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" || strings.Contains(baseURL, "#") {
		return "", fmt.Errorf("invalid ANTIGRAVITY_BASE_URL: URL must not contain a fragment")
	}
	return strings.TrimRight(baseURL, "/"), nil
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
