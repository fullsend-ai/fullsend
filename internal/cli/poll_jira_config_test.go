package cli

import (
	"os"
	"path/filepath"
	"testing"

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

func TestApplyJiraTrackerConfig_ReadsConfig(t *testing.T) {
	dir := writeJiraPollConfig(t, `tracker:
  jira:
    base_url: https://example.atlassian.net
    project: PROJ
    poll:
      enabled: false
      query: 'labels = "fullsend" ORDER BY updated DESC'
`)
	var jiraURL, jiraProject, jql string
	require.NoError(t, applyJiraTrackerConfig(dir, &jiraURL, &jiraProject, &jql))
	assert.Equal(t, "https://example.atlassian.net", jiraURL)
	assert.Equal(t, "PROJ", jiraProject)
	assert.Equal(t, `project = "PROJ" AND (labels = "fullsend") ORDER BY updated DESC`, jql)
}

func TestApplyJiraTrackerConfig_DefaultQuery(t *testing.T) {
	dir := writeJiraPollConfig(t, "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n")
	var jiraURL, jiraProject, jql string
	require.NoError(t, applyJiraTrackerConfig(dir, &jiraURL, &jiraProject, &jql))
	assert.Equal(t, `project = "PROJ" AND (statusCategory != Done) ORDER BY updated DESC`, jql)
}

func TestApplyJiraTrackerConfig_RejectsFlagOverrides(t *testing.T) {
	dir := writeJiraPollConfig(t, "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n")
	jiraURL, jiraProject, jql := "https://other.atlassian.net", "", "project = X"
	err := applyJiraTrackerConfig(dir, &jiraURL, &jiraProject, &jql)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--jira-url, --jql cannot be used when tracker.jira is configured")
}

func TestApplyJiraTrackerConfig_InvalidConfig(t *testing.T) {
	dir := writeJiraPollConfig(t, "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n")
	var jiraURL, jiraProject, jql string
	err := applyJiraTrackerConfig(dir, &jiraURL, &jiraProject, &jql)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project is required")
}

func TestApplyJiraTrackerConfig_LegacyFlagsWithoutSettings(t *testing.T) {
	for name, body := range map[string]string{
		"no config tracker": "",
		"selection only":    "tracker:\n  jira: {}\n",
		"github tracker":    "tracker:\n  github: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeJiraPollConfig(t, body)
			jiraURL, jiraProject, jql := "https://flag.atlassian.net", "FLAG", ""
			require.NoError(t, applyJiraTrackerConfig(dir, &jiraURL, &jiraProject, &jql))
			assert.Equal(t, "https://flag.atlassian.net", jiraURL)
			assert.Equal(t, "FLAG", jiraProject)
			assert.Empty(t, jql)
		})
	}
}

func TestApplyJiraTrackerConfig_MissingConfigDir(t *testing.T) {
	var jiraURL, jiraProject, jql string
	require.NoError(t, applyJiraTrackerConfig(t.TempDir(), &jiraURL, &jiraProject, &jql))
	assert.Empty(t, jiraURL)
}

func TestApplyJiraTrackerConfig_MalformedConfig(t *testing.T) {
	dir := writeJiraPollConfig(t, "tracker:\n  jira:\n    token: abc\n")
	var jiraURL, jiraProject, jql string
	err := applyJiraTrackerConfig(dir, &jiraURL, &jiraProject, &jql)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load config")
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

func TestRunJiraPoll_RejectsFlagWithConfig(t *testing.T) {
	dir := writeJiraPollConfig(t, "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n")
	err := runJiraPoll(nil, "", "PROJ", "", "acme/repo", "out.json", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--jira-project cannot be used")

	err = runJiraPollGitLab(nil, jiraPollGitLabFlags{jiraProject: "PROJ", fullsendDir: dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--jira-project cannot be used")
}
