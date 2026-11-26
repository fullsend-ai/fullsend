package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseEntityConfig parses one config.yaml layer.
func parseEntityConfig(t *testing.T, body string) PerRepoConfigReader {
	t.Helper()
	r, err := ParsePerRepoConfig([]byte(body))
	require.NoError(t, err)
	return r
}

var githubOrigin = &ConfigOrigin{Provider: ConnectionKindGitHub, Host: "github.com", Repository: "example/project"}

const contractExample = `
connections:
  jira1:
    kind: jira
    base_url: https://example.atlassian.net
    api_url: https://api.atlassian.com
entity_sources:
  github:
    query: 'is:open -label:blocked'
  jira_work:
    connection: jira1
    projects: [PROJ1]
    query: 'statusCategory != Done'
    discovery: true
  jira_related:
    connection: jira1
    projects: [PROJ1, PROJ2]
    query: ''
agents:
  - name: code
    entities:
      source: [jira_work]
      target: [native]
      related_work: [jira_related]
  - name: triage
    entities:
      source: [native, jira_work]
`

func TestResolveEntityConfig_ContractExample(t *testing.T) {
	r := parseEntityConfig(t, contractExample)
	require.NoError(t, r.(interface{ Validate() error }).Validate())
	ec, err := ResolveEntityConfig(r, githubOrigin)
	require.NoError(t, err)

	native := ec.Sources["github"]
	assert.Equal(t, EntryExplicit, native.Origin)
	assert.Equal(t, "https://github.com", native.Connection.BaseURL, "the origin host fills the provider-default connection")
	assert.Equal(t, EntrySynthesized, native.Connection.Origin)
	assert.Equal(t, []string{"example/project"}, native.Repositories, "the clone path is the default scope")

	code, err := ec.AgentBindings("code", map[string]string{"source": EntityKindWorkItem, "target": EntityKindChangeProposal, "related_work": EntityKindWorkItem})
	require.NoError(t, err)
	assert.Equal(t, "jira_work", code["source"].Sources[0].Name)
	assert.Equal(t, "github", code["target"].Sources[0].Name)
	assert.Equal(t, []string{"PROJ1", "PROJ2"}, code["related_work"].Sources[0].Projects)

	kinds, err := ResolveEntityKinds(map[string]string{})
	require.NoError(t, err)
	triage, err := ec.AgentBindings("triage", kinds)
	require.NoError(t, err)
	require.Len(t, triage["target"].Sources, 2, "an omitted target copies the source list")
	assert.Equal(t, "github", triage["target"].Sources[0].Name)
	assert.Equal(t, "jira_work", triage["target"].Sources[1].Name)

	// Only jira_work is bound to a primary source slot.
	scope, err := ec.JiraDiscoveryScope()
	require.NoError(t, err)
	assert.True(t, scope.Enabled)
	assert.Equal(t, []string{"jira_work"}, scope.Sources)
	assert.Equal(t, []string{"PROJ1"}, scope.Projects)
	assert.Equal(t, "https://example.atlassian.net", scope.Connection.BaseURL)
}

func TestAgentBindings_Defaults(t *testing.T) {
	r := parseEntityConfig(t, "agents:\n  - name: triage\n    entities:\n      source: [native, github]\n")
	ec, err := ResolveEntityConfig(r, githubOrigin)
	require.NoError(t, err)
	kinds, _ := ResolveEntityKinds(map[string]string{})
	got, err := ec.AgentBindings("triage", kinds)
	require.NoError(t, err)
	assert.Len(t, got["source"].Sources, 1, "native and its provider name deduplicate")
	assert.Equal(t, "", got["source"].Sources[0].Query, "the synthesized GitHub native source is all-state")

	// An agent without bindings defaults to [native] for both slots.
	got, err = ec.AgentBindings("review", kinds)
	require.NoError(t, err)
	assert.Equal(t, "github", got["source"].Sources[0].Name)
	assert.Equal(t, "github", got["target"].Sources[0].Name)

	// An explicit empty source suppresses defaults, and the target copies it.
	r = parseEntityConfig(t, "agents:\n  - name: triage\n    entities:\n      source: []\n")
	ec, err = ResolveEntityConfig(r, githubOrigin)
	require.NoError(t, err)
	got, err = ec.AgentBindings("triage", kinds)
	require.NoError(t, err)
	assert.Empty(t, got["source"].Sources)
	assert.Empty(t, got["target"].Sources)

	_, err = ec.AgentBindings("triage", nil)
	require.Error(t, err, "a legacy harness without entity_kinds has no bindings")
}

