package botidentity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMintBotRoleResponse(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantRole string
		wantErr  string
	}{
		{name: "recognized", status: 200, body: `{"outcome":"recognized","role":"review"}`, wantRole: "review"},
		{name: "recognized custom role with underscore", status: 200, body: `{"outcome":"recognized","role":"code_review"}`, wantRole: "code_review"},
		{name: "no match", status: 200, body: `{"outcome":"no_match"}`},
		{name: "recognized without role", status: 200, body: `{"outcome":"recognized"}`, wantErr: "invalid role"},
		{name: "recognized with bad role", status: 200, body: `{"outcome":"recognized","role":"Review!"}`, wantErr: "invalid role"},
		{name: "no match with role", status: 200, body: `{"outcome":"no_match","role":"review"}`, wantErr: "no_match with role"},
		{name: "unknown outcome", status: 200, body: `{"outcome":"maybe"}`, wantErr: "unknown outcome"},
		{name: "unreadable 200", status: 200, body: `not json`, wantErr: "unreadable"},
		{name: "unauthorized", status: 401, body: `{"error":"authentication failed"}`, wantErr: "HTTP 401: authentication failed"},
		{name: "server error without body", status: 502, body: ``, wantErr: "HTTP 502: no error message"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ParseMintBotRoleResponse(tc.status, []byte(tc.body))
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrLookup)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Equal(t, BotRoleResult{}, res)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantRole, res.Role)
		})
	}
}

func staticToken(token string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return token, nil }
}

func TestMintResolverResolveBotRole(t *testing.T) {
	var gotReq MintBotRoleRequest
	var gotAuth, gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"outcome":"recognized","role":"review"}`))
	}))
	defer srv.Close()

	m := &MintResolver{MintURL: srv.URL + "/", OIDCToken: staticToken("oidc-jwt"), HTTPClient: srv.Client()}
	res, err := m.ResolveBotRole(context.Background(), BotRoleQuery{Forge: "github", Target: "o/r", Login: "app[bot]"})
	require.NoError(t, err)
	assert.Equal(t, "review", res.Role)
	assert.Equal(t, "Bearer oidc-jwt", gotAuth)
	assert.Equal(t, MintBotRolePath, gotPath)
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, MintBotRoleRequest{ActorLogin: "app[bot]", Repo: "o/r"}, gotReq)
}

func TestMintResolverNoMatchAndFailure(t *testing.T) {
	status, body := http.StatusOK, `{"outcome":"no_match"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	m := &MintResolver{MintURL: srv.URL, OIDCToken: staticToken("t")}
	q := BotRoleQuery{Forge: "github", Target: "o/r", Login: "app[bot]"}

	res, err := m.ResolveBotRole(context.Background(), q)
	require.NoError(t, err)
	assert.False(t, res.Recognized())

	status, body = http.StatusUnauthorized, `{"error":"authentication failed"}`
	_, err = m.ResolveBotRole(context.Background(), q)
	assert.ErrorIs(t, err, ErrLookup)
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial failed")
}

type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errBody) Close() error             { return nil }

type badBodyTransport struct{}

func (badBodyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: errBody{}}, nil
}

func TestMintResolverLookupFailures(t *testing.T) {
	good := BotRoleQuery{Forge: "github", Target: "o/r", Login: "app[bot]"}
	tests := []struct {
		name    string
		m       *MintResolver
		q       BotRoleQuery
		wantErr string
	}{
		{name: "unsupported forge", m: &MintResolver{MintURL: "https://mint", OIDCToken: staticToken("t")}, q: BotRoleQuery{Forge: "gitlab", Target: "o/r", Login: "x"}, wantErr: "no mint bot-role lookup"},
		{name: "missing login", m: &MintResolver{MintURL: "https://mint", OIDCToken: staticToken("t")}, q: BotRoleQuery{Forge: "github", Target: "o/r"}, wantErr: "required"},
		{name: "unconfigured", m: &MintResolver{}, q: good, wantErr: "not configured"},
		{name: "oidc failure", m: &MintResolver{MintURL: "https://mint", OIDCToken: func(context.Context) (string, error) { return "", errors.New("no oidc") }}, q: good, wantErr: "fetching OIDC token"},
		{name: "plaintext url", m: &MintResolver{MintURL: "http://mint.example.com", OIDCToken: staticToken("t"), HTTPClient: &http.Client{Transport: failingTransport{}}}, q: good, wantErr: "must use https"},
		{name: "non-http scheme", m: &MintResolver{MintURL: "ftp://mint.example.com", OIDCToken: staticToken("t"), HTTPClient: &http.Client{Transport: failingTransport{}}}, q: good, wantErr: "must use https"},
		{name: "bad url", m: &MintResolver{MintURL: "://bad", OIDCToken: staticToken("t")}, q: good, wantErr: "building request"},
		{name: "transport", m: &MintResolver{MintURL: "https://mint", OIDCToken: staticToken("t"), HTTPClient: &http.Client{Transport: failingTransport{}}}, q: good, wantErr: "calling mint"},
		{name: "body read", m: &MintResolver{MintURL: "https://mint", OIDCToken: staticToken("t"), HTTPClient: &http.Client{Transport: badBodyTransport{}}}, q: good, wantErr: "reading mint response"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.m.ResolveBotRole(context.Background(), tc.q)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrLookup)
			assert.True(t, strings.Contains(err.Error(), tc.wantErr), err.Error())
		})
	}
}

