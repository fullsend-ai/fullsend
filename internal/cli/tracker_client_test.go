package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/tracker"
)

func TestNewForgeTrackerClient_UnsupportedTracker(t *testing.T) {
	_, err := newForgeTrackerClient("servicenow", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported tracker")
	assert.Contains(t, err.Error(), "servicenow")
}

func TestNewForgeTrackerClient_GitHub_NoToken(t *testing.T) {
	// With no token and no env var, GitHub falls back to `gh auth token`
	// which may or may not be available. We just verify it doesn't panic.
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	_, err := newForgeTrackerClient(trackerGitHub, "")
	// Error is acceptable (no token available in CI) — we're testing
	// that the code path runs without panicking.
	if err != nil {
		assert.Contains(t, err.Error(), "token")
	}
}

func TestNewForgeTrackerClient_GitHub_ExplicitToken(t *testing.T) {
	tc, err := newForgeTrackerClient(trackerGitHub, "ghp_test123")
	assert.NoError(t, err)
	assert.NotNil(t, tc)
}

func TestNewForgeTrackerClient_GitLab_NoToken(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "")
	_, err := newForgeTrackerClient(trackerGitLab, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "GitLab token")
}

func TestNewForgeTrackerClient_GitLab_ExplicitToken(t *testing.T) {
	tc, err := newForgeTrackerClient(trackerGitLab, "glpat-test123")
	assert.NoError(t, err)
	assert.NotNil(t, tc)
}

func TestNewForgeTrackerClient_JiraNotForge(t *testing.T) {
	_, err := newForgeTrackerClient(trackerJira, "token")
	assert.Error(t, err)
}

const testJiraConfig = "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n"

func parseTestConfig(t *testing.T, body string) config.PerRepoConfigReader {
	t.Helper()
	reader, err := config.ParsePerRepoConfig([]byte(body))
	require.NoError(t, err)
	return reader
}

func TestNewConfiguredTrackerClient_Jira(t *testing.T) {
	t.Setenv(jira.TokenEnvVar, "jira-token")
	t.Setenv(jira.EmailEnvVar, "")

	t.Run("from fullsend dir", func(t *testing.T) {
		tc, err := newConfiguredTrackerClient(trackerJira, "", writeJiraPollConfig(t, testJiraConfig), nil)
		require.NoError(t, err)
		require.IsType(t, &tracker.JiraClient{}, tc)
	})
	t.Run("from config reader", func(t *testing.T) {
		tc, err := newConfiguredTrackerClient(trackerJira, "", "", parseTestConfig(t, testJiraConfig))
		require.NoError(t, err)
		assert.NotNil(t, tc)
	})
	t.Run("token flag rejected", func(t *testing.T) {
		_, err := newConfiguredTrackerClient(trackerJira, "flag-token", "", parseTestConfig(t, testJiraConfig))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--token is not supported with --tracker jira")
		assert.Contains(t, err.Error(), jira.TokenEnvVar)
		assert.NotContains(t, err.Error(), "flag-token")
	})
	t.Run("no config", func(t *testing.T) {
		_, err := newConfiguredTrackerClient(trackerJira, "", "", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tracker.jira.base_url")
		assert.Contains(t, err.Error(), "--fullsend-dir")
	})
	t.Run("selection only", func(t *testing.T) {
		_, err := newConfiguredTrackerClient(trackerJira, "", writeJiraPollConfig(t, "tracker:\n  jira: {}\n"), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tracker.jira.base_url")
	})
	t.Run("other provider", func(t *testing.T) {
		_, err := newConfiguredTrackerClient(trackerJira, "", "", parseTestConfig(t, "tracker:\n  github: {}\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tracker.jira.base_url")
	})
	t.Run("invalid config", func(t *testing.T) {
		_, err := newConfiguredTrackerClient(trackerJira, "", writeJiraPollConfig(t, "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n"), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "project is required")
	})
	t.Run("malformed config", func(t *testing.T) {
		_, err := newConfiguredTrackerClient(trackerJira, "", writeJiraPollConfig(t, "tracker:\n  jira:\n    email: a@example.com\n"), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "loading config for tracker.jira")
	})
}

func TestNewConfiguredTrackerClient_JiraMissingToken(t *testing.T) {
	t.Setenv(jira.TokenEnvVar, "")
	_, err := newConfiguredTrackerClient(trackerJira, "", "", parseTestConfig(t, testJiraConfig))
	require.Error(t, err)
	assert.Contains(t, err.Error(), jira.TokenEnvVar)
}

func TestNewConfiguredTrackerClient_JiraLegacyTokenHint(t *testing.T) {
	t.Setenv(jira.TokenEnvVar, "")
	t.Setenv("JIRA_TOKEN", "legacy-token")
	_, err := newConfiguredTrackerClient(trackerJira, "", "", parseTestConfig(t, testJiraConfig))
	require.Error(t, err)
	assert.Contains(t, err.Error(), jira.TokenEnvVar+" is required")
	assert.Contains(t, err.Error(), "legacy JIRA_TOKEN is no longer read")
	assert.NotContains(t, err.Error(), "legacy-token")
}

func TestNewConfiguredTrackerClient_NonJiraIgnoresJiraConfig(t *testing.T) {
	tc, err := newConfiguredTrackerClient(trackerGitLab, "glpat-test123", writeJiraPollConfig(t, testJiraConfig), nil)
	require.NoError(t, err)
	assert.NotNil(t, tc)
}

func TestNewJiraTrackerClientFromConfig(t *testing.T) {
	t.Setenv(jira.TokenEnvVar, "jira-token")
	t.Setenv("GITHUB_ACTIONS", "")

	tc, err := newJiraTrackerClientFromConfig(parseTestConfig(t, testJiraConfig).IssueTracking())
	require.NoError(t, err)
	assert.NotNil(t, tc)

	_, err = newJiraTrackerClientFromConfig(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker.jira.base_url")
}

func TestNewJiraTrackerClient_MasksTokenInGitHubActions(t *testing.T) {
	t.Setenv(jira.TokenEnvVar, "jira-token")
	t.Setenv("GITHUB_ACTIONS", "true")

	r, w, err := os.Pipe()
	require.NoError(t, err)
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	tc, err := newJiraTrackerClientFromConfig(parseTestConfig(t, testJiraConfig).IssueTracking())
	os.Stderr = origStderr
	require.NoError(t, w.Close())
	require.NoError(t, err)
	assert.NotNil(t, tc)

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Contains(t, string(out), "::add-mask::jira-token")
}

func TestNewJiraTrackerClient_EscapesMaskedToken(t *testing.T) {
	t.Setenv(jira.TokenEnvVar, "jira\n::error::x%token")
	t.Setenv("GITHUB_ACTIONS", "true")

	r, w, err := os.Pipe()
	require.NoError(t, err)
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	_, err = newJiraTrackerClientFromConfig(parseTestConfig(t, testJiraConfig).IssueTracking())
	os.Stderr = origStderr
	require.NoError(t, w.Close())
	require.NoError(t, err)

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Contains(t, string(out), "::add-mask::jira%0A::error::x%25token\n")
	assert.NotContains(t, string(out), "\n::error::")
}

// jiraGatewayServer is a TLS test server standing in for both the Jira site
// (cloud ID discovery) and the API gateway. It records the Authorization
// header of each gateway request.
type jiraGatewayServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	auth []string
}

const testCloudID = "11111111-2222-3333-4444-555555555555"

func newJiraGatewayServer(t *testing.T) *jiraGatewayServer {
	t.Helper()
	g := &jiraGatewayServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/site/_edge/tenant_info", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"cloudId":"` + testCloudID + `"}`))
	})
	mux.HandleFunc("/gw/ex/jira/"+testCloudID+"/rest/api/3/issue/PROJ-7", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.auth = append(g.auth, r.Header.Get("Authorization"))
		g.mu.Unlock()
		_, _ = w.Write([]byte(`{"key":"PROJ-7","fields":{"summary":"title"}}`))
	})
	g.srv = httptest.NewTLSServer(mux)
	t.Cleanup(g.srv.Close)
	orig := jiraScopedTestOptions
	jiraScopedTestOptions = []jira.Option{jira.WithHTTPClient(g.srv.Client())}
	t.Cleanup(func() { jiraScopedTestOptions = orig })
	return g
}

