package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const jiraTrackerYAML = `version: "1"
tracker:
  jira:
    base_url: https://example.atlassian.net
    api_url: https://gateway.example.com
    project: PROJ
    allowed_targets:
      projects: [OTHER]
    poll:
      enabled: true
      query: 'labels = "fullsend" AND statusCategory != Done ORDER BY updated DESC'
`

func TestJiraTracking_ParseFullMapping(t *testing.T) {
	cfg, err := ParsePerRepoConfigWriter([]byte(jiraTrackerYAML))
	require.NoError(t, err)
	require.NoError(t, cfg.Validate())

	jira := cfg.IssueTracking().Jira
	require.NotNil(t, jira)
	assert.Equal(t, TrackingProviderJira, cfg.IssueTracking().Provider())
	assert.Equal(t, "https://example.atlassian.net", jira.BaseURL)
	assert.Equal(t, "https://gateway.example.com", jira.EffectiveAPIURL())
	assert.Equal(t, "PROJ", jira.Project)
	assert.Equal(t, []string{"OTHER"}, jira.AllowedTargets.Projects)
	assert.True(t, jira.PollEnabled())
	assert.Equal(t, `project = PROJ AND (labels = "fullsend" AND statusCategory != Done) ORDER BY updated DESC`, jira.PollJQL())

	// Round trip through Marshal.
	data, err := cfg.Marshal()
	require.NoError(t, err)
	parsed, err := ParsePerRepoConfig(data)
	require.NoError(t, err)
	assert.Equal(t, cfg.IssueTracking(), parsed.IssueTracking())
}

func TestJiraTracking_Defaults(t *testing.T) {
	cfg, err := ParsePerRepoConfigWriter([]byte("version: \"1\"\ntracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n"))
	require.NoError(t, err)
	require.NoError(t, cfg.Validate())
	jira := cfg.IssueTracking().Jira
	assert.Equal(t, DefaultJiraAPIURL, jira.EffectiveAPIURL())
	assert.False(t, jira.PollEnabled(), "omitted poll does not install the poll workflow")
	assert.Equal(t, DefaultJiraPollQuery, jira.PollQuery())
	assert.Equal(t, "project = PROJ AND (statusCategory != Done) ORDER BY updated DESC", jira.PollJQL())

	var nilJira *JiraTrackingConfig
	assert.Equal(t, DefaultJiraAPIURL, nilJira.EffectiveAPIURL())
	assert.False(t, nilJira.PollEnabled())
	assert.Equal(t, DefaultJiraPollQuery, nilJira.PollQuery())
	assert.False(t, nilJira.HasConnectionSettings())
	assert.NoError(t, nilJira.ValidateConnection())
}

func TestJiraTracking_PollEnabledFalse(t *testing.T) {
	cfg, err := ParsePerRepoConfigWriter([]byte("version: \"1\"\ntracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n    poll:\n      enabled: false\n"))
	require.NoError(t, err)
	require.NoError(t, cfg.Validate())
	jira := cfg.IssueTracking().Jira
	require.NotNil(t, jira.Poll)
	require.NotNil(t, jira.Poll.Enabled)
	assert.False(t, jira.PollEnabled())
}

func TestJiraTracking_Validate(t *testing.T) {
	cases := []struct {
		name    string
		jira    string
		wantErr string
	}{
		{name: "selection only stays valid", jira: "{}"},
		{name: "allowed targets only stays valid", jira: "\n    allowed_targets:\n      projects: [OTHER]"},
		{name: "missing base_url", jira: "\n    project: PROJ", wantErr: "base_url is required"},
		{name: "missing project", jira: "\n    base_url: https://example.atlassian.net", wantErr: "project is required"},
		{name: "poll without connection", jira: "\n    poll:\n      enabled: true", wantErr: "base_url is required"},
		{name: "api_url without connection", jira: "\n    api_url: https://api.atlassian.com", wantErr: "base_url is required"},
		{name: "non-https base_url", jira: "\n    base_url: ftp://example.atlassian.net\n    project: PROJ", wantErr: "base_url \"ftp://example.atlassian.net\" must be an https URL"},
		{name: "base_url with credentials", jira: "\n    base_url: https://u:p@example.atlassian.net\n    project: PROJ", wantErr: "must be an https URL"},
		{name: "base_url with query", jira: "\n    base_url: https://example.atlassian.net?x=1\n    project: PROJ", wantErr: "must be an https URL"},
		{name: "bad api_url", jira: "\n    base_url: https://example.atlassian.net\n    api_url: ftp://gw\n    project: PROJ", wantErr: "api_url"},
		{name: "bad project key", jira: "\n    base_url: https://example.atlassian.net\n    project: proj", wantErr: "project \"proj\" must be a Jira project key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := ParsePerRepoConfigWriter([]byte("version: \"1\"\ntracker:\n  jira: " + c.jira + "\n"))
			require.NoError(t, err)
			err = cfg.Validate()
			if c.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.wantErr)
		})
	}
}

