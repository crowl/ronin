package config

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	appName = "ronin"

	configFileName   = "config.json"
	skillsDirName    = "skills"
	workflowsDirName = "workflows"

	defaultModelProvider  = "openai"
	defaultModelName      = "gpt-5.5"
	defaultReasoningLevel = "medium"
	defaultMaxTurns       = 512
)

//go:embed providers.json
var defaultProvidersJSON []byte

type Model struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

type OptionalBool struct {
	Set   bool
	Value bool
}

func (b *OptionalBool) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return errors.New("boolean must not be null")
	}
	if err := json.Unmarshal(data, &b.Value); err != nil {
		return err
	}
	b.Set = true
	return nil
}

func (b OptionalBool) MarshalJSON() ([]byte, error) {
	return json.Marshal(b.Value)
}

func (b OptionalBool) IsZero() bool {
	return !b.Set
}

type OptionalUint32 struct {
	Set   bool
	Value uint32
}

func (n *OptionalUint32) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return errors.New("number must not be null")
	}
	if err := json.Unmarshal(data, &n.Value); err != nil {
		return err
	}
	n.Set = true
	return nil
}

func (n OptionalUint32) MarshalJSON() ([]byte, error) {
	return json.Marshal(n.Value)
}

func (n OptionalUint32) IsZero() bool {
	return !n.Set
}

type OptionalFloat64 struct {
	Set   bool
	Value float64
}

func (n *OptionalFloat64) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return errors.New("number must not be null")
	}
	if err := json.Unmarshal(data, &n.Value); err != nil {
		return err
	}
	if n.Value < 0 {
		return errors.New("number must be non-negative")
	}
	n.Set = true
	return nil
}

func (n OptionalFloat64) MarshalJSON() ([]byte, error) {
	return json.Marshal(n.Value)
}

func (n OptionalFloat64) IsZero() bool {
	return !n.Set
}

type Pricing struct {
	Input      OptionalFloat64 `json:"input"`
	Output     OptionalFloat64 `json:"output"`
	CacheRead  OptionalFloat64 `json:"cache_read"`
	CacheWrite OptionalFloat64 `json:"cache_write"`
}

func (p Pricing) IsZero() bool {
	return !p.Input.Set && !p.Output.Set && !p.CacheRead.Set && !p.CacheWrite.Set
}

type Reasoning struct {
	Mode   string   `json:"mode"`
	Levels []string `json:"levels"`
}

type ProviderModel struct {
	Enabled       OptionalBool   `json:"enabled"`
	ContextWindow OptionalUint32 `json:"context_window"`
	Reasoning     Reasoning      `json:"reasoning"`
	Pricing       Pricing        `json:"pricing"`
}

type Provider struct {
	Enabled    OptionalBool             `json:"enabled"`
	Adapter    string                   `json:"adapter"`
	BaseURL    string                   `json:"base_url"`
	BaseURLEnv string                   `json:"base_url_env"`
	APIKeyEnv  string                   `json:"api_key_env"`
	Models     map[string]ProviderModel `json:"models"`
}

type MCPServer struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
}

type Settings struct {
	Model          Model                `json:"model"`
	ReasoningLevel string               `json:"reasoning_level"`
	MaxTurns       int                  `json:"max_turns"`
	Providers      map[string]Provider  `json:"providers,omitempty"`
	MCPServers     map[string]MCPServer `json:"mcp_servers,omitempty"`
}

// Load resolves the config directory, writes a default config.json on first
// run when none exists, then strictly parses and validates the file.
func Load() (Settings, error) {
	if err := EnsureDir(); err != nil {
		return Settings{}, err
	}

	path, err := ConfigFilePath()
	if err != nil {
		return Settings{}, err
	}

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := writeDefault(path); err != nil {
			return Settings{}, fmt.Errorf("write default config %q: %w", path, err)
		}
	} else if err != nil {
		return Settings{}, fmt.Errorf("stat config %q: %w", path, err)
	}

	file, err := os.Open(path)
	if err != nil {
		return Settings{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()

	var settings Settings
	if err := decoder.Decode(&settings); err != nil {
		return Settings{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Settings{}, fmt.Errorf("parse config %q: %w", path, err)
	}

	if err := validate(settings); err != nil {
		return Settings{}, fmt.Errorf("invalid config %q: %w", path, err)
	}

	providers, err := mergedProviders(settings.Providers)
	if err != nil {
		return Settings{}, fmt.Errorf("invalid config %q: %w", path, err)
	}
	settings.Providers = providers

	return settings, nil
}

// Dir resolves the ronin config directory: $XDG_CONFIG_HOME/ronin when
// XDG_CONFIG_HOME is set, otherwise $HOME/.config/ronin.
func Dir() (string, error) {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, appName), nil
	}

	home := os.Getenv("HOME")
	if home == "" {
		return "", errors.New("resolve config dir: neither XDG_CONFIG_HOME nor HOME is set")
	}

	return filepath.Join(home, ".config", appName), nil
}

func EnsureDir() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir %q: %w", dir, err)
	}
	return nil
}

