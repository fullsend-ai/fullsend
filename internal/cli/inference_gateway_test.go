package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/inference/actionsoidc"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const testGatewayAssertion = "eyJhbGciOiJSUzI1NiJ9.secret-assertion.sig"

func writeGatewayStatusConfig(t *testing.T, overlay, base string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".fullsend")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	if overlay != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(overlay), 0o644))
	}
	if base != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte(base), 0o644))
	}
	return dir
}

type gatewayStatusFixture struct {
	env      map[string]string
	fetched  []actionsoidc.AssertionConfig
	fetchErr error
	deps     gatewayStatusDeps
}

func newGatewayStatusFixture(env map[string]string, client *http.Client) *gatewayStatusFixture {
	f := &gatewayStatusFixture{env: env}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	f.deps = gatewayStatusDeps{
		getenv: func(k string) string { return f.env[k] },
		fetchAssertion: func(_ context.Context, cfg actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
			f.fetched = append(f.fetched, cfg)
			if f.fetchErr != nil {
				return nil, f.fetchErr
			}
			return &actionsoidc.Assertion{
				Value:     testGatewayAssertion,
				IssuedAt:  now.Add(-time.Minute),
				ExpiresAt: now.Add(4 * time.Minute),
			}, nil
		},
		httpClient: client,
		now:        func() time.Time { return now },
	}
	return f
}

func actionsEnv(repo string) map[string]string {
	return map[string]string{
		"ACTIONS_ID_TOKEN_REQUEST_URL":   "https://pipelines.actions.githubusercontent.com/token",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "request-token",
		"GITHUB_REPOSITORY":              repo,
	}
}

func runGatewayStatusForTest(t *testing.T, dir string, f *gatewayStatusFixture) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := runInferenceGatewayStatus(context.Background(), ui.New(&buf), "acme/widget", dir, f.deps)
	return buf.String(), err
}

func TestResolveGatewayStatusSources_Layers(t *testing.T) {
	dir := writeGatewayStatusConfig(t,
		"inference:\n  gateway:\n    audience: repo-aud\n    models:\n      m1:\n        api: openai-responses\n",
		"inference:\n  gateway:\n    url: https://gw.example.com\n    audience: base-aud\n")
	s, err := resolveGatewayStatusSources(dir)
	require.NoError(t, err)
	assert.Equal(t, "https://gw.example.com", s.Block.URL)
	assert.Equal(t, "config.base.yaml", s.URLSource)
	assert.Equal(t, "repo-aud", s.Block.Audience)
	assert.Equal(t, "config.yaml", s.AudienceSource)
	assert.Equal(t, "config.yaml", s.ModelsSource)
}

func TestResolveGatewayStatusSources_NoConfig(t *testing.T) {
	dir := writeGatewayStatusConfig(t, "", "")
	s, err := resolveGatewayStatusSources(dir)
	require.NoError(t, err)
	assert.True(t, s.Block.IsZero())
	assert.Empty(t, s.URLSource)
}

func TestInferenceGatewayStatus_NotConfigured(t *testing.T) {
	dir := writeGatewayStatusConfig(t, "runtime: pi\n", "")
	f := newGatewayStatusFixture(actionsEnv("acme/widget"), nil)
	out, err := runGatewayStatusForTest(t, dir, f)
	require.Error(t, err)
	assert.Contains(t, out, "No inference.gateway block configured")
	assert.Empty(t, f.fetched)
}

