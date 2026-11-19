package mintcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFailingIssuer returns an issuer whose discovery document is served
// by discovery and whose JWKS endpoint is served by jwks. A nil handler
// serves the healthy response for a key set that has no keys.
func newFailingIssuer(t *testing.T, discovery, jwks http.HandlerFunc) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if discovery != nil {
			discovery(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{
			"issuer":   srv.URL,
			"jwks_uri": srv.URL + "/.well-known/jwks",
		})
	})
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
		if jwks != nil {
			jwks(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func respondStatus(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }
}

// stall answers only after the request is abandoned, like an issuer that
// does not respond within the mint's request budget.
func stall(w http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
	}
}

func TestJWKSVerifier_KeyFetchFailureIsKeysUnavailable(t *testing.T) {
	tests := []struct {
		name      string
		discovery http.HandlerFunc
		jwks      http.HandlerFunc
		closed    bool
		timeout   time.Duration
	}{
		{name: "discovery 503", discovery: respondStatus(http.StatusServiceUnavailable)},
		{name: "discovery 522", discovery: respondStatus(522)},
		{name: "discovery 429", discovery: respondStatus(http.StatusTooManyRequests)},
		{name: "jwks 500", jwks: respondStatus(http.StatusInternalServerError)},
		{name: "discovery timeout", discovery: stall, timeout: 100 * time.Millisecond},
		{name: "jwks timeout", jwks: stall, timeout: 100 * time.Millisecond},
		{name: "issuer unreachable", closed: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issuer := newFailingIssuer(t, tc.discovery, tc.jwks)
			v, err := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: issuer.URL})
			require.NoError(t, err)
			token := newTestOIDCServer(t).signJWT(t, nil, map[string]any{"iss": issuer.URL})
			if tc.closed {
				issuer.Close()
			}
			ctx := t.Context()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}

			_, err = v.Verify(ctx, token)
			require.Error(t, err)
			assert.ErrorIs(t, err, errOIDCKeysUnavailable)
		})
	}
}

func TestJWKSVerifier_DefiniteFailuresAreNotKeysUnavailable(t *testing.T) {
	s := newTestOIDCServer(t)
	v, err := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: s.server.URL})
	require.NoError(t, err)
	valid := s.signJWT(t, nil, nil)

	tests := map[string]string{
		"invalid signature": valid[:len(valid)-4] + "XXXX",
		"wrong audience":    s.signJWT(t, nil, map[string]any{"aud": "other"}),
		"expired":           s.signJWT(t, nil, map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}),
		"unknown kid":       s.signJWT(t, map[string]any{"kid": "unknown-key"}, nil),
		"malformed":         "not-a-jwt",
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := v.Verify(t.Context(), token)
			require.Error(t, err)
			assert.NotErrorIs(t, err, errOIDCKeysUnavailable)
		})
	}

	// Answers from the issuer that are definite, not transient, stay
	// ordinary failures.
	for name, discovery := range map[string]http.HandlerFunc{
		"discovery 404": respondStatus(http.StatusNotFound),
		"discovery not JSON": func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, "<html>")
		},
	} {
		t.Run(name, func(t *testing.T) {
			issuer := newFailingIssuer(t, discovery, nil)
			v, err := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: issuer.URL})
			require.NoError(t, err)
			_, err = v.Verify(t.Context(), s.signJWT(t, nil, map[string]any{"iss": issuer.URL}))
			require.Error(t, err)
			assert.NotErrorIs(t, err, errOIDCKeysUnavailable)
		})
	}
}