func TestAgentBindings_GitLabOrigin(t *testing.T) {
	r := parseEntityConfig(t, "agents:\n  - name: triage\n")
	ec, err := ResolveEntityConfig(r, &ConfigOrigin{Provider: ConnectionKindGitLab, Host: "gitlab.example.com", Repository: "group/sub/project"})
	require.NoError(t, err)
	src := ec.Sources["gitlab"]
	assert.Equal(t, "all", src.Query)
	assert.Equal(t, "https://gitlab.example.com", src.Connection.BaseURL)
	assert.Equal(t, []string{"group/sub/project"}, src.Repositories)
}

func TestAgentBindings_Errors(t *testing.T) {
	jiraConn := "connections:\n  jira1:\n    kind: jira\n    base_url: https://example.atlassian.net\n" +
		"entity_sources:\n  jira_work:\n    connection: jira1\n    projects: [PROJ]\n    query: ''\n"
	for name, tc := range map[string]struct {
		body        string
		kinds       map[string]string
		origin      *ConfigOrigin
		wantErr     string
		unsupported bool
	}{
		"jira target needs explicit change-proposal target": {
			body:        jiraConn + "agents:\n  - name: code\n    entities:\n      source: [jira_work]\n",
			kinds:       map[string]string{"source": EntityKindWorkItem, "target": EntityKindChangeProposal},
			wantErr:     "does not support the change_proposal kind",
			unsupported: true,
		},
		"unknown slot": {
			body:    "agents:\n  - name: code\n    entities:\n      extra: [native]\n",
			kinds:   map[string]string{"source": EntityKindWorkItem, "target": EntityKindWorkItem},
			wantErr: "unknown slot",
		},
		"additional slot needs a binding": {
			body:    "agents:\n  - name: code\n",
			kinds:   map[string]string{"source": EntityKindWorkItem, "target": EntityKindWorkItem, "related": EntityKindWorkItem},
			wantErr: "explicit, nonempty binding list",
		},
		"native without origin": {
			body:    "agents:\n  - name: code\n",
			kinds:   map[string]string{"source": EntityKindWorkItem, "target": EntityKindWorkItem},
			origin:  nil,
			wantErr: "no trusted configuration origin",
		},
		"unbound cross-provider preset": {
			body:    "entity_sources:\n  gitlab:\n    query: open\nagents:\n  - name: code\n    entities:\n      source: [gitlab]\n",
			kinds:   map[string]string{"source": EntityKindWorkItem, "target": EntityKindWorkItem},
			wantErr: "preset for another provider",
		},
		"undeclared provider name": {
			body:    "agents:\n  - name: code\n    entities:\n      source: [gitlab]\n",
			kinds:   map[string]string{"source": EntityKindWorkItem, "target": EntityKindWorkItem},
			wantErr: "needs connections.gitlab",
		},
		"query kind assertion disagrees": {
			body:    "entity_sources:\n  github:\n    query: 'is:pr is:open'\n",
			kinds:   map[string]string{"source": EntityKindWorkItem, "target": EntityKindWorkItem},
			wantErr: "applies to change_proposal entities",
		},
	} {
		t.Run(name, func(t *testing.T) {
			origin := githubOrigin
			if name == "native without origin" {
				origin = tc.origin
			}
			ec, err := ResolveEntityConfig(parseEntityConfig(t, tc.body), origin)
			require.NoError(t, err)
			_, err = ec.AgentBindings("code", tc.kinds)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Equal(t, tc.unsupported, errors.Is(err, ErrEntityUnsupported))
		})
	}
}

func TestResolveEntityConfig_ExternalGitLabSource(t *testing.T) {
	body := `
connections:
  github:
    kind: github
    base_url: https://github.example.com
  gitlab:
    kind: gitlab
    base_url: https://gitlab.example.com
entity_sources:
  github:
    query: 'is:open -label:blocked'
  gitlab:
    repositories: [example/related-work]
    query: open
agents:
  - name: triage
    entities:
      source: [native, gitlab]
      target: [github]
`
	r := parseEntityConfig(t, body)
	require.NoError(t, r.(interface{ Validate() error }).Validate())
	ec, err := ResolveEntityConfig(r, githubOrigin)
	require.NoError(t, err)
	assert.Equal(t, "https://github.example.com", ec.Sources["github"].Connection.BaseURL, "an explicit endpoint wins over the clone host")
	assert.Equal(t, []string{"example/project"}, ec.Sources["github"].Repositories, "the clone path carries over to the enterprise endpoint")
	got, err := ec.AgentBindings("triage", map[string]string{"source": EntityKindWorkItem, "target": EntityKindWorkItem})
	require.NoError(t, err)
	require.Len(t, got["source"].Sources, 2)
	assert.Equal(t, []string{"example/related-work"}, got["source"].Sources[1].Repositories)
}

