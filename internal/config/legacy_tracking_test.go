package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestMigrateLegacyTrackingYAML covers the conversion of each legacy
// issue-tracking shape in a single configuration document (#9075).
func TestMigrateLegacyTrackingYAML(t *testing.T) {
	tests := []struct {
		name            string
		in              string
		defaultProvider string
		want            *TrackingConfig
		wantErr         string
	}{
		{
			name: "scalar github",
			in:   "tracker: github\n",
			want: &TrackingConfig{GitHub: &GitHubTrackingConfig{}},
		},
		{
			name: "scalar gitlab",
			in:   "tracker: gitlab\n",
			want: &TrackingConfig{GitLab: &GitLabTrackingConfig{}},
		},
		{
			name:            "scalar jira on a github forge",
			in:              "tracker: jira\n",
			defaultProvider: TrackingProviderGitHub,
			want:            &TrackingConfig{Jira: &JiraTrackingConfig{}},
		},
		{
			name: "github orgs stay orgs and repos stay repos",
			in:   "tracker: github\ncreate_issues:\n  allow_targets:\n    orgs: [acme]\n    repos: [other/tools]\n",
			want: &TrackingConfig{GitHub: &GitHubTrackingConfig{AllowedTargets: GitHubAllowedTargets{
				Orgs: []string{"acme"}, Repos: []string{"other/tools"},
			}}},
		},
		{
			name:            "no tracker uses the forge default (github)",
			in:              "create_issues:\n  allow_targets:\n    repos: [fullsend-ai/fullsend]\n",
			defaultProvider: TrackingProviderGitHub,
			want: &TrackingConfig{GitHub: &GitHubTrackingConfig{AllowedTargets: GitHubAllowedTargets{
				Repos: []string{"fullsend-ai/fullsend"},
			}}},
		},
		{
			name:            "no tracker uses the forge default (gitlab)",
			in:              "create_issues:\n  allow_targets:\n    repos: [group/sub/project]\n",
			defaultProvider: TrackingProviderGitLab,
			want: &TrackingConfig{GitLab: &GitLabTrackingConfig{AllowedTargets: GitLabAllowedTargets{
				Repos: []string{"group/sub/project"},
			}}},
		},
		{
			name:            "explicit empty allow_targets on gitlab",
			in:              "create_issues:\n  allow_targets: {}\n",
			defaultProvider: TrackingProviderGitLab,
			want:            &TrackingConfig{GitLab: &GitLabTrackingConfig{}},
		},
		{
			name:            "empty scalar is unset and falls back to the forge",
			in:              "tracker: \"\"\ncreate_issues:\n  allow_targets:\n    repos: [a/b]\n",
			defaultProvider: TrackingProviderGitHub,
			want: &TrackingConfig{GitHub: &GitHubTrackingConfig{AllowedTargets: GitHubAllowedTargets{
				Repos: []string{"a/b"},
			}}},
		},
		{
			name:            "null create_issues grants nothing",
			in:              "tracker: github\ncreate_issues:\n",
			defaultProvider: TrackingProviderGitHub,
			want:            &TrackingConfig{GitHub: &GitHubTrackingConfig{}},
		},
		{
			name:            "gitlab orgs need explicit groups",
			in:              "create_issues:\n  allow_targets:\n    orgs: [acme]\n",
			defaultProvider: TrackingProviderGitLab,
			wantErr:         "tracker.gitlab.allowed_targets.groups",
		},
		{
			name:    "jira never reinterprets repository paths",
			in:      "tracker: jira\ncreate_issues:\n  allow_targets:\n    repos: [acme/api]\n",
			wantErr: "tracker.jira.allowed_targets.projects",
		},
		{
			name:    "unknown scalar provider",
			in:      "tracker: bitbucket\n",
			wantErr: `unknown provider "bitbucket"`,
		},
		{
			name:    "mapping and create_issues together",
			in:      "tracker:\n  github: {}\ncreate_issues:\n  allow_targets:\n    repos: [a/b]\n",
			wantErr: "remove create_issues",
		},
		{
			name:    "misspelled create_issues key",
			in:      "tracker: github\ncreate_issues:\n  allow_target:\n    repos: [a/b]\n",
			wantErr: "create_issues:",
		},
		{
			name:    "no provider and no default",
			in:      "create_issues:\n  allow_targets:\n    repos: [a/b]\n",
			wantErr: "none can be inferred",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, changed, err := MigrateLegacyTrackingYAML([]byte(tt.in), tt.defaultProvider)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.True(t, changed)
			assert.NotContains(t, string(out), "create_issues")

			cfg, err := ParsePerRepoConfigWriter(out)
			require.NoError(t, err, "migrated output must parse:\n%s", out)
			require.NoError(t, ValidateManagedLayer(cfg))
			assert.Equal(t, tt.want, cfg.IssueTracking())

			again, changed, err := MigrateLegacyTrackingYAML(out, tt.defaultProvider)
			require.NoError(t, err)
			assert.False(t, changed, "a second migration must be a no-op")
			assert.Equal(t, out, again)
		})
	}
}

