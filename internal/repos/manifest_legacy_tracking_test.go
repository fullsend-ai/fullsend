package repos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
)

// legacyTrackingManifest uses the pre-#9074 tracker/create_issues keys on
// every config layer.
const legacyTrackingManifest = `version: 1
defaults:
  inference:
    auth: vertex-wif
  config:
    kill_switch: true
    create_issues:
      allow_targets:
        repos: [fullsend-ai/fullsend, fullsend-ai/agents]
github:
  repos:
    - name: acme/api
    - name: acme/own
      config:
        tracker: github
    - name: acme/jira
      config:
        tracker: jira
        create_issues:
          allow_targets: {}
    - name: acme/orgs
      config:
        create_issues:
          allow_targets:
            orgs: [acme]
gitlab:
  url: https://gitlab.example.com
  config:
    tracker: gitlab
  repos:
    - name: group/proj
    - name: "group/*"
      config:
        create_issues:
          allow_targets:
            repos: [group/sub/tools]
`

func managedTracking(t *testing.T, m *Manifest, forgeName, name string) *config.TrackingConfig {
	t.Helper()
	platform := m.PlatformFor(forgeName)
	require.NotNil(t, platform)
	for _, e := range platform.Repos {
		if e.Name == name {
			return m.mergeManagedConfig(platform, e).IssueTracking()
		}
	}
	t.Fatalf("no %s entry %s", forgeName, name)
	return nil
}

func githubRepos(repos ...string) *config.TrackingConfig {
	return &config.TrackingConfig{GitHub: &config.GitHubTrackingConfig{AllowedTargets: config.GitHubAllowedTargets{Repos: repos}}}
}

func gitlabRepos(repos ...string) *config.TrackingConfig {
	return &config.TrackingConfig{GitLab: &config.GitLabTrackingConfig{AllowedTargets: config.GitLabAllowedTargets{Repos: repos}}}
}

func assertLegacyManifestMigrated(t *testing.T, m *Manifest) {
	t.Helper()
	upstream := []string{"fullsend-ai/fullsend", "fullsend-ai/agents"}
	assert.Nil(t, m.Defaults.Config.Writer().IssueTracking(), "defaults.config spans forges, so its targets move to the forge sections")
	assert.Equal(t, githubRepos(upstream...), m.GitHub.Config.Writer().IssueTracking())
	assert.Equal(t, githubRepos(upstream...), managedTracking(t, m, ForgeGitHub, "acme/api"))
	assert.Equal(t, githubRepos(upstream...), managedTracking(t, m, ForgeGitHub, "acme/own"), "a scalar-only layer keeps its inherited targets")
	assert.Equal(t, &config.TrackingConfig{Jira: &config.JiraTrackingConfig{}}, managedTracking(t, m, ForgeGitHub, "acme/jira"))
	assert.Equal(t, &config.TrackingConfig{GitHub: &config.GitHubTrackingConfig{AllowedTargets: config.GitHubAllowedTargets{Orgs: []string{"acme"}}}},
		managedTracking(t, m, ForgeGitHub, "acme/orgs"), "organization grants stay organization grants")
	assert.Equal(t, gitlabRepos(upstream...), managedTracking(t, m, ForgeGitLab, "group/proj"))
	assert.Equal(t, gitlabRepos("group/sub/tools"), managedTracking(t, m, ForgeGitLab, "group/*"))
	assert.True(t, m.Defaults.Config.Writer().IsKillSwitchActive(), "unrelated config is kept")
	require.NoError(t, m.ValidateStructure())
}

func TestParseManifest_MigratesLegacyTracking(t *testing.T) {
	var m Manifest
	require.NoError(t, parseManifestBytes([]byte(legacyTrackingManifest), &m))
	assertLegacyManifestMigrated(t, &m)

	var paths []string
	for _, mig := range m.LegacyTrackingMigrations() {
		paths = append(paths, mig.Path)
		assert.NotEmpty(t, mig.Result)
	}
	assert.Equal(t, []string{
		"defaults.config",
		"github.config",
		"github.repos[acme/own].config",
		"github.repos[acme/jira].config",
		"github.repos[acme/orgs].config",
		"gitlab.config",
		"gitlab.repos[group/*].config",
	}, paths)

	body, ok, err := m.RenderManagedConfig(ForgeGitHub, m.GitHub.Repos[0])
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotContains(t, string(body), "create_issues")
	assert.Contains(t, string(body), "tracker:\n    github:\n        allowed_targets:\n            repos:\n")
}

