package jev

import "time"

const (
	defaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	defaultModel    = "jev-latest"
	defaultTimeout  = 10 * time.Second
)

// config holds the fixed Jev client connection settings. It is intentionally
// private: TYPESAFE_API_KEY is the only user-facing Jev configuration.
type config struct {
	Endpoint string
	Model    string
	APIKey   string
	Timeout  time.Duration
}
