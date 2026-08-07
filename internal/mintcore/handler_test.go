package mintcore

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func generateTestRSAKey() ([]byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}), nil
}

type fakePEMAccessor struct {
	pems map[string][]byte
	err  error
}

type fakeOIDCVerifier struct {
	claims *Claims
	err    error
}

func (f *fakeOIDCVerifier) Verify(_ context.Context, _ string) (*Claims, error) {
	return f.claims, f.err
}

// testAllowedOrgs is no longer needed since authorization config moved from
// verifiers to the handler. The handler reads ALLOWED_ORGS from the
// environment via NewHandler. Tests that need per-repo or workflow config
// set them directly on the handler fields.

func (f *fakePEMAccessor) AccessPEM(_ context.Context, role string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := PemSecretRole(role)
	data, ok := f.pems[key]
	if !ok {
		return nil, fmt.Errorf("PEM not found for %s", key)
	}
	cp := append([]byte(nil), data...)
	return cp, nil
}

func mustNewHandler(t *testing.T, pemAccessor PEMAccessor, verifier OIDCVerifier) *Handler {
	t.Helper()
	h, err := NewHandler(pemAccessor, verifier)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

// testOIDCEnv sets up a mock OIDC server and returns a handler with the
// OIDCVerifier pointing at it, along with a function to sign JWTs.
type testOIDCEnv struct {
	handler   *Handler
	server    *httptest.Server
	key       *rsa.PrivateKey
	kid       string
	issuerURL string
}

func newTestOIDCEnv(t *testing.T, pemAccessor PEMAccessor) *testOIDCEnv {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	kid := "test-key-1"
	env := &testOIDCEnv{key: key, kid: kid}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"issuer":   env.server.URL,
			"jwks_uri": env.server.URL + "/.well-known/jwks",
		})
	})
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{
				{
					"kty": "RSA", "alg": "RS256", "use": "sig",
					"kid": kid,
					"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
					"e":   base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
				},
			},
		})
	})

	env.server = httptest.NewServer(mux)
	t.Cleanup(env.server.Close)
	env.issuerURL = env.server.URL

	verifier := NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL: env.server.URL,
		Audience:  os.Getenv("OIDC_AUDIENCE"),
	})
	h, err := NewHandler(pemAccessor, verifier)
	if err != nil {
		t.Fatalf("creating handler: %v", err)
	}
	env.handler = h
	return env
}

func (e *testOIDCEnv) signToken(t *testing.T, claimsOverrides map[string]interface{}) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": e.kid})
	now := time.Now()
	claims := map[string]interface{}{
		"iss": e.issuerURL,
		"aud": "fullsend-mint",
		"iat": now.Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		// Default claim matches common ["test-repo"] mint bodies so tests
		// exercise requesting-repo-only scope (compat off) unless overridden.
		// job_workflow_ref stays on .fullsend so OIDC enrollment checks pass.
		"repository":       "test-org/test-repo",
		"repository_owner": "test-org",
		"job_workflow_ref": "test-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
	}
	for k, v := range claimsOverrides {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	claimsJSON, _ := json.Marshal(claims)
	hB64 := base64.RawURLEncoding.EncodeToString(header)
	cB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	input := hB64 + "." + cB64
	hashed := sha256.Sum256([]byte(input))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, e.key, crypto.SHA256, hashed[:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestHandler_HealthEndpoint(t *testing.T) {
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health: expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("GET /health: expected application/json, got %s", ct)
	}
}

func TestHandler_HealthEndpoint_IncludesVersion(t *testing.T) {
	Version = "1.2.3"
	Commit = "abc123def456"
	t.Cleanup(func() { Version = ""; Commit = "" })
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health: expected 200, got %d", rec.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode health response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("expected status ok, got %q", resp["status"])
	}
	if resp["version"] != "1.2.3" {
		t.Fatalf("expected version 1.2.3, got %q", resp["version"])
	}
	if resp["commit"] != "abc123def456" {
		t.Fatalf("expected commit abc123def456, got %q", resp["commit"])
	}
}

func TestHandler_HealthEndpoint_OmitsEmptyVersion(t *testing.T) {
	Version = ""
	Commit = ""
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "version") {
		t.Fatalf("health response should omit version when empty: %s", body)
	}
	if strings.Contains(body, "commit") {
		t.Fatalf("health response should omit commit when empty: %s", body)
	}
}

func TestHandler_StatusEndpoint(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+env.signToken(t, nil))
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp statusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode status response: %v", err)
	}
	if resp.Org != "test-org" {
		t.Fatalf("expected org %q, got %q", "test-org", resp.Org)
	}
	if len(resp.Roles) == 0 {
		t.Fatal("expected roles in response")
	}

	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("expected Cache-Control: no-store, got %s", cc)
	}

	// Verify no app IDs or org-scoped keys are leaked in the response.
	body := rec.Body.String()
	if strings.Contains(body, "100") || strings.Contains(body, "200") {
		t.Fatalf("status response should not contain app IDs: %s", body)
	}
	if strings.Contains(body, "test-org/") {
		t.Fatalf("status response should not contain org-scoped role keys: %s", body)
	}

	// Verify roles are org-stripped names only.
	for _, role := range resp.Roles {
		if strings.Contains(role, "/") {
			t.Fatalf("role %q should not contain org prefix", role)
		}
	}

	// Verify response does not list all orgs.
	if strings.Contains(body, "orgs") {
		t.Fatalf("status response should not contain orgs array: %s", body)
	}

}

func TestHandler_StatusEndpoint_IncludesVersion(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	Version = "0.27.0"
	Commit = "1e7877ae"
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
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode status response: %v", err)
	}
	if resp.Version != "0.27.0" {
		t.Fatalf("expected version 0.27.0, got %q", resp.Version)
	}
	if resp.Commit != "1e7877ae" {
		t.Fatalf("expected commit 1e7877ae, got %q", resp.Commit)
	}
}

func TestHandler_StatusEndpoint_PostNotAllowed(t *testing.T) {
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/status", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for POST /v1/status, got %d", rec.Code)
	}
}

func TestHandler_StatusEndpoint_NoAuth(t *testing.T) {
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRoleOnlyAppIDs_IgnoresLegacyOrgScopedKeys(t *testing.T) {
	ids := map[string]string{
		"coder":            "200",
		"test-org/coder":   "999",
		"other-org/triage": "100",
		"triage":           "100",
	}
	got := RoleOnlyAppIDs(ids)
	want := map[string]string{"coder": "200", "triage": "100"}
	if len(got) != len(want) {
		t.Fatalf("expected %d entries, got %d: %v", len(want), len(got), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("RoleOnlyAppIDs[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestRoleOnlyAppIDs_ReturnsNilForEmpty(t *testing.T) {
	if RoleOnlyAppIDs(nil) != nil {
		t.Fatal("expected nil for nil input")
	}
	if RoleOnlyAppIDs(map[string]string{}) != nil {
		t.Fatal("expected nil for empty map")
	}
}

func TestLegacyAppIDsOnly(t *testing.T) {
	if legacyAppIDsOnly(nil) {
		t.Fatal("expected false for nil")
	}
	if legacyAppIDsOnly(map[string]string{}) {
		t.Fatal("expected false for empty map")
	}
	if legacyAppIDsOnly(map[string]string{"coder": "100"}) {
		t.Fatal("expected false for role-only keys")
	}
	if legacyAppIDsOnly(map[string]string{"acme/coder": "100", "coder": "200"}) {
		t.Fatal("expected false when role-only keys present")
	}
	if !legacyAppIDsOnly(map[string]string{"acme/coder": "100"}) {
		t.Fatal("expected true for legacy-only keys")
	}
}

func TestHandler_HealthEndpoint_EmptyMint(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", "")
	t.Setenv("ALLOWED_ROLES", "")
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health: expected 200 for empty mint, got %d", rec.Code)
	}
}

func TestHandler_HealthEndpoint_LegacyOnlyRoleAppIDs(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"test-org/coder":"200"}`)
	t.Setenv("ALLOWED_ROLES", "")
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /health: expected 503 for legacy-only ROLE_APP_IDS, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unhealthy") {
		t.Fatalf("expected unhealthy status, got %q", rec.Body.String())
	}
}

func TestHandler_StatusEndpoint_MixedCaseOrgClaim(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200","triage":"100"}`)
	t.Setenv("ALLOWED_ORGS", "Test-Org")

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+env.signToken(t, map[string]interface{}{
		"repository_owner": "Test-Org",
	}))
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp statusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode status response: %v", err)
	}
	if resp.Org != "test-org" {
		t.Fatalf("expected lowercased org %q, got %q", "test-org", resp.Org)
	}
	if len(resp.Roles) != 2 {
		t.Fatalf("expected 2 roles, got %d: %v", len(resp.Roles), resp.Roles)
	}
	for _, role := range resp.Roles {
		if strings.Contains(role, "/") {
			t.Fatalf("role %q should not contain org prefix", role)
		}
		if role != strings.ToLower(role) {
			t.Fatalf("role %q should be lowercase", role)
		}
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/token", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandler_NotFound(t *testing.T) {
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/wrong/path", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandler_RootPathAccepted(t *testing.T) {
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET on /, got %d", rec.Code)
	}
}

