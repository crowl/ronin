package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func newClient(cfg Config) *client {
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Response{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("typesafe %s: %s", resp.Status, truncate(string(payload), 256))
	}

	var out Response
	if err := json.Unmarshal(payload, &out); err != nil {
		return Response{}, fmt.Errorf("decode typesafe response: %w", err)
	}
	if out.Answers == nil {
		out.Answers = map[string]Answer{}
	}
	return out, nil
}

func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}
