package internal_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/llm/openai/internal"
)

// Helper to create a fake JWT with specified exp and accountID
func createFakeJWT(t *testing.T, exp int64, accountID string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	
	claims := map[string]any{
		"exp": exp,
	}
	if accountID != "" {
		claims["https://api.openai.com/auth"] = map[string]any{
			"chatgpt_account_id": accountID,
		}
	}
	
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signature := base64.RawURLEncoding.EncodeToString([]byte("signature"))
	
	return fmt.Sprintf("%s.%s.%s", header, payload, signature)
}

func TestParseJWTClaims(t *testing.T) {
	expTime := time.Now().Add(1 * time.Hour).Unix()
	expectedID := "acc_123456789"
	token := createFakeJWT(t, expTime, expectedID)

	exp, accountID, err := internal.ParseJWTClaims(token)
	if err != nil {
		t.Fatalf("unexpected error parsing claims: %v", err)
	}

	if exp != expTime {
		t.Errorf("expected exp %d, got %d", expTime, exp)
	}
	if accountID != expectedID {
		t.Errorf("expected accountID %q, got %q", expectedID, accountID)
	}
}

func TestShouldRefresh(t *testing.T) {
	am := internal.NewAuthManager(nil)
	now := time.Now()

	// 1. Expired token (expires in the past)
	t.Run("ExpiredToken", func(t *testing.T) {
		token := createFakeJWT(t, now.Add(-10*time.Minute).Unix(), "acc_1")
		auth := &internal.AuthFile{
			Tokens: &internal.StoredTokens{
				AccessToken: token,
			},
		}
		if !am.ShouldRefresh(auth, now) {
			t.Error("expected token to need refresh")
		}
	})

	// 2. Token expiring within margin (expires in 2 minutes)
	t.Run("ExpiringWithinMargin", func(t *testing.T) {
		token := createFakeJWT(t, now.Add(2*time.Minute).Unix(), "acc_1")
		auth := &internal.AuthFile{
			Tokens: &internal.StoredTokens{
				AccessToken: token,
			},
		}
		if !am.ShouldRefresh(auth, now) {
			t.Error("expected token to need refresh within 5 minutes margin")
		}
	})

	// 3. Fully valid token (expires in 2 hours)
	t.Run("ValidToken", func(t *testing.T) {
		token := createFakeJWT(t, now.Add(2*time.Hour).Unix(), "acc_1")
		auth := &internal.AuthFile{
			Tokens: &internal.StoredTokens{
				AccessToken: token,
			},
		}
		if am.ShouldRefresh(auth, now) {
			t.Error("expected token to NOT need refresh")
		}
	})

	// 4. Fallback to last_refresh
	t.Run("FallbackLastRefresh", func(t *testing.T) {
		auth := &internal.AuthFile{
			Tokens: &internal.StoredTokens{
				AccessToken: "not-a-valid-jwt",
			},
			LastRefresh: now.Add(-1 * time.Hour).Format(time.RFC3339),
		}
		if !am.ShouldRefresh(auth, now) {
			t.Error("expected token to need refresh based on last_refresh > 55m")
		}
	})
}