func TestMigrateLegacyTrackingYAML_KeepsUnrelatedKeysAndPosition(t *testing.T) {
	in := "kill_switch: true\ncreate_issues:\n  allow_targets:\n    repos: [a/b]\nruntime: claude\n"
	out, changed, err := MigrateLegacyTrackingYAML([]byte(in), TrackingProviderGitHub)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, "kill_switch: true\ntracker:\n    github:\n        allowed_targets:\n            repos:\n                - a/b\nruntime: claude\n", string(out))
}

func TestMigrateLegacyTrackingYAML_Unchanged(t *testing.T) {
	for name, in := range map[string]string{
		"no tracking keys": "kill_switch: true\n",
		"current mapping":  "tracker:\n  jira:\n    allowed_targets:\n      projects: [PROJ]\n",
		"null tracker":     "tracker: null\n",
		"malformed":        "tracker: [\n",
		"not a mapping":    "- a\n- b\n",
		"empty":            "",
	} {
		t.Run(name, func(t *testing.T) {
			out, changed, err := MigrateLegacyTrackingYAML([]byte(in), TrackingProviderGitHub)
			require.NoError(t, err)
			assert.False(t, changed)
			assert.Equal(t, in, string(out))
		})
	}
}

func mustMappingNode(t *testing.T, raw string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(raw), &doc))
	return doc.Content[0]
}

