package mintcore

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewHandler_ReadsEnv(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200"}`)
	t.Setenv("ALLOWED_ROLES", "")
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	h, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	if !h.checkAllowedRole("triage") {
		t.Fatal("triage should be allowed")
	}
	if !h.checkAllowedRole("coder") {
		t.Fatal("coder should be allowed")
	}
}

func TestNewHandler_ExplicitAllowedRoles(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200","review":"300"}`)
	t.Setenv("ALLOWED_ROLES", "triage,coder")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	h, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if !h.checkAllowedRole("triage") {
		t.Fatal("triage should be allowed")
	}
	if !h.checkAllowedRole("coder") {
		t.Fatal("coder should be allowed")
	}
	if h.checkAllowedRole("review") {
		t.Fatal("review should not be allowed when not in ALLOWED_ROLES")
	}
}

func TestNewHandler_MissingRoleAppIDs(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", "")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	_, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err != nil {
		t.Fatalf("expected no error for empty ROLE_APP_IDS, got: %v", err)
	}
}

func TestNewHandler_InvalidRoleAppIDsJSON(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", "not-json")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	_, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse ROLE_APP_IDS") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewHandler_InvalidAllowedRoleFormat(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ROLES", "INVALID")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	_, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err == nil {
		t.Fatal("expected error for invalid role format")
	}
	if !strings.Contains(err.Error(), "ALLOWED_ROLES contains invalid entry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewHandler_AllowedRoleNotInPermissions(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"nonexistent":"100"}`)
	t.Setenv("ALLOWED_ROLES", "nonexistent")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	_, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err == nil {
		t.Fatal("expected error for role not in RolePermissions")
	}
	if !strings.Contains(err.Error(), "RolePermissions has no entry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewHandler_AllowedRoleNotInAppIDs(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ROLES", "triage")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	_, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err == nil {
		t.Fatal("expected error for role not in ROLE_APP_IDS")
	}
	if !strings.Contains(err.Error(), "ROLE_APP_IDS has no entry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewHandler_InjectsHTTPClient(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	client := &http.Client{}
	h, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, client)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if h.httpClient != client {
		t.Fatal("expected injected HTTP client")
	}
}

func TestNewHandler_PerRepoWIFRepos(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("PER_REPO_WIF_REPOS", "org/repo-a, Org/Repo-B")
	t.Setenv("ALLOWED_ORGS", "org")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	h, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	// Verify perRepoWIFRepos was parsed and lowercased.
	if !h.perRepoWIFRepos["org/repo-a"] {
		t.Fatal("expected org/repo-a in perRepoWIFRepos")
	}
	if !h.perRepoWIFRepos["org/repo-b"] {
		t.Fatal("expected org/repo-b (lowercased) in perRepoWIFRepos")
	}
	if len(h.perRepoWIFRepos) != 2 {
		t.Fatalf("expected 2 entries in perRepoWIFRepos, got %d", len(h.perRepoWIFRepos))
	}

	// Verify allowedOrgs was set on the handler.
	if len(h.allowedOrgs) != 1 || h.allowedOrgs[0] != "org" {
		t.Fatalf("expected allowedOrgs=[org], got %v", h.allowedOrgs)
	}
}

func TestNewHandler_WorkflowHostRepos(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "org")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("WORKFLOW_HOST_REPOS", "acme/workflows, Acme/Other")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	h, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if !h.workflowHostRepos["acme/workflows"] {
		t.Fatal("expected acme/workflows in workflowHostRepos")
	}
	if !h.workflowHostRepos["acme/other"] {
		t.Fatal("expected acme/other (lowercased) in workflowHostRepos")
	}
	if len(h.workflowHostRepos) != 2 {
		t.Fatalf("expected 2 entries in workflowHostRepos, got %d", len(h.workflowHostRepos))
	}
}

func TestNewHandler_WorkflowHostReposDefault(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "org")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("WORKFLOW_HOST_REPOS", "")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	h, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	// Empty WORKFLOW_HOST_REPOS should default to fullsend-ai/fullsend.
	if !h.workflowHostRepos["fullsend-ai/fullsend"] {
		t.Fatal("expected fullsend-ai/fullsend as default in workflowHostRepos")
	}
	if len(h.workflowHostRepos) != 1 {
		t.Fatalf("expected 1 entry in workflowHostRepos, got %d", len(h.workflowHostRepos))
	}
}

func TestNewHandler_ServeHTTPWorks(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	h, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health: expected 200, got %d", rec.Code)
	}
}

