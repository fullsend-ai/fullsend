package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testJiraCloudID = "11111111-2222-3333-4444-555555555555"

// clearJiraEnv blanks every Jira variable so a developer's environment
// cannot leak into these tests.
func clearJiraEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{"JIRA_API_URL", "JIRA_BASE_URL", "JIRA_TOKEN", "JIRA_USER_EMAIL", "GITHUB_ACTIONS"} {
		t.Setenv(v, "")
	}
}

// fakeAtlassian stands in for the Platform API Gateway and a site's
// tenant_info endpoint. It records the path and Authorization header of the
// last gateway request and counts tenant_info lookups.
type fakeAtlassian struct {
	gateway, site *httptest.Server
	gatewayAuth   string
	gatewayPath   string
	discoveries   atomic.Int32
}

func newFakeAtlassian(t *testing.T) *fakeAtlassian {
	t.Helper()
	f := &fakeAtlassian{}
	f.gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gatewayAuth, f.gatewayPath = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"accountId": "svc"})
	}))
	f.site = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_edge/tenant_info" {
			t.Errorf("unexpected request to site URL: %s", r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.discoveries.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"cloudId": testJiraCloudID})
	}))
	t.Cleanup(f.gateway.Close)
	t.Cleanup(f.site.Close)

	prev := jiraGatewayURL
	jiraGatewayURL = f.gateway.URL
	t.Cleanup(func() { jiraGatewayURL = prev })
	return f
}

func (f *fakeAtlassian) gatewayAPIURL() string {
	return f.gateway.URL + "/ex/jira/" + testJiraCloudID
}

func basicAuth(email, token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+token))
}

func TestResolveJiraAPIURL(t *testing.T) {
	ctx := context.Background()
	t.Run("explicit JIRA_API_URL wins, trailing slash trimmed", func(t *testing.T) {
		clearJiraEnv(t)
		f := newFakeAtlassian(t)
		t.Setenv("JIRA_API_URL", "https://api.example.com/ex/jira/abc/")
		u, err := resolveJiraAPIURL(ctx, f.site.URL)
		require.NoError(t, err)
		assert.Equal(t, "https://api.example.com/ex/jira/abc", u)
		assert.Zero(t, f.discoveries.Load(), "no discovery when JIRA_API_URL is set")
	})
	t.Run("explicit insecure JIRA_API_URL is rejected", func(t *testing.T) {
		clearJiraEnv(t)
		t.Setenv("JIRA_API_URL", "http://api.example.com")
		_, err := resolveJiraAPIURL(ctx, "https://acme.atlassian.net")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "JIRA_API_URL")
		assert.Contains(t, err.Error(), "insecure scheme")
	})
	t.Run("defaults to the gateway with the discovered cloud ID", func(t *testing.T) {
		clearJiraEnv(t)
		f := newFakeAtlassian(t)
		u, err := resolveJiraAPIURL(ctx, f.site.URL+"/")
		require.NoError(t, err)
		assert.Equal(t, f.gatewayAPIURL(), u)
		assert.Equal(t, int32(1), f.discoveries.Load())
	})
	t.Run("discovery failure names JIRA_API_URL", func(t *testing.T) {
		clearJiraEnv(t)
		newFakeAtlassian(t)
		site := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(site.Close)
		_, err := resolveJiraAPIURL(ctx, site.URL)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "JIRA_API_URL is unset and cloud ID discovery for "+site.URL+" failed")
		assert.Contains(t, err.Error(), "set JIRA_API_URL")
		assert.Contains(t, err.Error(), "unexpected status 404")
	})
}

// TestNewJiraClient_RoutesThroughGateway verifies the client every command
// uses: Basic auth with the email and token, calls to the gateway for the
// site's discovered cloud ID, nothing sent to the site URL, and discovery
// deferred to the first request.
func TestNewJiraClient_RoutesThroughGateway(t *testing.T) {
	clearJiraEnv(t)
	f := newFakeAtlassian(t)
	c, err := newJiraClient(f.site.URL, "svc@serviceaccount.atlassian.com", "scoped-token")
	require.NoError(t, err)
	assert.Zero(t, f.discoveries.Load(), "building the client makes no network call")

	u, err := c.GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "svc", u.AccountID)
	assert.Equal(t, "/ex/jira/"+testJiraCloudID+"/rest/api/3/myself", f.gatewayPath)
	assert.Equal(t, basicAuth("svc@serviceaccount.atlassian.com", "scoped-token"), f.gatewayAuth)

	_, err = c.GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(1), f.discoveries.Load(), "the resolved URL is cached")
}

func TestNewJiraClient_ExplicitAPIURL(t *testing.T) {
	clearJiraEnv(t)
	f := newFakeAtlassian(t)
	t.Setenv("JIRA_API_URL", f.gatewayAPIURL())
	c, err := newJiraClient("https://acme.atlassian.net", "bot@acme.com", "classic-token")
	require.NoError(t, err)
	_, err = c.GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/ex/jira/"+testJiraCloudID+"/rest/api/3/myself", f.gatewayPath)
	assert.Equal(t, basicAuth("bot@acme.com", "classic-token"), f.gatewayAuth)
}