// TestLoadManifest_LegacyTrackingRoundTripIsIdempotent writes the converted
// manifest as install does and loads it again: nothing is left to convert
// and every repository keeps the same rendered configuration.
func TestLoadManifest_LegacyTrackingRoundTripIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.yaml")
	require.NoError(t, os.WriteFile(path, []byte(legacyTrackingManifest), 0o644))
	first, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	require.NotEmpty(t, first.LegacyTrackingMigrations())

	require.NoError(t, WriteManifest(path, first))
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(written), "create_issues")

	second, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	assert.Empty(t, second.LegacyTrackingMigrations())
	assertLegacyManifestMigrated(t, second)
	for i, e := range first.GitHub.Repos {
		want, _, err := first.RenderManagedConfig(ForgeGitHub, e)
		require.NoError(t, err)
		got, _, err := second.RenderManagedConfig(ForgeGitHub, second.GitHub.Repos[i])
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), e.Name)
	}

	require.NoError(t, WriteManifest(path, second))
	again, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(written[lineAfterHeader(written):]), string(again[lineAfterHeader(again):]), "a second write changes nothing")
}

// lineAfterHeader returns the offset just past the generated header lines,
// whose date may differ between writes.
func lineAfterHeader(data []byte) int {
	offset := 0
	for offset < len(data) && data[offset] == '#' {
		for offset < len(data) && data[offset] != '\n' {
			offset++
		}
		offset++
	}
	return offset
}

func TestParseManifest_LegacyTrackingDefaultsWithProvider(t *testing.T) {
	var m Manifest
	require.NoError(t, parseManifestBytes([]byte(`version: 1
defaults:
  config:
    tracker: github
    create_issues:
      allow_targets:
        orgs: [acme]
github:
  repos:
    - name: acme/api
    - name: acme/narrow
      config:
        create_issues:
          allow_targets:
            repos: [acme/tools]
`), &m))
	acme := &config.TrackingConfig{GitHub: &config.GitHubTrackingConfig{AllowedTargets: config.GitHubAllowedTargets{Orgs: []string{"acme"}}}}
	assert.Equal(t, acme, m.Defaults.Config.Writer().IssueTracking())
	assert.Nil(t, m.GitHub.Config.Writer(), "a provider on defaults.config needs no forge-section copy")
	assert.Equal(t, acme, managedTracking(t, &m, ForgeGitHub, "acme/api"))
	assert.Equal(t, githubRepos("acme/tools"), managedTracking(t, &m, ForgeGitHub, "acme/narrow"))
}

