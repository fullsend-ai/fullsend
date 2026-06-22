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

func testAllowedOrgs() []string {
	var orgs []string
	for _, entry := range strings.Split(os.Getenv("ALLOWED_ORGS"), ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			orgs = append(orgs, trimmed)
		}
	}
	return orgs
}

func (f *fakePEMAccessor) AccessPEM(_ context.Context, role string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := PemSecretRole(role)
	data, ok := f.pems[key]
	if !ok {
		return nil, fmt.Errorf("PEM not found for %s", key)
	}
	return data, nil
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

	var allowedOrgs []string
	for _, entry := range strings.Split(os.Getenv("ALLOWED_ORGS"), ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			allowedOrgs = append(allowedOrgs, trimmed)
		}
	}

	verifier := NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:            env.server.URL,
		Audience:             os.Getenv("OIDC_AUDIENCE"),
		AllowedOrgs:          allowedOrgs,
		AllowedWorkflowFiles: []string{"*"},
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
		"iss":              e.issuerURL,
		"aud":              "fullsend-mint",
		"iat":              now.Unix(),
		"exp":              now.Add(10 * time.Minute).Unix(),
		"repository":       "test-org/.fullsend",
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

func TestHandler_EmptyRepos(t *testing.T) {
	t.Setenv("ALLOWED_ROLES", "coder")
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{})

	body := `{"role":"coder"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if !strings.Contains(resp["error"], "repos is required") {
		t.Fatalf("expected repos required error, got: %s", resp["error"])
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

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})
	token := env.signToken(t, nil)

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
		"repository":       "other-org/.fullsend",
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
		"repository":       "org-a/.fullsend",
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
		AllowedOrgs:        []string{"test-org"},
		AllowedWorkflows:   []string{"*"},
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
		"repository":       "test-org/.fullsend",
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
		AllowedOrgs:        []string{"test-org"},
		AllowedWorkflows:   []string{"dispatch.yml"},
		OIDCAudience:       "fullsend-mint",
	})
	h := mustNewHandler(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	}, verifier)

	buildToken := func(workflowRef string) string {
		now := time.Now()
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
		claims := map[string]interface{}{
			"iss":              "https://token.actions.githubusercontent.com",
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
		"repository":       "org-a/.fullsend",
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
		IssuerURL:            server.URL,
		Audience:             os.Getenv("OIDC_AUDIENCE"),
		AllowedOrgs:          []string{os.Getenv("ALLOWED_ORGS")},
		AllowedWorkflowFiles: []string{"dispatch.yml"},
	})
	h := mustNewHandler(t, pemAccessor, verifier)

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
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/custom-repo")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatal(err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})

	env.handler.oidcVerifier = NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:            env.issuerURL,
		Audience:             os.Getenv("OIDC_AUDIENCE"),
		AllowedOrgs:          testAllowedOrgs(),
		AllowedWorkflowFiles: []string{"ci.yml", "dispatch.yml"},
		PerRepoWIFRepos:      map[string]bool{"test-org/custom-repo": true},
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/test-repo/installation":
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
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
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
	req2 := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
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
		IssuerURL:            env.issuerURL,
		Audience:             os.Getenv("OIDC_AUDIENCE"),
		AllowedOrgs:          testAllowedOrgs(),
		AllowedWorkflowFiles: []string{"dispatch.yml"},
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/test-repo/installation":
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
		strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for upstream workflow ref, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PerRepoCrossRepoRef(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")

	env := newTestOIDCEnv(t, &fakePEMAccessor{})

	env.handler.oidcVerifier = NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:            env.issuerURL,
		Audience:             os.Getenv("OIDC_AUDIENCE"),
		AllowedOrgs:          testAllowedOrgs(),
		AllowedWorkflowFiles: []string{"dispatch.yml"},
		PerRepoWIFRepos:      map[string]bool{"test-org/repo-a": true},
	})

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
		IssuerURL:            env.issuerURL,
		Audience:             os.Getenv("OIDC_AUDIENCE"),
		AllowedOrgs:          testAllowedOrgs(),
		AllowedWorkflowFiles: []string{"*"},
	})

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
		IssuerURL:            env.issuerURL,
		Audience:             os.Getenv("OIDC_AUDIENCE"),
		AllowedOrgs:          testAllowedOrgs(),
		AllowedWorkflowFiles: []string{"dispatch.yml"},
		PerRepoWIFRepos:      map[string]bool{"test-org/registered-repo": true},
	})

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
	t.Setenv("ALLOWED_ORGS", "test-org")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatal(err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	})

	env.handler.oidcVerifier = NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:            env.issuerURL,
		Audience:             os.Getenv("OIDC_AUDIENCE"),
		AllowedOrgs:          testAllowedOrgs(),
		AllowedWorkflowFiles: []string{"ci.yml"},
		PerRepoWIFRepos:      map[string]bool{"test-org/my-repo": true},
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/test-repo/installation":
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
		strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for mixed-case per-repo WIF match, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_STSVerifier_PerRepoWIF_RestrictedWorkflows(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org")
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
		AllowedOrgs:        []string{"test-org"},
		AllowedWorkflows:   []string{"ci.yml", "dispatch.yml"},
		OIDCAudience:       "fullsend-mint",
		PerRepoWIFRepos:    map[string]bool{"test-org/custom-repo": true},
	})
	h := mustNewHandler(t, &fakePEMAccessor{
		pems: map[string][]byte{"coder": pemData},
	}, verifier)

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
		case r.URL.Path == "/repos/test-org/test-repo/installation":
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
		strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
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
		strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
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