// DataDir resolves the ronin data directory: $XDG_DATA_HOME/ronin when
// XDG_DATA_HOME is set, otherwise $HOME/.local/share/ronin. Durable runtime
// data lives here, separate from configuration.
func DataDir() (string, error) {
	if base := os.Getenv("XDG_DATA_HOME"); base != "" {
		return filepath.Join(base, appName), nil
	}

	home := os.Getenv("HOME")
	if home == "" {
		return "", errors.New("resolve data dir: neither XDG_DATA_HOME nor HOME is set")
	}

	return filepath.Join(home, ".local", "share", appName), nil
}

// EnsureDataDir creates the ronin data directory if it does not exist and
// returns its path.
func EnsureDataDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create data dir %q: %w", dir, err)
	}
	return dir, nil
}

func SkillsDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, skillsDirName), nil
}

func WorkflowsDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, workflowsDirName), nil
}

func ConfigFilePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFileName), nil
}

func defaultSettings() Settings {
	return Settings{
		Model: Model{
			Provider: defaultModelProvider,
			Name:     defaultModelName,
		},
		ReasoningLevel: defaultReasoningLevel,
		MaxTurns:       defaultMaxTurns,
	}
}

func writeDefault(path string) error {
	data, err := json.MarshalIndent(defaultSettings(), "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func validate(settings Settings) error {
	if settings.Model.Provider == "" {
		return errors.New("model.provider must not be empty")
	}
	if settings.Model.Name == "" {
		return errors.New("model.name must not be empty")
	}
	if settings.ReasoningLevel == "" {
		return errors.New("reasoning_level must not be empty")
	}
	if settings.MaxTurns <= 0 {
		return fmt.Errorf("max_turns must be greater than 0, got %d", settings.MaxTurns)
	}
	for name, server := range settings.MCPServers {
		if name == "" {
			return errors.New("mcp_servers names must not be empty")
		}
		hasCommand := strings.TrimSpace(server.Command) != ""
		hasURL := strings.TrimSpace(server.URL) != ""
		if hasCommand == hasURL {
			return fmt.Errorf("mcp_servers.%s must set exactly one of command or url", name)
		}
		if hasURL && (len(server.Args) != 0 || len(server.Env) != 0) {
			return fmt.Errorf("mcp_servers.%s args and env require command", name)
		}
		for key := range server.Env {
			if key == "" || strings.ContainsRune(key, '=') {
				return fmt.Errorf("mcp_servers.%s.env contains invalid name %q", name, key)
			}
		}
	}
	return nil
}

func mergedProviders(overrides map[string]Provider) (map[string]Provider, error) {
	var defaults struct {
		Providers map[string]Provider `json:"providers"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(defaultProvidersJSON)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&defaults); err != nil {
		return nil, fmt.Errorf("parse embedded provider catalog: %w", err)
	}

	providers := make(map[string]Provider, len(defaults.Providers)+len(overrides))
	for name, provider := range defaults.Providers {
		providers[name] = cloneProvider(provider)
	}
	for name, override := range overrides {
		provider := providers[name]
		provider = mergeProvider(provider, override)
		providers[name] = provider
	}
	if err := validateProviders(providers); err != nil {
		return nil, err
	}
	return providers, nil
}

func cloneProvider(provider Provider) Provider {
	provider.Models = cloneProviderModels(provider.Models)
	return provider
}

func cloneProviderModels(models map[string]ProviderModel) map[string]ProviderModel {
	if models == nil {
		return nil
	}
	cloned := make(map[string]ProviderModel, len(models))
	for name, model := range models {
		model.Reasoning.Levels = append([]string(nil), model.Reasoning.Levels...)
		cloned[name] = model
	}
	return cloned
}

func mergeProvider(base, override Provider) Provider {
	if override.Enabled.Set {
		base.Enabled = override.Enabled
	}
	if override.Adapter != "" {
		base.Adapter = override.Adapter
	}
	if override.BaseURL != "" {
		base.BaseURL = override.BaseURL
	}
	if override.BaseURLEnv != "" {
		base.BaseURLEnv = override.BaseURLEnv
	}
	if override.APIKeyEnv != "" {
		base.APIKeyEnv = override.APIKeyEnv
	}
	if base.Models == nil {
		base.Models = make(map[string]ProviderModel)
	}
	for name, modelOverride := range override.Models {
		base.Models[name] = mergeProviderModel(base.Models[name], modelOverride)
	}
	return base
}

func mergeProviderModel(base, override ProviderModel) ProviderModel {
	if override.Enabled.Set {
		base.Enabled = override.Enabled
	}
	if override.ContextWindow.Set {
		base.ContextWindow = override.ContextWindow
	}
	if override.Reasoning.Mode != "" {
		base.Reasoning.Mode = override.Reasoning.Mode
	}
	if override.Reasoning.Levels != nil {
		base.Reasoning.Levels = append([]string(nil), override.Reasoning.Levels...)
	}
	if override.Pricing.Input.Set {
		base.Pricing.Input = override.Pricing.Input
	}
	if override.Pricing.Output.Set {
		base.Pricing.Output = override.Pricing.Output
	}
	if override.Pricing.CacheRead.Set {
		base.Pricing.CacheRead = override.Pricing.CacheRead
	}
	if override.Pricing.CacheWrite.Set {
		base.Pricing.CacheWrite = override.Pricing.CacheWrite
	}
	return base
}

func validateProviders(providers map[string]Provider) error {
	providerNames := make([]string, 0, len(providers))
	for name := range providers {
		providerNames = append(providerNames, name)
	}
	slices.Sort(providerNames)
	for _, name := range providerNames {
		provider := providers[name]
		if name == "" {
			return errors.New("providers names must not be empty")
		}
		if provider.Enabled.Set && !provider.Enabled.Value {
			continue
		}
		switch provider.Adapter {
		case "openai", "anthropic", "google":
		default:
			return fmt.Errorf("providers.%s.adapter %q is not supported", name, provider.Adapter)
		}
		if provider.BaseURL == "" {
			return fmt.Errorf("providers.%s.base_url must not be empty", name)
		}
		if err := validateProviderURL(provider.BaseURL); err != nil {
			return fmt.Errorf("providers.%s.base_url: %w", name, err)
		}
		if provider.APIKeyEnv == "" {
			return fmt.Errorf("providers.%s.api_key_env must not be empty", name)
		}
		if !validEnvironmentName(provider.APIKeyEnv) {
			return fmt.Errorf("providers.%s.api_key_env contains invalid environment variable name %q", name, provider.APIKeyEnv)
		}
		if provider.BaseURLEnv != "" && !validEnvironmentName(provider.BaseURLEnv) {
			return fmt.Errorf("providers.%s.base_url_env contains invalid environment variable name %q", name, provider.BaseURLEnv)
		}
		modelNames := make([]string, 0, len(provider.Models))
		for modelName := range provider.Models {
			if modelName == "" {
				return fmt.Errorf("providers.%s.models names must not be empty", name)
			}
			modelNames = append(modelNames, modelName)
		}
		slices.Sort(modelNames)
		for _, modelName := range modelNames {
			model := provider.Models[modelName]
			if model.Enabled.Set && !model.Enabled.Value {
				continue
			}
			if !model.ContextWindow.Set || model.ContextWindow.Value == 0 {
				return fmt.Errorf("providers.%s.models.%s.context_window must be greater than zero", name, modelName)
			}
			switch model.Reasoning.Mode {
			case "none":
			case "effort":
			case "budget":
				if provider.Adapter != "anthropic" {
					return fmt.Errorf("providers.%s.models.%s reasoning mode budget requires the anthropic adapter", name, modelName)
				}
			default:
				return fmt.Errorf("providers.%s.models.%s.reasoning.mode %q is invalid", name, modelName, model.Reasoning.Mode)
			}
			if len(model.Reasoning.Levels) == 0 {
				return fmt.Errorf("providers.%s.models.%s.reasoning.levels must not be empty", name, modelName)
			}
			seen := make(map[string]bool, len(model.Reasoning.Levels))
			previousRank := -1
			for _, level := range model.Reasoning.Levels {
				if seen[level] {
					return fmt.Errorf("providers.%s.models.%s.reasoning.levels contains duplicate %q", name, modelName, level)
				}
				seen[level] = true
				rank := reasoningLevelRank(level)
				if rank < 0 {
					return fmt.Errorf("providers.%s.models.%s.reasoning.levels contains invalid level %q", name, modelName, level)
				}
				if rank <= previousRank {
					return fmt.Errorf("providers.%s.models.%s.reasoning.levels must use ascending order", name, modelName)
				}
				previousRank = rank
			}
			if model.Reasoning.Mode == "none" && (len(model.Reasoning.Levels) != 1 || model.Reasoning.Levels[0] != "off") {
				return fmt.Errorf("providers.%s.models.%s reasoning mode none only supports off", name, modelName)
			}
			for _, entry := range pricingRates(model.Pricing) {
				if entry.rate.Set && entry.rate.Value < 0 {
					return fmt.Errorf("providers.%s.models.%s.pricing.%s must be non-negative", name, modelName, entry.name)
				}
			}
		}
	}
	return nil
}

func reasoningLevelRank(level string) int {
	switch level {
	case "off":
		return 0
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	case "xhigh":
		return 4
	default:
		return -1
	}
}

func pricingRates(pricing Pricing) []struct {
	name string
	rate OptionalFloat64
} {
	return []struct {
		name string
		rate OptionalFloat64
	}{
		{name: "input", rate: pricing.Input},
		{name: "output", rate: pricing.Output},
		{name: "cache_read", rate: pricing.CacheRead},
		{name: "cache_write", rate: pricing.CacheWrite},
	}
}

func validEnvironmentName(name string) bool {
	for i, r := range name {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return name != ""
}

func validateProviderURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return err
	}
	if !parsed.IsAbs() || parsed.Hostname() == "" {
		return errors.New("must be an absolute URL with a host")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("scheme must be http or https")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("must not contain a query or fragment")
	}
	return nil
}