func TestParseManifest_LegacyTrackingErrors(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		want     []string
	}{
		{
			name: "jira repo inherits repository targets",
			manifest: `version: 1
defaults:
  config:
    tracker: github
    create_issues:
      allow_targets:
        repos: [acme/tools]
github:
  repos:
    - name: acme/api
      config:
        tracker: jira
`,
			want: []string{"github.repos[acme/api].config", "tracker.jira.allowed_targets.projects"},
		},
		{
			name: "gitlab org grant",
			manifest: `version: 1
gitlab:
  config:
    create_issues:
      allow_targets:
        orgs: [group]
  repos:
    - name: group/proj
`,
			want: []string{"gitlab.config", "tracker.gitlab.allowed_targets.groups"},
		},
		{
			name: "defaults targets with no forge section",
			manifest: `version: 1
defaults:
  config:
    create_issues:
      allow_targets:
        repos: [a/b]
`,
			want: []string{"defaults.config", "no forge section"},
		},
		{
			name: "defaults mapping mixed with create_issues",
			manifest: `version: 1
defaults:
  config:
    tracker:
      github: {}
    create_issues: {}
github:
  repos: []
`,
			want: []string{"defaults.config", "remove create_issues"},
		},
		{
			name: "invalid forge-section scalar",
			manifest: `version: 1
github:
  config:
    tracker: other
  repos: []
`,
			want: []string{"github.config", `unknown provider "other"`},
		},
		{
			name: "defaults gitlab scalar with an org grant",
			manifest: `version: 1
defaults:
  config:
    tracker: gitlab
    create_issues:
      allow_targets:
        orgs: [group]
github:
  repos: []
`,
			want: []string{"defaults.config", "tracker.gitlab.allowed_targets.groups"},
		},
		{
			name: "forge-section push-down with an org grant on gitlab",
			manifest: `version: 1
defaults:
  config:
    create_issues:
      allow_targets:
        orgs: [acme]
gitlab:
  repos: []
`,
			want: []string{"gitlab.config", "tracker.gitlab.allowed_targets.groups"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Manifest
			err := parseManifestBytes([]byte(tt.manifest), &m)
			require.Error(t, err)
			for _, want := range tt.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestParseManifest_NoLegacyTrackingIsUntouched(t *testing.T) {
	var m Manifest
	require.NoError(t, parseManifestBytes([]byte(`version: 1
defaults:
  config:
    create_issues:
      allow_targets:
        repos: [a/b]
github:
  config:
    tracker:
      github:
        allowed_targets:
          orgs: [acme]
  repos:
    - name: acme/api
`), &m))
	assert.Equal(t, &config.TrackingConfig{GitHub: &config.GitHubTrackingConfig{AllowedTargets: config.GitHubAllowedTargets{Orgs: []string{"acme"}}}},
		managedTracking(t, &m, ForgeGitHub, "acme/api"), "a forge section with a tracker mapping overrides the pushed-down defaults")

	var clean Manifest
	require.NoError(t, parseManifestBytes([]byte("version: 1\ngithub:\n  repos:\n    - name: acme/api\n"), &clean))
	assert.Empty(t, clean.LegacyTrackingMigrations())
	out, migrations, err := migrateLegacyManifestTracking([]byte("not: [valid"))
	require.NoError(t, err)
	assert.Nil(t, migrations)
	assert.Equal(t, "not: [valid", string(out))
}

func TestParseManifest_LegacyTrackingPushDownReplacesNullConfig(t *testing.T) {
	out, migrations, err := migrateLegacyManifestTracking([]byte(`version: 1
defaults:
  config:
    create_issues:
      allow_targets:
        repos: [a/b]
github:
  config:
  repos: []
`))
	require.NoError(t, err)
	require.Len(t, migrations, 2)
	var m Manifest
	require.NoError(t, parseManifestBytes(out, &m))
	assert.Equal(t, githubRepos("a/b"), m.GitHub.Config.Writer().IssueTracking())
}

// legacyRenderedOverlay is the managed .fullsend/config.yaml an earlier
// install rendered from a legacy manifest.
const legacyRenderedOverlay = managedConfigMarker + "tracker: github\ncreate_issues:\n  allow_targets:\n    orgs:\n      - acme\n"

func TestValidateExistingOverlay_LegacyTracking(t *testing.T) {
	assert.NoError(t, validateExistingOverlay([]byte(legacyRenderedOverlay), ForgeGitHub),
		"a managed legacy overlay is converted in memory so install can re-render it")
	err := validateExistingOverlay([]byte("tracker: github\n"), ForgeGitHub)
	require.Error(t, err, "a markerless user file is not converted")
	assert.Contains(t, err.Error(), "scalar form")

	err = validateExistingOverlay([]byte(managedConfigMarker+"create_issues:\n  allow_targets:\n    orgs: [group]\n"), ForgeGitLab)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker.gitlab.allowed_targets.groups")
}

func TestValidateExistingOverlay_SequenceTracker(t *testing.T) {
	overlay := managedConfigMarker + "tracker: [jira]\ncreate_issues:\n  allow_targets:\n    repos: [acme/tools]\n"
	require.Error(t, validateExistingOverlay([]byte(overlay), ForgeGitHub),
		"a malformed tracker in a managed overlay is rejected, not converted into a grant")
}

// TestConvergeManagedConfigFiles_RewritesLegacyManagedOverlay: a managed
// overlay rendered from the legacy schema is re-rendered from the converted
// manifest (same effective policy, so the safety gate passes), and a second
// run changes nothing.
func TestConvergeManagedConfigFiles_RewritesLegacyManagedOverlay(t *testing.T) {
	var m Manifest
	require.NoError(t, parseManifestBytes([]byte(`version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  repos:
    - name: acme/api
      config:
        tracker: github
        create_issues:
          allow_targets:
            orgs: [acme]
`), &m))
	desired := mustDesiredManaged(t, &m, "acme", "api")
	assert.NotContains(t, string(desired), "create_issues")

	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(legacyRenderedOverlay)
	resolved := managedResolved(t, fc, &m, "acme", "api")

	require.NoError(t, checkEstablishedOverlayGate(context.Background(), resolved, desired))
	files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
	require.Len(t, files, 1)
	assert.Equal(t, desired, files[0].Content)
	require.Len(t, actions, 1)
	assert.Equal(t, "update", actions[0].Action)

	fc.FileContents["acme/api/"+preset.OverlayPath] = desired
	files, actions = convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
	assert.Empty(t, files)
	assert.Empty(t, actions)
}

func TestManagedSafetyGate_LegacyManagedOverlay(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "{}")

	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "tracker: github\ncreate_issues:\n  allow_targets:\n    repos: [acme/tools]\n")
	resolved := managedResolved(t, fc, m, "acme", "api")
	relaxations, err := evaluateManagedSafetyGate(context.Background(), resolved)
	require.NoError(t, err, "the legacy scalar in a managed overlay is converted before the comparison")
	assert.Empty(t, relaxations, "dropping the converted targets narrows the policy")

	m.Defaults.Config = mustManagedConfig(t, "kill_switch: false\n")
	fc.FileContents["acme/api/"+preset.BasePath] = []byte("tracker:\n  github:\n    allowed_targets:\n      orgs: [acme]\n")
	resolved = managedResolved(t, fc, m, "acme", "api")
	rejected := managedSafetyRejectedAction(context.Background(), resolved)
	require.NotNil(t, rejected, "the converted overlay's targets are protected: falling through to a wider base is a relaxation")
	assert.Equal(t, ActionSafetyRejected, rejected.Action)
	assert.Contains(t, rejected.Detail, "tracker.github.allowed_targets")

	delete(fc.FileContents, "acme/api/"+preset.BasePath)
	relaxations, err = presetReplacementRelaxations(context.Background(), resolved, nil, true, []byte("kill_switch: false\n"))
	require.NoError(t, err)
	assert.Empty(t, relaxations)

	resolved.Forge = ForgeGitLab
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "create_issues:\n  allow_targets:\n    orgs: [group]\n")
	rejected = managedSafetyRejectedAction(context.Background(), resolved)
	require.NotNil(t, rejected)
	assert.Equal(t, "error", rejected.Action)
	assert.Contains(t, rejected.Detail, "tracker.gitlab.allowed_targets.groups")
	_, err = presetReplacementRelaxations(context.Background(), resolved, nil, true, nil)
	require.Error(t, err)
}

