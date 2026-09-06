package mintcore

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cfAccessTestEnv holds the test infrastructure for CF Access JWT
// validation tests.
type cfAccessTestEnv struct {
	key     *rsa.PrivateKey
	kid     string
	team    string
	aud     string
	server  *httptest.Server
	handler *Handler
}

func newCFAccessTestEnv(t *testing.T) *cfAccessTestEnv {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	kid := "cf-test-key-1"
	team := "testteam"
	aud := "test-cf-access-aud-1234"

	// Serve the CF Access JWKS endpoint.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cdn-cgi/access/certs" {
			json.NewEncoder(w).Encode(map[string]any{
				"keys": []map[string]string{
					{
						"kty": "RSA", "alg": "RS256", "use": "sig",
						"kid": kid,
						"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
						"e":   base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	// Override mintHTTP to redirect CF Access JWKS requests to the test
	// server. The test server URL replaces the real
	// https://<team>.cloudflareaccess.com origin.
	SetMintHTTPForTest(t, func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Host, team+".cloudflareaccess.com") {
			req.URL.Scheme = "http"
			req.URL.Host = strings.TrimPrefix(server.URL, "http://")
		}
		return http.DefaultClient.Do(req)
	})

	// Configure CF Access consts.
	StatusCFAccessAud = aud
	StatusCFAccessTeam = team
	t.Cleanup(func() {
		StatusCFAccessAud = ""
		StatusCFAccessTeam = ""
	})

	// Reset JWKS cache so tests don't interfere with each other.
	resetCFAccessKeysForTest(t)

	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "alpha-org,beta-org")

	env := newTestOIDCEnv(t, &fakePEMAccessor{})

	return &cfAccessTestEnv{
		key:     key,
		kid:     kid,
		team:    team,
		aud:     aud,
		server:  server,
		handler: env.handler,
	}
}

func (e *cfAccessTestEnv) signCFAccessToken(t *testing.T, overrides map[string]any) string {
	t.Helper()

	header, _ := json.Marshal(map[string]string{
		"alg": "RS256",
		"typ": "JWT",
		"kid": e.kid,
	})
	now := time.Now()
	claims := map[string]any{
		"iss":   "https://" + e.team + ".cloudflareaccess.com",
		"aud":   e.aud,
		"iat":   now.Unix(),
		"exp":   now.Add(10 * time.Minute).Unix(),
		"email": "alice@example.com",
		"type":  "app",
		"sub":   "cf-access-sub-1234",
	}
	for k, v := range overrides {
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

func resetCFAccessKeysForTest(t *testing.T) {
	t.Helper()
	cfAccessKeys.mu.Lock()
	cfAccessKeys.keys = nil
	cfAccessKeys.fetchedAt = time.Time{}
	cfAccessKeys.lastKidMissAt = time.Time{}
	cfAccessKeys.mu.Unlock()
}

func TestStatusCFAccess_ValidToken_Authenticated(t *testing.T) {
	env := newCFAccessTestEnv(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", env.signCFAccessToken(t, nil))
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp statusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Non-OIDC auth should report all allowed orgs, not a single org.
	if resp.Org != "" {
		t.Fatalf("non-OIDC auth should not set org, got %q", resp.Org)
	}
	if len(resp.AllowedOrgs) != 2 {
		t.Fatalf("expected 2 allowed orgs, got %v", resp.AllowedOrgs)
	}
	if len(resp.Roles) == 0 {
		t.Fatal("expected roles in response")
	}
}

func TestStatusCFAccess_ExpiredToken_Returns401(t *testing.T) {
	env := newCFAccessTestEnv(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", env.signCFAccessToken(t, map[string]any{
		"iat": time.Now().Add(-2 * time.Hour).Unix(),
		"exp": time.Now().Add(-1 * time.Hour).Unix(),
	}))
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStatusCFAccess_ExpiryBoundary_ClockSkew(t *testing.T) {
	// Test the boundary condition: a token whose exp equals exactly
	// now-maxClockSkew is considered expired (<=), while exp one second
	// later is valid. This matches the pattern in JWKSVerifier.
	env := newCFAccessTestEnv(t)

	now := time.Now()
	skew := int64(maxClockSkew.Seconds())

	t.Run("exp at boundary is expired", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
		req.Header.Set("Cf-Access-Jwt-Assertion", env.signCFAccessToken(t, map[string]any{
			"iat": now.Add(-10 * time.Minute).Unix(),
			"exp": now.Unix() - skew, // exactly at the boundary
		}))
		env.handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for exp at clock-skew boundary, got %d: %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("exp one second past boundary is valid", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
		req.Header.Set("Cf-Access-Jwt-Assertion", env.signCFAccessToken(t, map[string]any{
			"iat": now.Add(-10 * time.Minute).Unix(),
			"exp": now.Unix() - skew + 1, // one second past the boundary
		}))
		env.handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for exp one second past clock-skew boundary, got %d: %s",
				rec.Code, rec.Body.String())
		}
	})
}

func TestStatusCFAccess_WrongAudience_Returns401(t *testing.T) {
	env := newCFAccessTestEnv(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", env.signCFAccessToken(t, map[string]any{
		"aud": "wrong-audience",
	}))
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStatusCFAccess_WrongIssuer_Returns401(t *testing.T) {
	env := newCFAccessTestEnv(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", env.signCFAccessToken(t, map[string]any{
		"iss": "https://evil.cloudflareaccess.com",
	}))
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStatusCFAccess_NoHeader_Returns401(t *testing.T) {
	env := newCFAccessTestEnv(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	// No Cf-Access-Jwt-Assertion header and no valid OIDC.
	req.Header.Set("Authorization", "Bearer not-a-valid-oidc-jwt")
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStatusCFAccess_NotConfigured_Returns401(t *testing.T) {
	// When StatusCFAccessAud/Team are empty, the validator returns
	// errStatusAuthSkip. Without OIDC or GitHub, this yields 401.
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")

	StatusCFAccessAud = ""
	StatusCFAccessTeam = ""

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", "some-token")
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStatusCFAccess_OIDCSuccess_BypassesCFAccess(t *testing.T) {
	// When OIDC succeeds, the CF Access validator should NOT be called.
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")

	StatusCFAccessAud = "test-cf-aud"
	StatusCFAccessTeam = "testteam"
	t.Cleanup(func() {
		StatusCFAccessAud = ""
		StatusCFAccessTeam = ""
	})

	// Set up mintHTTP to fail loudly if CF Access JWKS is fetched.
	SetMintHTTPForTest(t, func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/cdn-cgi/access/certs") {
			t.Error("CF Access JWKS should not be fetched when OIDC succeeds")
		}
		return http.DefaultClient.Do(req)
	})

	oidcEnv := newTestOIDCEnv(t, &fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+oidcEnv.signToken(t, nil))
	req.Header.Set("Cf-Access-Jwt-Assertion", "some-cf-access-token")
	oidcEnv.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp statusResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Org != "test-org" {
		t.Fatalf("expected OIDC org-scoped response, got org=%q", resp.Org)
	}
}

func TestStatusCFAccess_ValidateDirectCall(t *testing.T) {
	// Test the validator function directly for edge cases.
	env := newCFAccessTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", env.signCFAccessToken(t, nil))

	err := validateStatusCFAccess(req.Context(), req)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestStatusCFAccess_MissingIat_Returns401(t *testing.T) {
	env := newCFAccessTestEnv(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", env.signCFAccessToken(t, map[string]any{
		"iat": 0,
	}))
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStatusCFAccess_FutureToken_Returns401(t *testing.T) {
	env := newCFAccessTestEnv(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", env.signCFAccessToken(t, map[string]any{
		"iat": time.Now().Add(2 * time.Hour).Unix(),
		"exp": time.Now().Add(3 * time.Hour).Unix(),
	}))
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStatusCFAccess_JWKSEndpointError_Returns401(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_ORGS", "test-org")

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	kid := "cf-err-key"
	team := "errteam"
	aud := "test-aud-err"

	StatusCFAccessAud = aud
	StatusCFAccessTeam = team
	t.Cleanup(func() {
		StatusCFAccessAud = ""
		StatusCFAccessTeam = ""
	})
	resetCFAccessKeysForTest(t)

	// JWKS endpoint returns 500.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	SetMintHTTPForTest(t, func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Host, team+".cloudflareaccess.com") {
			req.URL.Scheme = "http"
			req.URL.Host = strings.TrimPrefix(server.URL, "http://")
		}
		return http.DefaultClient.Do(req)
	})

	// Sign a token manually.
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	now := time.Now()
	claims := map[string]any{
		"iss": "https://" + team + ".cloudflareaccess.com",
		"aud": aud, "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
		"email": "bob@example.com", "type": "app", "sub": "sub-1",
	}
	claimsJSON, _ := json.Marshal(claims)
	hB64 := base64.RawURLEncoding.EncodeToString(header)
	cB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	input := hB64 + "." + cB64
	hashed := sha256.Sum256([]byte(input))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
	rawJWT := input + "." + base64.RawURLEncoding.EncodeToString(sig)

	env := newTestOIDCEnv(t, &fakePEMAccessor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", rawJWT)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when JWKS endpoint fails, got %d: %s",
			rec.Code, rec.Body.String())
	}
}

