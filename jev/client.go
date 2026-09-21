package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/crowl/ronin/plugin"
)

// question is the TypeSafe encoding of a decision question. Criteria is a map
// for noul/choice and a string slice for score; it is encoded as-is.
type question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// request is the TypeSafe System One request body.
type request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]question `json:"questions"`
}

// response is the TypeSafe System One response body.
type response struct {
	Model   string            `json:"model"`
	Answers map[string]answer `json:"answers"`
}

// answer is the TypeSafe encoding of one typed decision answer.
type answer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type Client struct {
	http     *http.Client
	endpoint string
	model    string
	apiKey   string
}

const (
	maxAttempts   = 3
	baseDelay     = 100 * time.Millisecond
	maxRetryDelay = time.Second
)

// NewClient returns a Jev client that posts decisions to the TypeSafe System
// One endpoint. The client is safe for concurrent use. apiKey must be
// non-empty; surrounding whitespace is not trimmed.
func NewClient(apiKey string) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("jev: API key is required")
	}
	return newClient(config{
		Endpoint: defaultEndpoint,
		Model:    defaultModel,
		APIKey:   apiKey,
		Timeout:  defaultTimeout,
	}), nil
}

func newClient(cfg config) *Client {
	return &Client{
		http:     &http.Client{Timeout: cfg.Timeout},
		endpoint: cfg.Endpoint,
		model:    cfg.Model,
		apiKey:   cfg.APIKey,
	}
}

// Decide posts state and questions and returns the decoded answers. Network
// failures, non-success statuses, and malformed responses are returned as
// errors; 429 and 529 are retried a bounded number of times.
func (c *Client) Decide(ctx context.Context, state any, questions map[string]plugin.Question) (map[string]plugin.Answer, error) {
	body, err := json.Marshal(request{Model: c.model, State: state, Questions: encodeQuestions(questions)})
	if err != nil {
		return nil, err
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := c.do(ctx, body)
		if err != nil {
			return nil, err
		}
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529 {
			if attempt < maxAttempts {
				if err := waitForRetry(ctx, retryDelay(resp, attempt)); err != nil {
					return nil, err
				}
				continue
			}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("typesafe %s: %s", resp.Status, truncateBytes(payload, 256))
		}

		var out response
		if err := json.Unmarshal(payload, &out); err != nil {
			return nil, fmt.Errorf("decode typesafe response: %w", err)
		}
		return decodeAnswers(out.Answers), nil
	}
	return nil, errorsAfterRetries()
}

func encodeQuestions(in map[string]plugin.Question) map[string]question {
	out := make(map[string]question, len(in))
	for name, q := range in {
		out[name] = question{Type: q.Type, Instructions: q.Instructions, Criteria: q.Criteria}
	}
	return out
}

func decodeAnswers(in map[string]answer) map[string]plugin.Answer {
	out := make(map[string]plugin.Answer, len(in))
	for name, a := range in {
		out[name] = plugin.Answer{
			Type: a.Type, Noul: a.Noul, Choice: a.Choice, Score: a.Score,
			Confidence: a.Confidence, Probabilities: a.Probabilities,
		}
	}
	return out
}

var _ plugin.Decider = (*Client)(nil)

func (c *Client) do(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return c.http.Do(req)
}

func retryDelay(resp *http.Response, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds >= 0 {
		return min(time.Duration(seconds)*time.Second, maxRetryDelay)
	}
	return min(baseDelay*time.Duration(1<<(attempt-1)), maxRetryDelay)
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func errorsAfterRetries() error {
	return fmt.Errorf("typesafe request failed after %d attempts", maxAttempts)
}

func truncateBytes(b []byte, n int) string {
	if n <= 0 || len(b) <= n {
		return string(b)
	}
	b = b[:n]
	for !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b)
}
