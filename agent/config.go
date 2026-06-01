package agent

import (
	"os"
	"path/filepath"
)

const (
	appName = "ronin"

	skillsDir = "skills"
)

func SkillsDir() (string, error) {
	configDir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, skillsDir), nil
}

func EnsureConfigDir() error {
	configDir, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}
	return nil
}

func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName), nil
}