func TestJiraTracking_MultipleProvidersStillRejected(t *testing.T) {
	cfg, err := ParsePerRepoConfigWriter([]byte("version: \"1\"\ntracker:\n  github: {}\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n"))
	require.NoError(t, err)
	err = cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most one provider mapping")
}

func TestJiraTracking_RejectsCredentialAndUnknownKeys(t *testing.T) {
	cases := []struct {
		name    string
		jira    string
		wantErr string
	}{
		{name: "token", jira: "token: abc", wantErr: "FULLSEND_JIRA_TOKEN"},
		{name: "email", jira: "email: a@example.com", wantErr: "\"email\" must not be set in config"},
		{name: "api_token uppercase", jira: "API_TOKEN: abc", wantErr: "must not be set in config"},
		{name: "unknown key", jira: "base_uri: https://example.atlassian.net", wantErr: "tracker.jira: unknown field \"base_uri\""},
		{name: "unknown poll key", jira: "poll:\n      interval: 5m", wantErr: "tracker.jira.poll: unknown field \"interval\""},
		{name: "unknown allowed_targets key", jira: "allowed_targets:\n      repos: [a/b]", wantErr: "tracker.jira.allowed_targets: unknown field \"repos\""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParsePerRepoConfig([]byte("version: \"1\"\ntracker:\n  jira:\n    " + c.jira + "\n"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.wantErr)

			// The managed (repos.yaml config:) decode rejects it too.
			var m ManagedConfig
			err = yaml.Unmarshal([]byte("tracker:\n  jira:\n    "+c.jira+"\n"), &m)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.wantErr)
		})
	}
}

func TestJiraTracking_ManagedConfigSurvivesMergeAndClone(t *testing.T) {
	var m ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte(`tracker:
  jira:
    base_url: https://example.atlassian.net
    project: PROJ
    allowed_targets:
      projects: [OTHER]
    poll:
      enabled: true
      query: labels = "fullsend"
`), &m))
	require.True(t, m.IsSet())
	want := m.Writer().IssueTracking()
	require.NotNil(t, want.Jira)
	require.NoError(t, m.Writer().Validate())

	clone := m.Clone()
	assert.Equal(t, want, clone.Writer().IssueTracking())

	merged := MergeManaged(&perRepoConfig{}, m.Writer())
	got := merged.IssueTracking()
	assert.Equal(t, want, got)
	assert.Equal(t, `project = PROJ AND (labels = "fullsend")`, got.Jira.PollJQL())

	// The clone is deep: mutating it leaves the source untouched.
	copied := cloneTracking(want)
	*copied.Jira.Poll.Enabled = false
	copied.Jira.AllowedTargets.Projects[0] = "CHANGED"
	assert.True(t, want.Jira.PollEnabled())
	assert.Equal(t, "OTHER", want.Jira.AllowedTargets.Projects[0])

	// Re-marshal through the managed form keeps the settings.
	data, err := yaml.Marshal(m)
	require.NoError(t, err)
	var again ManagedConfig
	require.NoError(t, yaml.Unmarshal(data, &again))
	assert.Equal(t, want, again.Writer().IssueTracking())
}

func TestScopeJQLToProject(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{name: "with order by", query: `labels = "fullsend" AND statusCategory != Done ORDER BY updated DESC`, want: `project = PROJ AND (labels = "fullsend" AND statusCategory != Done) ORDER BY updated DESC`},
		{name: "without order by", query: `labels = "fullsend"`, want: `project = PROJ AND (labels = "fullsend")`},
		{name: "or stays grouped", query: `labels = a OR labels = b`, want: `project = PROJ AND (labels = a OR labels = b)`},
		{name: "lowercase order by", query: `status = Open order by created`, want: `project = PROJ AND (status = Open) order by created`},
		{name: "order by inside quotes is not split", query: `summary ~ "order by" AND status = Open`, want: `project = PROJ AND (summary ~ "order by" AND status = Open)`},
		{name: "order by inside single quotes", query: `summary ~ 'x order by y' ORDER BY key`, want: `project = PROJ AND (summary ~ 'x order by y') ORDER BY key`},
		{name: "escaped quote", query: `summary ~ "a \" order by" ORDER BY key`, want: `project = PROJ AND (summary ~ "a \" order by") ORDER BY key`},
		{name: "only order by", query: `ORDER BY updated DESC`, want: `project = PROJ ORDER BY updated DESC`},
		{name: "unterminated quote", query: `summary ~ "order by`, want: `project = PROJ AND (summary ~ "order by)`},
		{name: "word containing order is not split", query: `labels = reorder`, want: `project = PROJ AND (labels = reorder)`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, ScopeJQLToProject("PROJ", c.query))
		})
	}
}