func TestInferenceGatewayStatus_Partial(t *testing.T) {
	dir := writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: https://gw.example.com\n", "")
	f := newGatewayStatusFixture(actionsEnv("acme/widget"), nil)
	out, err := runGatewayStatusForTest(t, dir, f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing audience")
	assert.Contains(t, out, "Partial inference.gateway block: missing audience")
	assert.Contains(t, out, "https://gw.example.com (from config.yaml)")
	assert.Empty(t, f.fetched)
}

func TestInferenceGatewayStatus_OutsideActions(t *testing.T) {
	dir := writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: https://gw.example.com\n    audience: aud\n", "")
	f := newGatewayStatusFixture(map[string]string{}, nil)
	out, err := runGatewayStatusForTest(t, dir, f)
	require.NoError(t, err)
	assert.Contains(t, out, "Not inside a GitHub Actions job")
	assert.Empty(t, f.fetched)
}

func TestInferenceGatewayStatus_OtherRepository(t *testing.T) {
	dir := writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: https://gw.example.com\n    audience: aud\n", "")
	f := newGatewayStatusFixture(actionsEnv("acme/other"), nil)
	out, err := runGatewayStatusForTest(t, dir, f)
	require.NoError(t, err)
	assert.Contains(t, out, "This job runs in acme/other")
	assert.Empty(t, f.fetched)
}

// gatewayTestServer is a TLS gateway stub answering every request with
// status and body. It records the request and whether the handler ran.
type gatewayTestServer struct {
	srv           *httptest.Server
	handlerCalled bool
	gotAuth       string
	gotRequest    string
}

func newGatewayTestServer(t *testing.T, status int, body string) *gatewayTestServer {
	t.Helper()
	g := &gatewayTestServer{}
	g.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.handlerCalled = true
		g.gotAuth = r.Header.Get("Authorization")
		g.gotRequest = r.Method + " " + r.URL.Path
		if status >= 300 && status < 400 {
			w.Header().Set("Location", "https://elsewhere.example.com/")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(g.srv.Close)
	return g
}

// client returns the stub's TLS client with redirects not followed, as
// the production client does.
func (g *gatewayTestServer) client() *http.Client {
	c := g.srv.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

func runGatewayStatusAgainst(t *testing.T, g *gatewayTestServer) (*gatewayStatusFixture, string, error) {
	t.Helper()
	dir := writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: "+g.srv.URL+"/\n    audience: gw-aud\n", "")
	f := newGatewayStatusFixture(actionsEnv("acme/widget"), g.client())
	out, err := runGatewayStatusForTest(t, dir, f)
	assert.True(t, g.handlerCalled, "gateway handler was not called")
	assert.Equal(t, "GET /v1/models", g.gotRequest)
	assert.Equal(t, "Bearer "+testGatewayAssertion, g.gotAuth)
	assert.NotContains(t, out, testGatewayAssertion)
	assert.NotContains(t, out, "secret-assertion")
	if err != nil {
		assert.NotContains(t, err.Error(), testGatewayAssertion)
	}
	return f, out, err
}

func TestInferenceGatewayStatus_ModelsAuthorised(t *testing.T) {
	g := newGatewayTestServer(t, http.StatusOK, `{"object":"list","data":[{"id":"gpt-5","object":"model"},{"id":"claude-sonnet"}]}`)
	f, out, err := runGatewayStatusAgainst(t, g)
	require.NoError(t, err)
	require.Len(t, f.fetched, 1)
	assert.Equal(t, "gw-aud", f.fetched[0].Audience)
	assert.Equal(t, actionsEnv("acme/widget")["ACTIONS_ID_TOKEN_REQUEST_TOKEN"], f.fetched[0].OIDCRequestToken)
	assert.Contains(t, out, "2026-10-10T12:04:00Z")
	assert.Contains(t, out, "5m0s")
	assert.Contains(t, out, "200")
	assert.Contains(t, out, "Gateway accepted the assertion; 2 model(s) authorised for acme/widget")
	assert.Contains(t, out, "gpt-5")
	assert.Contains(t, out, "claude-sonnet")
}

func TestInferenceGatewayStatus_ModelIDsSanitised(t *testing.T) {
	// A gateway that echoes the bearer value, another JWT or a terminal
	// escape as a model id must not get any of them into the output.
	otherJWT := strings.Join([]string{"eyJhbGciOiJub25lIn0", "eyJzdWIiOiJ4In0", "c2ln"}, ".")
	body := `{"data":[{"id":"` + testGatewayAssertion + `"},{"id":"` + otherJWT + `"},{"id":"evil\u001b[2Jmodel"},{"id":"ok-model"}]}`
	g := newGatewayTestServer(t, http.StatusOK, body)
	_, out, err := runGatewayStatusAgainst(t, g)
	require.NoError(t, err)
	assert.NotContains(t, out, otherJWT)
	assert.NotContains(t, out, "\x1b")
	assert.Contains(t, out, "<redacted>")
	assert.Contains(t, out, "<redacted-jwt>")
	assert.Contains(t, out, "evil?[2Jmodel")
	assert.Contains(t, out, "ok-model")
}

func TestDisplayModelID_WorkflowCommands(t *testing.T) {
	// A gateway-supplied id printed in a job log must not form a GitHub
	// Actions workflow command marker.
	for id, want := range map[string]string{
		"::add-mask::x":  ": :add-mask: :x",
		"::error::x":     ": :error: :x",
		"a:::b":          "a: : :b",
		"::::":           ": : : :",
		"x\n::error::y":  "x?: :error: :y",
		"vendor:model:1": "vendor:model:1",
		"ok-model":       "ok-model",
	} {
		got := displayModelID(id, "")
		assert.Equal(t, want, got, "displayModelID(%q)", id)
		assert.NotContains(t, got, "::", "displayModelID(%q)", id)
	}
}

func TestInferenceGatewayStatus_ModelListCapped(t *testing.T) {
	var data []string
	for i := range gatewayProbeMaxListed + 5 {
		data = append(data, fmt.Sprintf(`{"id":"model-%02d"}`, i))
	}
	g := newGatewayTestServer(t, http.StatusOK, `{"data":[`+strings.Join(data, ",")+`]}`)
	_, out, err := runGatewayStatusAgainst(t, g)
	require.NoError(t, err)
	assert.Contains(t, out, fmt.Sprintf("%d model(s) authorised", gatewayProbeMaxListed+5))
	assert.Contains(t, out, fmt.Sprintf("model-%02d", gatewayProbeMaxListed-1))
	assert.NotContains(t, out, fmt.Sprintf("model-%02d", gatewayProbeMaxListed))
	assert.Contains(t, out, "(and 5 more)")
}

func TestInferenceGatewayStatus_NoModelsAuthorised(t *testing.T) {
	g := newGatewayTestServer(t, http.StatusOK, `{"object":"list","data":[]}`)
	_, out, err := runGatewayStatusAgainst(t, g)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authorises no models for acme/widget")
	assert.Contains(t, out, "no models are authorised for acme/widget")
	assert.NotContains(t, out, "Gateway accepted")
}

func TestInferenceGatewayStatus_Refused(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		g := newGatewayTestServer(t, status, `{"error":"denied"}`)
		_, out, err := runGatewayStatusAgainst(t, g)
		require.Error(t, err, status)
		assert.Contains(t, out, "Gateway refused the assertion")
		assert.NotContains(t, out, "Gateway accepted")
	}
}

func TestInferenceGatewayStatus_Redirect(t *testing.T) {
	g := newGatewayTestServer(t, http.StatusFound, "")
	_, out, err := runGatewayStatusAgainst(t, g)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redirect")
	assert.NotContains(t, out, "Gateway accepted")
}

func TestInferenceGatewayStatus_CouldNotConfirm(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"not found", http.StatusNotFound, `{"error":"no route"}`},
		{"method not allowed", http.StatusMethodNotAllowed, ""},
		{"rate limited", http.StatusTooManyRequests, ""},
		{"server error", http.StatusInternalServerError, "boom"},
		{"bad gateway", http.StatusBadGateway, ""},
		{"bad json", http.StatusOK, "<html>ok</html>"},
		{"no data key", http.StatusOK, `{"object":"list"}`},
		// A gateway that admits requests without a valid credential
		// (permissive OIDC check plus optional keys) answers /v1/models
		// with an empty bare list: never reported as healthy.
		{"bare empty list", http.StatusOK, `[]`},
		{"too large", http.StatusOK, `{"data":[],"pad":"` + strings.Repeat("x", gatewayProbeBodyLimit) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGatewayTestServer(t, tc.status, tc.body)
			_, out, err := runGatewayStatusAgainst(t, g)
			require.Error(t, err)
			assert.Contains(t, err.Error(), fmt.Sprintf("could not confirm authentication with the gateway (HTTP %d)", tc.status))
			assert.Contains(t, out, fmt.Sprintf("Could not confirm authentication (HTTP %d)", tc.status))
			assert.NotContains(t, out, "Gateway accepted")
		})
	}
}

