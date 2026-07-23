package mintcore

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewHandlerFromConfig_Basic(t *testing.T) {
	roleAppIDs := `{"triage":"100","coder":"200"}`
	h, err := NewHandlerFromConfig(roleAppIDs, "", &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandlerFromConfig: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil handler")
	}

	// Verify roles were parsed (both triage and coder should be allowed).
	if !h.checkAllowedRole("triage") {
		t.Fatal("triage should be allowed")
	}
	if !h.checkAllowedRole("coder") {
		t.Fatal("coder should be allowed")
	}
}

func TestNewHandlerFromConfig_ExplicitAllowedRoles(t *testing.T) {
	roleAppIDs := `{"triage":"100","coder":"200","review":"300"}`
	h, err := NewHandlerFromConfig(roleAppIDs, "triage,coder", &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandlerFromConfig: %v", err)
	}

	if !h.checkAllowedRole("triage") {
		t.Fatal("triage should be allowed")
	}
	if !h.checkAllowedRole("coder") {
		t.Fatal("coder should be allowed")
	}
	if h.checkAllowedRole("review") {
		t.Fatal("review should not be allowed when not in AllowedRoles")
	}
}

func TestNewHandlerFromConfig_MissingRoleAppIDs(t *testing.T) {
	_, err := NewHandlerFromConfig("", "", &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err != nil {
		t.Fatalf("expected no error for empty RoleAppIDs, got: %v", err)
	}
}

func TestNewHandlerFromConfig_InvalidRoleAppIDsJSON(t *testing.T) {
	_, err := NewHandlerFromConfig("not-json", "", &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse RoleAppIDs") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewHandlerFromConfig_InvalidAllowedRoleFormat(t *testing.T) {
	roleAppIDs := `{"coder":"200"}`
	_, err := NewHandlerFromConfig(roleAppIDs, "INVALID", &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err == nil {
		t.Fatal("expected error for invalid role format")
	}
	if !strings.Contains(err.Error(), "AllowedRoles contains invalid entry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewHandlerFromConfig_AllowedRoleNotInPermissions(t *testing.T) {
	roleAppIDs := `{"nonexistent":"100"}`
	_, err := NewHandlerFromConfig(roleAppIDs, "nonexistent", &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err == nil {
		t.Fatal("expected error for role not in RolePermissions")
	}
	if !strings.Contains(err.Error(), "RolePermissions has no entry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewHandlerFromConfig_AllowedRoleNotInAppIDs(t *testing.T) {
	roleAppIDs := `{"coder":"200"}`
	_, err := NewHandlerFromConfig(roleAppIDs, "triage", &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err == nil {
		t.Fatal("expected error for role not in RoleAppIDs")
	}
	if !strings.Contains(err.Error(), "RoleAppIDs has no entry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewHandlerFromConfig_InjectsHTTPClient(t *testing.T) {
	roleAppIDs := `{"coder":"200"}`
	client := &http.Client{}
	h, err := NewHandlerFromConfig(roleAppIDs, "", &fakePEMAccessor{}, &fakeOIDCVerifier{}, client)
	if err != nil {
		t.Fatalf("NewHandlerFromConfig: %v", err)
	}
	if h.httpClient != client {
		t.Fatal("expected injected HTTP client")
	}
}

func TestNewHandlerFromConfig_NoEnvDependency(t *testing.T) {
	// Verify that NewHandlerFromConfig does not read from os.Getenv
	// by clearing ROLE_APP_IDS and ALLOWED_ROLES.
	t.Setenv("ROLE_APP_IDS", "")
	t.Setenv("ALLOWED_ROLES", "")

	roleAppIDs := `{"triage":"100","coder":"200"}`
	h, err := NewHandlerFromConfig(roleAppIDs, "coder", &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandlerFromConfig: %v", err)
	}

	// Verify the handler was configured from explicit params, not env.
	if !h.checkAllowedRole("coder") {
		t.Fatal("coder should be allowed from explicit config")
	}
	if h.checkAllowedRole("triage") {
		t.Fatal("triage should not be allowed when AllowedRoles is 'coder'")
	}
}

func TestNewHandlerFromConfig_ServeHTTPWorks(t *testing.T) {
	roleAppIDs := `{"coder":"200"}`
	h, err := NewHandlerFromConfig(roleAppIDs, "", &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandlerFromConfig: %v", err)
	}

	// Verify the handler serves HTTP requests (health endpoint).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health: expected 200, got %d", rec.Code)
	}
}

func TestParseWorkerConfig_Basic(t *testing.T) {
	cfg := WorkerConfig{
		RoleAppIDs:   `{"triage":"100","coder":"200"}`,
		AllowedOrgs:  "test-org",
		OIDCAudience: "fullsend-mint",
	}
	h, err := ParseWorkerConfig(cfg, &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err != nil {
		t.Fatalf("ParseWorkerConfig: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}

func TestParseWorkerConfig_MissingRequired(t *testing.T) {
	tests := []struct {
		name string
		cfg  WorkerConfig
		want string
	}{
		{
			name: "missing RoleAppIDs",
			cfg: WorkerConfig{
				AllowedOrgs:  "test-org",
				OIDCAudience: "fullsend-mint",
			},
			want: "RoleAppIDs is required",
		},
		{
			name: "missing OIDCAudience",
			cfg: WorkerConfig{
				RoleAppIDs:  `{"coder":"200"}`,
				AllowedOrgs: "test-org",
			},
			want: "OIDCAudience is required",
		},
		{
			name: "missing AllowedOrgs",
			cfg: WorkerConfig{
				RoleAppIDs:   `{"coder":"200"}`,
				OIDCAudience: "fullsend-mint",
			},
			want: "AllowedOrgs is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseWorkerConfig(tc.cfg, &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestParseWorkerConfig_WithCustomRolePermissions(t *testing.T) {
	defer RegisterCustomRolePermissions(nil)

	cfg := WorkerConfig{
		RoleAppIDs:            `{"triage":"100","coder":"200","deployer":"300"}`,
		AllowedOrgs:           "test-org",
		OIDCAudience:          "fullsend-mint",
		CustomRolePermissions: `{"deployer":{"contents":"write","deployments":"write"}}`,
		AllowedRoles:          "deployer",
	}
	h, err := ParseWorkerConfig(cfg, &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err != nil {
		t.Fatalf("ParseWorkerConfig: %v", err)
	}

	if !h.checkAllowedRole("deployer") {
		t.Fatal("deployer should be allowed")
	}
}

func TestParseWorkerConfig_InvalidCustomPermissions(t *testing.T) {
	cfg := WorkerConfig{
		RoleAppIDs:            `{"coder":"200"}`,
		AllowedOrgs:           "test-org",
		OIDCAudience:          "fullsend-mint",
		CustomRolePermissions: "not-json",
	}
	_, err := ParseWorkerConfig(cfg, &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err == nil {
		t.Fatal("expected error for invalid custom permissions JSON")
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

func TestNewHandlerFromConfig_FullMintFlow(t *testing.T) {
	// Test that a handler created from config can process requests
	// the same way as a handler created from env vars.
	roleAppIDs := `{"coder":"200"}`
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
			fmt.Fprintf(w, `{"token":"ghs_config_test","expires_at":"2026-01-01T00:00:00Z"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()

	h, err := NewHandlerFromConfig(roleAppIDs, "", pemAccessor, verifier, github.Client())
	if err != nil {
		t.Fatalf("NewHandlerFromConfig: %v", err)
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

	// Verify the response contains the expected token.
	respBody := rec.Body.String()
	if !strings.Contains(respBody, "ghs_config_test") {
		t.Fatalf("expected response to contain token, got: %s", respBody)
	}
}

func TestNewHandlerFromConfig_LegacyAppIDsOnly(t *testing.T) {
	roleAppIDs := `{"test-org/coder":"200"}`
	h, err := NewHandlerFromConfig(roleAppIDs, "", &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandlerFromConfig: unexpected error: %v", err)
	}

	// Health check should report unhealthy for legacy-only keys.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for legacy-only ROLE_APP_IDS, got %d", rec.Code)
	}
}

func TestNewHandlerFromConfig_DefaultGithubBaseURL(t *testing.T) {
	roleAppIDs := `{"coder":"200"}`
	h, err := NewHandlerFromConfig(roleAppIDs, "", &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err != nil {
		t.Fatalf("NewHandlerFromConfig: %v", err)
	}
	if h.githubBaseURL != "https://api.github.com" {
		t.Fatalf("expected default github base URL, got %s", h.githubBaseURL)
	}
}

func TestNewHandlerFromConfig_NilHTTPClient(t *testing.T) {
	// Passing nil HTTP client should still work (handler stores nil,
	// which will fail at runtime when making requests, but construction
	// should succeed).
	roleAppIDs := `{"coder":"200"}`
	h, err := NewHandlerFromConfig(roleAppIDs, "", &fakePEMAccessor{}, &fakeOIDCVerifier{}, nil)
	if err != nil {
		t.Fatalf("NewHandlerFromConfig: %v", err)
	}
	if h.httpClient != nil {
		t.Fatal("expected nil HTTP client")
	}
}

func TestParseWorkerConfig_SetsAllowedOrgsOnVerifier(t *testing.T) {
	// Verifies ParseWorkerConfig sets up the full pipeline including
	// passing allowed orgs and workflow config.
	cfg := WorkerConfig{
		RoleAppIDs:           `{"coder":"200"}`,
		AllowedOrgs:          "test-org",
		OIDCAudience:         "fullsend-mint",
		AllowedWorkflowFiles: "dispatch.yml",
		PerRepoWIFRepos:      "test-org/my-repo",
	}

	h, err := ParseWorkerConfig(cfg, &fakePEMAccessor{}, &fakeOIDCVerifier{}, &http.Client{})
	if err != nil {
		t.Fatalf("ParseWorkerConfig: %v", err)
	}

	// Verify the handler was created successfully and can serve health checks.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
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
