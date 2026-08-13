package mintcore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseStatusAuthModes(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", []string{"oidc"}},
		{"oidc", []string{"oidc"}},
		{"github", []string{"github"}},
		{"oidc,github", []string{"oidc", "github"}},
		{" oidc , github ", []string{"oidc", "github"}},
		{"OIDC,GITHUB", []string{"oidc", "github"}},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := ParseStatusAuthModes(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("ParseStatusAuthModes(%q) = %v, want %v", tc.input, got, tc.want)
			}
			for i, g := range got {
				if g != tc.want[i] {
					t.Fatalf("ParseStatusAuthModes(%q)[%d] = %q, want %q", tc.input, i, g, tc.want[i])
				}
			}
		})
	}
}

func TestValidateStatusAuthConfig(t *testing.T) {
	tests := []struct {
		name         string
		modes        []string
		group        string
		clientID     string
		clientSecret string
		wantErr      string
	}{
		{
			name:  "oidc only - valid",
			modes: []string{"oidc"},
		},
		{
			name:         "github mode - valid",
			modes:        []string{"github"},
			group:        "myorg/myteam",
			clientID:     "client-id",
			clientSecret: "client-secret",
		},
		{
			name:         "both modes - valid",
			modes:        []string{"oidc", "github"},
			group:        "myorg/myteam",
			clientID:     "client-id",
			clientSecret: "client-secret",
		},
		{
			name:    "github mode - missing group",
			modes:   []string{"github"},
			wantErr: "STATUS_GITHUB_GROUP is required",
		},
		{
			name:    "github mode - invalid group format",
			modes:   []string{"github"},
			group:   "myorg-only",
			wantErr: "STATUS_GITHUB_GROUP must be in ORG/TEAM format",
		},
		{
			name:    "github mode - missing client ID",
			modes:   []string{"github"},
			group:   "myorg/myteam",
			wantErr: "STATUS_GITHUB_CLIENT_ID is required",
		},
		{
			name:     "github mode - missing client secret",
			modes:    []string{"github"},
			group:    "myorg/myteam",
			clientID: "client-id",
			wantErr:  "STATUS_GITHUB_CLIENT_SECRET is required",
		},
		{
			name:    "unknown mode",
			modes:   []string{"unknown"},
			wantErr: "STATUS_AUTH contains unknown mode",
		},
		{
			name:  "access mode - accepted for forward compat",
			modes: []string{"access"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateStatusAuthConfig(tc.modes, tc.group, tc.clientID, tc.clientSecret)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
				}
			}
		})
	}
}

func TestParseGitHubGroup(t *testing.T) {
	org, team := parseGitHubGroup("myorg/myteam")
	if org != "myorg" || team != "myteam" {
		t.Fatalf("parseGitHubGroup('myorg/myteam') = (%q, %q), want ('myorg', 'myteam')", org, team)
	}

	org, team = parseGitHubGroup("org/nested/team")
	if org != "org" || team != "nested/team" {
		t.Fatalf("parseGitHubGroup('org/nested/team') = (%q, %q), want ('org', 'nested/team')", org, team)
	}
}

func TestGitHubUserFromToken(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "token valid-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(githubUserResponse{Login: "testuser"})
	}))
	defer github.Close()

	t.Run("valid token", func(t *testing.T) {
		login, err := GitHubUserFromToken(t.Context(), github.Client(), github.URL, "valid-token")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if login != "testuser" {
			t.Fatalf("expected login 'testuser', got %q", login)
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		_, err := GitHubUserFromToken(t.Context(), github.Client(), github.URL, "bad-token")
		if err == nil {
			t.Fatal("expected error for invalid token")
		}
		if !strings.Contains(err.Error(), "401") {
			t.Fatalf("expected 401 error, got: %v", err)
		}
	})
}

