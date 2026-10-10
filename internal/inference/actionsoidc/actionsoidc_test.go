package actionsoidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	require.NoError(t, err)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + enc.EncodeToString(b) + ".sig"
}

func newServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestParseJWTLifetime(t *testing.T) {
	tok := testJWT(t, map[string]any{"iat": 1000, "exp": 1300})
	iat, exp, err := ParseJWTLifetime(tok)
	require.NoError(t, err)
	assert.Equal(t, int64(1000), iat.Unix())
	assert.Equal(t, int64(1300), exp.Unix())

	for name, bad := range map[string]string{
		"not a jwt":    "abc",
		"bad base64":   "a.!!!.c",
		"bad json":     "a." + base64.RawURLEncoding.EncodeToString([]byte("{")) + ".c",
		"no exp":       testJWT(t, map[string]any{"iat": 1000}),
		"exp <= iat":   testJWT(t, map[string]any{"iat": 1000, "exp": 1000}),
		"non-integer":  testJWT(t, map[string]any{"iat": 1000.5, "exp": 1300}),
		"string claim": testJWT(t, map[string]any{"iat": "x", "exp": 1300}),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseJWTLifetime(bad)
			assert.Error(t, err)
		})
	}
}

func TestFetchAssertion(t *testing.T) {
	tok := testJWT(t, map[string]any{"iat": 1000, "exp": 1300})
	var gotAudience, gotAuth string
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAudience = r.URL.Query().Get("audience")
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprintf(w, `{"value":%q}`, tok)
	})

	a, err := FetchAssertion(context.Background(), AssertionConfig{
		Audience:         "gw-aud",
		OIDCRequestURL:   srv.URL + "/?api-version=2.0",
		OIDCRequestToken: "req-token",
	})
	require.NoError(t, err)
	assert.Equal(t, "gw-aud", gotAudience)
	assert.Equal(t, "bearer req-token", gotAuth)
	assert.Equal(t, tok, a.Value)
	assert.Equal(t, 300*time.Second, a.Lifetime())
}

func TestFetchAssertion_Errors(t *testing.T) {
	notJWT := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"value":"secret-opaque"}`)
	})
	status := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	empty := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"value":""}`)
	})
	notJSON := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `not json`)
	})
	huge := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, strings.Repeat("A", MaxResponseBytes+10))
	})

	cases := map[string]struct {
		cfg  AssertionConfig
		want string
	}{
		"no audience":  {AssertionConfig{OIDCRequestURL: notJWT.URL, OIDCRequestToken: "t"}, "audience is required"},
		"no url":       {AssertionConfig{Audience: "a", OIDCRequestToken: "t"}, "ACTIONS_ID_TOKEN_REQUEST_URL is required"},
		"no token":     {AssertionConfig{Audience: "a", OIDCRequestURL: notJWT.URL}, "ACTIONS_ID_TOKEN_REQUEST_TOKEN is required"},
		"http remote":  {AssertionConfig{Audience: "a", OIDCRequestURL: "http://example.com", OIDCRequestToken: "t"}, "must use https"},
		"foreign host": {AssertionConfig{Audience: "a", OIDCRequestURL: "https://example.com", OIDCRequestToken: "t"}, "not GitHub's Actions token service"},
		"not a jwt":    {AssertionConfig{Audience: "a", OIDCRequestURL: notJWT.URL, OIDCRequestToken: "t"}, "assertion is not a JWT"},
		"status":       {AssertionConfig{Audience: "a", OIDCRequestURL: status.URL, OIDCRequestToken: "t"}, "OIDC endpoint returned 403"},
		"empty":        {AssertionConfig{Audience: "a", OIDCRequestURL: empty.URL, OIDCRequestToken: "t"}, "empty assertion"},
		"not json":     {AssertionConfig{Audience: "a", OIDCRequestURL: notJSON.URL, OIDCRequestToken: "t"}, "parsing response"},
		"oversized":    {AssertionConfig{Audience: "a", OIDCRequestURL: huge.URL, OIDCRequestToken: "t"}, "response exceeds"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := FetchAssertion(context.Background(), tc.cfg)
			require.Error(t, err)
			assert.True(t, strings.HasPrefix(err.Error(), "actionsoidc: "), err.Error())
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), "secret-opaque", "errors never leak the assertion")
		})
	}
}

func TestRequireGitHubOIDCHost(t *testing.T) {
	for _, ok := range []string{
		"https://pipelines.actions.githubusercontent.com/abc",
		"http://127.0.0.1:8080/",
		"http://localhost/",
		"http://[::1]/",
	} {
		assert.NoError(t, RequireGitHubOIDCHost(ok), ok)
	}
	for _, bad := range []string{
		"https://actions.githubusercontent.com.evil.example/",
		"https://example.com/",
		"%zz",
	} {
		assert.Error(t, RequireGitHubOIDCHost(bad), bad)
	}
}

func TestRequireSecureURL(t *testing.T) {
	assert.NoError(t, RequireSecureURL("u", "https://example.com"))
	assert.NoError(t, RequireSecureURL("u", "http://127.0.0.1:1"))
	assert.ErrorContains(t, RequireSecureURL("u", "http://example.com"), `u must use https (got scheme "http")`)
	assert.ErrorContains(t, RequireSecureURL("u", "%zz"), "parsing u")
}
