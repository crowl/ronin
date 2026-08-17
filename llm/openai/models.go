package openai

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

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

	models = []llm.Model{
		Gpt56Sol,
		Gpt56Terra,
		Gpt56Luna,
		Gpt55,
		Gpt55Pro,
		Gpt54,
		Gpt54Mini,
		Gpt54Nano,
	}
)

// Setup registers the OpenAI models using the default Responses API endpoint.
func Setup(apiKey string) error {
	return registerModels(func(model llm.Model, level llm.ReasoningLevel) (llm.ModelClient, error) {
		newLLM, err := NewLLM(LLMConfig{
			APIKey:         apiKey,
			Model:          model,
			ReasoningLevel: level,
		})
		if err != nil {
			return nil, fmt.Errorf("create openai llm: %w", err)
		}
		return newLLM, nil
	})
}

// SetupWithBaseURL registers the OpenAI models using baseURL as the API root.
func SetupWithBaseURL(apiKey, baseURL string) error {
	endpoint, err := responsesEndpoint(baseURL)
	if err != nil {
		return err
	}
	return registerModels(func(model llm.Model, level llm.ReasoningLevel) (llm.ModelClient, error) {
		newLLM, err := NewLLM(LLMConfig{
			BaseURL:        endpoint,
			APIKey:         apiKey,
			Model:          model,
			ReasoningLevel: level,
		})
		if err != nil {
			return nil, fmt.Errorf("create openai llm: %w", err)
		}
		return newLLM, nil
	})
}

func responsesEndpoint(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid OPENAI_BASE_URL: %w", err)
	}
	if !parsed.IsAbs() {
		return "", fmt.Errorf("invalid OPENAI_BASE_URL: URL must be absolute")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("invalid OPENAI_BASE_URL: URL scheme must be http or https")
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("invalid OPENAI_BASE_URL: URL must include a host")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", fmt.Errorf("invalid OPENAI_BASE_URL: URL must not contain a query string")
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" || strings.Contains(baseURL, "#") {
		return "", fmt.Errorf("invalid OPENAI_BASE_URL: URL must not contain a fragment")
	}
	return strings.TrimRight(baseURL, "/") + "/responses", nil
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

// HasLocalOAuth checks if a valid auth.json is discoverable on the system.
func HasLocalOAuth() bool {
	am := internal.NewAuthManager(nil)
	path, _, err := am.FindAndLoadAuthFile()
	return err == nil && path != ""
}

// SetupOAuth registers the OpenAI models configured with the transparent OAuth proxy.
func SetupOAuth() error {
	return registerModels(func(model llm.Model, level llm.ReasoningLevel) (llm.ModelClient, error) {
		newLLM, err := NewLLM(LLMConfig{
			APIKey:         "oauth-placeholder",
			Model:          model,
			ReasoningLevel: level,
			Client:         &http.Client{Transport: internal.NewOAuthTransport(nil)},
		})
		if err != nil {
			return nil, fmt.Errorf("create openai oauth llm: %w", err)
		}
		return newLLM, nil
	})
}