func TestCheckTeamMembership(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/orgs/myorg/teams/myteam/memberships/member-user":
			json.NewEncoder(w).Encode(teamMembershipResponse{State: "active"})
		case "/orgs/myorg/teams/myteam/memberships/pending-user":
			json.NewEncoder(w).Encode(teamMembershipResponse{State: "pending"})
		case "/orgs/myorg/teams/myteam/memberships/nonmember-user":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()

	t.Run("active member", func(t *testing.T) {
		ok, err := CheckTeamMembership(t.Context(), github.Client(), github.URL, "token", "myorg", "myteam", "member-user")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatal("expected member to be in team")
		}
	})

	t.Run("pending member", func(t *testing.T) {
		ok, err := CheckTeamMembership(t.Context(), github.Client(), github.URL, "token", "myorg", "myteam", "pending-user")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Fatal("pending member should not be considered active")
		}
	})

	t.Run("non-member", func(t *testing.T) {
		ok, err := CheckTeamMembership(t.Context(), github.Client(), github.URL, "token", "myorg", "myteam", "nonmember-user")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Fatal("non-member should not be in team")
		}
	})
}

func TestHandler_StatusEndpoint_GitHubAuth_HappyPath(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("STATUS_AUTH", "github")
	t.Setenv("STATUS_GITHUB_GROUP", "test-org/developers")
	t.Setenv("STATUS_GITHUB_CLIENT_ID", "test-client-id")
	t.Setenv("STATUS_GITHUB_CLIENT_SECRET", "test-client-secret")

	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			json.NewEncoder(w).Encode(githubUserResponse{Login: "testuser"})
		case "/orgs/test-org/teams/developers/memberships/testuser":
			json.NewEncoder(w).Encode(teamMembershipResponse{State: "active"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	h.githubBaseURL = github.URL

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer user-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp statusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode status response: %v", err)
	}
	if resp.Org != "test-org" {
		t.Fatalf("expected org 'test-org', got %q", resp.Org)
	}
	if len(resp.Roles) == 0 {
		t.Fatal("expected roles in response")
	}
}

func TestHandler_StatusEndpoint_GitHubAuth_NotMember(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("STATUS_AUTH", "github")
	t.Setenv("STATUS_GITHUB_GROUP", "test-org/developers")
	t.Setenv("STATUS_GITHUB_CLIENT_ID", "test-client-id")
	t.Setenv("STATUS_GITHUB_CLIENT_SECRET", "test-client-secret")

	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			json.NewEncoder(w).Encode(githubUserResponse{Login: "outsider"})
		case "/orgs/test-org/teams/developers/memberships/outsider":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	h.githubBaseURL = github.URL

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer user-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_StatusEndpoint_GitHubAuth_InvalidToken(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("STATUS_AUTH", "github")
	t.Setenv("STATUS_GITHUB_GROUP", "test-org/developers")
	t.Setenv("STATUS_GITHUB_CLIENT_ID", "test-client-id")
	t.Setenv("STATUS_GITHUB_CLIENT_SECRET", "test-client-secret")

	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer github.Close()
	h.githubBaseURL = github.URL

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer bad-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_StatusEndpoint_OIDCCoexistence(t *testing.T) {
	// When both oidc and github modes are enabled, a valid OIDC JWT
	// should still succeed (OIDC is tried first).
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("STATUS_AUTH", "oidc,github")
	t.Setenv("STATUS_GITHUB_GROUP", "test-org/developers")
	t.Setenv("STATUS_GITHUB_CLIENT_ID", "test-client-id")
	t.Setenv("STATUS_GITHUB_CLIENT_SECRET", "test-client-secret")

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	env.handler.statusAuthModes = []string{"oidc", "github"}
	env.handler.statusGithubGroup = "test-org/developers"
	env.handler.statusGithubClientID = "test-client-id"
	env.handler.statusGithubClientSecret = "test-client-secret"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+env.signToken(t, nil))
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp statusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode status response: %v", err)
	}
	if resp.Org != "test-org" {
		t.Fatalf("expected org 'test-org', got %q", resp.Org)
	}
}

func TestHandler_StatusEndpoint_OIDCFallbackToGitHub(t *testing.T) {
	// When both modes are enabled and OIDC fails, GitHub auth should be tried.
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("STATUS_AUTH", "oidc,github")
	t.Setenv("STATUS_GITHUB_GROUP", "test-org/developers")
	t.Setenv("STATUS_GITHUB_CLIENT_ID", "test-client-id")
	t.Setenv("STATUS_GITHUB_CLIENT_SECRET", "test-client-secret")

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	env.handler.statusAuthModes = []string{"oidc", "github"}
	env.handler.statusGithubGroup = "test-org/developers"
	env.handler.statusGithubClientID = "test-client-id"
	env.handler.statusGithubClientSecret = "test-client-secret"

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			json.NewEncoder(w).Encode(githubUserResponse{Login: "testuser"})
		case "/orgs/test-org/teams/developers/memberships/testuser":
			json.NewEncoder(w).Encode(teamMembershipResponse{State: "active"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	// Use a non-JWT token that will fail OIDC verification but succeed as user token.
	req.Header.Set("Authorization", "Bearer ghp_user_token_here")
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (github fallback), got %d: %s", rec.Code, rec.Body.String())
	}

	var resp statusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode status response: %v", err)
	}
	if resp.Org != "test-org" {
		t.Fatalf("expected org 'test-org', got %q", resp.Org)
	}
}

func TestHandler_StatusEndpoint_OIDCOnly_RejectsUserToken(t *testing.T) {
	// When STATUS_AUTH=oidc (default), a GitHub user token should be rejected.
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	// STATUS_AUTH defaults to oidc, so don't set it.

	env := newTestOIDCEnv(t, &fakePEMAccessor{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer ghp_user_token_here")
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for user token with oidc-only mode, got %d", rec.Code)
	}
}

func TestNewHandler_StatusAuthConfigValidation(t *testing.T) {
	// github mode without required config should fail at startup.
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("STATUS_AUTH", "github")
	// Missing STATUS_GITHUB_GROUP, STATUS_GITHUB_CLIENT_ID, STATUS_GITHUB_CLIENT_SECRET.
	t.Setenv("STATUS_GITHUB_GROUP", "")
	t.Setenv("STATUS_GITHUB_CLIENT_ID", "")
	t.Setenv("STATUS_GITHUB_CLIENT_SECRET", "")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")

	_, err := NewHandler(&fakePEMAccessor{}, &fakeOIDCVerifier{})
	if err == nil {
		t.Fatal("expected error for github mode without required config")
	}
	if !strings.Contains(err.Error(), "STATUS_GITHUB_GROUP is required") {
		t.Fatalf("expected STATUS_GITHUB_GROUP error, got: %v", err)
	}
}

func TestNewHandler_StatusAuthConfigValidation_NoGroup(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("STATUS_AUTH", "github")
	t.Setenv("STATUS_GITHUB_GROUP", "")
	t.Setenv("STATUS_GITHUB_CLIENT_ID", "id")
	t.Setenv("STATUS_GITHUB_CLIENT_SECRET", "secret")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")

	_, err := NewHandler(&fakePEMAccessor{}, &fakeOIDCVerifier{})
	if err == nil {
		t.Fatal("expected error for github mode without group")
	}
	if !strings.Contains(err.Error(), "STATUS_GITHUB_GROUP is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewHandler_StatusAuthConfigValidation_BadGroupFormat(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("STATUS_AUTH", "github")
	t.Setenv("STATUS_GITHUB_GROUP", "just-org")
	t.Setenv("STATUS_GITHUB_CLIENT_ID", "id")
	t.Setenv("STATUS_GITHUB_CLIENT_SECRET", "secret")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")

	_, err := NewHandler(&fakePEMAccessor{}, &fakeOIDCVerifier{})
	if err == nil {
		t.Fatal("expected error for invalid group format")
	}
	if !strings.Contains(err.Error(), "ORG/TEAM format") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewHandler_StatusAuthFutureCompatAccess(t *testing.T) {
	// STATUS_AUTH=oidc,access should parse without error.
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("STATUS_AUTH", "oidc,access")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")

	h, err := NewHandler(&fakePEMAccessor{}, &fakeOIDCVerifier{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h.statusAuthModes) != 2 {
		t.Fatalf("expected 2 status auth modes, got %d", len(h.statusAuthModes))
	}
}
