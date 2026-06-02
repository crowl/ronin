package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	appName = "ronin"

	configFileName = "config.json"
	skillsDirName  = "skills"

	defaultModelProvider  = "openai"
	defaultModelName      = "gpt-5.5"
	defaultReasoningLevel = "medium"
	defaultMaxTurns       = 512
)

type Model struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

type Settings struct {
	Model          Model  `json:"model"`
	ReasoningLevel string `json:"reasoning_level"`
	MaxTurns       int    `json:"max_turns"`
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

	if err := validate(settings); err != nil {
		return Settings{}, fmt.Errorf("invalid config %q: %w", path, err)
	}

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

func SkillsDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, skillsDirName), nil
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
	return nil
}
