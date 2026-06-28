package mintclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func init() {
	retryBaseDelay = 0
}

func TestMintToken_HappyPath(t *testing.T) {
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "bearer test-request-token" {
			t.Errorf("OIDC auth header = %q, want %q", got, "bearer test-request-token")
		}
		if got := r.URL.Query().Get("audience"); got != "fullsend-mint" {
			t.Errorf("audience = %q, want %q", got, "fullsend-mint")
		}
		json.NewEncoder(w).Encode(oidcTokenResponse{Value: "oidc-jwt-value"})
	}))
	defer oidcServer.Close()

	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/token" {
			t.Errorf("path = %q, want /v1/token", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer oidc-jwt-value" {
			t.Errorf("mint auth header = %q, want %q", got, "Bearer oidc-jwt-value")
		}

		var body mintRequestBody
		json.NewDecoder(r.Body).Decode(&body)
		if body.Role != "triage" {
			t.Errorf("role = %q, want %q", body.Role, "triage")
		}
		if len(body.Repos) != 1 || body.Repos[0] != "my-repo" {
			t.Errorf("repos = %v, want [my-repo]", body.Repos)
		}

		json.NewEncoder(w).Encode(MintResult{
			Token:     "ghu_test_token",
			ExpiresAt: "2026-06-11T23:30:00Z",
		})
	}))
	defer mintServer.Close()

	origEnv := envLookup
	envLookup = func(key string) string {
		switch key {
		case "ACTIONS_ID_TOKEN_REQUEST_URL":
			return oidcServer.URL + "?dummy=1"
		case "ACTIONS_ID_TOKEN_REQUEST_TOKEN":
			return "test-request-token"
		default:
			return ""
		}
	}
	defer func() { envLookup = origEnv }()

	result, err := MintToken(context.Background(), MintRequest{
		MintURL: mintServer.URL,
		Role:    "triage",
		Repos:   []string{"my-repo"},
	})
	if err != nil {
		t.Fatalf("MintToken() error = %v", err)
	}
	if result.Token != "ghu_test_token" {
		t.Errorf("token = %q, want %q", result.Token, "ghu_test_token")
	}
	if result.ExpiresAt != "2026-06-11T23:30:00Z" {
		t.Errorf("expires_at = %q, want %q", result.ExpiresAt, "2026-06-11T23:30:00Z")
	}
}

func TestMintToken_CustomAudience(t *testing.T) {
	var gotAudience string
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAudience = r.URL.Query().Get("audience")
		json.NewEncoder(w).Encode(oidcTokenResponse{Value: "jwt"})
	}))
	defer oidcServer.Close()

	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(MintResult{Token: "tok", ExpiresAt: "2026-01-01T00:00:00Z"})
	}))
	defer mintServer.Close()

	origEnv := envLookup
	envLookup = func(key string) string {
		switch key {
		case "ACTIONS_ID_TOKEN_REQUEST_URL":
			return oidcServer.URL + "?dummy=1"
		case "ACTIONS_ID_TOKEN_REQUEST_TOKEN":
			return "tok"
		default:
			return ""
		}
	}
	defer func() { envLookup = origEnv }()

	_, err := MintToken(context.Background(), MintRequest{
		MintURL:  mintServer.URL,
		Role:     "triage",
		Repos:    []string{"repo"},
		Audience: "custom-aud",
	})
	if err != nil {
		t.Fatalf("MintToken() error = %v", err)
	}
	if gotAudience != "custom-aud" {
		t.Errorf("audience = %q, want %q", gotAudience, "custom-aud")
	}
}