func TestStatusCFAccess_UnknownKid_Returns401(t *testing.T) {
	env := newCFAccessTestEnv(t)

	// Sign a token with a kid not in the JWKS.
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "unknown-kid"})
	now := time.Now()
	claims := map[string]any{
		"iss":   "https://" + env.team + ".cloudflareaccess.com",
		"aud":   env.aud,
		"iat":   now.Unix(),
		"exp":   now.Add(10 * time.Minute).Unix(),
		"email": "charlie@example.com",
		"type":  "app",
		"sub":   "sub-2",
	}
	claimsJSON, _ := json.Marshal(claims)
	hB64 := base64.RawURLEncoding.EncodeToString(header)
	cB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	input := hB64 + "." + cB64
	hashed := sha256.Sum256([]byte(input))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, env.key, crypto.SHA256, hashed[:])
	rawJWT := input + "." + base64.RawURLEncoding.EncodeToString(sig)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", rawJWT)
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unknown kid, got %d: %s",
			rec.Code, rec.Body.String())
	}
}

func TestStatusCFAccess_InvalidJWTFormat_Returns401(t *testing.T) {
	env := newCFAccessTestEnv(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", "not-a-jwt")
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid JWT format, got %d: %s",
			rec.Code, rec.Body.String())
	}
}

func TestStatusCFAccess_GHTokenAlone_DoesNotSatisfyCFAccess(t *testing.T) {
	// Verify that a GH_TOKEN in the Authorization header does not satisfy
	// CF Access — the validator looks at Cf-Access-Jwt-Assertion, not
	// Authorization. Per the issue: "GH_TOKEN alone does not satisfy Access."
	env := newCFAccessTestEnv(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer gh-token-value")
	// No Cf-Access-Jwt-Assertion header.
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when only GH_TOKEN is present, got %d: %s",
			rec.Code, rec.Body.String())
	}
}
