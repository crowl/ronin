package jev

import "time"

const (
	defaultEndpoint      = "https://api.typesafe.ai/v1/systemone"
	defaultModel         = "jev-latest"
	defaultTimeout       = 3 * time.Second
	defaultMinConfidence = 0.55
	maxTaskBytes         = 8 << 10
	maxContextBytes      = 16 << 10
	maxArgumentsBytes    = 16 << 10
)

// config holds the fixed Jev client and policy settings. It is intentionally
// private: TYPESAFE_API_KEY is the only user-facing Jev configuration.
type config struct {
	Endpoint      string
	Model         string
	APIKey        string
	Timeout       time.Duration
	MinConfidence float64
}