func TestHandler_MissingAuthHeader(t *testing.T) {
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader("{}"))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHandler_InvalidJSON(t *testing.T) {
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader("not-json"))
	req.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandler_MissingRole(t *testing.T) {
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if !strings.Contains(resp["error"], "role") {
		t.Fatalf("expected role error, got: %s", resp["error"])
	}
}

func TestHandler_InvalidRoleFormat(t *testing.T) {
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	tests := []struct {
		name string
		role string
	}{
		{"path traversal", "../etc"},
		{"shell metachar", "code;rm"},
		{"uppercase", "CODER"},
		{"spaces", "code r"},
		{"starts with number", "1bad"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"role":%q}`, tc.role)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer test-token")
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for role=%q, got %d", tc.role, rec.Code)
			}
		})
	}
}

func TestHandler_RoleAllowed(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})
	token := env.signToken(t, nil)

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	// Should pass role check (will fail at GitHub API since no mock, but not 403 for role)
	if rec.Code == http.StatusForbidden {
		var resp map[string]string
		json.NewDecoder(rec.Body).Decode(&resp)
		if strings.Contains(resp["error"], "role") {
			t.Fatalf("valid role 'coder' should not be rejected at role check: %s", resp["error"])
		}
	}
}

func TestHandler_RoleNotAllowed(t *testing.T) {
	t.Setenv("ALLOWED_ROLES", "triage,coder")
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200"}`)
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	body := `{"role":"deploy"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestHandler_InvalidRepoName(t *testing.T) {
	t.Setenv("ALLOWED_ROLES", "coder")
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	tests := []struct {
		name  string
		repos string
	}{
		{"dot dot", `["../evil"]`},
		{"slash", `["org/repo"]`},
		{"spaces", `["my repo"]`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"role":"coder","repos":%s}`, tc.repos)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer test-token")
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandler_EmptyRepos_FullOrgToken(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	body := `{"role":"coder"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for omitted repos, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(resp["error"], "repos is required") {
		t.Fatalf("unexpected error: %s", resp["error"])
	}
}

func TestHandler_EmptyReposList_Rejected(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	body := `{"role":"coder","repos":[]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty repos list, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(resp["error"], "repos is required") {
		t.Fatalf("unexpected error: %s", resp["error"])
	}
}

func TestHandler_StarRepos_SameOrgDenied(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})
	token := env.signToken(t, nil)

	body := `{"role":"coder","repos":["*"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for same-org * repos, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_EmptyRepos_CrossOrgRejected(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,fullsend-ai")
	t.Setenv("ROLE_APP_IDS", `{"e2e":"300"}`)

	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	body := `{"role":"e2e","target_org":"pool-org"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for cross-org omitted repos, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(resp["error"], "repos is required") {
		t.Fatalf("unexpected error: %s", resp["error"])
	}
}

