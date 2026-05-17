package function

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
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

func makeTestOIDCToken(iss, aud, repo, owner, jobWorkflowRef string, exp int64) string {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]interface{}{
		"iss":              iss,
		"aud":              aud,
		"iat":              time.Now().Unix(),
		"exp":              exp,
		"repository":       repo,
		"repository_owner": owner,
		"job_workflow_ref": jobWorkflowRef,
	}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	return headerB64 + "." + claimsB64 + ".fakesig"
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

type fakeTokenValidator struct {
	err          error
	lastProvider string
}

func (f *fakeTokenValidator) Validate(_ context.Context, _ string, providerName string) error {
	f.lastProvider = providerName
	return f.err
}

func validOIDCToken() string {
	return makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/.fullsend",
		"test-org",
		"test-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)
}

func TestAudienceUnmarshalJSON(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		var a audience
		if err := json.Unmarshal([]byte(`"fullsend-mint"`), &a); err != nil {
			t.Fatalf("unmarshal string: %v", err)
		}
		if len(a) != 1 || a[0] != "fullsend-mint" {
			t.Fatalf("expected [fullsend-mint], got %v", a)
		}
	})

	t.Run("array", func(t *testing.T) {
		var a audience
		if err := json.Unmarshal([]byte(`["aud1","aud2"]`), &a); err != nil {
			t.Fatalf("unmarshal array: %v", err)
		}
		if len(a) != 2 || a[0] != "aud1" || a[1] != "aud2" {
			t.Fatalf("expected [aud1 aud2], got %v", a)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		var a audience
		if err := json.Unmarshal([]byte(`123`), &a); err == nil {
			t.Fatal("expected error for non-string/array value")
		}
	})

	t.Run("contains", func(t *testing.T) {
		a := audience{"a", "b", "c"}
		if !a.contains("b") {
			t.Fatal("should contain b")
		}
		if a.contains("d") {
			t.Fatal("should not contain d")
		}
	})
}

func TestPrevalidateOIDCToken_FutureIssuedAt(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	futureIAT := time.Now().Add(5 * time.Minute).Unix()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]interface{}{
		"iss":              "https://token.actions.githubusercontent.com",
		"aud":              "fullsend-mint",
		"exp":              time.Now().Add(10 * time.Minute).Unix(),
		"iat":              futureIAT,
		"repository":       "test-org/.fullsend",
		"repository_owner": "test-org",
		"job_workflow_ref": "test-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
	}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	token := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON) + ".fakesig"

	_, err := h.prevalidateOIDCToken(token)
	if err == nil {
		t.Fatal("expected error for future-issued token")
	}
	if !strings.Contains(err.Error(), "issued in the future") {
		t.Fatalf("expected 'issued in the future' error, got: %v", err)
	}
}

func TestPrevalidateOIDCToken_MissingIssuedAt(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]interface{}{
		"iss":              "https://token.actions.githubusercontent.com",
		"aud":              "fullsend-mint",
		"exp":              time.Now().Add(10 * time.Minute).Unix(),
		"repository":       "test-org/.fullsend",
		"repository_owner": "test-org",
		"job_workflow_ref": "test-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
	}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	token := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON) + ".fakesig"

	_, err := h.prevalidateOIDCToken(token)
	if err == nil {
		t.Fatal("expected error for token missing iat claim")
	}
	if !strings.Contains(err.Error(), "missing iat") {
		t.Fatalf("expected 'missing iat' error, got: %v", err)
	}
}

func TestHandler_HealthEndpoint(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	for _, path := range []string{"/health"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: expected 200, got %d", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("GET %s: expected application/json, got %s", path, ct)
		}
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/token", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandler_NotFound(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/wrong/path", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandler_RootPathAccepted(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	// Root path is accepted (method check comes next).
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET on /, got %d", rec.Code)
	}
}