func TestMintToken_ValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		req  MintRequest
		want string
	}{
		{"empty mint URL", MintRequest{Role: "triage", Repos: []string{"r"}}, "mint URL is required"},
		{"non-HTTPS mint URL", MintRequest{MintURL: "http://example.com", Role: "triage", Repos: []string{"r"}}, "mint URL must use HTTPS"},
		{"empty role", MintRequest{MintURL: "https://mint.example.com", Repos: []string{"r"}}, "role is required"},
		{"no repos", MintRequest{MintURL: "https://mint.example.com", Role: "triage"}, "at least one repo is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := MintToken(context.Background(), tt.req)
			if err == nil {
				t.Fatal("expected error")
			}
			if got := err.Error(); got != tt.want {
				t.Errorf("error = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMintToken_MissingOIDCEnvVars(t *testing.T) {
	origEnv := envLookup
	defer func() { envLookup = origEnv }()

	t.Run("missing request URL", func(t *testing.T) {
		envLookup = func(key string) string { return "" }
		_, err := MintToken(context.Background(), MintRequest{
			MintURL: "https://mint.example.com",
			Role:    "triage",
			Repos:   []string{"r"},
		})
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "ACTIONS_ID_TOKEN_REQUEST_URL not set") {
			t.Errorf("error = %q, want to contain ACTIONS_ID_TOKEN_REQUEST_URL", err.Error())
		}
	})

	t.Run("missing request token", func(t *testing.T) {
		envLookup = func(key string) string {
			if key == "ACTIONS_ID_TOKEN_REQUEST_URL" {
				return "http://oidc"
			}
			return ""
		}
		_, err := MintToken(context.Background(), MintRequest{
			MintURL: "https://mint.example.com",
			Role:    "triage",
			Repos:   []string{"r"},
		})
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "ACTIONS_ID_TOKEN_REQUEST_TOKEN not set") {
			t.Errorf("error = %q, want to contain ACTIONS_ID_TOKEN_REQUEST_TOKEN", err.Error())
		}
	})
}

func TestMintToken_OIDCEndpointFailure(t *testing.T) {
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer oidcServer.Close()

	origEnv := envLookup
	envLookup = func(key string) string {
		switch key {
		case "ACTIONS_ID_TOKEN_REQUEST_URL":
			return oidcServer.URL + "?d=1"
		case "ACTIONS_ID_TOKEN_REQUEST_TOKEN":
			return "tok"
		default:
			return ""
		}
	}
	defer func() { envLookup = origEnv }()

	_, err := MintToken(context.Background(), MintRequest{
		MintURL: "https://mint.example.com",
		Role:    "triage",
		Repos:   []string{"r"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error = %q, want to contain HTTP 500", err.Error())
	}
}

func TestMintToken_OIDCEmptyToken(t *testing.T) {
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(oidcTokenResponse{Value: ""})
	}))
	defer oidcServer.Close()

	origEnv := envLookup
	envLookup = func(key string) string {
		switch key {
		case "ACTIONS_ID_TOKEN_REQUEST_URL":
			return oidcServer.URL + "?d=1"
		case "ACTIONS_ID_TOKEN_REQUEST_TOKEN":
			return "tok"
		default:
			return ""
		}
	}
	defer func() { envLookup = origEnv }()

	_, err := MintToken(context.Background(), MintRequest{
		MintURL: "https://mint.example.com",
		Role:    "triage",
		Repos:   []string{"r"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "empty token") {
		t.Errorf("error = %q, want to contain 'empty token'", err.Error())
	}
}

func TestMintToken_OIDCNetworkError(t *testing.T) {
	origEnv := envLookup
	envLookup = func(key string) string {
		switch key {
		case "ACTIONS_ID_TOKEN_REQUEST_URL":
			return "http://127.0.0.1:1?d=1"
		case "ACTIONS_ID_TOKEN_REQUEST_TOKEN":
			return "tok"
		default:
			return ""
		}
	}
	defer func() { envLookup = origEnv }()

	_, err := MintToken(context.Background(), MintRequest{
		MintURL: "https://mint.example.com",
		Role:    "triage",
		Repos:   []string{"r"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "requesting OIDC token") {
		t.Errorf("error = %q, want to contain 'requesting OIDC token'", err.Error())
	}
}

func TestMintToken_OIDCInvalidJSON(t *testing.T) {
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer oidcServer.Close()

	origEnv := envLookup
	envLookup = func(key string) string {
		switch key {
		case "ACTIONS_ID_TOKEN_REQUEST_URL":
			return oidcServer.URL + "?d=1"
		case "ACTIONS_ID_TOKEN_REQUEST_TOKEN":
			return "tok"
		default:
			return ""
		}
	}
	defer func() { envLookup = origEnv }()

	_, err := MintToken(context.Background(), MintRequest{
		MintURL: "https://mint.example.com",
		Role:    "triage",
		Repos:   []string{"r"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "parsing response") {
		t.Errorf("error = %q, want to contain 'parsing response'", err.Error())
	}
}

func TestMintToken_MintNetworkError(t *testing.T) {
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(oidcTokenResponse{Value: "jwt"})
	}))
	defer oidcServer.Close()

	origEnv := envLookup
	envLookup = func(key string) string {
		switch key {
		case "ACTIONS_ID_TOKEN_REQUEST_URL":
			return oidcServer.URL + "?d=1"
		case "ACTIONS_ID_TOKEN_REQUEST_TOKEN":
			return "tok"
		default:
			return ""
		}
	}
	defer func() { envLookup = origEnv }()

	_, err := MintToken(context.Background(), MintRequest{
		MintURL: "http://127.0.0.1:1",
		Role:    "triage",
		Repos:   []string{"r"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "requesting token") {
		t.Errorf("error = %q, want to contain 'requesting token'", err.Error())
	}
}

func TestMintToken_MintServiceFailure(t *testing.T) {
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(oidcTokenResponse{Value: "jwt"})
	}))
	defer oidcServer.Close()

	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantSubstr string
	}{
		{
			"401 unauthorized",
			func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
			"HTTP 401",
		},
		{
			"500 internal error",
			func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			"HTTP 500",
		},
		{
			"empty token in response",
			func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(MintResult{Token: ""})
			},
			"empty token",
		},
		{
			"invalid JSON",
			func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("not json"))
			},
			"parsing response",
		},
	}

	origEnv := envLookup
	envLookup = func(key string) string {
		switch key {
		case "ACTIONS_ID_TOKEN_REQUEST_URL":
			return oidcServer.URL + "?d=1"
		case "ACTIONS_ID_TOKEN_REQUEST_TOKEN":
			return "tok"
		default:
			return ""
		}
	}
	defer func() { envLookup = origEnv }()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mintServer := httptest.NewServer(tt.handler)
			defer mintServer.Close()

			_, err := MintToken(context.Background(), MintRequest{
				MintURL: mintServer.URL,
				Role:    "triage",
				Repos:   []string{"r"},
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantSubstr)
			}
		})
	}
}