func TestHandler_EmptyRepos_CrossOrgStarAlias(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,fullsend-ai")
	t.Setenv("ROLE_APP_IDS", `{"e2e":"300"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"e2e": pemData}})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "fullsend-ai/fullsend",
		"repository_owner": "fullsend-ai",
		"job_workflow_ref": "fullsend-ai/fullsend/.github/workflows/e2e.yml@refs/heads/main",
	})

	var tokenCalls int
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/orgs/pool-org/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 999, Account: struct {
					Login string `json:"login"`
				}{Login: "pool-org"},
			})
		case r.URL.Path == "/app/installations/999/access_tokens" && r.Method == http.MethodPost:
			tokenCalls++
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			if tokenCalls == 1 {
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_policy_token"})
				return
			}
			if _, ok := body["repositories"]; ok {
				t.Error("expected installation token request to omit repositories")
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:               "ghs_e2e_star",
				ExpiresAt:           "2026-05-06T12:00:00Z",
				Permissions:         map[string]string{"contents": "write", "metadata": "read"},
				RepositorySelection: "all",
			})
		case r.URL.Path == "/orgs/pool-org/actions/variables/FULLSEND_FOREIGN_E2E_REPOS" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(variableResponse{
				Name:  "FULLSEND_FOREIGN_E2E_REPOS",
				Value: "fullsend-ai/fullsend",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"e2e","target_org":"pool-org","repos":["*"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp mintResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Token != "ghs_e2e_star" {
		t.Fatalf("expected token=ghs_e2e_star, got %s", resp.Token)
	}
}

func TestHandler_ReposScope_EnrolledCompat(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "test-org/api",
		"repository_owner": "test-org",
		"job_workflow_ref": "test-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/installation") && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 1, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_ok",
				ExpiresAt: "2026-05-06T12:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	for _, repos := range []string{`[".fullsend"]`, `["api",".fullsend"]`, `["api"]`} {
		body := `{"role":"coder","repos":` + repos + `}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		env.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("repos=%s: expected 200, got %d: %s", repos, rec.Code, rec.Body.String())
		}
	}

	body := `{"role":"coder","repos":["other"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for other repo, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_ReposScope_PerRepoDenied(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	// Per-repo callers can only request their own repository.
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/test-repo")
	// Clear ALLOWED_ORGS (set by TestMain) so the dual-enrollment guard
	// does not fire — this test must exercise the per-repo denial path
	// (repos_scope.go:73), not the per-org catch-all.
	t.Setenv("ALLOWED_ORGS", "")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})
	// Use upstream workflow ref so workflow host validation passes;
	// the test exercises repos scope denial, not workflow host denial.
	token := env.signToken(t, map[string]interface{}{
		"job_workflow_ref": "fullsend-ai/fullsend/.github/workflows/code.yml@refs/heads/main",
	})

	body := `{"role":"coder","repos":["api"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for per-repo caller requesting different repo, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_ReposScope_DualEnrollment(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	// Dual enrollment: repo in PER_REPO_WIF_REPOS AND org in ALLOWED_ORGS.
	// The caller should get per-org scope treatment (superset of per-repo).
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/test-repo")
	t.Setenv("ALLOWED_ORGS", "test-org")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})
	token := env.signToken(t, nil) // test-org/test-repo

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/installation") && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 1, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_dual",
				ExpiresAt: "2026-08-04T12:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	// Org-mode shapes that should succeed for dual-enrolled callers.
	for _, repos := range []string{`[".fullsend"]`, `["test-repo",".fullsend"]`, `["test-repo"]`} {
		body := `{"role":"coder","repos":` + repos + `}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		env.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("repos=%s: expected 200 for dual-enrolled caller, got %d: %s", repos, rec.Code, rec.Body.String())
		}
	}

	// Shape not allowed even for per-org callers.
	body := `{"role":"coder","repos":["other"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disallowed per-org shape, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_ReposScope_DualEnrollmentWildcardOrgs(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	// ALLOWED_ORGS=* with specific PER_REPO_WIF_REPOS: per-repo callers
	// are upgraded to per-org scope because ValidateOrgAllowed succeeds
	// for any org, and IsPublicMintRepos returns false for non-wildcard
	// PER_REPO_WIF_REPOS. This is consistent because all non-per-repo
	// callers already receive per-org treatment in this configuration.
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/test-repo")
	t.Setenv("ALLOWED_ORGS", "*")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})
	token := env.signToken(t, nil) // test-org/test-repo

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/installation") && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 1, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_wildcard_dual",
				ExpiresAt: "2026-08-04T12:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	// Org-mode shapes should succeed: dual-enrollment guard upgrades
	// per-repo to per-org because ALLOWED_ORGS=* matches any org.
	for _, repos := range []string{`[".fullsend"]`, `["test-repo",".fullsend"]`, `["test-repo"]`} {
		body := `{"role":"coder","repos":` + repos + `}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		env.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("repos=%s: expected 200 for wildcard-org dual-enrolled caller, got %d: %s", repos, rec.Code, rec.Body.String())
		}
	}

	// Shape not allowed even for per-org callers.
	body := `{"role":"coder","repos":["other"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disallowed per-org shape with wildcard orgs, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_DualEnrollment_WorkflowRefAcceptsBothModes(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	// Dual enrollment: repo in PER_REPO_WIF_REPOS AND org in ALLOWED_ORGS.
	// Workflow ref validation should accept sources from EITHER mode:
	// per-repo (workflowHostRepos) or per-org ({org}/.fullsend, upstream).
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/test-repo")
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("WORKFLOW_HOST_REPOS", "test-org/custom-workflows")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/installation") && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 1, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_dual_wf",
				ExpiresAt: "2026-08-05T12:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()

	tests := []struct {
		name        string
		workflowRef string
		wantOK      bool
	}{
		{
			"per-org source: .fullsend repo",
			"test-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
			true,
		},
		{
			"per-repo source: workflow host repo",
			"test-org/custom-workflows/.github/workflows/code.yml@refs/heads/main",
			true,
		},
		{
			"upstream always accepted",
			"fullsend-ai/fullsend/.github/workflows/code.yml@refs/heads/main",
			true,
		},
		{
			"unlisted repo rejected",
			"test-org/random-repo/.github/workflows/code.yml@refs/heads/main",
			false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestOIDCEnv(t, &fakePEMAccessor{
				pems: map[string][]byte{"coder": pemData},
			})
			env.handler.workflowHostRepos = map[string]bool{"test-org/custom-workflows": true}
			env.handler.githubBaseURL = github.URL

			token := env.signToken(t, map[string]interface{}{
				"job_workflow_ref": tc.workflowRef,
			})

			body := `{"role":"coder","repos":["test-repo"]}`
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			env.handler.ServeHTTP(rec, req)

			if tc.wantOK {
				if rec.Code != http.StatusOK {
					t.Fatalf("expected 200 for dual-enrolled caller with %s, got %d: %s",
						tc.name, rec.Code, rec.Body.String())
				}
			} else {
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("expected 401 for dual-enrolled caller with %s, got %d: %s",
						tc.name, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

func TestHandler_TooManyRepos(t *testing.T) {
	t.Setenv("ALLOWED_ROLES", "coder")
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	repos := make([]string, maxRepos+1)
	for i := range repos {
		repos[i] = fmt.Sprintf("repo-%d", i)
	}
	reposJSON, _ := json.Marshal(repos)
	body := fmt.Sprintf(`{"role":"coder","repos":%s}`, reposJSON)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	want := fmt.Sprintf("max %d", maxRepos)
	if !strings.Contains(resp["error"], want) {
		t.Fatalf("expected error to contain %q, got: %s", want, resp["error"])
	}
}

func TestHandler_OIDCVerification_WrongOrg(t *testing.T) {
	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	token := env.signToken(t, map[string]interface{}{
		"repository_owner": "evil-org",
		"repository":       "evil-org/.fullsend",
		"job_workflow_ref": "evil-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
	})

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHandler_OIDCVerification_BadWorkflowRef(t *testing.T) {
	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "test-org/some-repo",
		"job_workflow_ref": "test-org/some-repo/.github/workflows/malicious.yml@refs/heads/main",
	})

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHandler_OIDCVerification_InvalidJWT(t *testing.T) {
	env := newTestOIDCEnv(t, &fakePEMAccessor{})

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid JWT, got %d", rec.Code)
	}
}

func TestHandler_OIDCVerification_ExpiredToken(t *testing.T) {
	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	token := env.signToken(t, map[string]interface{}{
		"iat": time.Now().Add(-2 * time.Hour).Unix(),
		"exp": time.Now().Add(-1 * time.Hour).Unix(),
	})

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired token, got %d", rec.Code)
	}
}

func TestHandler_OIDCVerification_BadAudience(t *testing.T) {
	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	token := env.signToken(t, map[string]interface{}{
		"aud": "wrong-audience",
	})

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad audience, got %d", rec.Code)
	}
}

func TestHandler_SecretAccessError(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	env := newTestOIDCEnv(t, &fakePEMAccessor{err: fmt.Errorf("access denied")})
	token := env.signToken(t, nil)

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["error"] != "mint failed" {
		t.Fatalf("expected 'mint failed', got: %s", resp["error"])
	}
}

func TestHandler_FullFlow(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	pemAccessor := &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	}
	env := newTestOIDCEnv(t, pemAccessor)
	token := env.signToken(t, nil)

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/test-repo/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 12345, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/12345/access_tokens") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_test_token",
				ExpiresAt: "2026-05-06T12:00:00Z",
				Permissions: map[string]string{
					"contents":      "write",
					"pull_requests": "write",
					"issues":        "write",
					"checks":        "read",
					"metadata":      "read",
				},
				Repositories: []installationTokenRepository{
					{FullName: "test-org/test-repo"},
				},
				RepositorySelection: "selected",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp mintResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Token != "ghs_test_token" {
		t.Fatalf("expected token=ghs_test_token, got %s", resp.Token)
	}
	if resp.ExpiresAt != "2026-05-06T12:00:00Z" {
		t.Fatalf("expected expires_at=2026-05-06T12:00:00Z, got %s", resp.ExpiresAt)
	}
	if len(resp.GrantedRepos) != 1 || resp.GrantedRepos[0] != "test-org/test-repo" {
		t.Fatalf("expected granted_repos=[test-org/test-repo], got %v", resp.GrantedRepos)
	}
	if resp.RepoSelection != "selected" {
		t.Fatalf("expected repository_selection=selected, got %s", resp.RepoSelection)
	}
	if resp.GrantedPerms["contents"] != "write" {
		t.Fatalf("expected granted_permissions to include contents=write, got %v", resp.GrantedPerms)
	}
}

func TestHandler_FullFlowGrantedScopeAll(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})
	token := env.signToken(t, nil)

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/test-repo/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 12345, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/12345/access_tokens") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			// Simulate GitHub returning repository_selection "all" with no
			// repositories array — the scenario #1916 is investigating.
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:               "ghs_all_repos_token",
				ExpiresAt:           "2026-05-06T12:00:00Z",
				Permissions:         map[string]string{"contents": "read", "issues": "write", "metadata": "read"},
				RepositorySelection: "all",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp mintResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Token != "ghs_all_repos_token" {
		t.Fatalf("expected token=ghs_all_repos_token, got %s", resp.Token)
	}
	if resp.RepoSelection != "all" {
		t.Fatalf("expected repository_selection=all, got %s", resp.RepoSelection)
	}
	if len(resp.GrantedRepos) != 0 {
		t.Fatalf("expected empty granted_repos for selection=all, got %v", resp.GrantedRepos)
	}
	if resp.GrantedPerms["issues"] != "write" {
		t.Fatalf("expected granted_permissions to include issues=write, got %v", resp.GrantedPerms)
	}
}

func TestHandler_FullFlowWithRepos(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	// .fullsend caller is per-org (not per-repo), so org-mode shapes are allowed.

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})
	// .fullsend caller (per-org) may mint a multi-repo list.
	token := env.signToken(t, map[string]interface{}{
		"repository":       "test-org/.fullsend",
		"repository_owner": "test-org",
		"job_workflow_ref": "test-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
	})

	var capturedTokenReq map[string]interface{}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/my-repo/installation":
			json.NewEncoder(w).Encode(installationResponse{
				ID: 1, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			reqBody, _ := io.ReadAll(r.Body)
			json.Unmarshal(reqBody, &capturedTokenReq)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_scoped",
				ExpiresAt: "2026-05-06T12:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"coder","repos":["my-repo","other-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	repos, ok := capturedTokenReq["repositories"].([]interface{})
	if !ok {
		t.Fatal("expected repositories in token request")
	}
	if len(repos) != 2 || repos[0] != "my-repo" || repos[1] != "other-repo" {
		t.Fatalf("unexpected repos: %v", repos)
	}

	perms, ok := capturedTokenReq["permissions"].(map[string]interface{})
	if !ok {
		t.Fatal("expected permissions in token request")
	}
	if perms["contents"] != "write" {
		t.Fatalf("expected contents:write for coder role, got %v", perms["contents"])
	}
}

func TestHandler_FullFlowWithRepos_Retro(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"retro":"600"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"retro": pemData},
	})
	token := env.signToken(t, nil)

	var capturedTokenReq map[string]interface{}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/test-repo/installation":
			json.NewEncoder(w).Encode(installationResponse{
				ID: 1, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			reqBody, _ := io.ReadAll(r.Body)
			json.Unmarshal(reqBody, &capturedTokenReq)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_retro_token",
				ExpiresAt: "2026-05-06T12:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"retro","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	perms, ok := capturedTokenReq["permissions"].(map[string]interface{})
	if !ok {
		t.Fatal("expected permissions in token request")
	}
	if perms["actions"] != "read" {
		t.Fatalf("expected actions:read for retro role, got %v", perms["actions"])
	}
	if perms["contents"] != "read" {
		t.Fatalf("expected contents:read for retro role, got %v", perms["contents"])
	}
	if perms["pull_requests"] != "write" {
		t.Fatalf("expected pull_requests:write for retro role, got %v", perms["pull_requests"])
	}
	if perms["issues"] != "write" {
		t.Fatalf("expected issues:write for retro role, got %v", perms["issues"])
	}
	if perms["metadata"] != "read" {
		t.Fatalf("expected metadata:read for retro role, got %v", perms["metadata"])
	}
	if len(perms) != 5 {
		t.Fatalf("expected exactly 5 permissions for retro role, got %d: %v", len(perms), perms)
	}
}

func TestHandler_FullFlowWithRepos_Prioritize(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"prioritize":"700"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"prioritize": pemData},
	})
	token := env.signToken(t, nil)

	var capturedTokenReq map[string]interface{}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/test-repo/installation":
			json.NewEncoder(w).Encode(installationResponse{
				ID: 1, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			reqBody, _ := io.ReadAll(r.Body)
			json.Unmarshal(reqBody, &capturedTokenReq)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_prioritize_token",
				ExpiresAt: "2026-05-06T12:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"prioritize","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	perms, ok := capturedTokenReq["permissions"].(map[string]interface{})
	if !ok {
		t.Fatal("expected permissions in token request")
	}
	if perms["contents"] != "read" {
		t.Fatalf("expected contents:read for prioritize role, got %v", perms["contents"])
	}
	if perms["issues"] != "write" {
		t.Fatalf("expected issues:write for prioritize role, got %v", perms["issues"])
	}
	if perms["organization_projects"] != "write" {
		t.Fatalf("expected organization_projects:write for prioritize role, got %v", perms["organization_projects"])
	}
	if perms["metadata"] != "read" {
		t.Fatalf("expected metadata:read for prioritize role, got %v", perms["metadata"])
	}
	if len(perms) != 4 {
		t.Fatalf("expected exactly 4 permissions for prioritize role, got %d: %v", len(perms), perms)
	}
}

func TestHandler_InstallationNotFound(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})
	token := env.signToken(t, nil)

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["error"] != "mint failed" {
		t.Fatalf("expected 'mint failed', got: %s", resp["error"])
	}
}

func TestHandler_LargeBody(t *testing.T) {
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	largePayload := bytes.Repeat([]byte("x"), 128<<10)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(largePayload))
	req.Header.Set("Authorization", "Bearer token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCheckAllowedRole(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200","review":"300"}`)
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	if !h.checkAllowedRole("coder") {
		t.Fatal("coder should be allowed")
	}
	if h.checkAllowedRole("deploy") {
		t.Fatal("deploy should not be allowed")
	}
}

func TestCheckAllowedRole_Empty(t *testing.T) {
	t.Setenv("ALLOWED_ROLES", "")
	t.Setenv("ROLE_APP_IDS", "")
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})
	if h.checkAllowedRole("coder") {
		t.Fatal("should fail closed when no roles configured")
	}
}

func TestLookupRoleAppID(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200"}`)
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	id, err := h.lookupRoleAppID("coder")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "200" {
		t.Fatalf("expected 200, got %s", id)
	}

	_, err = h.lookupRoleAppID("deploy")
	if err == nil {
		t.Fatal("expected error for unknown role")
	}
}