func TestHandler_MissingAuthHeader(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader("{}"))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHandler_InvalidJSON(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader("not-json"))
	req.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandler_MissingRole(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})
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
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

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
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	body := `{"role":"deploy"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestHandler_RoleAllowed(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"test-org/coder":"200"}`)
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	oidcToken := validOIDCToken()

	// Should pass role validation and fail at STS (no mock).
	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

	// Should not be 403 with "role not allowed".
	if rec.Code == http.StatusForbidden {
		var resp map[string]string
		json.NewDecoder(rec.Body).Decode(&resp)
		if strings.Contains(resp["error"], "role") {
			t.Fatal("allowed role was rejected")
		}
	}
}

func TestHandler_InvalidRepoName(t *testing.T) {
	t.Setenv("ALLOWED_ROLES", "coder")
	t.Setenv("ROLE_APP_IDS", `{"test-org/coder":"200"}`)
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

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
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

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
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

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
}

func TestHandler_OIDCPrevalidation_BadIssuer(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	oidcToken := makeTestOIDCToken(
		"https://evil.example.com",
		"fullsend-mint",
		"test-org/.fullsend",
		"test-org",
		"test-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestHandler_OIDCPrevalidation_ExpiredToken(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/.fullsend",
		"test-org",
		"test-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
		time.Now().Add(-10*time.Minute).Unix(),
	)

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestHandler_OIDCPrevalidation_WrongOrg(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"evil-org/.fullsend",
		"evil-org",
		"evil-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestHandler_OIDCPrevalidation_BadAudience(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"wrong-audience",
		"test-org/.fullsend",
		"test-org",
		"test-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestHandler_OIDCPrevalidation_MissingJobWorkflowRef(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	// Token without job_workflow_ref.
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]interface{}{
		"iss":              "https://token.actions.githubusercontent.com",
		"aud":              "fullsend-mint",
		"exp":              time.Now().Add(10 * time.Minute).Unix(),
		"repository":       "test-org/.fullsend",
		"repository_owner": "test-org",
	}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	oidcToken := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON) + ".fakesig"

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestHandler_OIDCPrevalidation_UpstreamWorkflowRef(t *testing.T) {
	setMintEnv(t)
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	// When reusable workflows are invoked via workflow_call, the OIDC
	// job_workflow_ref reflects fullsend-ai/fullsend, not {org}/.fullsend.
	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/.fullsend",
		"test-org",
		"fullsend-ai/fullsend/.github/workflows/reusable-code.yml@refs/tags/v1",
		time.Now().Add(10*time.Minute).Unix(),
	)

	claims, err := h.prevalidateOIDCToken(oidcToken)
	if err != nil {
		t.Fatalf("prevalidation should accept upstream workflow ref: %v", err)
	}
	if claims.RepositoryOwner != "test-org" {
		t.Fatalf("expected owner test-org, got %s", claims.RepositoryOwner)
	}
}

func TestHandler_OIDCPrevalidation_UpstreamNonWorkflowPath(t *testing.T) {
	setMintEnv(t)
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/.fullsend",
		"test-org",
		"fullsend-ai/fullsend/scripts/evil.sh@refs/tags/v1",
		time.Now().Add(10*time.Minute).Unix(),
	)

	_, err := h.prevalidateOIDCToken(oidcToken)
	if err == nil {
		t.Fatal("prevalidation should reject upstream non-workflow path")
	}
	if !strings.Contains(err.Error(), "does not reference a workflow file") {
		t.Fatalf("expected workflow file error, got: %v", err)
	}
}

func TestHandler_OIDCPrevalidation_BadJobWorkflowRef(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/some-repo",
		"test-org",
		"test-org/some-repo/.github/workflows/malicious.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestHandler_OIDCPrevalidation_PerRepoWorkflowRef(t *testing.T) {
	setMintEnv(t)
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/my-service")
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/my-service",
		"test-org",
		"test-org/my-service/.github/workflows/gh-classify.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)

	claims, err := h.prevalidateOIDCToken(oidcToken)
	if err != nil {
		t.Fatalf("prevalidation should accept registered per-repo workflow ref: %v", err)
	}
	if claims.Repository != "test-org/my-service" {
		t.Fatalf("expected repository test-org/my-service, got %s", claims.Repository)
	}
}