func (g *jiraGatewayServer) config() string {
	return "tracker:\n  jira:\n    base_url: " + g.srv.URL + "/site\n    api_url: " + g.srv.URL + "/gw\n    project: PROJ\n"
}

func TestNewConfiguredTrackerClient_JiraUsesGatewayAuth(t *testing.T) {
	for _, tt := range []struct {
		name, email, wantPrefix string
	}{
		{name: "service account", wantPrefix: "Bearer "},
		{name: "bot user", email: "bot@example.com", wantPrefix: "Basic "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := newJiraGatewayServer(t)
			t.Setenv(jira.TokenEnvVar, "jira-token")
			t.Setenv(jira.EmailEnvVar, tt.email)

			tc, err := newConfiguredTrackerClient(trackerJira, "", "", parseTestConfig(t, g.config()))
			require.NoError(t, err)
			issue, err := tc.GetIssue(context.Background(), "PROJ", 7)
			require.NoError(t, err)
			assert.Equal(t, g.srv.URL+"/site/browse/PROJ-7", issue.URL, "browse links stay on the site URL")

			require.Len(t, g.auth, 1)
			assert.True(t, strings.HasPrefix(g.auth[0], tt.wantPrefix), "Authorization %q, want prefix %q", g.auth[0], tt.wantPrefix)
		})
	}
}

func TestResolveIssueProject(t *testing.T) {
	reader := parseTestConfig(t, testJiraConfig)

	got, err := resolveIssueProject(trackerJira, "OTHER", "", reader)
	require.NoError(t, err)
	assert.Equal(t, "OTHER", got, "an explicit --project is kept")

	got, err = resolveIssueProject(trackerJira, "", "", reader)
	require.NoError(t, err)
	assert.Equal(t, "PROJ", got, "Jira defaults to tracker.jira.project")

	_, err = resolveIssueProject(trackerJira, "", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no default Jira project is configured")

	_, err = resolveIssueProject(trackerGitHub, " ", "", reader)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project must not be empty for --tracker github")
}

func TestCheckJiraTargetProject(t *testing.T) {
	settings := &config.JiraTrackingConfig{
		Project:        "PROJ",
		AllowedTargets: config.JiraAllowedTargets{Projects: []string{"OPS"}},
	}
	assert.NoError(t, checkJiraTargetProject(settings, "PROJ"))
	assert.NoError(t, checkJiraTargetProject(settings, "proj"), "project keys are case-insensitive")
	assert.NoError(t, checkJiraTargetProject(settings, "OPS"))

	err := checkJiraTargetProject(settings, "SEC")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `Jira project "SEC" is not allowed`)
	assert.Contains(t, err.Error(), "tracker.jira.allowed_targets.projects")
}