func TestLookupRoleAppID_FixAliasUsesCoderAppID(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200","fix":"400"}`)
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	id, err := h.lookupRoleAppID("fix")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "200" {
		t.Fatalf("expected fix to resolve via coder alias to 200, got %s", id)
	}
}

func TestLookupRoleAppID_LegacyOrgScopedKeysIgnored(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"test-org/coder":"200"}`)
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	_, err := h.lookupRoleAppID("coder")
	if err == nil {
		t.Fatal("expected error when only legacy org-scoped keys are configured")
	}
}

func TestLookupRoleAppID_NotSet(t *testing.T) {
	t.Setenv("ALLOWED_ROLES", "")
	t.Setenv("ROLE_APP_IDS", "")
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	_, err := h.lookupRoleAppID("coder")
	if err == nil {
		t.Fatal("expected error when ROLE_APP_IDS not set")
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusBadRequest, "test error")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}
	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["error"] != "test error" {
		t.Fatalf("expected 'test error', got %s", resp["error"])
	}
}

func TestHandler_MultiOrg_FullFlow(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,other-org")
	t.Setenv("GCP_PROJECT_NUMBER", "123456")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("ROLE_APP_IDS", `{"triage":"100","coder":"200","review":"300","fix":"400","fullsend":"500"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "other-org/test-repo",
		"repository_owner": "other-org",
		"job_workflow_ref": "other-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
	})

	var gotInstallationPath string
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/other-org/test-repo/installation" && r.Method == http.MethodGet:
			gotInstallationPath = r.URL.Path
			json.NewEncoder(w).Encode(installationResponse{
				ID: 99999, Account: struct {
					Login string `json:"login"`
				}{Login: "other-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/99999/access_tokens") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_other_org_token",
				ExpiresAt: "2026-05-07T12:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp mintResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Token != "ghs_other_org_token" {
		t.Fatalf("expected token=ghs_other_org_token, got %s", resp.Token)
	}

	if gotInstallationPath != "/repos/other-org/test-repo/installation" {
		t.Fatalf("expected repo-based installation lookup for other-org, got path: %s", gotInstallationPath)
	}
}

func TestHandler_CrossOrgInstallationMismatch(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "org-a,org-b")
	t.Setenv("GCP_PROJECT_NUMBER", "123456")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("ROLE_APP_IDS", `{"retro":"999"}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"retro": pemData},
	})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "org-a/seshi",
		"repository_owner": "org-a",
		"job_workflow_ref": "org-a/.fullsend/.github/workflows/retro.yml@refs/heads/main",
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/org-a/seshi/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 77777, Account: struct {
					Login string `json:"login"`
				}{Login: "org-b"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/77777/access_tokens") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_CROSS_ORG_TOKEN",
				ExpiresAt: "2026-05-07T12:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"retro","repos":["seshi"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		var resp mintResponse
		json.NewDecoder(rec.Body).Decode(&resp)
		t.Fatalf("mint should reject cross-org installation mismatch, but returned 200 with token=%s", resp.Token)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for cross-org installation mismatch, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_STSVerifier_Integration(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	pemAccessor := &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	}

	// Mock STS server that accepts any token and returns a federated access token.
	stsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "ya29.federated-token",
			"token_type":   "Bearer",
		})
	}))
	defer stsServer.Close()

	verifier := NewSTSVerifier(STSVerifierConfig{
		HTTPClient:         stsServer.Client(),
		STSURL:             stsServer.URL,
		GCPProjectNum:      "123456",
		WIFPoolName:        "fullsend-pool",
		DefaultWIFProvider: "github-oidc",
		OIDCAudience:       "fullsend-mint",
	})
	h := mustNewHandler(t, pemAccessor, verifier)

	// Build a minimal valid OIDC token with the correct claims.
	// The STS mock will accept any token, but prevalidate still checks claims.
	now := time.Now()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims := map[string]interface{}{
		"iss":              "https://token.actions.githubusercontent.com",
		"aud":              "fullsend-mint",
		"iat":              now.Unix(),
		"exp":              now.Add(10 * time.Minute).Unix(),
		"repository":       "test-org/my-repo",
		"repository_owner": "test-org",
		"job_workflow_ref": "test-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
	}
	claimsJSON, _ := json.Marshal(claims)
	hB64 := base64.RawURLEncoding.EncodeToString(header)
	cB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	oidcToken := hB64 + "." + cB64 + ".fake-signature"

	// Mock GitHub API for the full flow.
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/my-repo/installation":
			json.NewEncoder(w).Encode(installationResponse{
				ID: 12345, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/12345/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_sts_test_token",
				ExpiresAt: "2026-06-02T12:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	h.githubBaseURL = github.URL

	body := `{"role":"coder","repos":["my-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp mintResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Token != "ghs_sts_test_token" {
		t.Fatalf("expected token=ghs_sts_test_token, got %s", resp.Token)
	}
}

func TestHandler_STSVerifier_RestrictedWorkflows(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	stsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "ya29.federated-token",
			"token_type":   "Bearer",
		})
	}))
	defer stsServer.Close()

	verifier := NewSTSVerifier(STSVerifierConfig{
		HTTPClient:         stsServer.Client(),
		STSURL:             stsServer.URL,
		GCPProjectNum:      "123456",
		WIFPoolName:        "fullsend-pool",
		DefaultWIFProvider: "github-oidc",
		OIDCAudience:       "fullsend-mint",
	})
	h := mustNewHandler(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	}, verifier)
	h.allowedWorkflowFiles = []string{"dispatch.yml"}

	buildToken := func(workflowRef string) string {
		now := time.Now()
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
		claims := map[string]interface{}{
			"iss":              "https://token.actions.githubusercontent.com",
			"aud":              "fullsend-mint",
			"iat":              now.Unix(),
			"exp":              now.Add(10 * time.Minute).Unix(),
			"repository":       "test-org/my-repo",
			"repository_owner": "test-org",
			"job_workflow_ref": workflowRef,
		}
		claimsJSON, _ := json.Marshal(claims)
		hB64 := base64.RawURLEncoding.EncodeToString(header)
		cB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
		return hB64 + "." + cB64 + ".fake-signature"
	}

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/my-repo/installation":
			json.NewEncoder(w).Encode(installationResponse{
				ID: 12345, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/12345/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_sts_restricted",
				ExpiresAt: "2026-06-02T12:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	h.githubBaseURL = github.URL

	// Allowed workflow through STSVerifier path
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token",
		strings.NewReader(`{"role":"coder","repos":["my-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+buildToken(
		"test-org/.fullsend/.github/workflows/dispatch.yml@refs/heads/main"))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for allowed workflow via STS, got %d: %s", rec.Code, rec.Body.String())
	}

	// Disallowed workflow through STSVerifier path
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/token",
		strings.NewReader(`{"role":"coder","repos":["my-repo"]}`))
	req2.Header.Set("Authorization", "Bearer "+buildToken(
		"test-org/.fullsend/.github/workflows/evil.yml@refs/heads/main"))
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for disallowed workflow via STS, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestHandler_CrossOrgInstallation_SameOrgPasses(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "org-a,org-b")
	t.Setenv("GCP_PROJECT_NUMBER", "123456")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("ROLE_APP_IDS", `{"retro":"999"}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"retro": pemData},
	})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "org-a/seshi",
		"repository_owner": "org-a",
		"job_workflow_ref": "org-a/.fullsend/.github/workflows/retro.yml@refs/heads/main",
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/org-a/seshi/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 88888, Account: struct {
					Login string `json:"login"`
				}{Login: "org-a"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/88888/access_tokens") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_correct_org_token",
				ExpiresAt: "2026-05-07T12:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"retro","repos":["seshi"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when installation matches OIDC org, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp mintResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Token != "ghs_correct_org_token" {
		t.Fatalf("expected ghs_correct_org_token, got %s", resp.Token)
	}
}