func TestHandler_OIDCPrevalidation_PerRepoUnregistered(t *testing.T) {
	setMintEnv(t)
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/unregistered-repo",
		"test-org",
		"test-org/unregistered-repo/.github/workflows/steal-tokens.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)

	_, err := h.prevalidateOIDCToken(oidcToken)
	if err == nil {
		t.Fatal("prevalidation should reject unregistered per-repo workflow ref")
	}
	if !strings.Contains(err.Error(), "registered per-repo repo") {
		t.Fatalf("expected per-repo rejection error, got: %v", err)
	}
}

func TestHandler_OIDCPrevalidation_PerRepoNonWorkflowPath(t *testing.T) {
	setMintEnv(t)
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/my-service")
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/my-service",
		"test-org",
		"test-org/my-service/scripts/evil.sh@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)

	_, err := h.prevalidateOIDCToken(oidcToken)
	if err == nil {
		t.Fatal("prevalidation should reject per-repo non-workflow path")
	}
	if !strings.Contains(err.Error(), "does not reference a workflow file") {
		t.Fatalf("expected workflow file error, got: %v", err)
	}
}

func TestHandler_OIDCPrevalidation_PerRepoCrossRepoRef(t *testing.T) {
	setMintEnv(t)
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/my-service,test-org/other-repo")
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/my-service",
		"test-org",
		"test-org/other-repo/.github/workflows/evil.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)

	_, err := h.prevalidateOIDCToken(oidcToken)
	if err == nil {
		t.Fatal("prevalidation should reject per-repo token with cross-repo job_workflow_ref")
	}
}

func TestHandler_OIDCPrevalidation_PerRepoMixedCase(t *testing.T) {
	setMintEnv(t)
	t.Setenv("PER_REPO_WIF_REPOS", "Test-Org/My-Service")
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/my-service",
		"test-org",
		"test-org/my-service/.github/workflows/gh-classify.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)

	claims, err := h.prevalidateOIDCToken(oidcToken)
	if err != nil {
		t.Fatalf("prevalidation should accept per-repo ref with case-insensitive matching: %v", err)
	}
	if claims.Repository != "test-org/my-service" {
		t.Fatalf("expected repository test-org/my-service, got %s", claims.Repository)
	}
}

func TestHandler_OIDCPrevalidation_NonWorkflowPath(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/.fullsend",
		"test-org",
		"test-org/.fullsend/scripts/evil.sh@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-workflow path, got %d", rec.Code)
	}
}

func TestHandler_OIDCPrevalidation_WorkflowAllowlist(t *testing.T) {
	t.Setenv("ALLOWED_WORKFLOW_FILES", "dispatch.yml,code.yml")
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	// Allowed workflow — prevalidation must succeed.
	allowedToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/.fullsend",
		"test-org",
		"test-org/.fullsend/.github/workflows/dispatch.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)
	claims, err := h.prevalidateOIDCToken(allowedToken)
	if err != nil {
		t.Fatalf("prevalidation should pass for allowed workflow: %v", err)
	}
	if claims.RepositoryOwner != "test-org" {
		t.Fatalf("expected owner test-org, got %s", claims.RepositoryOwner)
	}

	// Disallowed workflow — prevalidation must reject.
	disallowedToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/.fullsend",
		"test-org",
		"test-org/.fullsend/.github/workflows/malicious.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)
	_, err = h.prevalidateOIDCToken(disallowedToken)
	if err == nil {
		t.Fatal("prevalidation should reject disallowed workflow")
	}
	if !strings.Contains(err.Error(), "not in allowed list") {
		t.Fatalf("expected 'not in allowed list' error, got: %v", err)
	}
}

func TestHandler_OIDCPrevalidation_WorkflowAllowlistUnset(t *testing.T) {
	t.Setenv("ALLOWED_WORKFLOW_FILES", "")
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	token := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/.fullsend",
		"test-org",
		"test-org/.fullsend/.github/workflows/dispatch.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)
	_, err := h.prevalidateOIDCToken(token)
	if err == nil {
		t.Fatal("prevalidation should reject when ALLOWED_WORKFLOW_FILES is unset")
	}
	if !strings.Contains(err.Error(), "not in allowed list") {
		t.Fatalf("expected 'not in allowed list' error, got: %v", err)
	}
}