// TestParseManifest_LegacyTrackingSharedAliasesStayIndependent: layers that
// share a config node through a YAML anchor are each converted against their
// own forge and inherited targets, not against the first layer's result.
func TestParseManifest_LegacyTrackingSharedAliasesStayIndependent(t *testing.T) {
	var m Manifest
	require.NoError(t, parseManifestBytes([]byte(`version: 1
github:
  config: &legacy
    create_issues:
      allow_targets:
        repos: [shared/tools]
  repos:
    - name: acme/api
gitlab:
  config: *legacy
  repos:
    - name: group/proj
`), &m))
	assert.Equal(t, githubRepos("shared/tools"), m.GitHub.Config.Writer().IssueTracking())
	assert.Equal(t, gitlabRepos("shared/tools"), m.GitLab.Config.Writer().IssueTracking(),
		"the aliased layer resolves its provider from its own forge")
	assert.Equal(t, gitlabRepos("shared/tools"), managedTracking(t, &m, ForgeGitLab, "group/proj"))
}

// TestParseManifest_LegacyTrackingSharedRepoConfigKeepsInheritedPolicy: a
// repository config shared across forges inherits each forge's own targets
// instead of the targets written into the shared node by the first entry.
func TestParseManifest_LegacyTrackingSharedRepoConfigKeepsInheritedPolicy(t *testing.T) {
	var m Manifest
	require.NoError(t, parseManifestBytes([]byte(`version: 1
github:
  config:
    create_issues:
      allow_targets:
        orgs: [acme]
  repos:
    - name: acme/api
      config: &shared
        tracker: github
gitlab:
  url: https://gitlab.example.com
  config:
    tracker: github
    create_issues:
      allow_targets:
        repos: [trusted/tools]
  repos:
    - name: group/proj
      config: *shared
`), &m))
	assert.Equal(t, &config.TrackingConfig{GitHub: &config.GitHubTrackingConfig{AllowedTargets: config.GitHubAllowedTargets{Orgs: []string{"acme"}}}},
		managedTracking(t, &m, ForgeGitHub, "acme/api"))
	assert.Equal(t, githubRepos("trusted/tools"), managedTracking(t, &m, ForgeGitLab, "group/proj"),
		"the narrower inherited grant is not replaced by the organization-wide one")
}

