package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/inference/openaiwif"
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
	fetched  []openaiwif.AssertionConfig
	fetchErr error
	deps     gatewayStatusDeps
}

func newGatewayStatusFixture(env map[string]string, client *http.Client) *gatewayStatusFixture {
	f := &gatewayStatusFixture{env: env}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	f.deps = gatewayStatusDeps{
		getenv: func(k string) string { return f.env[k] },
		fetchAssertion: func(_ context.Context, cfg openaiwif.AssertionConfig) (*openaiwif.Assertion, error) {
			f.fetched = append(f.fetched, cfg)
			if f.fetchErr != nil {
				return nil, f.fetchErr
			}
			return &openaiwif.Assertion{
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

func gatewayTestServer(t *testing.T, status int, gotAuth *string, gotPath *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")
		*gotPath = r.Method + " " + r.URL.Path
		w.WriteHeader(status)
		_, _ = w.Write([]byte(strings.Repeat("x", gatewayProbeBodyLimit*2)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInferenceGatewayStatus_AuthenticatedRequest(t *testing.T) {
	var gotAuth, gotPath string
	srv := gatewayTestServer(t, http.StatusBadRequest, &gotAuth, &gotPath)
	dir := writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: "+srv.URL+"/\n    audience: gw-aud\n", "")
	f := newGatewayStatusFixture(actionsEnv("acme/widget"), srv.Client())

	out, err := runGatewayStatusForTest(t, dir, f)
	require.NoError(t, err)
	require.Len(t, f.fetched, 1)
	assert.Equal(t, "gw-aud", f.fetched[0].Audience)
	assert.Equal(t, "request-token", f.fetched[0].OIDCRequestToken)
	assert.Equal(t, "Bearer "+testGatewayAssertion, gotAuth)
	assert.Equal(t, "POST /v1/chat/completions", gotPath)
	assert.Contains(t, out, "2026-10-10T12:04:00Z")
	assert.Contains(t, out, "5m0s")
	assert.Contains(t, out, "400")
	assert.Contains(t, out, "Gateway accepted the assertion")
	assert.NotContains(t, out, testGatewayAssertion)
}

func TestInferenceGatewayStatus_Refused(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		var gotAuth, gotPath string
		srv := gatewayTestServer(t, status, &gotAuth, &gotPath)
		dir := writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: "+srv.URL+"\n    audience: gw-aud\n", "")
		f := newGatewayStatusFixture(actionsEnv("acme/widget"), srv.Client())
		out, err := runGatewayStatusForTest(t, dir, f)
		require.Error(t, err)
		assert.Contains(t, out, "Gateway refused the assertion")
		assert.NotContains(t, out, testGatewayAssertion)
		assert.NotContains(t, err.Error(), testGatewayAssertion)
	}
}

func TestInferenceGatewayStatus_ServerErrorAndRedirect(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusFound} {
		var gotAuth, gotPath string
		srv := gatewayTestServer(t, status, &gotAuth, &gotPath)
		dir := writeGatewayStatusConfig(t, "inference:\n  gateway:\n    url: "+srv.URL+"\n    audience: gw-aud\n", "")
		client := srv.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		f := newGatewayStatusFixture(actionsEnv("acme/widget"), client)
		_, err := runGatewayStatusForTest(t, dir, f)
		require.Error(t, err)
	}
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