func TestHandler_OIDCValidationFailure(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"test-org/coder":"200"}`)
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{err: fmt.Errorf("STS returned status 403")})

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+validOIDCToken())
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if strings.Contains(resp["error"], "STS") {
		t.Fatalf("error message leaks internal details: %s", resp["error"])
	}
}

func TestHandler_SecretAccessError(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"test-org/coder":"200"}`)
	h := NewHandler(&fakePEMAccessor{err: fmt.Errorf("access denied")}, &fakeTokenValidator{})

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+validOIDCToken())
	h.ServeHTTP(rec, req)

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

	oidcToken := validOIDCToken()

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
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()

	pemAccessor := &fakePEMAccessor{
		pems: map[string][]byte{
			"test-org/coder": pemData,
		},
	}

	h := NewHandler(pemAccessor, &fakeTokenValidator{})
	h.githubBaseURL = github.URL

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

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

	oidcToken := validOIDCToken()

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

	h := NewHandler(&fakePEMAccessor{
		pems: map[string][]byte{
			"test-org/coder": pemData,
		},
	}, &fakeTokenValidator{})
	h.githubBaseURL = github.URL

	body := `{"role":"coder","repos":["my-repo","other-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

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

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer github.Close()

	h := NewHandler(&fakePEMAccessor{
		pems: map[string][]byte{
			"test-org/coder": pemData,
		},
	}, &fakeTokenValidator{})
	h.githubBaseURL = github.URL

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+validOIDCToken())
	h.ServeHTTP(rec, req)

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
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})
	largePayload := bytes.Repeat([]byte("x"), 128<<10)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", bytes.NewReader(largePayload))
	req.Header.Set("Authorization", "Bearer token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestGenerateAppJWT(t *testing.T) {
	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	jwt, err := generateAppJWT("12345", pemData)
	if err != nil {
		t.Fatalf("generating JWT: %v", err)
	}

	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decoding header: %v", err)
	}
	var header map[string]string
	json.Unmarshal(headerBytes, &header)
	if header["alg"] != "RS256" {
		t.Errorf("expected alg=RS256, got %s", header["alg"])
	}

	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decoding claims: %v", err)
	}
	var claims map[string]interface{}
	json.Unmarshal(claimsBytes, &claims)
	if claims["iss"] != "12345" {
		t.Errorf("expected iss=12345, got %v", claims["iss"])
	}
}

func TestGenerateAppJWT_InvalidPEM(t *testing.T) {
	_, err := generateAppJWT("123", []byte("not-a-pem"))
	if err == nil {
		t.Fatal("expected error for invalid PEM")
	}
}

func TestCheckAllowedRole(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"test-org/triage":"100","test-org/coder":"200","test-org/review":"300"}`)
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

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
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})
	if h.checkAllowedRole("coder") {
		t.Fatal("should fail closed when no roles configured")
	}
}