func TestNewHandler_FullMintFlow(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	verifier := &fakeOIDCVerifier{
		claims: &Claims{
			Issuer:          "https://token.actions.githubusercontent.com",
			Repository:      "test-org/.fullsend",
			RepositoryOwner: "test-org",
			JobWorkflowRef:  "test-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
		},
	}

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	pemAccessor := &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	}

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/installation"):
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"id":12345,"account":{"login":"test-org"}}`)
		case strings.HasPrefix(r.URL.Path, "/app/installations/12345/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"ghs_getenv_test","expires_at":"2026-01-01T00:00:00Z"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()

	h, err := NewHandler(verifier, pemAccessor, github.Client())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	h.githubBaseURL = github.URL

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	respBody := rec.Body.String()
	if !strings.Contains(respBody, "ghs_getenv_test") {
		t.Fatalf("expected response to contain token, got: %s", respBody)
	}
}

func TestNewHandler_LegacyAppIDsOnly(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"test-org/coder":"200"}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	h, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for legacy-only ROLE_APP_IDS, got %d", rec.Code)
	}
}

func TestNewHandler_DefaultGithubBaseURL(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	h, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if h.githubBaseURL != "https://api.github.com" {
		t.Fatalf("expected default github base URL, got %s", h.githubBaseURL)
	}
}

func TestNewHandler_NilHTTPClient(t *testing.T) {
	// Passing nil HTTP client should still work (handler stores nil,
	// which will fail at runtime when making requests, but construction
	// should succeed).
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	h, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if h.httpClient != nil {
		t.Fatal("expected nil HTTP client")
	}
}

func TestNewHandler_EmptyAllowedOrgs(t *testing.T) {
	// Verifies that an empty ALLOWED_ORGS with PER_REPO_WIF_REPOS works.
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/my-repo")
	t.Setenv("ALLOWED_ORGS", "")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	h, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandler should succeed with empty ALLOWED_ORGS: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestNewHandler_NilOIDCVerifier(t *testing.T) {
	_, err := NewHandler(nil, &fakePEMAccessor{}, &http.Client{})
	if err == nil {
		t.Fatal("expected error for nil oidcVerifier")
	}
	if !strings.Contains(err.Error(), "oidcVerifier must not be nil") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewHandler_CustomRolePermissions(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","custom-role":"200"}`)
	t.Setenv("CUSTOM_ROLE_PERMISSIONS", `{"custom-role":{"contents":"read","metadata":"read"}}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	h, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if !h.checkAllowedRole("custom-role") {
		t.Fatal("custom-role should be allowed")
	}
	if !HasRole("custom-role") {
		t.Fatal("custom-role should be registered")
	}
	// Clean up custom roles to avoid polluting other tests.
	t.Cleanup(func() { RegisterCustomRolePermissions(nil) })
}

func TestNewHandler_InvalidCustomRolePermissions(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"triage":"100"}`)
	t.Setenv("CUSTOM_ROLE_PERMISSIONS", "not-json")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	_, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err == nil {
		t.Fatal("expected error for invalid CUSTOM_ROLE_PERMISSIONS JSON")
	}
	if !strings.Contains(err.Error(), "CUSTOM_ROLE_PERMISSIONS") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewHandler_EmptyOIDCAudience(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "")

	// NewHandler no longer reads OIDC_AUDIENCE — verifiers do. Verify
	// that NewJWKSVerifier rejects an empty audience.
	_, err := NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL: "https://token.actions.githubusercontent.com",
	})
	if err == nil {
		t.Fatal("expected error for empty OIDC_AUDIENCE")
	}
	if !strings.Contains(err.Error(), "OIDC_AUDIENCE must be configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewHandler_RegisterCustomRolePermissionsError(t *testing.T) {
	// Use a built-in role name in CUSTOM_ROLE_PERMISSIONS to trigger a
	// collision error from RegisterCustomRolePermissions.
	t.Setenv("ROLE_APP_IDS", `{"triage":"100"}`)
	t.Setenv("CUSTOM_ROLE_PERMISSIONS", `{"triage":{"contents":"read"}}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	_, err := NewHandler(&fakeOIDCVerifier{}, &fakePEMAccessor{}, &http.Client{})
	if err == nil {
		t.Fatal("expected error when custom role collides with built-in")
	}
	if !strings.Contains(err.Error(), "registering custom role permissions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// fakeHTTPDoer implements HTTPDoer for testing.
type fakeHTTPDoer struct {
	err error
}

func (f *fakeHTTPDoer) Do(_ *http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{StatusCode: 200}, nil
}

// fakeContextPEMAccessor records the context passed to AccessPEM.
type fakeContextPEMAccessor struct {
	pems   map[string][]byte
	gotCtx context.Context
}

func (f *fakeContextPEMAccessor) AccessPEM(ctx context.Context, role string) ([]byte, error) {
	f.gotCtx = ctx
	key := PemSecretRole(role)
	data, ok := f.pems[key]
	if !ok {
		return nil, fmt.Errorf("PEM not found for %s", key)
	}
	return append([]byte(nil), data...), nil
}
