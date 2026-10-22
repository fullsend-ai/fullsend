package mintcore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatusAuth_OIDCSuccess_ReturnsOrgScoped(t *testing.T) {
	// The default signed token's repository (test-org/test-repo) is
	// enrolled via PER_REPO_WIF_REPOS (TestMain default).
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200"}`)

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+env.signToken(t, nil))
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp statusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Org != "test-org" {
		t.Fatalf("OIDC auth should set org=%q, got %q", "test-org", resp.Org)
	}
	if len(resp.Roles) == 0 {
		t.Fatal("expected roles in response")
	}
}

func TestStatusAuth_OIDCFail_NoValidators_Returns401(t *testing.T) {
	// Without the github build tag, validateStatusGitHub returns
	// errStatusAuthSkip. An invalid OIDC token should yield 401.
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer not-a-valid-jwt")
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStatusAuth_OIDCNotEnrolled_Returns401(t *testing.T) {
	// A validly signed OIDC token from a repository that is not in
	// PER_REPO_WIF_REPOS must be rejected on /v1/status, including the
	// org's .fullsend repo (formerly authorized via per-org enrollment).
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/test-repo")

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	for _, repo := range []string{"test-org/other-repo", "test-org/.fullsend"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
		req.Header.Set("Authorization", "Bearer "+env.signToken(t, map[string]interface{}{
			"repository": repo,
		}))
		env.handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("repository %s: expected 401, got %d: %s", repo, rec.Code, rec.Body.String())
		}
	}
}

func TestStatusAuth_MissingAuthHeader_Returns401(t *testing.T) {
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestStatusAuth_OIDCSuccess_VersionCommit(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	Version = "1.0.0"
	Commit = "abc123"
	t.Cleanup(func() { Version = ""; Commit = "" })

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+env.signToken(t, nil))
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp statusResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Version != "1.0.0" {
		t.Fatalf("expected version 1.0.0, got %q", resp.Version)
	}
	if resp.Commit != "abc123" {
		t.Fatalf("expected commit abc123, got %q", resp.Commit)
	}
}

func TestStatusAuth_ErrStatusAuthSkip_IsSentinel(t *testing.T) {
	// Verify that errStatusAuthSkip is a distinct error that stubs return.
	if errStatusAuthSkip == nil {
		t.Fatal("errStatusAuthSkip must not be nil")
	}
	if errStatusAuthSkip.Error() == "" {
		t.Fatal("errStatusAuthSkip must have a non-empty message")
	}
}