func TestParseManifest_LegacyTrackingRejectsSequenceTracker(t *testing.T) {
	err := parseManifestBytes([]byte(`version: 1
github:
  config:
    tracker: [jira]
    create_issues:
      allow_targets:
        repos: [acme/tools]
  repos: []
`), &Manifest{})
	require.Error(t, err, "a malformed tracker is not rewritten into a grant")

	err = parseManifestBytes([]byte(`version: 1
defaults:
  config:
    create_issues:
      allow_targets:
        repos: [acme/tools]
github:
  config:
    tracker: [jira]
  repos: []
`), &Manifest{})
	require.Error(t, err, "push-down does not overwrite a malformed tracker")
}

func TestExpandAliasesBounded(t *testing.T) {
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("a: &a [x]\nb: &b [*a, *a]\nc: *b\n"), &doc))
	out, err := expandAliasesBounded(doc.Content[0])
	require.NoError(t, err)
	var seen func(n *yaml.Node)
	seen = func(n *yaml.Node) {
		assert.NotEqual(t, yaml.AliasNode, n.Kind)
		assert.Empty(t, n.Anchor)
		for _, c := range n.Content {
			seen(c)
		}
	}
	seen(out)

	// A node that contains an alias to itself expands without end.
	cyclic := &yaml.Node{Kind: yaml.SequenceNode}
	cyclic.Content = []*yaml.Node{{Kind: yaml.AliasNode, Alias: cyclic}}
	_, err = expandAliasesBounded(cyclic)
	require.Error(t, err)
}

func TestParseManifest_LegacyTrackingPushDownRejectsMalformedConfig(t *testing.T) {
	for name, value := range map[string]string{
		"sequence":        "[invalid]",
		"non-null scalar": "invalid",
	} {
		t.Run(name, func(t *testing.T) {
			err := parseManifestBytes([]byte(`version: 1
defaults:
  config:
    create_issues:
      allow_targets:
        repos: [acme/tools]
github:
  config: `+value+`
  repos: []
`), &Manifest{})
			require.Error(t, err, "a malformed forge config is not replaced by the inherited selection")
		})
	}
}

func TestExpandAliasesBounded_ScalarByteBudget(t *testing.T) {
	// Few nodes, but every alias occurrence repeats a large anchored scalar.
	big := strings.Repeat("x", 512<<10)
	var b strings.Builder
	b.WriteString("a: &a " + big + "\nb: [")
	for i := 0; i < 40; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("*a")
	}
	b.WriteString("]\n")
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(b.String()), &doc))
	_, err := expandAliasesBounded(doc.Content[0])
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bytes")

	_, _, err = migrateLegacyManifestTracking([]byte(b.String() + "defaults:\n  config:\n    tracker: github\n"))
	require.Error(t, err)
}

func TestExpandAliasesBounded_CommentAndTagByteBudget(t *testing.T) {
	// Few nodes and tiny scalar values, but the anchored node carries a large
	// comment that every alias occurrence would repeat when encoded.
	big := strings.Repeat("c", 512<<10)
	var b strings.Builder
	b.WriteString("a: &a x # " + big + "\nb: [")
	for i := 0; i < 40; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("*a")
	}
	b.WriteString("]\n")
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(b.String()), &doc))
	_, err := expandAliasesBounded(doc.Content[0])
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bytes")

	_, _, err = migrateLegacyManifestTracking([]byte(b.String() + "defaults:\n  config:\n    tracker: github\n"))
	require.Error(t, err)

	// A large explicit tag is charged the same way.
	b.Reset()
	b.WriteString("a: &a !" + strings.Repeat("t", 512<<10) + " x\nb: [")
	for i := 0; i < 40; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("*a")
	}
	b.WriteString("]\n")
	doc = yaml.Node{}
	require.NoError(t, yaml.Unmarshal([]byte(b.String()), &doc))
	_, err = expandAliasesBounded(doc.Content[0])
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bytes")
}