func TestResolveAuthFileCandidates(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("CHATGPT_LOCAL_HOME", filepath.Join(tempHome, "custom"))
	t.Setenv("CODEX_HOME", filepath.Join(tempHome, "codex_custom"))

	am := internal.NewAuthManager(nil)
	candidates := am.ResolveAuthFileCandidates()

	expectedCustom := filepath.Join(tempHome, "custom", "auth.json")
	expectedCodex := filepath.Join(tempHome, "codex_custom", "auth.json")

	hasCustom := false
	hasCodex := false
	for _, c := range candidates {
		if c == expectedCustom {
			hasCustom = true
		}
		if c == expectedCodex {
			hasCodex = true
		}
	}

	if !hasCustom {
		t.Errorf("expected candidates to contain %q", expectedCustom)
	}
	if !hasCodex {
		t.Errorf("expected candidates to contain %q", expectedCodex)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestOAuthTransport_RoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")

	// Setenv so our discovery checks the custom home first
	t.Setenv("CHATGPT_LOCAL_HOME", tempDir)

	now := time.Now()
	expiredToken := createFakeJWT(t, now.Add(-10*time.Minute).Unix(), "acc_old")
	newAccessToken := createFakeJWT(t, now.Add(1*time.Hour).Unix(), "acc_new")

	// Create initial auth.json on disk
	initialAuth := &internal.AuthFile{
		Tokens: &internal.StoredTokens{
			AccessToken:  expiredToken,
			RefreshToken: "refresh_token_123",
			AccountID:    "acc_old",
		},
		LastRefresh: now.Add(-1 * time.Hour).Format(time.RFC3339),
	}
	initialBytes, _ := json.Marshal(initialAuth)
	if err := os.WriteFile(authPath, initialBytes, 0600); err != nil {
		t.Fatalf("failed to write initial auth: %v", err)
	}

	// Mock the external OAuth token and ChatGPT Responses endpoints
	mux := http.NewServeMux()
	
	// Server mock for https://auth.openai.com/oauth/token (redirected dynamically)
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]string
		_ = json.Unmarshal(body, &req)

		if req["grant_type"] != "refresh_token" || req["refresh_token"] != "refresh_token_123" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"access_token": %q,
			"id_token": %q,
			"refresh_token": "refresh_token_456"
		}`, newAccessToken, createFakeJWT(t, now.Add(1*time.Hour).Unix(), "acc_new"))))
	})

	// Server mock for upstream chatgpt.com responses backend
	mux.HandleFunc("POST /backend-api/codex/responses", func(w http.ResponseWriter, r *http.Request) {
		// Verify headers injected by transport
		if r.Host != "chatgpt.com" {
			t.Errorf("unexpected Host header over the wire: %s", r.Host)
		}
		if r.Header.Get("Authorization") != "Bearer "+newAccessToken {
			t.Errorf("unexpected Authorization header: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("chatgpt-account-id") != "acc_new" {
			t.Errorf("unexpected chatgpt-account-id header: %s", r.Header.Get("chatgpt-account-id"))
		}
		if r.Header.Get("OpenAI-Beta") != "responses=experimental" {
			t.Errorf("unexpected OpenAI-Beta header: %s", r.Header.Get("OpenAI-Beta"))
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"success"}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	mockURL, _ := url.Parse(server.URL)

	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			// Redirect any call to auth.openai.com or chatgpt.com to our mock test server!
			if req.URL.Host == "auth.openai.com" {
				req.URL.Scheme = mockURL.Scheme
				req.URL.Host = mockURL.Host
				req.URL.Path = "/oauth/token"
			} else if req.URL.Host == "chatgpt.com" {
				req.URL.Scheme = mockURL.Scheme
				req.URL.Host = mockURL.Host
			}
			return http.DefaultTransport.RoundTrip(req)
		}),
	}

	// Create our OAuthTransport using our redirect client
	transport := internal.NewOAuthTransport(client)
	
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.openai.com/v1/responses", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected RoundTrip error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status OK, got %d", resp.StatusCode)
	}

	// Verify auth.json on disk was updated with the new credentials
	updatedBytes, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("failed to read updated auth file: %v", err)
	}

	var updatedAuth internal.AuthFile
	if err := json.Unmarshal(updatedBytes, &updatedAuth); err != nil {
		t.Fatalf("failed to unmarshal updated auth: %v", err)
	}

	if updatedAuth.Tokens.AccessToken != newAccessToken {
		t.Errorf("expected access token to be updated, got %s", updatedAuth.Tokens.AccessToken)
	}
	if updatedAuth.Tokens.RefreshToken != "refresh_token_456" {
		t.Errorf("expected refresh token to be updated, got %s", updatedAuth.Tokens.RefreshToken)
	}
}