func TestBuildJiraClient_UsesGateway(t *testing.T) {
	clearJiraEnv(t)
	f := newFakeAtlassian(t)
	t.Setenv("JIRA_TOKEN", "tok")
	t.Setenv("JIRA_USER_EMAIL", "bot@acme.com")
	c, err := buildJiraClient(f.site.URL)
	require.NoError(t, err)
	_, err = c.GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/ex/jira/"+testJiraCloudID+"/rest/api/3/myself", f.gatewayPath)
	assert.Equal(t, basicAuth("bot@acme.com", "tok"), f.gatewayAuth)
}

// jiraWhoAmI is the one tracker method every Jira tracker client exposes
// that needs no issue to exist on the fake: it maps to GET /myself.
type jiraWhoAmI interface {
	AuthenticatedUser(ctx context.Context) (string, error)
}

func TestNewJiraTrackerClientFromEnv_UsesResolvedAPIURL(t *testing.T) {
	clearJiraEnv(t)
	f := newFakeAtlassian(t)
	t.Setenv("JIRA_BASE_URL", f.site.URL)
	t.Setenv("JIRA_TOKEN", "tok")
	t.Setenv("JIRA_USER_EMAIL", "bot@acme.com")
	tc, err := newJiraTrackerClientFromEnv()
	require.NoError(t, err)
	require.NotNil(t, tc)
	assert.Zero(t, f.discoveries.Load(), "building the tracker makes no network call")
	who, ok := any(tc).(jiraWhoAmI)
	require.True(t, ok, "tracker client exposes AuthenticatedUser")
	_, err = who.AuthenticatedUser(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/ex/jira/"+testJiraCloudID+"/rest/api/3/myself", f.gatewayPath)
	assert.Equal(t, basicAuth("bot@acme.com", "tok"), f.gatewayAuth)
}

func TestNewTrackerClient_Jira_RoutesThroughGateway(t *testing.T) {
	clearJiraEnv(t)
	f := newFakeAtlassian(t)
	tc, err := newTrackerClient(trackerJira, "tok", f.site.URL, "bot@acme.com")
	require.NoError(t, err)
	who, ok := any(tc).(jiraWhoAmI)
	require.True(t, ok, "tracker client exposes AuthenticatedUser")
	_, err = who.AuthenticatedUser(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/ex/jira/"+testJiraCloudID+"/rest/api/3/myself", f.gatewayPath)
	assert.Equal(t, basicAuth("bot@acme.com", "tok"), f.gatewayAuth)
}

func TestResolveJiraAPIURL_ExplicitValueIsScopedToItsSite(t *testing.T) {
	ctx := context.Background()
	clearJiraEnv(t)
	first := newFakeAtlassian(t)
	t.Setenv("JIRA_BASE_URL", first.site.URL+"/")
	t.Setenv("JIRA_API_URL", "https://api.example.com/ex/jira/abc")

	got, err := resolveJiraAPIURL(ctx, first.site.URL)
	require.NoError(t, err)
	assert.Equal(t, "https://api.example.com/ex/jira/abc", got, "same site: JIRA_API_URL wins")
	assert.Zero(t, first.discoveries.Load())

	other := newFakeAtlassian(t)
	got, err = resolveJiraAPIURL(ctx, other.site.URL)
	require.NoError(t, err)
	assert.Equal(t, other.gatewayAPIURL(), got, "another site: discovery for that site")
	assert.Equal(t, int32(1), other.discoveries.Load())
}

func TestExportJiraAPIURL(t *testing.T) {
	ctx := context.Background()
	t.Run("no JIRA_BASE_URL is a no-op", func(t *testing.T) {
		clearJiraEnv(t)
		f := newFakeAtlassian(t)
		exportJiraAPIURL(ctx, func(msg string) { t.Errorf("unexpected warning: %s", msg) })
		assert.Empty(t, os.Getenv("JIRA_API_URL"))
		assert.Zero(t, f.discoveries.Load())
	})
	t.Run("explicit JIRA_API_URL is kept", func(t *testing.T) {
		clearJiraEnv(t)
		f := newFakeAtlassian(t)
		t.Setenv("JIRA_BASE_URL", f.site.URL)
		t.Setenv("JIRA_API_URL", "https://api.example.com/ex/jira/abc")
		exportJiraAPIURL(ctx, func(msg string) { t.Errorf("unexpected warning: %s", msg) })
		assert.Equal(t, "https://api.example.com/ex/jira/abc", os.Getenv("JIRA_API_URL"))
		assert.Zero(t, f.discoveries.Load())
	})
	t.Run("sets JIRA_API_URL from discovery", func(t *testing.T) {
		clearJiraEnv(t)
		f := newFakeAtlassian(t)
		t.Setenv("JIRA_BASE_URL", f.site.URL)
		exportJiraAPIURL(ctx, func(msg string) { t.Errorf("unexpected warning: %s", msg) })
		assert.Equal(t, f.gatewayAPIURL(), os.Getenv("JIRA_API_URL"))
		assert.Contains(t, inheritedScriptEnv(), "JIRA_API_URL="+f.gatewayAPIURL(), "host-side scripts inherit the resolved URL")
	})
	t.Run("discovery failure warns and leaves JIRA_API_URL unset", func(t *testing.T) {
		clearJiraEnv(t)
		newFakeAtlassian(t)
		site := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(site.Close)
		t.Setenv("JIRA_BASE_URL", site.URL)
		var warned string
		exportJiraAPIURL(ctx, func(msg string) { warned = msg })
		assert.Contains(t, warned, "Jira API URL not resolved")
		assert.Contains(t, warned, "set JIRA_API_URL")
		assert.Empty(t, os.Getenv("JIRA_API_URL"))
	})
}
