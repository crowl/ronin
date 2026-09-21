package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Question is one typed Jev question. Criteria is a map for noul/choice and a
// string slice for score; it is encoded as-is.
type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Request is the TypeSafe System One request body.
type Request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// Response is the TypeSafe System One response body.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
}

// Answer is one typed Jev answer. Unused fields stay zero for other types.
type Answer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type client struct {
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

func newClient(cfg config) *client {
	return &client{
		http:     &http.Client{Timeout: cfg.Timeout},
		endpoint: cfg.Endpoint,
		model:    cfg.Model,
		apiKey:   cfg.APIKey,
	}
}

func (c *client) Decide(ctx context.Context, state any, questions map[string]Question) (Response, error) {
	body, err := json.Marshal(Request{Model: c.model, State: state, Questions: questions})
	if err != nil {
		return Response{}, err
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := c.do(ctx, body)
		if err != nil {
			return Response{}, err
		}
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return Response{}, readErr
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529 {
			if attempt < maxAttempts {
				if err := waitForRetry(ctx, retryDelay(resp, attempt)); err != nil {
					return Response{}, err
				}
				continue
			}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return Response{}, fmt.Errorf("typesafe %s: %s", resp.Status, truncate(string(payload), 256))
		}

		var out Response
		if err := json.Unmarshal(payload, &out); err != nil {
			return Response{}, fmt.Errorf("decode typesafe response: %w", err)
		}
		return out, nil
	}
	return Response{}, errorsAfterRetries()
}

func (c *client) do(ctx context.Context, body []byte) (*http.Response, error) {
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