// TestParseManifest_LegacyTrackingFollowsMergeKeys: a forge section or
// repository entry that inherits its legacy config through a YAML merge key is
// migrated like one that spells the config out.
func TestParseManifest_LegacyTrackingFollowsMergeKeys(t *testing.T) {
	var m Manifest
	require.NoError(t, parseManifestBytes([]byte(`version: 1
github: &platform
  config:
    create_issues:
      allow_targets:
        repos: [shared/tools]
  repos:
    - name: acme/api
gitlab:
  <<: *platform
  repos:
    - name: group/proj
`), &m))
	assert.Equal(t, githubRepos("shared/tools"), m.GitHub.Config.Writer().IssueTracking())
	assert.Equal(t, gitlabRepos("shared/tools"), m.GitLab.Config.Writer().IssueTracking(),
		"the config inherited through << resolves its provider from its own forge")
	assert.Equal(t, gitlabRepos("shared/tools"), managedTracking(t, &m, ForgeGitLab, "group/proj"))

	var entries Manifest
	require.NoError(t, parseManifestBytes([]byte(`version: 1
github:
  config:
    create_issues:
      allow_targets:
        orgs: [acme]
  repos:
    - name: acme/api
      config: &shared
        tracker: github
    - <<: {config: *shared}
      name: acme/web
`), &entries))
	assert.Equal(t, managedTracking(t, &entries, ForgeGitHub, "acme/api"), managedTracking(t, &entries, ForgeGitHub, "acme/web"))
}

func TestExpandAliasesBounded_MergeKeyPrecedence(t *testing.T) {
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(`base1: &b1 {a: 1, b: 1}
base2: &b2 {b: 2, c: 2}
out:
  <<: [*b1, *b2]
  c: explicit
bad:
  <<: scalar
`), &doc))
	out, err := expandAliasesBounded(doc.Content[0])
	require.NoError(t, err)
	got := mappingValue(out, "out")
	require.NotNil(t, got)
	values := map[string]string{}
	for i := 0; i+1 < len(got.Content); i += 2 {
		_, dup := values[got.Content[i].Value]
		assert.False(t, dup, "key %q appears once", got.Content[i].Value)
		values[got.Content[i].Value] = got.Content[i+1].Value
	}
	assert.Equal(t, map[string]string{"a": "1", "b": "1", "c": "explicit"}, values,
		"explicit keys win, then earlier merged mappings")
	assert.NotNil(t, mappingValue(out, "bad"), "an invalid merge value is left for the strict decoder")
	assert.NotNil(t, mappingValue(mappingValue(out, "bad"), "<<"))
}

func TestExpandAliasesBounded_RejectsDuplicateKeyInMergeSource(t *testing.T) {
	for name, src := range map[string]string{
		"inline merge source":   "config:\n  <<: {kill_switch: false, kill_switch: true}\n",
		"aliased merge source":  "base: &b {kill_switch: false, kill_switch: true}\nconfig:\n  <<: *b\n",
		"sequence merge source": "config:\n  <<: [{a: 1}, {kill_switch: false, kill_switch: true}]\n",
	} {
		var doc yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(src), &doc), name)
		_, err := expandAliasesBounded(doc.Content[0])
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "kill_switch", name)
	}

	// The same key in different merge sources is ordinary merge precedence.
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("config:\n  <<: [{a: 1}, {a: 2}]\n"), &doc))
	_, err := expandAliasesBounded(doc.Content[0])
	require.NoError(t, err)
}

func TestExpandAliasesBounded_DepthBudget(t *testing.T) {
	deep := strings.Repeat("[", maxExpandedYAMLDepth+10) + strings.Repeat("]", maxExpandedYAMLDepth+10)
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("a: "+deep+"\n"), &doc))
	_, err := expandAliasesBounded(doc.Content[0])
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deeply")
}