func TestResolveEntityConfig_ValidationErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		body        string
		wantErr     string
		unsupported bool
	}{
		"native source name": {
			body: "entity_sources:\n  native:\n    query: ''\n", wantErr: "reserved native binding selector",
		},
		"source declares kind": {
			body: "entity_sources:\n  s:\n    kind: work_item\n    query: ''\n", wantErr: "must not declare kind",
		},
		"unknown source field": {
			body: "entity_sources:\n  s:\n    connection: jira1\n    queries: ''\n", wantErr: "queries",
		},
		"missing query": {
			body: "connections:\n  jira1:\n    kind: jira\n    base_url: https://example.atlassian.net\nentity_sources:\n  s:\n    connection: jira1\n    projects: [A]\n", wantErr: "query is required",
		},
		"missing connection": {
			body: "entity_sources:\n  s:\n    projects: [A]\n    query: ''\n", wantErr: `connection "s" is not declared`,
		},
		"reserved source with other connection": {
			body: "connections:\n  enterprise:\n    kind: github\n    base_url: https://github.example.com\nentity_sources:\n  github:\n    connection: enterprise\n    query: ''\n", wantErr: "uses only its same-named connection",
		},
		"jira empty projects": {
			body: "connections:\n  jira1:\n    kind: jira\n    base_url: https://example.atlassian.net\nentity_sources:\n  s:\n    connection: jira1\n    projects: []\n    query: ''\n", wantErr: "nonempty projects",
		},
		"jira duplicate projects": {
			body: "connections:\n  jira1:\n    kind: jira\n    base_url: https://example.atlassian.net\nentity_sources:\n  s:\n    connection: jira1\n    projects: [A, A]\n    query: ''\n", wantErr: "duplicate entry",
		},
		"jira bad project key": {
			body: "connections:\n  jira1:\n    kind: jira\n    base_url: https://example.atlassian.net\nentity_sources:\n  s:\n    connection: jira1\n    projects: [lower]\n    query: ''\n", wantErr: "must be a Jira project key",
		},
		"jira unbalanced query": {
			body: "connections:\n  jira1:\n    kind: jira\n    base_url: https://example.atlassian.net\nentity_sources:\n  s:\n    connection: jira1\n    projects: [A]\n    query: 'a = b) OR (c = d'\n", wantErr: "unbalanced parentheses",
		},
		"vault connection for entities": {
			body: "connections:\n  v:\n    kind: vault\n    vault_address: https://vault.example.com\nentity_sources:\n  s:\n    connection: v\n    repositories: [a/b]\n    query: ''\n", wantErr: "not an entity connection kind",
		},
		"forgejo adapter": {
			body: "connections:\n  forgejo:\n    base_url: https://forgejo.example.com\nentity_sources:\n  forgejo:\n    repositories: [a/b]\n    query: ''\n", wantErr: "no enabled entity adapter", unsupported: true,
		},
		"named forge connection needs repositories": {
			body: "connections:\n  ghe:\n    kind: github\n    base_url: https://github.example.com\nentity_sources:\n  s:\n    connection: ghe\n    query: ''\n", wantErr: "repositories is required",
		},
		"gitlab bad query": {
			body: "connections:\n  gitlab:\n    base_url: https://gitlab.example.com\nentity_sources:\n  s:\n    connection: gitlab\n    repositories: [a/b]\n    query: opened\n", wantErr: "use open, closed, or all",
		},
		"forge source with projects": {
			body: "entity_sources:\n  github:\n    projects: [A]\n    query: ''\n", wantErr: "projects is valid only for Jira",
		},
		"unknown binding name": {
			body: "agents:\n  - name: code\n    entities:\n      source: [nowhere]\n", wantErr: `"nowhere" is not an entity source`,
		},
		"binding not a list": {
			body: "agents:\n  - name: code\n    entities:\n      source: native\n", wantErr: "must be a list of scope names",
		},
		"duplicate binding": {
			body: "agents:\n  - name: code\n    entities:\n      source: [native, native]\n", wantErr: "duplicate scope",
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := ParsePerRepoConfig([]byte(tc.body))
			if err == nil {
				_, err = ResolveEntityConfig(r, githubOrigin)
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Equal(t, tc.unsupported, errors.Is(err, ErrEntityUnsupported))
		})
	}
}