func TestMintResolverLookupFailureKeepsRootCause(t *testing.T) {
	m := &MintResolver{
		MintURL:   "https://mint",
		OIDCToken: func(context.Context) (string, error) { return "", context.DeadlineExceeded },
	}
	_, err := m.ResolveBotRole(context.Background(), BotRoleQuery{Forge: "github", Target: "o/r", Login: "app[bot]"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLookup)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestMintResolverStalledServerFailsWithinBound(t *testing.T) {
	orig := mintRequestTimeout
	mintRequestTimeout = 100 * time.Millisecond
	t.Cleanup(func() { mintRequestTimeout = orig })

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	// A nil HTTPClient and a caller context without a deadline.
	m := &MintResolver{MintURL: srv.URL, OIDCToken: staticToken("t")}
	start := time.Now()
	_, err := m.ResolveBotRole(context.Background(), BotRoleQuery{Forge: "github", Target: "o/r", Login: "app[bot]"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLookup)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestDefaultMintClientRefusesHTTPSDowngrade(t *testing.T) {
	httpsReq := &http.Request{URL: &url.URL{Scheme: "https", Host: "mint.example.com"}}
	httpReq := &http.Request{URL: &url.URL{Scheme: "http", Host: "mint.example.com"}}

	require.NoError(t, defaultMintClient.CheckRedirect(httpsReq, []*http.Request{httpsReq}))
	assert.ErrorContains(t, defaultMintClient.CheckRedirect(httpReq, []*http.Request{httpsReq}), "refusing redirect")
	assert.Error(t, defaultMintClient.CheckRedirect(httpsReq, make([]*http.Request, 10)))
}

func TestSuppliedMintClientRefusesHTTPSDowngrade(t *testing.T) {
	var plainHits int
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits++
		_, _ = w.Write([]byte(`{"outcome":"no_match"}`))
	}))
	defer plain.Close()
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	// The supplied client has no CheckRedirect of its own, and must still
	// not replay the Authorization header over plaintext.
	m := &MintResolver{MintURL: redirector.URL, OIDCToken: staticToken("t"), HTTPClient: redirector.Client()}
	_, err := m.ResolveBotRole(context.Background(), BotRoleQuery{Forge: "github", Target: "o/r", Login: "app[bot]"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLookup)
	assert.Contains(t, err.Error(), "refusing redirect")
	assert.Zero(t, plainHits, "the plaintext server must never be contacted")

	// A supplied CheckRedirect can add restrictions but not lift the policy.
	permissive := redirector.Client()
	permissive.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
	m.HTTPClient = permissive
	_, err = m.ResolveBotRole(context.Background(), BotRoleQuery{Forge: "github", Target: "o/r", Login: "app[bot]"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing redirect")
	assert.Zero(t, plainHits)
}

func TestValidMintURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://mint.example.com": true,
		"http://127.0.0.1:8080":    true,
		"http://localhost:8080":    true,
		"http://[::1]:8080":        true,
		"http://mint.example.com":  false,
		"http://10.0.0.1":          false,
		"ftp://mint.example.com":   false,
		"https://":                 false,
	} {
		u, err := url.Parse(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, validMintURL(u), raw)
	}
}

func TestMintResolverImplementsBotRoleResolver(t *testing.T) {
	var _ BotRoleResolver = (*MintResolver)(nil)
}
