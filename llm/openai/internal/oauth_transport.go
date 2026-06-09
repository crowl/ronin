package internal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type OAuthTransport struct {
	// Next is the underlying http.RoundTripper, defaulting to http.DefaultTransport.
	Next http.RoundTripper

	am *AuthManager

	mu       sync.Mutex
	authPath string
	authFile *AuthFile
}

// NewOAuthTransport returns a new initialized OAuthTransport.
func NewOAuthTransport(client *http.Client) *OAuthTransport {
	if client == nil {
		client = http.DefaultClient
	}
	return &OAuthTransport{
		Next: client.Transport,
		am:   NewAuthManager(client),
	}
}

// RoundTrip intercepts requests targeting api.openai.com responses endpoint,
// ensures the OAuth session is fresh, rewrites the URL, and injects custom headers.
func (t *OAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// We only intercept requests directed to api.openai.com/v1/responses
	isResponses := (req.URL.Host == "api.openai.com" && req.URL.Path == "/v1/responses") ||
		strings.HasSuffix(req.URL.Path, "/v1/responses") ||
		strings.HasSuffix(req.URL.Path, "/responses")

	if !isResponses {
		next := t.Next
		if next == nil {
			next = http.DefaultTransport
		}
		return next.RoundTrip(req)
	}

	// 1. Ensure we have valid and fresh tokens
	tokens, err := t.GetValidTokens(req.Context())
	if err != nil {
		return nil, fmt.Errorf("openai oauth: %w", err)
	}

	// 2. Clone the request to perform mutations safely
	clonedReq := req.Clone(req.Context())

	// 3. Rewrite request target to Codex responses backend API
	clonedReq.URL.Scheme = "https"
	clonedReq.URL.Host = "chatgpt.com"
	clonedReq.URL.Path = "/backend-api/codex/responses"
	clonedReq.Host = "chatgpt.com"

	// 4. Set mandatory headers
	clonedReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	clonedReq.Header.Set("chatgpt-account-id", tokens.AccountID)
	clonedReq.Header.Set("OpenAI-Beta", "responses=experimental")
	clonedReq.Header.Set("Origin", "https://chatgpt.com")
	clonedReq.Header.Set("Referer", "https://chatgpt.com")
	clonedReq.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	clonedReq.Header.Set("Accept", "text/event-stream, application/json, */*")
	clonedReq.Header.Set("Accept-Language", "en-US,en;q=0.9")

	next := t.Next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(clonedReq)
}

// GetValidTokens retrieves cached tokens, refreshes them if expired/near expiration, and returns them.
func (t *OAuthTransport) GetValidTokens(ctx context.Context) (*StoredTokens, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Lazy load auth.json if not already loaded
	if t.authFile == nil {
		path, authFile, err := t.am.FindAndLoadAuthFile()
		if err != nil {
			return nil, fmt.Errorf("load auth file: %w", err)
		}
		t.authPath = path
		t.authFile = authFile
	}

	if t.authFile.Tokens == nil || t.authFile.Tokens.AccessToken == "" {
		return nil, errors.New("access_token is missing in auth.json")
	}

	// Extract account ID if missing
	if t.authFile.Tokens.AccountID == "" {
		if _, accID, err := ParseJWTClaims(t.authFile.Tokens.AccessToken); err == nil && accID != "" {
			t.authFile.Tokens.AccountID = accID
		} else if _, accID, err := ParseJWTClaims(t.authFile.Tokens.IDToken); err == nil && accID != "" {
			t.authFile.Tokens.AccountID = accID
		}
	}

	// Check if refresh is needed
	if t.am.ShouldRefresh(t.authFile, time.Now()) {
		if t.authFile.Tokens.RefreshToken == "" {
			return nil, errors.New("refresh_token is missing in auth.json, cannot refresh expired session")
		}

		newTokens, err := t.am.RefreshTokens(ctx, t.authFile.Tokens.RefreshToken)
		if err != nil {
			return nil, fmt.Errorf("refresh ChatGPT OAuth session: %w", err)
		}

		// Update the cached tokens, preserving the account ID if not updated
		if newTokens.AccountID == "" {
			newTokens.AccountID = t.authFile.Tokens.AccountID
		}
		t.authFile.Tokens = newTokens
		t.authFile.LastRefresh = time.Now().UTC().Format(time.RFC3339)

		// Persist back to local auth.json
		if err := t.am.SaveAuthFile(t.authPath, t.authFile); err != nil {
			return nil, fmt.Errorf("save updated credentials: %w", err)
		}
	}

	if t.authFile.Tokens.AccountID == "" {
		return nil, errors.New("chatgpt-account-id could not be resolved from auth.json or JWT claims")
	}

	return t.authFile.Tokens, nil
}
