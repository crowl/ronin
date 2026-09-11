package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrContextLimit identifies a provider rejection caused by exhausted input context.
	ErrContextLimit = errors.New("model context limit exceeded")
	// ErrAuthentication identifies rejected credentials or insufficient authorization.
	ErrAuthentication = errors.New("provider authentication failed")
	// ErrRateLimit identifies provider throttling.
	ErrRateLimit = errors.New("provider rate limit exceeded")
	// ErrUnavailable identifies transient provider unavailability.
	ErrUnavailable = errors.New("provider unavailable")
)

// HTTPError preserves diagnostics while classifying structured provider codes.
// Text fallback examines only the error message, never arbitrary JSON fields.
func HTTPError(provider string, status int, body string) error {
	diagnostic := strings.TrimSpace(body)
	err := fmt.Errorf("%s status %d: %s", provider, status, diagnostic)
	var category error
	switch status {
	case 401, 403:
		category = ErrAuthentication
	case 429:
		category = ErrRateLimit
	case 500, 502, 503, 504, 529:
		category = ErrUnavailable
	case 400, 413, 422:
		var envelope struct {
			Error struct {
				Code    json.RawMessage `json:"code"`
				Type    string          `json:"type"`
				Status  string          `json:"status"`
				Message string          `json:"message"`
			} `json:"error"`
		}
		message := diagnostic
		if json.Valid([]byte(diagnostic)) {
			message = ""
			if json.Unmarshal([]byte(diagnostic), &envelope) == nil {
				var code string
				_ = json.Unmarshal(envelope.Error.Code, &code)
				for _, value := range []string{code, envelope.Error.Type, envelope.Error.Status} {
					switch strings.ToLower(value) {
					case "context_length_exceeded", "model_context_window_exceeded", "context_window_exceeded":
						category = ErrContextLimit
					}
				}
				message = envelope.Error.Message
			}
		}
		if category == nil {
			text := strings.ToLower(message)
			for _, marker := range []string{"maximum context length", "prompt is too long", "input token count exceeds", "exceeds the maximum number of tokens"} {
				if strings.Contains(text, marker) {
					category = ErrContextLimit
					break
				}
			}
		}
	}
	if category != nil {
		return fmt.Errorf("%w: %w", category, err)
	}
	return err
}