// TestReadLegacyTracking_Inheritance covers how a layer combines its legacy
// keys with the selection it inherits: the legacy keys were inherited
// independently, so a layer setting only one of them keeps the other.
func TestReadLegacyTracking_Inheritance(t *testing.T) {
	parentLegacy, kind, err := ReadLegacyTracking(mustMappingNode(t, "tracker: github\ncreate_issues:\n  allow_targets:\n    orgs: [acme]\n"), LegacyTracking{})
	require.NoError(t, err)
	require.Equal(t, TrackingLayerLegacy, kind)
	parentMapping, kind, err := ReadLegacyTracking(mustMappingNode(t, "tracker:\n  github:\n    allowed_targets:\n      repos: [acme/api]\n"), LegacyTracking{})
	require.NoError(t, err)
	require.Equal(t, TrackingLayerMapping, kind)

	tests := []struct {
		name     string
		parent   LegacyTracking
		child    string
		kind     TrackingLayerKind
		forge    string
		want     *TrackingConfig
		wantErr  string
		readFail bool
	}{
		{
			name:   "child scalar keeps inherited targets",
			parent: parentLegacy,
			child:  "tracker: github\n",
			kind:   TrackingLayerLegacy,
			want:   &TrackingConfig{GitHub: &GitHubTrackingConfig{AllowedTargets: GitHubAllowedTargets{Orgs: []string{"acme"}}}},
		},
		{
			name:   "child create_issues keeps inherited provider",
			parent: parentLegacy,
			child:  "create_issues:\n  allow_targets:\n    repos: [x/y]\n",
			kind:   TrackingLayerLegacy,
			forge:  TrackingProviderGitLab,
			want:   &TrackingConfig{GitHub: &GitHubTrackingConfig{AllowedTargets: GitHubAllowedTargets{Repos: []string{"x/y"}}}},
		},
		{
			name:    "child jira scalar with inherited repository targets",
			parent:  parentLegacy,
			child:   "tracker: jira\n",
			kind:    TrackingLayerLegacy,
			wantErr: "tracker.jira.allowed_targets.projects",
		},
		{
			name:   "no legacy keys inherit unchanged",
			parent: parentLegacy,
			child:  "kill_switch: true\n",
			kind:   TrackingLayerUnset,
			want:   &TrackingConfig{GitHub: &GitHubTrackingConfig{AllowedTargets: GitHubAllowedTargets{Orgs: []string{"acme"}}}},
		},
		{
			name:   "scalar repeating the inherited mapping's provider keeps it",
			parent: parentMapping,
			child:  "tracker: github\n",
			kind:   TrackingLayerLegacy,
			want:   &TrackingConfig{GitHub: &GitHubTrackingConfig{AllowedTargets: GitHubAllowedTargets{Repos: []string{"acme/api"}}}},
		},
		{
			name:     "scalar switching away from an inherited mapping with targets",
			parent:   parentMapping,
			child:    "tracker: jira\n",
			readFail: true,
			wantErr:  "replace the scalar with a tracker.jira mapping",
		},
		{
			name:   "scalar switching provider with its own create_issues",
			parent: parentMapping,
			child:  "tracker: gitlab\ncreate_issues:\n  allow_targets:\n    repos: [g/p]\n",
			kind:   TrackingLayerLegacy,
			want:   &TrackingConfig{GitLab: &GitLabTrackingConfig{AllowedTargets: GitLabAllowedTargets{Repos: []string{"g/p"}}}},
		},
		{
			name:   "scalar switching away from an inherited empty mapping",
			parent: LegacyTracking{mapping: &TrackingConfig{GitHub: &GitHubTrackingConfig{}}},
			child:  "tracker: jira\n",
			kind:   TrackingLayerLegacy,
			want:   &TrackingConfig{Jira: &JiraTrackingConfig{}},
		},
		{
			name:   "create_issues under an inherited mapping uses its provider",
			parent: parentMapping,
			child:  "create_issues:\n  allow_targets:\n    orgs: [acme]\n",
			kind:   TrackingLayerLegacy,
			forge:  TrackingProviderGitLab,
			want:   &TrackingConfig{GitHub: &GitHubTrackingConfig{AllowedTargets: GitHubAllowedTargets{Orgs: []string{"acme"}}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, kind, err := ReadLegacyTracking(mustMappingNode(t, tt.child), tt.parent)
			if tt.readFail {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.kind, kind)
			got, err := state.Resolve(tt.forge)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReadLegacyTracking_NonMappingLayer(t *testing.T) {
	parent := LegacyTracking{provider: TrackingProviderJira}
	state, kind, err := ReadLegacyTracking(nil, parent)
	require.NoError(t, err)
	assert.Equal(t, TrackingLayerUnset, kind)
	assert.Equal(t, parent, state)
	assert.True(t, state.HasProvider())
	assert.False(t, LegacyTracking{}.HasProvider())
	assert.False(t, LegacyTracking{mapping: &TrackingConfig{}}.HasProvider())
}

func TestReadLegacyTracking_MalformedMappingLeftToDecoder(t *testing.T) {
	_, kind, err := ReadLegacyTracking(mustMappingNode(t, "tracker:\n  github: [x]\n"), LegacyTracking{})
	require.NoError(t, err)
	assert.Equal(t, TrackingLayerMapping, kind)
}

func TestReplaceLegacyTracking(t *testing.T) {
	t.Run("nil removes both legacy keys", func(t *testing.T) {
		node := mustMappingNode(t, "tracker: github\nkill_switch: true\ncreate_issues: {}\n")
		require.NoError(t, ReplaceLegacyTracking(node, nil))
		out, err := yaml.Marshal(node)
		require.NoError(t, err)
		assert.Equal(t, "kill_switch: true\n", string(out))
	})
	t.Run("appends when neither key is present", func(t *testing.T) {
		node := mustMappingNode(t, "kill_switch: true\n")
		require.NoError(t, ReplaceLegacyTracking(node, &TrackingConfig{Jira: &JiraTrackingConfig{}}))
		out, err := yaml.Marshal(node)
		require.NoError(t, err)
		assert.Equal(t, "kill_switch: true\ntracker:\n    jira:\n        allowed_targets: {}\n", string(out))
	})
	t.Run("rejects a non-mapping", func(t *testing.T) {
		require.Error(t, ReplaceLegacyTracking(&yaml.Node{Kind: yaml.ScalarNode}, nil))
		require.Error(t, ReplaceLegacyTracking(nil, nil))
	})
}

func TestFormatTracking(t *testing.T) {
	assert.Equal(t, "tracker: unset", FormatTracking(nil))
	assert.Equal(t, "tracker.github.allowed_targets: orgs=[acme] repos=unset",
		FormatTracking(&TrackingConfig{GitHub: &GitHubTrackingConfig{AllowedTargets: GitHubAllowedTargets{Orgs: []string{"acme"}}}}))
	assert.Equal(t, "tracker.jira.allowed_targets: projects=unset", FormatTracking(&TrackingConfig{Jira: &JiraTrackingConfig{}}))
}

func TestReadLegacyTracking_SequenceTrackerLeftToDecoder(t *testing.T) {
	node := mustMappingNode(t, "tracker: [jira]\ncreate_issues:\n  allow_targets:\n    repos: [acme/tools]\n")
	_, kind, err := ReadLegacyTracking(node, LegacyTracking{})
	require.NoError(t, err)
	assert.Equal(t, TrackingLayerMapping, kind, "a malformed tracker is not classified as legacy")

	out, changed, err := MigrateLegacyTrackingYAML([]byte("tracker: [jira]\ncreate_issues:\n  allow_targets:\n    repos: [acme/tools]\n"), TrackingProviderGitHub)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Contains(t, string(out), "tracker: [jira]")
}