func TestHandler_ErrorMessageLeak(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	env := newTestOIDCEnv(t, &fakePEMAccessor{err: fmt.Errorf("secret projects/123/secrets/fullsend-coder-app-pem")})
	token := env.signToken(t, nil)

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	respBody := rec.Body.String()
	if strings.Contains(respBody, "projects/") || strings.Contains(respBody, "secrets/") {
		t.Fatalf("error response leaks internal details: %s", respBody)
	}
	var errResp map[string]string
	json.NewDecoder(strings.NewReader(respBody)).Decode(&errResp)
	if errResp["error"] != "mint failed" {
		t.Fatalf("expected generic 'mint failed', got: %s", errResp["error"])
	}
}

func TestHandler_RestrictedWorkflowFiles(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("ALLOWED_WORKFLOW_FILES", "dispatch.yml")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	pemAccessor := &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	}

	key, keyErr := rsa.GenerateKey(rand.Reader, 2048)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	kid := "test-key-1"

	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"issuer":   server.URL,
			"jwks_uri": server.URL + "/.well-known/jwks",
		})
	})
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{{
				"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
			}},
		})
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	verifier := NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL: server.URL,
		Audience:  os.Getenv("OIDC_AUDIENCE"),
	})
	h := mustNewHandler(t, pemAccessor, verifier)
	h.allowedWorkflowFiles = []string{"dispatch.yml"}

	signToken := func(workflowRef string) string {
		now := time.Now()
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
		claims := map[string]interface{}{
			"iss":              server.URL,
			"aud":              "fullsend-mint",
			"iat":              now.Unix(),
			"exp":              now.Add(10 * time.Minute).Unix(),
			"repository":       "test-org/.fullsend",
			"repository_owner": "test-org",
			"job_workflow_ref": workflowRef,
		}
		claimsJSON, _ := json.Marshal(claims)
		hB64 := base64.RawURLEncoding.EncodeToString(header)
		cB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
		input := hB64 + "." + cB64
		hashed := sha256.Sum256([]byte(input))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
		return input + "." + base64.RawURLEncoding.EncodeToString(sig)
	}

	// Allowed workflow should succeed at OIDC level (will fail at GitHub API since no mock)
	allowedToken := signToken("test-org/.fullsend/.github/workflows/dispatch.yml@refs/heads/main")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+allowedToken)
	h.ServeHTTP(rec, req)
	// Should pass OIDC but fail at GitHub API (502) since no mock
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("allowed workflow should not be rejected at OIDC level, got 401")
	}

	// Disallowed workflow should be rejected
	disallowedToken := signToken("test-org/.fullsend/.github/workflows/evil.yml@refs/heads/main")
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
	req2.Header.Set("Authorization", "Bearer "+disallowedToken)
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for disallowed workflow, got %d", rec2.Code)
	}
}

