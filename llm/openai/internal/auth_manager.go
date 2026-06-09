package internal

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	DefaultIssuer   = "https://auth.openai.com"
	AuthFilename    = "auth.json"
	
	RefreshExpiryMargin = 5 * time.Minute
	RefreshInterval     = 55 * time.Minute
)

type StoredTokens struct {
	IDToken      string `json:"id_token,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
}

type AuthFile struct {
	Tokens      *StoredTokens `json:"tokens,omitempty"`
	LastRefresh string        `json:"last_refresh,omitempty"`
}

type AuthManager struct {
	Client *http.Client
}

func NewAuthManager(client *http.Client) *AuthManager {
	if client == nil {
		client = http.DefaultClient
	}
	return &AuthManager{Client: client}
}

// ResolveAuthFileCandidates returns the list of possible locations for auth.json.
func (am *AuthManager) ResolveAuthFileCandidates() []string {
	var candidates []string

	if envHome := os.Getenv("CHATGPT_LOCAL_HOME"); envHome != "" {
		candidates = append(candidates, filepath.Join(envHome, AuthFilename))
	}
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		candidates = append(candidates, filepath.Join(codexHome, AuthFilename))
	}

	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".chatgpt-local", AuthFilename))
		candidates = append(candidates, filepath.Join(home, ".codex", AuthFilename))
	}

	return candidates
}

// FindAndLoadAuthFile searches candidates for a valid auth.json file.
func (am *AuthManager) FindAndLoadAuthFile() (string, *AuthFile, error) {
	candidates := am.ResolveAuthFileCandidates()
	if len(candidates) == 0 {
		return "", nil, errors.New("no auth.json search locations could be resolved")
	}

	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var authFile AuthFile
		if err := json.Unmarshal(data, &authFile); err != nil {
			continue
		}

		if authFile.Tokens == nil || authFile.Tokens.AccessToken == "" {
			continue
		}

		return path, &authFile, nil
	}

	return "", nil, fmt.Errorf("no valid auth.json found in candidate paths: %s", strings.Join(candidates, ", "))
}

// DecodeBase64URL decodes a base64url string with or without padding.
func DecodeBase64URL(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// ParseJWTClaims extracts expiration time and account ID from a JWT token.
func ParseJWTClaims(token string) (int64, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, "", errors.New("invalid jwt format")
	}

	payloadBytes, err := DecodeBase64URL(parts[1])
	if err != nil {
		return 0, "", fmt.Errorf("decode jwt payload: %w", err)
	}

	var claims struct {
		Exp       int64          `json:"exp"`
		AuthClaim map[string]any `json:"https://api.openai.com/auth"`
	}

	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return 0, "", fmt.Errorf("unmarshal jwt claims: %w", err)
	}

	var accountID string
	if claims.AuthClaim != nil {
		if id, ok := claims.AuthClaim["chatgpt_account_id"].(string); ok {
			accountID = id
		}
	}

	return claims.Exp, accountID, nil
}

// ShouldRefresh checks if the cached access token needs a refresh.
func (am *AuthManager) ShouldRefresh(authFile *AuthFile, now time.Time) bool {
	if authFile.Tokens == nil || authFile.Tokens.AccessToken == "" {
		return true
	}

	// Try parsing JWT expiration claim first
	exp, _, err := ParseJWTClaims(authFile.Tokens.AccessToken)
	if err == nil && exp > 0 {
		expiryTime := time.Unix(exp, 0)
		return now.Add(RefreshExpiryMargin).After(expiryTime)
	}

	// Fallback to last_refresh timestamp if JWT claim parsing is not available
	if authFile.LastRefresh != "" {
		if refreshedAt, err := time.Parse(time.RFC3339, authFile.LastRefresh); err == nil {
			return now.Sub(refreshedAt) >= RefreshInterval
		}
	}

	return true
}

type oauthRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
}

// RefreshTokens calls OpenAI's OAuth token endpoint to get new credentials.
func (am *AuthManager) RefreshTokens(ctx context.Context, refreshToken string) (*StoredTokens, error) {
	tokenURL := fmt.Sprintf("%s/oauth/token", DefaultIssuer)
	
	payload := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     DefaultClientID,
		"scope":         "openid profile email offline_access",
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal refresh token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create refresh token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := am.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send refresh token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("refresh token failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var oauthResp oauthRefreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&oauthResp); err != nil {
		return nil, fmt.Errorf("decode refresh token response: %w", err)
	}

	if oauthResp.AccessToken == "" {
		return nil, errors.New("refresh token response did not contain access_token")
	}

	// Derive account ID from the new ID token if possible, fallback to old or JWT parsing
	_, derivedAccountID, _ := ParseJWTClaims(oauthResp.IDToken)

	newTokens := &StoredTokens{
		AccessToken:  oauthResp.AccessToken,
		IDToken:      oauthResp.IDToken,
		RefreshToken: cmp.Or(oauthResp.RefreshToken, refreshToken),
		AccountID:    derivedAccountID,
	}

	return newTokens, nil
}

// SaveAuthFile writes the updated AuthFile safely to the given path with 0600 permissions.
func (am *AuthManager) SaveAuthFile(path string, authFile *AuthFile) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create directory %q: %w", dir, err)
	}

	data, err := json.MarshalIndent(authFile, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth file: %w", err)
	}
	data = append(data, '\n')

	// Write file with secure 0600 permissions
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write auth file %q: %w", path, err)
	}

	return nil
}