func TestValidateSecretConnection_EntityKinds(t *testing.T) {
	for name, tc := range map[string]struct {
		conn    SecretConnection
		connKey string
		wantErr string
	}{
		"reserved implied kind":   {conn: SecretConnection{BaseURL: "https://github.example.com"}, connKey: "github"},
		"reserved kind mismatch":  {conn: SecretConnection{Kind: "gitlab"}, connKey: "github", wantErr: "reserved provider-default connection"},
		"jira needs base_url":     {conn: SecretConnection{Kind: "jira"}, connKey: "j", wantErr: "base_url is required"},
		"credentials in base_url": {conn: SecretConnection{Kind: "jira", BaseURL: "https://user:pw@example.atlassian.net"}, connKey: "j", wantErr: "without credentials"},
		"api_url only jira":       {conn: SecretConnection{Kind: "github", APIURL: "https://api.example.com"}, connKey: "g", wantErr: "api_url is valid only with kind: jira"},
		"vault fields on entity":  {conn: SecretConnection{Kind: "gitlab", VaultRole: "r"}, connKey: "g", wantErr: "vault_* fields"},
		"entity fields on vault":  {conn: SecretConnection{Kind: "vault", VaultAddress: "https://vault.example.com", BaseURL: "https://x.example.com"}, connKey: "v", wantErr: "valid only with an entity connection kind"},
		"jira targets":            {conn: SecretConnection{Kind: "jira", BaseURL: "https://example.atlassian.net", AllowedTargets: &ConnectionAllowedTargets{Projects: []string{"A"}}}, connKey: "j"},
		"jira targets repos":      {conn: SecretConnection{Kind: "jira", BaseURL: "https://example.atlassian.net", AllowedTargets: &ConnectionAllowedTargets{Repos: []string{"a/b"}}}, connKey: "j", wantErr: "accepts only projects"},
		"github targets groups":   {conn: SecretConnection{Kind: "github", AllowedTargets: &ConnectionAllowedTargets{Groups: []string{"g"}}}, connKey: "g", wantErr: "accepts only orgs and repos"},
		"github bad repo":         {conn: SecretConnection{Kind: "github", AllowedTargets: &ConnectionAllowedTargets{Repos: []string{"a"}}}, connKey: "g", wantErr: "must be owner/repo"},
		"gitlab targets":          {conn: SecretConnection{Kind: "gitlab", AllowedTargets: &ConnectionAllowedTargets{Groups: []string{"g/s"}, Repos: []string{"g/s/p"}}}, connKey: "g"},
		"gitlab targets orgs":     {conn: SecretConnection{Kind: "gitlab", AllowedTargets: &ConnectionAllowedTargets{Orgs: []string{"o"}}}, connKey: "g", wantErr: "accepts only groups and repos"},
		"unknown kind":            {conn: SecretConnection{Kind: "bitbucket"}, connKey: "b", wantErr: "unknown kind"},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateSecretConnection(tc.connKey, tc.conn)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
	err := validateSecretSources(map[string]SecretConnection{"j": {Kind: "jira", BaseURL: "https://example.atlassian.net"}},
		map[string]SecretSource{"JIRA_TOKEN": {Connection: "j"}}, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a secret service")
}

func TestResolveEntityConfig_LegacyTracker(t *testing.T) {
	t.Run("jira becomes a one-project scope", func(t *testing.T) {
		r := parseEntityConfig(t, `tracker:
  jira:
    base_url: https://example.atlassian.net
    api_url: https://gateway.example.com
    project: PROJ
    allowed_targets:
      projects: [OTHER]
    poll:
      enabled: true
      query: 'labels = x ORDER BY created ASC'
`)
		ec, err := ResolveEntityConfig(r, nil)
		require.NoError(t, err)
		conn := ec.Connections[LegacyTrackerName]
		assert.Equal(t, EntryTranslated, conn.Origin)
		assert.Equal(t, ConnectionKindJira, conn.Kind)
		assert.Equal(t, []string{"OTHER", "PROJ"}, conn.AllowedTargets.Projects, "the primary project keeps its implicit grant")
		src := ec.Sources[LegacyTrackerName]
		assert.Equal(t, []string{"PROJ"}, src.Projects)
		assert.True(t, src.Discovery)
		assert.Equal(t, "labels = x ORDER BY created ASC", src.Query)

		scope, err := ec.JiraDiscoveryScope()
		require.NoError(t, err)
		assert.True(t, scope.Enabled)
		assert.Equal(t, []string{"PROJ"}, scope.Projects)
		assert.Equal(t, "https://gateway.example.com", scope.Connection.EffectiveAPIURL())

		// Normalization creates no entity bindings.
		for _, a := range r.AgentEntries() {
			assert.Empty(t, a.Entities)
		}
	})
	t.Run("poll disabled", func(t *testing.T) {
		r := parseEntityConfig(t, "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n")
		ec, err := ResolveEntityConfig(r, nil)
		require.NoError(t, err)
		scope, err := ec.JiraDiscoveryScope()
		require.NoError(t, err)
		assert.False(t, scope.Enabled)
		assert.Equal(t, []string{"PROJ"}, scope.Projects)
		assert.Equal(t, DefaultJiraPollQuery, scope.Query)
	})
	t.Run("provider-only blocks attach allowed_targets to legacy_tracker", func(t *testing.T) {
		for body, want := range map[string]ConnectionAllowedTargets{
			"tracker:\n  github:\n    allowed_targets:\n      orgs: [o]\n      repos: [a/b]\n": {Orgs: []string{"o"}, Repos: []string{"a/b"}},
			"tracker:\n  gitlab:\n    allowed_targets:\n      groups: [g]\n":                   {Groups: []string{"g"}},
			"tracker:\n  jira:\n    allowed_targets:\n      projects: [P]\n":                   {Projects: []string{"P"}},
		} {
			ec, err := ResolveEntityConfig(parseEntityConfig(t, body), githubOrigin)
			require.NoError(t, err)
			assert.Equal(t, want, ec.Connections[LegacyTrackerName].AllowedTargets)
			_, hasSource := ec.Sources[LegacyTrackerName]
			assert.False(t, hasSource, "a provider-only block creates no discovery scope")
			assert.Empty(t, ec.Connections["github"].AllowedTargets, "grants are not copied to a provider-default connection")
		}
	})
	t.Run("incomplete jira keeps its use-time failure", func(t *testing.T) {
		ec, err := ResolveEntityConfig(parseEntityConfig(t, "tracker:\n  jira:\n    project: PROJ\n"), nil)
		require.NoError(t, err)
		_, err = ec.JiraDiscoveryScope()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "base_url is required")
	})
	t.Run("explicit entries shadow translated ones", func(t *testing.T) {
		r := parseEntityConfig(t, `tracker:
  jira:
    base_url: https://example.atlassian.net
    project: PROJ
    poll:
      enabled: true
connections:
  legacy_tracker:
    kind: jira
    base_url: https://other.atlassian.net
entity_sources:
  legacy_tracker:
    projects: [A, B]
    query: ''
agents:
  - name: triage
    entities:
      source: [legacy_tracker]
`)
		ec, err := ResolveEntityConfig(r, nil)
		require.NoError(t, err)
		assert.Len(t, ec.Diagnostics, 2)
		assert.Contains(t, ec.Diagnostics[0], "connections.legacy_tracker replaces")
		assert.Contains(t, ec.Diagnostics[1], "entity_sources.legacy_tracker replaces")
		scope, err := ec.JiraDiscoveryScope()
		require.NoError(t, err)
		assert.Equal(t, []string{"A", "B"}, scope.Projects)
		assert.Equal(t, "https://other.atlassian.net", scope.Connection.BaseURL)
	})
	t.Run("replacement connection must stay jira", func(t *testing.T) {
		r := parseEntityConfig(t, "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\nconnections:\n  legacy_tracker:\n    kind: github\n")
		_, err := ResolveEntityConfig(r, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must have kind: jira")
	})
}

func TestJiraDiscoveryScope_Selection(t *testing.T) {
	base := `connections:
  jira1:
    kind: jira
    base_url: https://example.atlassian.net
  jira2:
    kind: jira
    base_url: https://other.atlassian.net
entity_sources:
  a:
    connection: jira1
    projects: [A, B]
    query: 'statusCategory != Done'
  b:
    connection: jira1
    projects: [B, C]
    query: 'statusCategory != Done'
  c:
    connection: jira2
    projects: [D]
    query: 'statusCategory != Done'
  d:
    connection: jira1
    projects: [E]
    query: 'labels = x'
  quiet:
    connection: jira1
    projects: [F]
    query: 'statusCategory != Done'
    discovery: false
`
	scopeFor := func(t *testing.T, agents string) (*JiraDiscoveryScope, error) {
		ec, err := ResolveEntityConfig(parseEntityConfig(t, base+agents), nil)
		require.NoError(t, err)
		return ec.JiraDiscoveryScope()
	}
	t.Run("projects union across sources", func(t *testing.T) {
		scope, err := scopeFor(t, "agents:\n  - name: triage\n    entities:\n      source: [a]\n  - name: code\n    entities:\n      source: [b]\n")
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "b"}, scope.Sources)
		assert.Equal(t, []string{"A", "B", "C"}, scope.Projects)
	})
	t.Run("other slots add no scheduled work", func(t *testing.T) {
		_, err := scopeFor(t, "agents:\n  - name: code\n    entities:\n      related: [a]\n")
		require.NoError(t, err)
		scope, err := scopeFor(t, "agents:\n  - name: code\n    entities:\n      related: [a]\n")
		require.NoError(t, err)
		assert.False(t, scope.Enabled)
	})
	t.Run("disabled agent", func(t *testing.T) {
		scope, err := scopeFor(t, "agents:\n  - name: code\n    enabled: false\n    entities:\n      source: [a]\n")
		require.NoError(t, err)
		assert.False(t, scope.Enabled)
	})
	t.Run("discovery false", func(t *testing.T) {
		scope, err := scopeFor(t, "agents:\n  - name: code\n    entities:\n      source: [quiet]\n")
		require.NoError(t, err)
		assert.False(t, scope.Enabled)
	})
	t.Run("different connections are unsupported", func(t *testing.T) {
		_, err := scopeFor(t, "agents:\n  - name: code\n    entities:\n      source: [a, c]\n")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrEntityUnsupported)
		assert.Contains(t, err.Error(), "one Jira connection")
	})
	t.Run("different queries are unsupported", func(t *testing.T) {
		_, err := scopeFor(t, "agents:\n  - name: code\n    entities:\n      source: [a, d]\n")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrEntityUnsupported)
		assert.Contains(t, err.Error(), "one query")
	})
	t.Run("no jira scope", func(t *testing.T) {
		ec, err := ResolveEntityConfig(parseEntityConfig(t, ""), nil)
		require.NoError(t, err)
		_, err = ec.JiraDiscoveryScope()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires tracker.jira in config.yaml or a Jira entity source")
	})
}

