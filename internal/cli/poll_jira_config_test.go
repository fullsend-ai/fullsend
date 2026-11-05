package cli

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
)

func writeJiraPollConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("version: \"1\"\n"+body), 0o600))
	return dir
}

// enabledJiraPollConfig is a minimal tracker.jira mapping with polling on.
const enabledJiraPollConfig = `tracker:
  jira:
    base_url: https://example.atlassian.net
    project: PROJ
    poll:
      enabled: true
`

func TestLoadJiraPollSettings_ReadsConfig(t *testing.T) {
	dir := writeJiraPollConfig(t, `tracker:
  jira:
    base_url: https://example.atlassian.net
    api_url: https://gateway.example.com
    project: PROJ
    poll:
      enabled: true
      query: 'labels = "fullsend" ORDER BY updated DESC'
`)
	got, err := loadJiraPollSettings(dir, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, jiraPollSettings{
		baseURL: "https://example.atlassian.net",
		apiURL:  "https://gateway.example.com",
		project: "PROJ",
		jql:     `project = "PROJ" AND (labels = "fullsend") ORDER BY updated DESC`,
		enabled: true,
	}, got)
}

func TestLoadJiraPollSettings_Defaults(t *testing.T) {
	dir := writeJiraPollConfig(t, "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n")
	got, err := loadJiraPollSettings(dir, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, config.DefaultJiraAPIURL, got.apiURL)
	assert.Equal(t, `project = "PROJ" AND (statusCategory != Done) ORDER BY updated DESC`, got.jql)
	assert.False(t, got.enabled, "an omitted poll mapping leaves polling disabled")
}

func TestLoadJiraPollSettings_QueryWithoutOrderBy(t *testing.T) {
	dir := writeJiraPollConfig(t, "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n    poll:\n      enabled: false\n      query: 'labels = a OR labels = b'\n")
	got, err := loadJiraPollSettings(dir, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, `project = "PROJ" AND (labels = a OR labels = b)`, got.jql,
		"an OR query stays inside the project scope")
	assert.False(t, got.enabled)
}

func TestLoadJiraPollSettings_RejectsRemovedFlags(t *testing.T) {
	for name, dir := range map[string]string{
		"with config":    writeJiraPollConfig(t, enabledJiraPollConfig),
		"without config": t.TempDir(),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadJiraPollSettings(dir, "https://other.atlassian.net", "", "project = X")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--jira-url, --jql no longer supported")
			assert.Contains(t, err.Error(), "tracker.jira.poll.query")

			_, err = loadJiraPollSettings(dir, "", "PROJ", "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--jira-project no longer supported")
		})
	}
}

func TestLoadJiraPollSettings_RequiresTrackerJira(t *testing.T) {
	// JIRA_BASE_URL is not a fallback for the Jira site.
	t.Setenv("JIRA_BASE_URL", "https://env.atlassian.net")
	for name, body := range map[string]string{
		"no config tracker":    "",
		"selection only":       "tracker:\n  jira: {}\n",
		"allowed targets only": "tracker:\n  jira:\n    allowed_targets:\n      projects: [OTHER]\n",
		"github tracker":       "tracker:\n  github: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadJiraPollSettings(writeJiraPollConfig(t, body), "", "", "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "requires tracker.jira in config.yaml")
		})
	}
	t.Run("missing config dir", func(t *testing.T) {
		_, err := loadJiraPollSettings(t.TempDir(), "", "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires tracker.jira in config.yaml")
	})
}