func TestProbeGateway_RefusesPlainHTTP(t *testing.T) {
	_, _, err := probeGateway(context.Background(), http.DefaultClient, "http://127.0.0.1:1/v1/models", testGatewayAssertion)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must use https")
	assert.NotContains(t, err.Error(), testGatewayAssertion)
}

// TestDefaultGatewayStatusDeps_RefusesInternalAddresses checks the
// production client: no environment proxy, and its dialer refuses
// loopback and private addresses before any connection is made.
func TestDefaultGatewayStatusDeps_RefusesInternalAddresses(t *testing.T) {
	deps := defaultGatewayStatusDeps()
	tr, ok := deps.httpClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, tr.Proxy)
	assert.Equal(t, gatewayProbeTimeout, deps.httpClient.Timeout)
	require.NotNil(t, deps.httpClient.CheckRedirect)
	assert.ErrorIs(t, deps.httpClient.CheckRedirect(nil, nil), http.ErrUseLastResponse)
	for _, addr := range []string{"127.0.0.1:443", "[::1]:443", "10.0.0.1:443", "169.254.169.254:443"} {
		conn, err := tr.DialContext(context.Background(), "tcp", addr)
		if conn != nil {
			_ = conn.Close()
		}
		require.Error(t, err, addr)
		assert.Contains(t, err.Error(), "blocked", addr)
	}

	// End to end: the probe with the default client cannot reach a
	// loopback gateway, even one that is listening.
	g := newGatewayTestServer(t, http.StatusOK, `{"data":[{"id":"m"}]}`)
	_, _, err := probeGateway(context.Background(), deps.httpClient, g.srv.URL+gatewayProbePath, testGatewayAssertion)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked")
	assert.False(t, g.handlerCalled)
}

func TestInferenceGatewayStatus_AssertionFetchFails(t *testing.T) {
	dir := writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: https://gw.example.com\n    audience: gw-aud\n", "")
	f := newGatewayStatusFixture(actionsEnv("acme/widget"), nil)
	f.fetchErr = errors.New("boom")
	out, err := runGatewayStatusForTest(t, dir, f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching OIDC assertion")
	assert.Contains(t, out, "Assertion fetch failed")
}

func TestInferenceGatewayStatusCmd_Registered(t *testing.T) {
	cmd := newRootCmd()
	found, _, err := cmd.Find([]string{"inference", "gateway", "status"})
	require.NoError(t, err)
	assert.Equal(t, "status <owner/repo>", found.Use)

	cmd = newRootCmd()
	cmd.SetArgs([]string{"inference", "gateway", "status", "acme"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected owner/repo")
}