func TestResolveEntityConfig_DefaultConnectionWithoutOrigin(t *testing.T) {
	body := "entity_sources:\n  actionable_work:\n    connection: github\n    query: 'is:open'\n" +
		"agents:\n  - name: triage\n    entities:\n      source: [actionable_work]\n"
	r := parseEntityConfig(t, body)
	require.NoError(t, r.(interface{ Validate() error }).Validate(),
		"a provider-default connection needs no connections entry")

	ec, err := ResolveEntityConfig(r, nil)
	require.NoError(t, err)
	src := ec.Sources["actionable_work"]
	assert.True(t, src.NeedsOrigin)
	assert.Equal(t, ConnectionKindGitHub, src.Connection.Kind)
	_, err = ec.AgentBindings("triage", map[string]string{"source": EntityKindWorkItem, "target": EntityKindWorkItem})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no trusted configuration origin")

	ec, err = ResolveEntityConfig(r, githubOrigin)
	require.NoError(t, err)
	assert.Equal(t, "https://github.com", ec.Sources["actionable_work"].Connection.BaseURL)

	// An unknown non-provider connection name is still undeclared.
	_, err = ResolveEntityConfig(parseEntityConfig(t, "entity_sources:\n  x:\n    connection: nope\n    repositories: [a/b]\n    query: ''\n"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not declared under connections")
}

func TestResolveEntityConfig_ForgeConnectionWithoutBaseURL(t *testing.T) {
	// An ordinary named forge connection needs its own base_url, with or
	// without a configuration origin.
	named := "connections:\n  other:\n    kind: github\n" +
		"entity_sources:\n  s:\n    connection: other\n    repositories: [owner/repo]\n    query: 'is:open'\n"
	for _, origin := range []*ConfigOrigin{nil, githubOrigin} {
		_, err := ResolveEntityConfig(parseEntityConfig(t, named), origin)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "needs an explicit base_url")
	}

	// A provider-default connection with explicit repositories takes its
	// host from the origin, so without one the source is not usable.
	defaults := "entity_sources:\n  s:\n    connection: github\n    repositories: [owner/repo]\n    query: 'is:open'\n" +
		"agents:\n  - name: triage\n    entities:\n      source: [s]\n"
	r := parseEntityConfig(t, defaults)
	ec, err := ResolveEntityConfig(r, nil)
	require.NoError(t, err)
	assert.True(t, ec.Sources["s"].NeedsOrigin)
	_, err = ec.AgentBindings("triage", map[string]string{"source": EntityKindWorkItem, "target": EntityKindWorkItem})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no trusted configuration origin")

	declared := "connections:\n  github:\n    kind: github\n" + defaults
	ec, err = ResolveEntityConfig(parseEntityConfig(t, declared), nil)
	require.NoError(t, err)
	assert.True(t, ec.Sources["s"].NeedsOrigin)
	_, err = ec.AgentBindings("triage", map[string]string{"source": EntityKindWorkItem, "target": EntityKindWorkItem})
	require.Error(t, err)

	ec, err = ResolveEntityConfig(parseEntityConfig(t, declared), githubOrigin)
	require.NoError(t, err)
	assert.Equal(t, "https://github.com", ec.Sources["s"].Connection.BaseURL)
}

func TestCheckUniformSourceBindings(t *testing.T) {
	base := `connections:
  jira1:
    kind: jira
    base_url: https://example.atlassian.net
entity_sources:
  a:
    connection: jira1
    projects: [A]
    query: ''
`
	check := func(t *testing.T, agents string) error {
		ec, err := ResolveEntityConfig(parseEntityConfig(t, base+agents), nil)
		require.NoError(t, err)
		scope, err := ec.JiraDiscoveryScope()
		require.NoError(t, err)
		return ec.CheckUniformSourceBindings(scope.Sources)
	}
	t.Run("same binding", func(t *testing.T) {
		require.NoError(t, check(t, "agents:\n  - name: triage\n    entities:\n      source: [a]\n  - name: code\n    entities:\n      source: [a]\n"))
	})
	t.Run("disabled agent is ignored", func(t *testing.T) {
		require.NoError(t, check(t, "agents:\n  - name: triage\n    entities:\n      source: [a]\n  - name: code\n    enabled: false\n"))
	})
	t.Run("agent bound to another source set", func(t *testing.T) {
		err := check(t, "agents:\n  - name: triage\n    entities:\n      source: [a]\n  - name: code\n    entities:\n      source: [native, a]\n")
		require.ErrorIs(t, err, ErrEntityUnsupported)
	})
	t.Run("agent without binding defaults to native", func(t *testing.T) {
		err := check(t, "agents:\n  - name: triage\n    entities:\n      source: [a]\n  - name: code\n")
		require.ErrorIs(t, err, ErrEntityUnsupported)
	})
	t.Run("explicitly empty binding", func(t *testing.T) {
		err := check(t, "agents:\n  - name: triage\n    entities:\n      source: [a]\n  - name: code\n    entities:\n      source: []\n")
		require.ErrorIs(t, err, ErrEntityUnsupported)
	})
}

func TestEntitySources_LayerPrecedence(t *testing.T) {
	dir := t.TempDir()
	base := `version: "1"
connections:
  jira1:
    kind: jira
    base_url: https://base.atlassian.net
entity_sources:
  work:
    connection: jira1
    projects: [BASE]
    query: 'labels = base'
  keep:
    connection: jira1
    projects: [KEEP]
    query: ''
agents:
  - name: code
    entities:
      source: [work]
      related: [keep]
`
	overlay := `connections:
  jira1:
    kind: jira
    base_url: https://overlay.atlassian.net
entity_sources:
  work:
    connection: jira1
    projects: [OVR1, OVR2]
    query: ''
agents:
  - name: code
    entities:
      source: [work, keep]
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, BaseConfigFile), []byte(base), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, OverlayConfigFile), []byte(overlay), 0o600))
	cfg, err := LoadConfig(dir, LoadOpts{})
	require.NoError(t, err)
	pr := cfg.(PerRepoConfigReader)
	require.NoError(t, pr.(interface{ Validate() error }).Validate())

	sources := pr.ConfigEntitySources()
	assert.Equal(t, []string{"OVR1", "OVR2"}, sources["work"].Projects, "a more-specific layer replaces the whole entry")
	assert.Nil(t, sources["work"].Discovery, "entries do not deep-merge")
	assert.Equal(t, []string{"KEEP"}, sources["keep"].Projects, "omitted entries inherit")

	entry, ok := AgentSettingsFor(pr.AgentEntries(), "code")
	require.True(t, ok)
	assert.Equal(t, []string{"work", "keep"}, entry.Entities["source"], "an explicit list replaces the slot")
	assert.Equal(t, []string{"keep"}, entry.Entities["related"], "omitted slots inherit")

	ec, err := ResolveEntityConfig(pr, githubOrigin)
	require.NoError(t, err)
	assert.Equal(t, "https://overlay.atlassian.net", ec.Sources["work"].Connection.BaseURL)
}

func TestManagedLayers_EntitySources(t *testing.T) {
	parent := &perRepoConfig{
		EntitySources: map[string]EntitySource{"a": {Connection: "c", Projects: []string{"A"}}},
		Connections:   map[string]SecretConnection{"c": {Kind: "jira", BaseURL: "https://example.atlassian.net", AllowedTargets: &ConnectionAllowedTargets{Projects: []string{"A"}}}},
		Agents:        []AgentEntry{{Name: "code", Entities: EntityBindings{"source": {"a"}}}},
	}
	child := &perRepoConfig{EntitySources: map[string]EntitySource{"b": {Connection: "c", Projects: []string{"B"}}}}
	out := clonePerRepo(parent)
	applyManagedLayer(out, child)
	assert.Len(t, out.EntitySources, 2)
	out.Connections["c"].AllowedTargets.Projects[0] = "MUTATED"
	out.Agents[0].Entities["source"][0] = "mutated"
	assert.Equal(t, "A", parent.Connections["c"].AllowedTargets.Projects[0], "connections are deep-cloned")
	assert.Equal(t, "a", parent.Agents[0].Entities["source"][0], "bindings are deep-cloned")
}

func TestResolveEntityKinds(t *testing.T) {
	got, err := ResolveEntityKinds(nil)
	require.NoError(t, err)
	assert.Nil(t, got, "a legacy harness stays on its compatibility path")

	got, err = ResolveEntityKinds(map[string]string{"source": EntityKindChangeProposal})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"source": EntityKindChangeProposal, "target": EntityKindChangeProposal}, got)

	got, err = ResolveEntityKinds(map[string]string{"target": EntityKindChangeProposal})
	require.NoError(t, err)
	assert.Equal(t, EntityKindWorkItem, got["source"])

	_, err = ResolveEntityKinds(map[string]string{"related": "issue"})
	require.ErrorContains(t, err, "must be one of")
	_, err = ResolveEntityKinds(map[string]string{"Bad-Slot": EntityKindWorkItem})
	require.ErrorContains(t, err, "invalid slot name")
}

func TestValidateEntityFilters(t *testing.T) {
	kinds := map[string]string{"source": EntityKindWorkItem, "target": EntityKindChangeProposal}
	require.NoError(t, ValidateEntityFilters(map[string]map[string]string{
		"source": {"github": `is:open label:"needs-code" no:assignee`, "gitlab": "open", "jira": "statusCategory != Done"},
		"target": {"github": "is:open draft:false base:main -label:blocked"},
	}, kinds))
	for name, tc := range map[string]struct {
		filters     map[string]map[string]string
		wantErr     string
		unsupported bool
	}{
		"unknown slot":       {filters: map[string]map[string]string{"other": {"github": ""}}, wantErr: "unknown slot"},
		"unknown provider":   {filters: map[string]map[string]string{"source": {"forgejo": ""}}, wantErr: "unknown provider"},
		"jira change target": {filters: map[string]map[string]string{"target": {"jira": ""}}, wantErr: "does not support", unsupported: true},
		"bad github":         {filters: map[string]map[string]string{"source": {"github": "open"}}, wantErr: "bare text"},
		"bad gitlab":         {filters: map[string]map[string]string{"source": {"gitlab": "is:open"}}, wantErr: "use open, closed, or all"},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateEntityFilters(tc.filters, kinds)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Equal(t, tc.unsupported, errors.Is(err, ErrEntityUnsupported))
		})
	}
}

func TestValidateGitHubQuery(t *testing.T) {
	for _, q := range []string{
		"",
		"is:open -label:blocked",
		"is:open (label:bug OR label:enhancement) -label:blocked",
		`label:"needs code" AND no:milestone`,
		"label:bug,enhancement author:octocat assignee:hubot milestone:v1",
		"created:>=2024-01-01 updated:2024-01-01..2024-02-01 closed:<2024-03-01T10:00:00Z",
		"type:Bug state:closed",
	} {
		assert.NoError(t, ValidateGitHubQuery(q, EntityKindWorkItem), q)
	}
	for _, q := range []string{"is:pr is:merged draft:true base:main head:feature merged:*..2024-01-01"} {
		assert.NoError(t, ValidateGitHubQuery(q, EntityKindChangeProposal), q)
		assert.NoError(t, ValidateGitHubQuery(q, ""), q)
	}
	for q, want := range map[string]string{
		"open":                  "bare text",
		"is:open NOT label:bug": "NOT is not supported",
		"repo:a/b":              "set the source's repositories",
		"sort:updated":          "adapter-owned",
		"in:title foo":          "not supported",
		"assignee:@me":          "viewer-relative",
		"created:>2024-01-01x":  "absolute date",
		"-no:label":             "-no: is not supported",
		"no:reviewer":           "no:reviewer is not supported",
		"is:draft":              "is:draft is not supported",
		"state:merged":          "state:merged is not supported",
		"draft:maybe":           "true or false",
		"(is:open":              "incomplete",
		"is:open)":              "unbalanced",
		"()":                    "empty parentheses",
		"is:open AND":           "incomplete",
		"OR is:open":            "must join",
		`label:"open`:           "unterminated",
		"label:":                "needs a value",
		"draft:true":            "applies to change_proposal",
		"is:issue":              "applies to work_item",
	} {
		kind := EntityKindWorkItem
		if q == "is:issue" {
			kind = EntityKindChangeProposal
		}
		err := ValidateGitHubQuery(q, kind)
		if assert.Error(t, err, q) {
			assert.Contains(t, err.Error(), want, q)
		}
	}
}

func TestEffectiveYAML_EntitySources(t *testing.T) {
	r := parseEntityConfig(t, contractExample)
	out, err := EffectiveYAML(r)
	require.NoError(t, err)
	assert.Contains(t, string(out), "entity_sources:")
	assert.Contains(t, string(out), "jira_related:")
}