func TestLoadJiraPollSettings_InvalidConfig(t *testing.T) {
	for name, tc := range map[string]struct{ body, wantErr string }{
		"missing project":  {"tracker:\n  jira:\n    base_url: https://example.atlassian.net\n", "project is required"},
		"missing base_url": {"tracker:\n  jira:\n    project: PROJ\n", "base_url is required"},
		"unbalanced query": {"tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n    poll:\n      query: 'a = b)'\n", "unbalanced parentheses"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadJiraPollSettings(writeJiraPollConfig(t, tc.body), "", "", "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLoadJiraPollSettings_MalformedConfig(t *testing.T) {
	dir := writeJiraPollConfig(t, "tracker:\n  jira:\n    token: abc\n")
	_, err := loadJiraPollSettings(dir, "", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load config")
}

// jiraGatewayServer serves the Jira site's tenant_info and the API
// gateway's /myself, recording the Authorization header /myself received.
func jiraGatewayServer(t *testing.T, gotAuth *string) *httptest.Server {
	t.Helper()
	const cloudID = "11111111-2222-3333-4444-555555555555"
	mux := http.NewServeMux()
	mux.HandleFunc("/site/_edge/tenant_info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"cloudId":"` + cloudID + `"}`))
	})
	mux.HandleFunc("/gw/ex/jira/"+cloudID+"/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"accountId":"acct-1","active":true}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestBuildJiraClient_ScopedAuth(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	legacy := map[string]string{"JIRA_TOKEN": "legacy-token", "JIRA_USER_EMAIL": "legacy@example.com"}

	t.Run("missing token", func(t *testing.T) {
		_, err := buildJiraClient(jiraPollSettings{baseURL: "https://example.atlassian.net"}, env(legacy))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "FULLSEND_JIRA_TOKEN is required", "legacy JIRA_TOKEN is not a fallback")
	})

	for name, tc := range map[string]struct {
		vars     map[string]string
		wantAuth string
	}{
		"service account without email": {
			vars:     map[string]string{"FULLSEND_JIRA_TOKEN": "scoped-token", "JIRA_USER_EMAIL": "legacy@example.com"},
			wantAuth: "Bearer scoped-token",
		},
		"bot user with email": {
			vars:     map[string]string{"FULLSEND_JIRA_TOKEN": "scoped-token", "FULLSEND_JIRA_EMAIL": "bot@example.com"},
			wantAuth: "Basic " + base64.StdEncoding.EncodeToString([]byte("bot@example.com:scoped-token")),
		},
	} {
		t.Run(name, func(t *testing.T) {
			var gotAuth string
			srv := jiraGatewayServer(t, &gotAuth)
			c, err := buildJiraClient(jiraPollSettings{baseURL: srv.URL + "/site", apiURL: srv.URL + "/gw"}, env(tc.vars))
			require.NoError(t, err)
			me, err := c.GetMyself(context.Background())
			require.NoError(t, err, "requests go through the configured API gateway")
			assert.Equal(t, "acct-1", me.AccountID)
			assert.Equal(t, tc.wantAuth, gotAuth)
		})
	}
}

func TestRunJiraPoll_RejectsRemovedFlags(t *testing.T) {
	dir := writeJiraPollConfig(t, enabledJiraPollConfig)
	err := runJiraPoll(nil, "", "PROJ", "", "acme/repo", "out.json", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--jira-project no longer supported")

	err = runJiraPollGitLab(nil, jiraPollGitLabFlags{jiraProject: "PROJ", fullsendDir: dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--jira-project no longer supported")
}

// TestRunJiraPoll_DisabledSkipsCycle checks that poll.enabled other than
// true ends the run before any credential is read or Jira is contacted:
// no Jira or GitLab credentials are set, so reaching either would fail.
func TestRunJiraPoll_DisabledSkipsCycle(t *testing.T) {
	for name, body := range map[string]string{
		"poll omitted":  "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n",
		"enabled false": "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n    poll:\n      enabled: false\n",
	} {
		t.Run(name, func(t *testing.T) {
			clearPollEnv(t)
			dir := writeJiraPollConfig(t, body)

			var stderr strings.Builder
			cmd := &cobra.Command{}
			cmd.SetErr(&stderr)
			out := filepath.Join(t.TempDir(), "dispatches.json")
			require.NoError(t, runJiraPoll(cmd, "", "", "", "acme/repo", out, dir))
			data, err := os.ReadFile(out)
			require.NoError(t, err)
			assert.Equal(t, "[]\n", string(data), "a disabled cycle writes no dispatches")
			assert.Contains(t, stderr.String(), "Jira polling is disabled")

			stderr.Reset()
			require.NoError(t, runJiraPollGitLab(cmd, jiraPollGitLabFlags{
				projectPath: "group/project",
				gitlabURL:   "https://gitlab.com",
				fullsendDir: dir,
			}))
			assert.Contains(t, stderr.String(), "Jira polling is disabled")
		})
	}
}

func TestSkipDisabledJiraPoll_WriteError(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetErr(&strings.Builder{})
	err := skipDisabledJiraPoll(cmd, filepath.Join(t.TempDir(), "missing", "dispatches.json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write dispatches")
}

func TestResolveConfiguredJiraURL(t *testing.T) {
	configured := "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n"

	t.Run("config base_url", func(t *testing.T) {
		got, err := resolveConfiguredJiraURL("", writeJiraPollConfig(t, configured), nil)
		require.NoError(t, err)
		assert.Equal(t, "https://example.atlassian.net", got)
	})
	t.Run("test config reader", func(t *testing.T) {
		reader, err := config.ParsePerRepoConfig([]byte(configured))
		require.NoError(t, err)
		got, err := resolveConfiguredJiraURL("", "", reader)
		require.NoError(t, err)
		assert.Equal(t, "https://example.atlassian.net", got)
	})
	t.Run("flag rejected with config", func(t *testing.T) {
		_, err := resolveConfiguredJiraURL("https://other.atlassian.net", writeJiraPollConfig(t, configured), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--jira-url cannot be used")
	})
	t.Run("invalid config", func(t *testing.T) {
		_, err := resolveConfiguredJiraURL("", writeJiraPollConfig(t, "tracker:\n  jira:\n    project: PROJ\n"), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "base_url is required")
	})
	t.Run("malformed config", func(t *testing.T) {
		_, err := resolveConfiguredJiraURL("", writeJiraPollConfig(t, "tracker:\n  jira:\n    email: a@example.com\n"), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "loading config for tracker.jira")
	})
	t.Run("selection only keeps flag", func(t *testing.T) {
		got, err := resolveConfiguredJiraURL("https://flag.atlassian.net", writeJiraPollConfig(t, "tracker:\n  jira: {}\n"), nil)
		require.NoError(t, err)
		assert.Equal(t, "https://flag.atlassian.net", got)
	})
	t.Run("no fullsend dir keeps flag", func(t *testing.T) {
		got, err := resolveConfiguredJiraURL("https://flag.atlassian.net", "", nil)
		require.NoError(t, err)
		assert.Equal(t, "https://flag.atlassian.net", got)
	})
}

func TestNewConfiguredTrackerClient(t *testing.T) {
	dir := writeJiraPollConfig(t, "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n")
	t.Setenv("JIRA_BASE_URL", "")

	tc, err := newConfiguredTrackerClient(trackerJira, "token123", "", "user@example.com", dir, nil)
	require.NoError(t, err, "base_url from config replaces --jira-url and JIRA_BASE_URL")
	assert.NotNil(t, tc)

	_, err = newConfiguredTrackerClient(trackerJira, "token123", "https://other.atlassian.net", "user@example.com", dir, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--jira-url cannot be used")

	// Non-Jira trackers do not read tracker.jira.
	tc, err = newConfiguredTrackerClient(trackerGitLab, "glpat-test123", "", "", dir, nil)
	require.NoError(t, err)
	assert.NotNil(t, tc)
}
