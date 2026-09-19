package jev

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEndpoint      = "https://api.typesafe.ai/v1/systemone"
	defaultModel         = "jev-latest"
	defaultTimeout       = 800 * time.Millisecond
	defaultMinConfidence = 0.55
	defaultAPIKeyEnv     = "TYPESAFE_API_KEY"
	maxStateBytes        = 8 << 10
)

// Mode controls whether the plugin calls Jev and whether denials are enforced.
type Mode string

const (
	// ModeOff skips Jev. This is the default when RONIN_JEV_MODE is unset.
	ModeOff Mode = "off"
	// ModeShadow calls Jev and logs the verdict but never denies the tool.
	ModeShadow Mode = "shadow"
	// ModeEnforce denies the tool when Jev's answers cross the policy thresholds.
	ModeEnforce Mode = "enforce"
)

// Config is loaded from the process environment at Start.
type Config struct {
	Mode          Mode
	Endpoint      string
	Model         string
	APIKey        string
	APIKeyEnv     string
	Timeout       time.Duration
	MinConfidence float64
}

func loadConfig() (Config, error) {
	cfg := Config{
		Mode:          ModeOff,
		Endpoint:      defaultEndpoint,
		Model:         defaultModel,
		APIKeyEnv:     defaultAPIKeyEnv,
		Timeout:       defaultTimeout,
		MinConfidence: defaultMinConfidence,
	}

	if raw := strings.TrimSpace(os.Getenv("RONIN_JEV_MODE")); raw != "" {
		switch Mode(strings.ToLower(raw)) {
		case ModeOff, ModeShadow, ModeEnforce:
			cfg.Mode = Mode(strings.ToLower(raw))
		default:
			return Config{}, fmt.Errorf("RONIN_JEV_MODE must be off, shadow, or enforce, got %q", raw)
		}
	}
	if raw := strings.TrimSpace(os.Getenv("RONIN_JEV_ENDPOINT")); raw != "" {
		cfg.Endpoint = raw
	}
	if raw := strings.TrimSpace(os.Getenv("RONIN_JEV_MODEL")); raw != "" {
		cfg.Model = raw
	}
	if raw := strings.TrimSpace(os.Getenv("RONIN_JEV_API_KEY_ENV")); raw != "" {
		cfg.APIKeyEnv = raw
	}
	cfg.APIKey = strings.TrimSpace(os.Getenv(cfg.APIKeyEnv))

	if raw := strings.TrimSpace(os.Getenv("RONIN_JEV_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("RONIN_JEV_TIMEOUT must be a positive Go duration, got %q", raw)
		}
		cfg.Timeout = d
	}
	if raw := strings.TrimSpace(os.Getenv("RONIN_JEV_MIN_CONFIDENCE")); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v < 0 || v > 1 {
			return Config{}, fmt.Errorf("RONIN_JEV_MIN_CONFIDENCE must be between 0 and 1, got %q", raw)
		}
		cfg.MinConfidence = v
	}
	return cfg, nil
}
