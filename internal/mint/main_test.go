package function

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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
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

func (f *fakePEMAccessor) AccessPEM(_ context.Context, org, role string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := org + "/" + role
	data, ok := f.pems[key]
	if !ok {
		return nil, fmt.Errorf("PEM not found for %s", key)
	}
	return data, nil
}

// testOIDCEnv sets up a mock OIDC server and returns a handler with the
// OIDCVerifier pointing at it, along with a function to sign JWTs.
type testOIDCEnv struct {
	handler  *Handler
	server   *httptest.Server
	key      *rsa.PrivateKey
	kid      string
	issuerURL string
}

func newTestOIDCEnv(t *testing.T, pemAccessor mintcore.PEMAccessor) *testOIDCEnv {
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

	h := NewHandler(pemAccessor)
	h.oidcVerifier = mintcore.NewJWKSVerifier(env.server.URL, h.oidcAudience, nil)
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
	h := NewHandler(&fakePEMAccessor{})
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
	t.Setenv("ROLE_APP_IDS", `{"test-org/triage":"100","test-org/coder":"200"}`)
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
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Orgs) == 0 {
		t.Fatal("expected orgs in response")
	}
	if len(resp.Roles) == 0 {
		t.Fatal("expected roles in response")
	}
}

func TestHandler_StatusEndpoint_NoAuth(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/token", nil)
	req.Header.Set("Authorization", "Bearer dummy-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandler_NotFound(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/wrong/path", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandler_RootPathAccepted(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer dummy-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET on /, got %d", rec.Code)
	}
}

func TestHandler_MissingAuthHeader(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader("{}"))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHandler_InvalidJSON(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader("not-json"))
	req.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandler_MissingRole(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{})
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
	h := NewHandler(&fakePEMAccessor{})

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

func TestHandler_RoleNotAllowed(t *testing.T) {
	t.Setenv("ALLOWED_ROLES", "triage,coder")
	t.Setenv("ROLE_APP_IDS", `{"test-org/triage":"100","test-org/coder":"200"}`)
	h := NewHandler(&fakePEMAccessor{})

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
	t.Setenv("ROLE_APP_IDS", `{"test-org/coder":"200"}`)
	h := NewHandler(&fakePEMAccessor{})

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
	t.Setenv("ROLE_APP_IDS", `{"test-org/coder":"200"}`)
	h := NewHandler(&fakePEMAccessor{})

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
	t.Setenv("ROLE_APP_IDS", `{"test-org/coder":"200"}`)
	h := NewHandler(&fakePEMAccessor{})

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

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
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

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestHandler_SecretAccessError(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"test-org/coder":"200"}`)
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
	t.Setenv("ROLE_APP_IDS", `{"test-org/coder":"200"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	pemAccessor := &fakePEMAccessor{
		pems: map[string][]byte{"test-org/coder": pemData},
	}
	env := newTestOIDCEnv(t, pemAccessor)
	token := env.signToken(t, nil)

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/test-repo/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(mintcore.InstallationResponse{
				ID: 12345, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/12345/access_tokens") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(mintcore.InstallationTokenResponse{
				Token:     "ghs_test_token",
				ExpiresAt: "2026-05-06T12:00:00Z",
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
}

func TestHandler_FullFlowWithRepos(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"test-org/coder":"200"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"test-org/coder": pemData},
	})
	token := env.signToken(t, nil)

	var capturedTokenReq map[string]interface{}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/my-repo/installation":
			json.NewEncoder(w).Encode(mintcore.InstallationResponse{
				ID: 1, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			reqBody, _ := io.ReadAll(r.Body)
			json.Unmarshal(reqBody, &capturedTokenReq)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(mintcore.InstallationTokenResponse{
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
	t.Setenv("ROLE_APP_IDS", `{"test-org/coder":"200"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"test-org/coder": pemData},
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
	h := NewHandler(&fakePEMAccessor{})
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
	t.Setenv("ROLE_APP_IDS", `{"test-org/triage":"100","test-org/coder":"200","test-org/review":"300"}`)
	h := NewHandler(&fakePEMAccessor{})

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
	h := NewHandler(&fakePEMAccessor{})
	if h.checkAllowedRole("coder") {
		t.Fatal("should fail closed when no roles configured")
	}
}

func TestLookupRoleAppID(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"test-org/triage":"100","test-org/coder":"200"}`)
	h := NewHandler(&fakePEMAccessor{})

	id, err := h.lookupRoleAppID("test-org", "coder")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "200" {
		t.Fatalf("expected 200, got %s", id)
	}

	_, err = h.lookupRoleAppID("test-org", "deploy")
	if err == nil {
		t.Fatal("expected error for unknown role")
	}

	_, err = h.lookupRoleAppID("other-org", "coder")
	if err == nil {
		t.Fatal("expected error for wrong org")
	}
}

func TestLookupRoleAppID_NotSet(t *testing.T) {
	t.Setenv("ALLOWED_ROLES", "")
	t.Setenv("ROLE_APP_IDS", "")
	h := NewHandler(&fakePEMAccessor{})

	_, err := h.lookupRoleAppID("test-org", "coder")
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
	t.Setenv("ROLE_APP_IDS", `{"test-org/triage":"100","test-org/coder":"200","test-org/review":"300","test-org/fix":"400","test-org/fullsend":"500","other-org/triage":"100","other-org/coder":"200","other-org/review":"300","other-org/fix":"400","other-org/fullsend":"500"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"other-org/coder": pemData},
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
			json.NewEncoder(w).Encode(mintcore.InstallationResponse{
				ID: 99999, Account: struct {
					Login string `json:"login"`
				}{Login: "other-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/99999/access_tokens") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(mintcore.InstallationTokenResponse{
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
	t.Setenv("ROLE_APP_IDS", `{"org-a/retro":"999","org-b/retro":"999"}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"org-a/retro": pemData},
	})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "org-a/.fullsend",
		"repository_owner": "org-a",
		"job_workflow_ref": "org-a/.fullsend/.github/workflows/retro.yml@refs/heads/main",
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/org-a/seshi/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(mintcore.InstallationResponse{
				ID: 77777, Account: struct {
					Login string `json:"login"`
				}{Login: "org-b"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/77777/access_tokens") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(mintcore.InstallationTokenResponse{
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

func TestHandler_CrossOrgInstallation_SameOrgPasses(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "org-a,org-b")
	t.Setenv("GCP_PROJECT_NUMBER", "123456")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("ROLE_APP_IDS", `{"org-a/retro":"999","org-b/retro":"999"}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	env := newTestOIDCEnv(t, &fakePEMAccessor{
		pems: map[string][]byte{"org-a/retro": pemData},
	})
	token := env.signToken(t, map[string]interface{}{
		"repository":       "org-a/.fullsend",
		"repository_owner": "org-a",
		"job_workflow_ref": "org-a/.fullsend/.github/workflows/retro.yml@refs/heads/main",
	})

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/org-a/seshi/installation" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(mintcore.InstallationResponse{
				ID: 88888, Account: struct {
					Login string `json:"login"`
				}{Login: "org-a"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/88888/access_tokens") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(mintcore.InstallationTokenResponse{
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