func TestHandler_PerRepoWIF_RestrictedWorkflows(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	// Clear ALLOWED_ORGS to prevent dual-enrollment upgrading to per-org mode.
	// This test exercises per-repo workflow host validation.
	t.Setenv("ALLOWED_ORGS", "")
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/custom-repo")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatal(err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})

	env.handler.oidcVerifier = NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL: env.issuerURL,
		Audience:  os.Getenv("OIDC_AUDIENCE"),
	})
	env.handler.allowedWorkflowFiles = []string{"ci.yml", "dispatch.yml"}
	env.handler.perRepoWIFRepos = map[string]bool{"test-org/custom-repo": true}
	env.handler.workflowHostRepos = map[string]bool{"test-org/custom-repo": true}

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/custom-repo/installation":
			json.NewEncoder(w).Encode(installationResponse{
				ID: 55555, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/55555/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_per_repo_token",
				ExpiresAt: "2026-06-02T12:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	// Allowed per-repo workflow should succeed through full ServeHTTP path
	allowedToken := env.signToken(t, map[string]interface{}{
		"repository":       "test-org/custom-repo",
		"repository_owner": "test-org",
		"job_workflow_ref": "test-org/custom-repo/.github/workflows/ci.yml@refs/heads/main",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(`{"role":"coder","repos":["custom-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+allowedToken)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for allowed per-repo workflow, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp mintResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Token != "ghs_per_repo_token" {
		t.Fatalf("expected ghs_per_repo_token, got %s", resp.Token)
	}

	// Disallowed per-repo workflow should be rejected
	disallowedToken := env.signToken(t, map[string]interface{}{
		"repository":       "test-org/custom-repo",
		"repository_owner": "test-org",
		"job_workflow_ref": "test-org/custom-repo/.github/workflows/evil.yml@refs/heads/main",
	})
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(`{"role":"coder","repos":["custom-repo"]}`))
	req2.Header.Set("Authorization", "Bearer "+disallowedToken)
	env.handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for disallowed per-repo workflow, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestHandler_UpstreamWorkflowRef(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatal(err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})

	env.handler.oidcVerifier = NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL: env.issuerURL,
		Audience:  os.Getenv("OIDC_AUDIENCE"),
	})
	env.handler.allowedWorkflowFiles = []string{"dispatch.yml"}

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/some-repo/installation":
			json.NewEncoder(w).Encode(installationResponse{
				ID: 55555, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/55555/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_upstream_token",
				ExpiresAt: "2026-06-02T12:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	token := env.signToken(t, map[string]interface{}{
		"repository":       "test-org/some-repo",
		"repository_owner": "test-org",
		"job_workflow_ref": "fullsend-ai/fullsend/.github/workflows/dispatch.yml@refs/heads/main",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token",
		strings.NewReader(`{"role":"coder","repos":["some-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for upstream workflow ref, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PublicMintMode(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "*")
	// Public mode is now * in PER_REPO_WIF_REPOS.
	// Public mode uses the same per-repo path with workflowHostRepos
	// and basename allowlist (ADR 0082 §2 revised 2026-08-05).
	t.Setenv("PER_REPO_WIF_REPOS", "*")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatal(err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})

	env.handler.oidcVerifier = NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL: env.issuerURL,
		Audience:  os.Getenv("OIDC_AUDIENCE"),
	})
	env.handler.allowedWorkflowFiles = []string{"dispatch.yml"}

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/other-org/some-repo/installation":
			json.NewEncoder(w).Encode(installationResponse{
				ID: 66666, Account: struct {
					Login string `json:"login"`
				}{Login: "other-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/66666/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_public_mint_token",
				ExpiresAt: "2026-06-02T12:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	// Use dispatch.yml which is in the allowed workflow files list;
	// public mode now enforces the basename allowlist.
	token := env.signToken(t, map[string]interface{}{
		"repository":       "other-org/some-repo",
		"repository_owner": "other-org",
		"job_workflow_ref": "fullsend-ai/fullsend/.github/workflows/dispatch.yml@refs/tags/v1.0.0",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token",
		strings.NewReader(`{"role":"coder","repos":["some-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for public mint upstream workflow, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PublicMintRejectsLegacyFullsendRef(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "*")
	t.Setenv("PER_REPO_WIF_REPOS", "*")

	env := newTestOIDCEnv(t, &fakePEMAccessor{})

	env.handler.oidcVerifier = NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL: env.issuerURL,
		Audience:  os.Getenv("OIDC_AUDIENCE"),
	})
	env.handler.allowedWorkflowFiles = []string{"*"}

	token := env.signToken(t, map[string]interface{}{
		"repository":       "other-org/.fullsend",
		"repository_owner": "other-org",
		"job_workflow_ref": "other-org/.fullsend/.github/workflows/dispatch.yml@refs/heads/main",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token",
		strings.NewReader(`{"role":"coder","repos":["some-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for legacy .fullsend ref in public mode, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PublicMintRejectsPerRepoSelfWorkflow(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "*")
	t.Setenv("PER_REPO_WIF_REPOS", "*")

	env := newTestOIDCEnv(t, &fakePEMAccessor{})

	env.handler.oidcVerifier = NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL: env.issuerURL,
		Audience:  os.Getenv("OIDC_AUDIENCE"),
	})
	env.handler.allowedWorkflowFiles = []string{"*"}

	token := env.signToken(t, map[string]interface{}{
		"repository":       "other-org/some-repo",
		"repository_owner": "other-org",
		"job_workflow_ref": "other-org/some-repo/.github/workflows/dispatch.yml@refs/heads/main",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token",
		strings.NewReader(`{"role":"coder","repos":["some-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for per-repo self-workflow in public mode, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PerRepoCrossRepoRef(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")

	env := newTestOIDCEnv(t, &fakePEMAccessor{})

	env.handler.oidcVerifier = NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL: env.issuerURL,
		Audience:  os.Getenv("OIDC_AUDIENCE"),
	})
	env.handler.allowedWorkflowFiles = []string{"dispatch.yml"}
	env.handler.perRepoWIFRepos = map[string]bool{"test-org/repo-a": true}

	token := env.signToken(t, map[string]interface{}{
		"repository":       "test-org/repo-b",
		"repository_owner": "test-org",
		"job_workflow_ref": "test-org/repo-a/.github/workflows/dispatch.yml@refs/heads/main",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token",
		strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for per-repo cross-repo workflow ref, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_NonWorkflowPath(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")

	env := newTestOIDCEnv(t, &fakePEMAccessor{})

	env.handler.oidcVerifier = NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL: env.issuerURL,
		Audience:  os.Getenv("OIDC_AUDIENCE"),
	})
	env.handler.allowedWorkflowFiles = []string{"*"}

	token := env.signToken(t, map[string]interface{}{
		"repository":       "test-org/.fullsend",
		"repository_owner": "test-org",
		"job_workflow_ref": "test-org/.fullsend/scripts/run.sh@refs/heads/main",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token",
		strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for non-workflow path, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PerRepoUnregistered(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")

	env := newTestOIDCEnv(t, &fakePEMAccessor{})

	env.handler.oidcVerifier = NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL: env.issuerURL,
		Audience:  os.Getenv("OIDC_AUDIENCE"),
	})
	env.handler.allowedWorkflowFiles = []string{"dispatch.yml"}
	env.handler.perRepoWIFRepos = map[string]bool{"test-org/registered-repo": true}

	token := env.signToken(t, map[string]interface{}{
		"repository":       "test-org/unregistered-repo",
		"repository_owner": "test-org",
		"job_workflow_ref": "test-org/unregistered-repo/.github/workflows/dispatch.yml@refs/heads/main",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token",
		strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unregistered per-repo WIF repo, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PerRepoMixedCase(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	// Clear ALLOWED_ORGS to prevent dual-enrollment upgrading to per-org mode.
	t.Setenv("ALLOWED_ORGS", "")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatal(err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})

	env.handler.oidcVerifier = NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL: env.issuerURL,
		Audience:  os.Getenv("OIDC_AUDIENCE"),
	})
	env.handler.allowedWorkflowFiles = []string{"ci.yml"}
	env.handler.perRepoWIFRepos = map[string]bool{"test-org/my-repo": true}
	env.handler.workflowHostRepos = map[string]bool{"test-org/my-repo": true}

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/My-Repo/installation":
			json.NewEncoder(w).Encode(installationResponse{
				ID: 55555, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/55555/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_mixed_case_token",
				ExpiresAt: "2026-06-02T12:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	token := env.signToken(t, map[string]interface{}{
		"repository":       "Test-Org/My-Repo",
		"repository_owner": "Test-Org",
		"job_workflow_ref": "Test-Org/My-Repo/.github/workflows/ci.yml@refs/heads/main",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token",
		strings.NewReader(`{"role":"coder","repos":["My-Repo"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for mixed-case per-repo WIF match, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_STSVerifier_PerRepoWIF_RestrictedWorkflows(t *testing.T) {
	// Clear ALLOWED_ORGS to prevent dual-enrollment upgrading to per-org mode.
	t.Setenv("ALLOWED_ORGS", "")
	t.Setenv("ALLOWED_ROLES", "coder")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	stsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "ya29.federated-token",
			"token_type":   "Bearer",
		})
	}))
	defer stsServer.Close()

	verifier := NewSTSVerifier(STSVerifierConfig{
		HTTPClient:         stsServer.Client(),
		STSURL:             stsServer.URL,
		GCPProjectNum:      "123456",
		WIFPoolName:        "fullsend-pool",
		DefaultWIFProvider: "github-oidc",
		OIDCAudience:       "fullsend-mint",
		PerRepoWIFRepos:    map[string]bool{"test-org/custom-repo": true},
	})
	h := mustNewHandler(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	}, verifier)
	h.allowedWorkflowFiles = []string{"ci.yml", "dispatch.yml"}
	h.perRepoWIFRepos = map[string]bool{"test-org/custom-repo": true}
	h.workflowHostRepos = map[string]bool{"test-org/custom-repo": true}

	buildToken := func(repo, workflowRef string) string {
		now := time.Now()
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
		claims := map[string]interface{}{
			"iss":              "https://token.actions.githubusercontent.com",
			"aud":              "fullsend-mint",
			"iat":              now.Unix(),
			"exp":              now.Add(10 * time.Minute).Unix(),
			"repository":       repo,
			"repository_owner": "test-org",
			"job_workflow_ref": workflowRef,
		}
		claimsJSON, _ := json.Marshal(claims)
		hB64 := base64.RawURLEncoding.EncodeToString(header)
		cB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
		return hB64 + "." + cB64 + ".fake-signature"
	}

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/custom-repo/installation":
			json.NewEncoder(w).Encode(installationResponse{
				ID: 55555, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/55555/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_sts_per_repo_token",
				ExpiresAt: "2026-06-02T12:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	h.githubBaseURL = github.URL

	// Allowed per-repo workflow via STSVerifier
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token",
		strings.NewReader(`{"role":"coder","repos":["custom-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+buildToken(
		"test-org/custom-repo",
		"test-org/custom-repo/.github/workflows/ci.yml@refs/heads/main"))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for allowed per-repo workflow via STS, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp mintResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Token != "ghs_sts_per_repo_token" {
		t.Fatalf("expected ghs_sts_per_repo_token, got %s", resp.Token)
	}

	// Disallowed per-repo workflow via STSVerifier
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/token",
		strings.NewReader(`{"role":"coder","repos":["custom-repo"]}`))
	req2.Header.Set("Authorization", "Bearer "+buildToken(
		"test-org/custom-repo",
		"test-org/custom-repo/.github/workflows/evil.yml@refs/heads/main"))
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for disallowed per-repo workflow via STS, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestHandler_LogsRequestedPermissionNotGranted(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})
	token := env.signToken(t, nil)

	// Simulate GitHub granting only a subset of the coder role's permissions:
	// contents and metadata are granted, but pull_requests, issues, and checks
	// are missing.
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/test-repo/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 12345, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/12345/access_tokens") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:       "ghs_partial",
				ExpiresAt:   "2026-05-06T12:00:00Z",
				Permissions: map[string]string{"contents": "write", "metadata": "read"},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	// Capture log output.
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	logs := logBuf.String()
	for _, perm := range []string{"pull_requests", "issues", "checks"} {
		expected := fmt.Sprintf("WARNING: requested permission not granted: %s=", perm)
		if !strings.Contains(logs, expected) {
			t.Errorf("expected log to contain %q, got:\n%s", expected, logs)
		}
	}
}

func TestHandler_SameOrgExplicitTargetOrg(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"coder": pemData}})
	token := env.signToken(t, nil)

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/test-repo/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 12345, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/12345/access_tokens") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_test_token",
				ExpiresAt: "2026-05-06T12:00:00Z",
				Permissions: map[string]string{
					"contents": "write", "pull_requests": "write", "issues": "write",
					"checks": "read", "metadata": "read",
				},
				Repositories:        []installationTokenRepository{{FullName: "test-org/test-repo"}},
				RepositorySelection: "selected",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"coder","target_org":"test-org","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_CrossOrgFullFlow(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,fullsend-ai")
	t.Setenv("ROLE_APP_IDS", `{"e2e":"300"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"e2e": pemData}})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "fullsend-ai/fullsend",
		"repository_owner": "fullsend-ai",
		"job_workflow_ref": "fullsend-ai/fullsend/.github/workflows/e2e.yml@refs/heads/main",
	})

	var tokenCalls int
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/orgs/pool-org/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 999, Account: struct {
					Login string `json:"login"`
				}{Login: "pool-org"},
			})
		case r.URL.Path == "/app/installations/999/access_tokens" && r.Method == http.MethodPost:
			tokenCalls++
			w.WriteHeader(http.StatusCreated)
			if tokenCalls == 1 {
				json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_policy_token"})
				return
			}
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:               "ghs_e2e_token",
				ExpiresAt:           "2026-05-06T12:00:00Z",
				Permissions:         map[string]string{"contents": "write", "metadata": "read"},
				RepositorySelection: "all",
			})
		case r.URL.Path == "/orgs/pool-org/actions/variables/FULLSEND_FOREIGN_E2E_REPOS" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(variableResponse{
				Name:  "FULLSEND_FOREIGN_E2E_REPOS",
				Value: "fullsend-ai/fullsend",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"e2e","target_org":"pool-org","repos":["*"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp mintResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Token != "ghs_e2e_token" {
		t.Fatalf("expected e2e token, got %q", resp.Token)
	}
}

func TestHandler_CrossOrgNonEmptyReposDenied(t *testing.T) {
	// Cross-org with specific repos is now allowed by validateReposScope
	// (repo-level FOREIGN grants), but still denied if neither org-level
	// nor repo-level FOREIGN variables authorize the caller.
	t.Setenv("ALLOWED_ORGS", "test-org,fullsend-ai")
	t.Setenv("ROLE_APP_IDS", `{"e2e":"300"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"e2e": pemData}})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "fullsend-ai/fullsend",
		"repository_owner": "fullsend-ai",
		"job_workflow_ref": "fullsend-ai/fullsend/.github/workflows/e2e.yml@refs/heads/main",
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/orgs/pool-org/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 999, Account: struct {
					Login string `json:"login"`
				}{Login: "pool-org"},
			})
		case r.URL.Path == "/app/installations/999/access_tokens" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_policy_token"})
		case r.URL.Path == "/orgs/pool-org/actions/variables/FULLSEND_FOREIGN_E2E_REPOS" && r.Method == http.MethodGet:
			// Org-level variable does not authorize this caller.
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/repos/pool-org/e2e-lock/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 998, Account: struct {
					Login string `json:"login"`
				}{Login: "pool-org"},
			})
		case r.URL.Path == "/app/installations/998/access_tokens" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_repo_policy"})
		case r.URL.Path == "/repos/pool-org/e2e-lock/actions/variables/FULLSEND_FOREIGN_E2E_REPOS" && r.Method == http.MethodGet:
			// Repo-level variable not set either.
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"e2e","target_org":"pool-org","repos":["e2e-lock"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_ForeignAllowlistCached(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,fullsend-ai")
	t.Setenv("ROLE_APP_IDS", `{"e2e":"300"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"e2e": pemData}})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "fullsend-ai/fullsend",
		"repository_owner": "fullsend-ai",
		"job_workflow_ref": "fullsend-ai/fullsend/.github/workflows/e2e.yml@refs/heads/main",
	})

	var foreignReads int
	var tokenCalls int
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/orgs/pool-org/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 999, Account: struct {
					Login string `json:"login"`
				}{Login: "pool-org"},
			})
		case r.URL.Path == "/app/installations/999/access_tokens" && r.Method == http.MethodPost:
			tokenCalls++
			w.WriteHeader(http.StatusCreated)
			if tokenCalls%2 == 1 {
				json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_policy_token"})
				return
			}
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_e2e_token",
				ExpiresAt: "2026-05-06T12:00:00Z",
			})
		case r.URL.Path == "/orgs/pool-org/actions/variables/FULLSEND_FOREIGN_E2E_REPOS" && r.Method == http.MethodGet:
			foreignReads++
			json.NewEncoder(w).Encode(variableResponse{
				Name:  "FULLSEND_FOREIGN_E2E_REPOS",
				Value: "fullsend-ai/fullsend",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"e2e","target_org":"pool-org","repos":["*"]}`
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		env.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if foreignReads != 1 {
		t.Fatalf("expected 1 FOREIGN variable read (cached), got %d", foreignReads)
	}
}

func TestHandler_ForeignAllowlistConcurrent(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,fullsend-ai")
	t.Setenv("ROLE_APP_IDS", `{"e2e":"300"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"e2e": pemData}})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "fullsend-ai/fullsend",
		"repository_owner": "fullsend-ai",
		"job_workflow_ref": "fullsend-ai/fullsend/.github/workflows/e2e.yml@refs/heads/main",
	})

	var foreignReads int
	var tokenCalls int
	var mu sync.Mutex
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/orgs/pool-org/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 999, Account: struct {
					Login string `json:"login"`
				}{Login: "pool-org"},
			})
		case r.URL.Path == "/app/installations/999/access_tokens" && r.Method == http.MethodPost:
			mu.Lock()
			tokenCalls++
			call := tokenCalls
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			if call%2 == 1 {
				json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_policy_token"})
				return
			}
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:     "ghs_e2e_token",
				ExpiresAt: "2026-05-06T12:00:00Z",
			})
		case r.URL.Path == "/orgs/pool-org/actions/variables/FULLSEND_FOREIGN_E2E_REPOS" && r.Method == http.MethodGet:
			mu.Lock()
			foreignReads++
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			json.NewEncoder(w).Encode(variableResponse{
				Name:  "FULLSEND_FOREIGN_E2E_REPOS",
				Value: "fullsend-ai/fullsend",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"e2e","target_org":"pool-org","repos":["*"]}`
	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			env.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()

	if foreignReads != 1 {
		t.Fatalf("expected 1 concurrent FOREIGN variable read, got %d", foreignReads)
	}
}

func TestHandler_CrossOrgForeignVariableMissing(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,fullsend-ai")
	t.Setenv("ROLE_APP_IDS", `{"e2e":"300"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"e2e": pemData}})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "fullsend-ai/fullsend",
		"repository_owner": "fullsend-ai",
		"job_workflow_ref": "fullsend-ai/fullsend/.github/workflows/e2e.yml@refs/heads/main",
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/orgs/pool-org/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 999, Account: struct {
					Login string `json:"login"`
				}{Login: "pool-org"},
			})
		case r.URL.Path == "/app/installations/999/access_tokens" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_policy_token"})
		case r.URL.Path == "/orgs/pool-org/actions/variables/FULLSEND_FOREIGN_E2E_REPOS" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"e2e","target_org":"pool-org","repos":["*"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_CrossOrgForeignDenied(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,evil-org")
	t.Setenv("ROLE_APP_IDS", `{"e2e":"300"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"e2e": pemData}})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "evil-org/.fullsend",
		"repository_owner": "evil-org",
		"job_workflow_ref": "evil-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/orgs/pool-org/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{ID: 999, Account: struct {
				Login string `json:"login"`
			}{Login: "pool-org"}})
		case r.URL.Path == "/app/installations/999/access_tokens" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_policy_token"})
		case r.URL.Path == "/orgs/pool-org/actions/variables/FULLSEND_FOREIGN_E2E_REPOS" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(variableResponse{
				Name:  "FULLSEND_FOREIGN_E2E_REPOS",
				Value: "fullsend-ai/fullsend",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"e2e","target_org":"pool-org","repos":["*"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Repo-level foreign allow-list tests (ADR 0083) ---

func TestHandler_RepoLevelForeignGrant_CrossOrg(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,fullsend-ai")
	t.Setenv("ROLE_APP_IDS", `{"coder":"400"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"coder": pemData}})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "fullsend-ai/fullsend",
		"repository_owner": "fullsend-ai",
		"job_workflow_ref": "fullsend-ai/fullsend/.github/workflows/code.yml@refs/heads/main",
	})

	var tokenCalls int
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// Repo-level foreign: variable set → authorized.
		// Org-level endpoints are NOT called for repo-scoped requests.
		case r.URL.Path == "/repos/target-org/target-repo/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 801, Account: struct {
					Login string `json:"login"`
				}{Login: "target-org"},
			})
		case r.URL.Path == "/app/installations/801/access_tokens" && r.Method == http.MethodPost:
			tokenCalls++
			w.WriteHeader(http.StatusCreated)
			if tokenCalls <= 1 {
				// Policy token for reading repo variable.
				json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_repo_policy"})
			} else {
				// Mint token for the actual scoped access.
				json.NewEncoder(w).Encode(installationTokenResponse{
					Token:               "ghs_coder_token",
					ExpiresAt:           "2026-08-06T12:00:00Z",
					Permissions:         map[string]string{"contents": "write"},
					RepositorySelection: "selected",
					Repositories: []installationTokenRepository{
						{FullName: "target-org/target-repo"},
					},
				})
			}
		case r.URL.Path == "/repos/target-org/target-repo/actions/variables/FULLSEND_FOREIGN_CODER_REPOS" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(variableResponse{
				Name:  "FULLSEND_FOREIGN_CODER_REPOS",
				Value: "fullsend-ai/fullsend",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"coder","target_org":"target-org","repos":["target-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp mintResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Token != "ghs_coder_token" {
		t.Fatalf("expected coder token, got %q", resp.Token)
	}
}

func TestHandler_RepoLevelForeignGrant_Denied(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,evil-org")
	t.Setenv("ROLE_APP_IDS", `{"coder":"400"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"coder": pemData}})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "evil-org/.fullsend",
		"repository_owner": "evil-org",
		"job_workflow_ref": "evil-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// Repo-level foreign: variable authorizes fullsend-ai/fullsend, not evil-org.
		// Org-level endpoints are NOT called for repo-scoped requests.
		case r.URL.Path == "/repos/target-org/target-repo/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 801, Account: struct {
					Login string `json:"login"`
				}{Login: "target-org"},
			})
		case r.URL.Path == "/app/installations/801/access_tokens" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_repo_policy"})
		case r.URL.Path == "/repos/target-org/target-repo/actions/variables/FULLSEND_FOREIGN_CODER_REPOS" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(variableResponse{
				Name:  "FULLSEND_FOREIGN_CODER_REPOS",
				Value: "fullsend-ai/fullsend",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"coder","target_org":"target-org","repos":["target-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_RepoLevelForeignGrant_OrgLevelForInstallationWide(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,fullsend-ai")
	t.Setenv("ROLE_APP_IDS", `{"e2e":"300"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"e2e": pemData}})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "fullsend-ai/fullsend",
		"repository_owner": "fullsend-ai",
		"job_workflow_ref": "fullsend-ai/fullsend/.github/workflows/e2e.yml@refs/heads/main",
	})

	var tokenCalls int
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/orgs/pool-org/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 999, Account: struct {
					Login string `json:"login"`
				}{Login: "pool-org"},
			})
		case r.URL.Path == "/app/installations/999/access_tokens" && r.Method == http.MethodPost:
			tokenCalls++
			w.WriteHeader(http.StatusCreated)
			if tokenCalls == 1 {
				json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_policy_token"})
				return
			}
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:               "ghs_e2e_token",
				ExpiresAt:           "2026-08-06T12:00:00Z",
				Permissions:         map[string]string{"contents": "write"},
				RepositorySelection: "all",
			})
		case r.URL.Path == "/orgs/pool-org/actions/variables/FULLSEND_FOREIGN_E2E_REPOS" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(variableResponse{
				Name:  "FULLSEND_FOREIGN_E2E_REPOS",
				Value: "fullsend-ai/fullsend",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"e2e","target_org":"pool-org","repos":["*"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_IntraOrgRepoForeignGrant(t *testing.T) {
	// Caller is per-repo enrolled only; not in ALLOWED_ORGS.
	t.Setenv("ALLOWED_ORGS", "other-org")
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/caller-repo")
	t.Setenv("ROLE_APP_IDS", `{"coder":"400"}`)
	t.Setenv("WORKFLOW_HOST_REPOS", "test-org/caller-repo")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"coder": pemData}})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "test-org/caller-repo",
		"repository_owner": "test-org",
		"job_workflow_ref": "test-org/caller-repo/.github/workflows/code.yml@refs/heads/main",
	})

	var tokenCalls int
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/target-repo/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 900, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case r.URL.Path == "/app/installations/900/access_tokens" && r.Method == http.MethodPost:
			tokenCalls++
			w.WriteHeader(http.StatusCreated)
			if tokenCalls == 1 {
				json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_repo_policy"})
			} else {
				json.NewEncoder(w).Encode(installationTokenResponse{
					Token:               "ghs_coder_token",
					ExpiresAt:           "2026-08-06T12:00:00Z",
					Permissions:         map[string]string{"contents": "write"},
					RepositorySelection: "selected",
					Repositories: []installationTokenRepository{
						{FullName: "test-org/target-repo"},
					},
				})
			}
		case r.URL.Path == "/repos/test-org/target-repo/actions/variables/FULLSEND_FOREIGN_CODER_REPOS" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(variableResponse{
				Name:  "FULLSEND_FOREIGN_CODER_REPOS",
				Value: "test-org/caller-repo",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"coder","repos":["target-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp mintResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Token != "ghs_coder_token" {
		t.Fatalf("expected coder token, got %q", resp.Token)
	}
}

func TestHandler_IntraOrgRepoForeignGrant_PartialDenied(t *testing.T) {
	// Per-repo caller requests two intra-org repos, but only one has a
	// repo-level FOREIGN grant. All-or-nothing semantics should deny.
	t.Setenv("ALLOWED_ORGS", "other-org")
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/caller-repo")
	t.Setenv("ROLE_APP_IDS", `{"coder":"400"}`)
	t.Setenv("WORKFLOW_HOST_REPOS", "test-org/caller-repo")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"coder": pemData}})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "test-org/caller-repo",
		"repository_owner": "test-org",
		"job_workflow_ref": "test-org/caller-repo/.github/workflows/code.yml@refs/heads/main",
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// authorized-repo: has FOREIGN grant for the caller.
		case r.URL.Path == "/repos/test-org/authorized-repo/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 900, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case r.URL.Path == "/app/installations/900/access_tokens" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_auth_policy"})
		case r.URL.Path == "/repos/test-org/authorized-repo/actions/variables/FULLSEND_FOREIGN_CODER_REPOS" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(variableResponse{
				Name:  "FULLSEND_FOREIGN_CODER_REPOS",
				Value: "test-org/caller-repo",
			})
		// unauthorized-repo: no FOREIGN grant.
		case r.URL.Path == "/repos/test-org/unauthorized-repo/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 901, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case r.URL.Path == "/app/installations/901/access_tokens" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_unauth_policy"})
		case r.URL.Path == "/repos/test-org/unauthorized-repo/actions/variables/FULLSEND_FOREIGN_CODER_REPOS" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"coder","repos":["authorized-repo","unauthorized-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for partial intra-org grant, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_RepoLevelForeignGrant_ScopeRestriction(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,fullsend-ai")
	t.Setenv("ROLE_APP_IDS", `{"coder":"400"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"coder": pemData}})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "fullsend-ai/fullsend",
		"repository_owner": "fullsend-ai",
		"job_workflow_ref": "fullsend-ai/fullsend/.github/workflows/code.yml@refs/heads/main",
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// Org-level endpoints are NOT called for repo-scoped requests.
		case r.URL.Path == "/repos/target-org/other-repo/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 802, Account: struct {
					Login string `json:"login"`
				}{Login: "target-org"},
			})
		case r.URL.Path == "/app/installations/802/access_tokens" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_other_policy"})
		case r.URL.Path == "/repos/target-org/other-repo/actions/variables/FULLSEND_FOREIGN_CODER_REPOS" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"coder","target_org":"target-org","repos":["other-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_OrgLevelForeignDoesNotAuthorizeRepoScoped verifies the disjoint
// authorization boundary from ADR 0083: an org-level FOREIGN grant does NOT
// authorize cross-org requests with specific repos when no repo-level grant
// exists on the target repo.
func TestHandler_OrgLevelForeignDoesNotAuthorizeRepoScoped(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,fullsend-ai")
	t.Setenv("ROLE_APP_IDS", `{"coder":"400"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{"coder": pemData}})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "fullsend-ai/fullsend",
		"repository_owner": "fullsend-ai",
		"job_workflow_ref": "fullsend-ai/fullsend/.github/workflows/code.yml@refs/heads/main",
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// Repo-level lookup for target-repo: no FOREIGN variable set.
		case r.URL.Path == "/repos/target-org/target-repo/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(installationResponse{
				ID: 801, Account: struct {
					Login string `json:"login"`
				}{Login: "target-org"},
			})
		case r.URL.Path == "/app/installations/801/access_tokens" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{Token: "ghs_repo_policy"})
		case r.URL.Path == "/repos/target-org/target-repo/actions/variables/FULLSEND_FOREIGN_CODER_REPOS" && r.Method == http.MethodGet:
			// No repo-level variable — 404.
			w.WriteHeader(http.StatusNotFound)
		// Org-level endpoints should NOT be called for repo-scoped requests.
		case r.URL.Path == "/orgs/target-org/installation":
			t.Errorf("org-level installation lookup should not be called for repo-scoped request")
			w.WriteHeader(http.StatusNotFound)
		case strings.HasPrefix(r.URL.Path, "/orgs/target-org/actions/variables/"):
			t.Errorf("org-level variable lookup should not be called for repo-scoped request")
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	env.handler.githubBaseURL = github.URL

	body := `{"role":"coder","target_org":"target-org","repos":["target-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (org-level FOREIGN should not authorize repo-scoped request), got %d: %s", rec.Code, rec.Body.String())
	}
}