// oidcAuthRequest builds a request to path that authenticates with token
// and is otherwise valid, so the response reflects the OIDC outcome.
func oidcAuthRequest(path, token string) *http.Request {
	var req *http.Request
	switch path {
	case "/v1/status":
		req = httptest.NewRequest(http.MethodGet, path, nil)
	case botRolePath:
		req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(botRoleBody))
	default:
		req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

var oidcAuthPaths = []string{"/v1/token", "/v1/status", botRolePath}

func assertOIDCAuthResponse(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantMessage string) {
	t.Helper()
	require.Equal(t, wantStatus, rec.Code, "body: %s", rec.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, wantMessage, body["error"])
}

func TestHandler_OIDCKeyFetchFailure_Returns503(t *testing.T) {
	signer := newTestOIDCEnv(t, &fakePEMAccessor{})
	for _, failure := range []struct {
		name      string
		discovery http.HandlerFunc
		jwks      http.HandlerFunc
	}{
		{name: "discovery 5xx", discovery: respondStatus(http.StatusBadGateway)},
		{name: "jwks 5xx", jwks: respondStatus(http.StatusServiceUnavailable)},
	} {
		issuer := newFailingIssuer(t, failure.discovery, failure.jwks)
		verifier, err := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: issuer.URL})
		require.NoError(t, err)
		h := mustNewHandler(t, &fakePEMAccessor{}, verifier)
		token := signer.signToken(t, map[string]any{"iss": issuer.URL})
		for _, path := range oidcAuthPaths {
			t.Run(failure.name+" "+path, func(t *testing.T) {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, oidcAuthRequest(path, token))
				assertOIDCAuthResponse(t, rec, http.StatusServiceUnavailable, oidcKeysUnavailableMessage)
			})
		}
	}
}

func TestHandler_OIDCVerifierErrors_StatusByKind(t *testing.T) {
	tests := []struct {
		name        string
		verifier    *fakeOIDCVerifier
		wantStatus  int
		wantMessage string
	}{
		{name: "keys unavailable",
			verifier:   &fakeOIDCVerifier{err: fmt.Errorf("getting signing key: %w: fetching JWKS: context deadline exceeded", errOIDCKeysUnavailable)},
			wantStatus: http.StatusServiceUnavailable, wantMessage: oidcKeysUnavailableMessage},
		{name: "invalid signature", verifier: &fakeOIDCVerifier{err: errors.New("invalid JWT signature")},
			wantStatus: http.StatusUnauthorized, wantMessage: "authentication failed"},
		{name: "wrong audience", verifier: &fakeOIDCVerifier{err: errors.New("audience mismatch")},
			wantStatus: http.StatusUnauthorized, wantMessage: "authentication failed"},
		{name: "expired", verifier: &fakeOIDCVerifier{err: errors.New("token expired")},
			wantStatus: http.StatusUnauthorized, wantMessage: "authentication failed"},
		{name: "unclassified", verifier: &fakeOIDCVerifier{err: errors.New("something went wrong")},
			wantStatus: http.StatusUnauthorized, wantMessage: "authentication failed"},
		{name: "unknown repository", verifier: &fakeOIDCVerifier{claims: &Claims{
			Issuer:          "https://token.actions.githubusercontent.com",
			Repository:      "evil-org/test-repo",
			RepositoryOwner: "evil-org",
			JobWorkflowRef:  "fullsend-ai/fullsend/.github/workflows/code.yml@refs/heads/main",
		}}, wantStatus: http.StatusUnauthorized, wantMessage: "authentication failed"},
	}
	for _, tc := range tests {
		h := mustNewHandler(t, &fakePEMAccessor{}, tc.verifier)
		for _, path := range oidcAuthPaths {
			t.Run(tc.name+" "+path, func(t *testing.T) {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, oidcAuthRequest(path, "header.claims.signature"))
				assertOIDCAuthResponse(t, rec, tc.wantStatus, tc.wantMessage)
			})
		}
	}
}

func TestStatusAuth_KeysUnavailable_IsNotAcceptedByFallback(t *testing.T) {
	h := mustNewHandler(t, &fakePEMAccessor{}, &fakeOIDCVerifier{
		err: fmt.Errorf("%w: STS returned status 503", errOIDCKeysUnavailable),
	})
	auth, err := h.authenticateStatus(t.Context(), oidcAuthRequest("/v1/status", "header.claims.signature"))
	assert.Nil(t, auth)
	assert.ErrorIs(t, err, errOIDCKeysUnavailable)
}
