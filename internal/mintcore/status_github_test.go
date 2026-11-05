//go:build github

package mintcore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusGitHub_ValidToken_TeamMember(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	// Set up a fake GitHub API server.
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authVal := r.Header.Get("Authorization")
		if authVal != "Bearer test-user-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/user" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]string{"login": "testuser"})
		case r.URL.Path == "/orgs/acme/teams/admins/memberships/testuser" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]string{"state": "active"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()

	// Override mintHTTP to route to fake GitHub.
	SetMintHTTPForTest(t, func(req *http.Request) (*http.Response, error) {
		// Rewrite api.github.com URLs to point at the test server.
		if strings.Contains(req.URL.Host, "api.github.com") {
			req.URL.Scheme = "http"
			req.URL.Host = strings.TrimPrefix(github.URL, "http://")
		}
		return http.DefaultClient.Do(req)
	})

	// Configure status consts.
	StatusGitHubGroup = "acme/admins"
	t.Cleanup(func() {
		StatusGitHubGroup = ""
	})

	// Create handler with OIDC that will fail (no valid JWKS).
	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer test-user-token")
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp statusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Non-OIDC auth is not scoped to a single org.
	if resp.Org != "" {
		t.Fatalf("non-OIDC auth should not set org, got %q", resp.Org)
	}
	if len(resp.Roles) == 0 {
		t.Fatal("expected roles in response")
	}
}

func TestStatusGitHub_InvalidToken_Returns401(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer github.Close()

	SetMintHTTPForTest(t, func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Host, "api.github.com") {
			req.URL.Scheme = "http"
			req.URL.Host = strings.TrimPrefix(github.URL, "http://")
		}
		return http.DefaultClient.Do(req)
	})

	StatusGitHubGroup = "acme/admins"
	t.Cleanup(func() {
		StatusGitHubGroup = ""
	})

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStatusGitHub_NotInTeam_Returns401(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authVal := r.Header.Get("Authorization")
		if authVal != "Bearer user-token-no-team" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/user":
			json.NewEncoder(w).Encode(map[string]string{"login": "outsider"})
		case strings.Contains(r.URL.Path, "/memberships/outsider"):
			// User is not a member.
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()

	SetMintHTTPForTest(t, func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Host, "api.github.com") {
			req.URL.Scheme = "http"
			req.URL.Host = strings.TrimPrefix(github.URL, "http://")
		}
		return http.DefaultClient.Do(req)
	})

	StatusGitHubGroup = "acme/admins"
	t.Cleanup(func() {
		StatusGitHubGroup = ""
	})

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer user-token-no-team")
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStatusGitHub_OIDCSuccess_BypassesValidator(t *testing.T) {
	// When OIDC succeeds, the GitHub validator should NOT be called.
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	StatusGitHubGroup = "acme/admins"
	t.Cleanup(func() {
		StatusGitHubGroup = ""
	})

	// Set up mintHTTP to fail loudly if GitHub API is called.
	SetMintHTTPForTest(t, func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Host, "api.github.com") {
			t.Error("GitHub API should not be called when OIDC succeeds")
		}
		return http.DefaultClient.Do(req)
	})

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+env.signToken(t, nil))
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp statusResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Org != "test-org" {
		t.Fatalf("expected OIDC org-scoped response, got org=%q", resp.Org)
	}
}

func TestStatusGitHub_NotConfigured_Returns401(t *testing.T) {
	// When StatusGitHubGroup is empty, the validator returns
	// errStatusAuthSkip. Without OIDC, this should yield 401.
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	StatusGitHubGroup = ""

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer not-a-valid-oidc-jwt")
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStatusGitHub_ValidateStatusGitHub_DirectCall(t *testing.T) {
	// Test the validator function directly to ensure
	// correct behavior for edge cases.
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user":
			json.NewEncoder(w).Encode(map[string]string{"login": "alice"})
		case r.URL.Path == "/orgs/org1/teams/devs/memberships/alice":
			json.NewEncoder(w).Encode(map[string]string{"state": "active"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()

	SetMintHTTPForTest(t, func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Host, "api.github.com") {
			req.URL.Scheme = "http"
			req.URL.Host = strings.TrimPrefix(github.URL, "http://")
		}
		return http.DefaultClient.Do(req)
	})

	StatusGitHubGroup = "org1/devs"
	t.Cleanup(func() {
		StatusGitHubGroup = ""
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer test-token")

	err := validateStatusGitHub(req.Context(), req)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestStatusGitHub_PendingMembership_Returns401(t *testing.T) {
	// GitHub returns state="pending" for invited but not-yet-accepted members.
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user":
			json.NewEncoder(w).Encode(map[string]string{"login": "pending-user"})
		case strings.Contains(r.URL.Path, "/memberships/"):
			json.NewEncoder(w).Encode(map[string]string{"state": "pending"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()

	SetMintHTTPForTest(t, func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Host, "api.github.com") {
			req.URL.Scheme = "http"
			req.URL.Host = strings.TrimPrefix(github.URL, "http://")
		}
		return http.DefaultClient.Do(req)
	})

	StatusGitHubGroup = "acme/team"
	t.Cleanup(func() {
		StatusGitHubGroup = ""
	})

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer pending-token")
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for pending membership, got %d: %s", rec.Code, rec.Body.String())
	}
}
