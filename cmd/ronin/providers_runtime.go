package main

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/anthropic"
	"github.com/crowl/ronin/llm/google"
	"github.com/crowl/ronin/llm/openai"
)

func setupProviders(settings ...config.Settings) error {
	if len(settings) > 1 {
		return fmt.Errorf("setup providers accepts at most one settings value")
	}
	var providerSettings config.Settings
	if len(settings) > 0 {
		providerSettings = settings[0]
	} else {
		var err error
		providerSettings, err = config.Load()
		if err != nil {
			return err
		}
	}
	providerNames := make([]string, 0, len(providerSettings.Providers))
	for providerName := range providerSettings.Providers {
		providerNames = append(providerNames, providerName)
	}
	slices.Sort(providerNames)
	for _, providerName := range providerNames {
		provider := providerSettings.Providers[providerName]
		if provider.Enabled.Set && !provider.Enabled.Value {
			continue
		}
		baseURL := provider.BaseURL
		baseURLWasOverridden := false
		baseURLEnv := provider.BaseURLEnv
		if baseURLEnv == "" {
			baseURLEnv = "base_url"
		}
		if provider.BaseURLEnv != "" {
			if value, ok := os.LookupEnv(provider.BaseURLEnv); ok {
				baseURL = value
				baseURLWasOverridden = true
			}
		}
		apiKey := os.Getenv(provider.APIKeyEnv)
		if apiKey == "" {
			if baseURLWasOverridden {
				return fmt.Errorf("%s is required when %s is set", provider.APIKeyEnv, provider.BaseURLEnv)
			}
			continue
		}
		if err := validateRuntimeProviderURL(baseURL); err != nil {
			return fmt.Errorf("%s: invalid %s: %w", providerName, baseURLEnv, err)
		}

		models := configuredModels(providerName, provider.Models)
		if len(models) == 0 {
			continue
		}
		var err error
		switch provider.Adapter {
		case "openai":
			err = openai.SetupModels(apiKey, baseURL, models)
		case "anthropic":
			err = anthropic.SetupModels(apiKey, baseURL, models)
		case "google":
			err = google.SetupModels(apiKey, baseURL, models)
		default:
			return fmt.Errorf("provider %q uses unsupported adapter %q", providerName, provider.Adapter)
		}
		if err != nil {
			return fmt.Errorf("%s LLM provider setup failed: %w", providerName, err)
		}
	}
	return nil
}

func validateRuntimeProviderURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return err
	}
	if !parsed.IsAbs() || parsed.Hostname() == "" {
		return fmt.Errorf("URL must be absolute and include a host")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("URL scheme must be http or https")
	}
	return nil
}

func configuredModels(provider string, configured map[string]config.ProviderModel) []llm.Model {
	models := make([]llm.Model, 0, len(configured))
	for name, model := range configured {
		if model.Enabled.Set && !model.Enabled.Value {
			continue
		}
		levels := make([]llm.ReasoningLevel, 0, len(model.Reasoning.Levels))
		for _, level := range model.Reasoning.Levels {
			levels = append(levels, llm.ReasoningLevel(level))
		}
		models = append(models, llm.Model{
			Provider:           provider,
			Name:               name,
			ContextWindow:      model.ContextWindow.Value,
			ReasoningMode:      llm.ReasoningMode(model.Reasoning.Mode),
			SupportedReasoning: llm.NewReasoningSet(levels...),
			Pricing: llm.ModelPricing{
				Input:         model.Pricing.Input.Value,
				Output:        model.Pricing.Output.Value,
				CacheRead:     model.Pricing.CacheRead.Value,
				CacheWrite:    model.Pricing.CacheWrite.Value,
				HasInput:      model.Pricing.Input.Set,
				HasOutput:     model.Pricing.Output.Set,
				HasCacheRead:  model.Pricing.CacheRead.Set,
				HasCacheWrite: model.Pricing.CacheWrite.Set,
			},
		})
	}
	slices.SortFunc(models, func(a, b llm.Model) int { return strings.Compare(a.Name, b.Name) })
	return models
}