func TestLookupRoleAppID(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"test-org/triage":"100","test-org/coder":"200"}`)
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

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
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

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

func TestPrevalidateOIDCToken_InvalidFormat(t *testing.T) {
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	_, err := h.prevalidateOIDCToken("not.a.valid.jwt.token")
	if err == nil {
		t.Fatal("expected error for invalid JWT format")
	}

	_, err = h.prevalidateOIDCToken("single-segment")
	if err == nil {
		t.Fatal("expected error for single segment")
	}
}

func TestCheckAllowedOrg(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		org      string
		want     bool
	}{
		{"match", "acme,widgetco", "acme", true},
		{"case insensitive", "acme,widgetco", "ACME", true},
		{"second entry", "acme,widgetco", "widgetco", true},
		{"not in list", "acme,widgetco", "evil", false},
		{"empty env", "", "acme", false},
		{"single org match", "acme", "acme", true},
		{"single org no match", "acme", "evil", false},
		{"whitespace trimmed", " acme , widgetco ", "acme", true},
		{"trailing comma", "acme,", "acme", true},
		{"trailing comma no match empty", "acme,", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ALLOWED_ORGS", tc.envValue)
			h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})
			if got := h.checkAllowedOrg(tc.org); got != tc.want {
				t.Fatalf("checkAllowedOrg(%q) = %v, want %v", tc.org, got, tc.want)
			}
		})
	}
}

func TestHandler_MultiOrg_FullFlow(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,other-org")
	t.Setenv("GCP_PROJECT_NUMBER", "123456")
	t.Setenv("WIF_POOL_NAME", "test-pool")
	t.Setenv("WIF_PROVIDER_NAME", "github-oidc")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("ROLE_APP_IDS", `{"test-org/triage":"100","test-org/coder":"200","test-org/review":"300","test-org/fix":"400","test-org/fullsend":"500","other-org/triage":"100","other-org/coder":"200","other-org/review":"300","other-org/fix":"400","other-org/fullsend":"500"}`)

	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"other-org/.fullsend",
		"other-org",
		"other-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)

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

	pemAccessor := &fakePEMAccessor{
		pems: map[string][]byte{
			"other-org/coder": pemData,
		},
	}

	h := NewHandler(pemAccessor, &fakeTokenValidator{})
	h.githubBaseURL = github.URL

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

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

func TestHandler_MultiOrg_WrongOrg(t *testing.T) {
	t.Setenv("ALLOWED_ORGS", "test-org,other-org")
	t.Setenv("GCP_PROJECT_NUMBER", "123456")
	t.Setenv("WIF_POOL_NAME", "test-pool")
	t.Setenv("WIF_PROVIDER_NAME", "github-oidc")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("ROLE_APP_IDS", `{"test-org/triage":"100","test-org/coder":"200","test-org/review":"300","test-org/fix":"400","test-org/fullsend":"500","other-org/triage":"100","other-org/coder":"200","other-org/review":"300","other-org/fix":"400","other-org/fullsend":"500"}`)

	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"evil-org/.fullsend",
		"evil-org",
		"evil-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)

	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for org not in ALLOWED_ORGS, got %d", rec.Code)
	}
}

func TestResolveWIFProvider(t *testing.T) {
	h := &Handler{
		defaultWIFProvider: "github-oidc",
		perRepoWIFRepos:    map[string]bool{"acme-corp/my-service": true},
	}

	t.Run("dotFullsend uses default provider", func(t *testing.T) {
		got := h.resolveWIFProvider("acme-corp/.fullsend")
		if got != "github-oidc" {
			t.Fatalf("expected github-oidc, got %s", got)
		}
	})

	t.Run("registered per-repo uses dynamic provider", func(t *testing.T) {
		got := h.resolveWIFProvider("acme-corp/my-service")
		want := buildRepoProviderID("acme-corp", "my-service")
		if got != want {
			t.Fatalf("expected %s, got %s", want, got)
		}
	})

	t.Run("unregistered repo falls back to default", func(t *testing.T) {
		got := h.resolveWIFProvider("acme-corp/other-repo")
		if got != "github-oidc" {
			t.Fatalf("expected github-oidc for unregistered repo, got %s", got)
		}
	})

	t.Run("unparseable falls back to default", func(t *testing.T) {
		got := h.resolveWIFProvider("no-slash")
		if got != "github-oidc" {
			t.Fatalf("expected github-oidc for unparseable repo, got %s", got)
		}
	})

	t.Run("case-insensitive lookup", func(t *testing.T) {
		got := h.resolveWIFProvider("Acme-Corp/My-Service")
		want := buildRepoProviderID("Acme-Corp", "My-Service")
		if got != want {
			t.Fatalf("expected %s, got %s", want, got)
		}
	})
}

// SYNC: these test cases must match TestBuildRepoProviderID in
// internal/dispatch/gcf/provisioner_test.go to catch divergence.
func TestBuildRepoProviderID(t *testing.T) {
	tests := []struct {
		owner, repo string
		want        string
	}{
		{"acme", "widget", "gh-acme-widget"},
		{"Acme", "My.Repo_v2", "gh-acme-my-repo-v2"},
		{"org", "very-long-repository-name-that-exceeds-limit", "gh-org-very-long-repository-name"},
		{"a", "b", "gh-a-b"},
		{"nonflux", "integration-service", "gh-nonflux-integration-service"},
		{"halfsend", "test-repo", "gh-halfsend-test-repo"},
	}
	for _, tt := range tests {
		t.Run(tt.owner+"/"+tt.repo, func(t *testing.T) {
			got := buildRepoProviderID(tt.owner, tt.repo)
			if got != tt.want {
				t.Fatalf("buildRepoProviderID(%q, %q) = %q, want %q", tt.owner, tt.repo, got, tt.want)
			}
			if len(got) < 4 || len(got) > 32 {
				t.Fatalf("provider ID %q length %d outside 4-32 range", got, len(got))
			}
			if got[len(got)-1] == '-' {
				t.Fatalf("provider ID %q ends with hyphen", got)
			}
		})
	}
}

func TestPrevalidateOIDCToken_MissingRepository(t *testing.T) {
	setMintEnv(t)
	h := NewHandler(&fakePEMAccessor{}, &fakeTokenValidator{})

	// Build a token with empty repository field.
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]interface{}{
		"iss":              "https://token.actions.githubusercontent.com",
		"aud":              "fullsend-mint",
		"iat":              time.Now().Unix(),
		"exp":              time.Now().Add(10 * time.Minute).Unix(),
		"repository":       "",
		"repository_owner": "test-org",
		"job_workflow_ref": "test-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
	}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	token := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON) + ".fakesig"

	_, err := h.prevalidateOIDCToken(token)
	if err == nil || !strings.Contains(err.Error(), "missing repository claim") {
		t.Fatalf("expected 'missing repository claim' error, got: %v", err)
	}
}

func TestServeHTTP_PerRepoProvider(t *testing.T) {
	setMintEnv(t)
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/integration-service")
	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatal(err)
	}
	tv := &fakeTokenValidator{}
	h := NewHandler(
		&fakePEMAccessor{pems: map[string][]byte{"test-org/coder": pemData}},
		tv,
	)

	// Token from a per-repo install (not .fullsend).
	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/integration-service",
		"test-org",
		"test-org/.fullsend/.github/workflows/code.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

	// The fake validator succeeds, so the request proceeds to minting.
	// We verify the correct provider was resolved.
	want := buildRepoProviderID("test-org", "integration-service")
	if tv.lastProvider != want {
		t.Fatalf("expected provider %q, got %q", want, tv.lastProvider)
	}
}

func TestServeHTTP_PerRepoDirectWorkflow(t *testing.T) {
	setMintEnv(t)
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/integration-service")
	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatal(err)
	}
	tv := &fakeTokenValidator{}
	h := NewHandler(
		&fakePEMAccessor{pems: map[string][]byte{"test-org/coder": pemData}},
		tv,
	)

	// Token from a per-repo workflow_dispatch — job_workflow_ref references the
	// target repo itself, not .fullsend or fullsend-ai/fullsend.
	oidcToken := makeTestOIDCToken(
		"https://token.actions.githubusercontent.com",
		"fullsend-mint",
		"test-org/integration-service",
		"test-org",
		"test-org/integration-service/.github/workflows/gh-classify.yml@refs/heads/main",
		time.Now().Add(10*time.Minute).Unix(),
	)

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

	want := buildRepoProviderID("test-org", "integration-service")
	if tv.lastProvider != want {
		t.Fatalf("expected provider %q, got %q", want, tv.lastProvider)
	}
}

func TestServeHTTP_DotFullsendProvider(t *testing.T) {
	setMintEnv(t)
	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatal(err)
	}
	tv := &fakeTokenValidator{}
	h := NewHandler(
		&fakePEMAccessor{pems: map[string][]byte{"test-org/coder": pemData}},
		tv,
	)

	oidcToken := validOIDCToken()

	body := `{"role":"coder","repos":["test-repo"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	h.ServeHTTP(rec, req)

	if tv.lastProvider != "github-oidc" {
		t.Fatalf("expected provider %q for .fullsend repo, got %q", "github-oidc", tv.lastProvider)
	}
}