func TestMintToken_RetriesOn500(t *testing.T) {
	var attempts int
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(oidcTokenResponse{Value: "jwt"})
	}))
	defer oidcServer.Close()

	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(MintResult{Token: "tok", ExpiresAt: "2026-01-01T00:00:00Z"})
	}))
	defer mintServer.Close()

	origEnv := envLookup
	envLookup = func(key string) string {
		switch key {
		case "ACTIONS_ID_TOKEN_REQUEST_URL":
			return oidcServer.URL + "?d=1"
		case "ACTIONS_ID_TOKEN_REQUEST_TOKEN":
			return "tok"
		default:
			return ""
		}
	}
	defer func() { envLookup = origEnv }()

	result, err := MintToken(context.Background(), MintRequest{
		MintURL: mintServer.URL,
		Role:    "triage",
		Repos:   []string{"r"},
	})
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if result.Token != "tok" {
		t.Errorf("token = %q, want %q", result.Token, "tok")
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestMintToken_RetryRespectsContextCancel(t *testing.T) {
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(oidcTokenResponse{Value: "jwt"})
	}))
	defer oidcServer.Close()

	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mintServer.Close()

	origEnv := envLookup
	envLookup = func(key string) string {
		switch key {
		case "ACTIONS_ID_TOKEN_REQUEST_URL":
			return oidcServer.URL + "?d=1"
		case "ACTIONS_ID_TOKEN_REQUEST_TOKEN":
			return "tok"
		default:
			return ""
		}
	}
	defer func() { envLookup = origEnv }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := MintToken(ctx, MintRequest{
		MintURL: mintServer.URL,
		Role:    "triage",
		Repos:   []string{"r"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMintToken_DoesNotRetryOn4xx(t *testing.T) {
	var attempts int
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(oidcTokenResponse{Value: "jwt"})
	}))
	defer oidcServer.Close()

	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer mintServer.Close()

	origEnv := envLookup
	envLookup = func(key string) string {
		switch key {
		case "ACTIONS_ID_TOKEN_REQUEST_URL":
			return oidcServer.URL + "?d=1"
		case "ACTIONS_ID_TOKEN_REQUEST_TOKEN":
			return "tok"
		default:
			return ""
		}
	}
	defer func() { envLookup = origEnv }()

	_, err := MintToken(context.Background(), MintRequest{
		MintURL: mintServer.URL,
		Role:    "triage",
		Repos:   []string{"r"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Errorf("4xx should not retry: attempts = %d, want 1", attempts)
	}
}

func TestMintToken_LocalhostHTTPAllowed(t *testing.T) {
	origEnv := envLookup
	envLookup = func(key string) string { return "" }
	defer func() { envLookup = origEnv }()

	_, err := MintToken(context.Background(), MintRequest{
		MintURL: "http://localhost:8080",
		Role:    "triage",
		Repos:   []string{"r"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("localhost HTTP should be allowed, got: %v", err)
	}
}
