package actionsoidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
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

func TestRequireGitHubOIDCHost_ConfiguredServer(t *testing.T) {
	cases := []struct {
		name, server, oidcURL string
		ok                    bool
	}{
		{"server host", "https://ghes.example.com", "https://ghes.example.com/_services/token?api-version=2.0", true},
		{"server url case and slash", "https://GHES.example.com/", "https://ghes.EXAMPLE.com/_services/token", true},
		{"explicit default port", "https://ghes.example.com", "https://ghes.example.com:443/_services/token", true},
		{"server with port", "https://ghes.example.com:8443", "https://ghes.example.com:8443/_services/token", true},
		{"other port", "https://ghes.example.com:8443", "https://ghes.example.com/_services/token", false},
		{"other host", "https://ghes.example.com", "https://elsewhere.example/token", false},
		{"look-alike host", "https://ghes.example.com", "https://ghes.example.com.evil.example/token", false},
		{"subdomain of server", "https://ghes.example.com", "https://x.ghes.example.com/token", false},
		{"plain http on server", "https://ghes.example.com", "http://ghes.example.com/token", false},
		{"http server url", "http://ghes.example.com", "https://ghes.example.com/token", false},
		{"server url with path", "https://ghes.example.com/sub", "https://ghes.example.com/token", false},
		{"github.com server", "https://github.com", "https://github.com/token", false},
		{"unset server", "", "https://ghes.example.com/token", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GITHUB_SERVER_URL", tc.server)
			err := RequireGitHubOIDCHost(tc.oidcURL)
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, "not GitHub's Actions token service")
			}
		})
	}
}

func TestRequireGitHubOIDCHost_GitHubComUnchanged(t *testing.T) {
	for _, server := range []string{"", "https://github.com", "https://GitHub.com/"} {
		t.Run(server, func(t *testing.T) {
			t.Setenv("GITHUB_SERVER_URL", server)
			assert.NoError(t, RequireGitHubOIDCHost("https://pipelines.actions.githubusercontent.com/abc/token"))
			assert.Error(t, RequireGitHubOIDCHost("https://github.com/token"))
			assert.Error(t, RequireGitHubOIDCHost("https://example.com/"))
		})
	}
}

// TestFetchAssertion_ConfiguredServer fetches an assertion from a token
// service on a GitHub server other than github.com: the request URL is on
// the GITHUB_SERVER_URL host, and the request reaches it with the audience
// and the bearer token.
func TestFetchAssertion_ConfiguredServer(t *testing.T) {
	tok := testJWT(t, map[string]any{"iat": 1000, "exp": 1300})
	var gotHost, gotPath, gotAudience, gotVersion, gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotPath = r.URL.Path
		gotAudience = r.URL.Query().Get("audience")
		gotVersion = r.URL.Query().Get("api-version")
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprintf(w, `{"value":%q}`, tok)
	}))
	t.Cleanup(srv.Close)
	// The test server's certificate names example.com; dial the server for
	// it so the request goes to a non-loopback, non-github.com host name.
	client := srv.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	client.Transport = transport
	t.Setenv("GITHUB_SERVER_URL", "https://example.com")

	a, err := FetchAssertion(context.Background(), AssertionConfig{
		Audience:         "gw-aud",
		OIDCRequestURL:   "https://example.com/_services/token?api-version=2.0",
		OIDCRequestToken: "req-token",
		HTTPClient:       client,
	})
	require.NoError(t, err)
	assert.Equal(t, "example.com", gotHost)
	assert.Equal(t, "/_services/token", gotPath)
	assert.Equal(t, "gw-aud", gotAudience)
	assert.Equal(t, "2.0", gotVersion)
	assert.Equal(t, "bearer req-token", gotAuth)
	assert.Equal(t, tok, a.Value)

	// With no GitHub server configured the same URL is refused before any
	// request is made.
	gotHost = ""
	t.Setenv("GITHUB_SERVER_URL", "")
	_, err = FetchAssertion(context.Background(), AssertionConfig{
		Audience:         "gw-aud",
		OIDCRequestURL:   "https://example.com/_services/token?api-version=2.0",
		OIDCRequestToken: "req-token",
		HTTPClient:       client,
	})
	assert.ErrorContains(t, err, "not GitHub's Actions token service")
	assert.Empty(t, gotHost, "no request is sent to a refused host")
}

func TestRequireSecureURL(t *testing.T) {
	assert.NoError(t, RequireSecureURL("u", "https://example.com"))
	assert.NoError(t, RequireSecureURL("u", "http://127.0.0.1:1"))
	assert.ErrorContains(t, RequireSecureURL("u", "http://example.com"), `u must use https (got scheme "http")`)
	assert.ErrorContains(t, RequireSecureURL("u", "%zz"), "parsing u")
}
