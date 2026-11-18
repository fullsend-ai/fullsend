package config

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestManagedConfigSchemaDeclarationMatchesVersion(t *testing.T) {
	data, err := os.ReadFile("managed-config-schema")
	require.NoError(t, err)
	v, err := ParseManagedConfigSchemaDeclaration(data)
	require.NoError(t, err)
	assert.Equal(t, ManagedConfigSchemaVersion, v)
}

func TestParseManagedConfigSchemaDeclaration(t *testing.T) {
	v, err := ParseManagedConfigSchemaDeclaration([]byte("# comment\n\n 3 \n"))
	require.NoError(t, err)
	assert.Equal(t, 3, v)

	for _, bad := range []string{"", "# only\n", "1\n2\n", "two\n", "0\n", "-1\n"} {
		_, err := ParseManagedConfigSchemaDeclaration([]byte(bad))
		assert.Error(t, err, "%q", bad)
	}
}

func TestRenderManagedConfigSchemaCurrentUnchanged(t *testing.T) {
	in := []byte("tracker:\n    github:\n        allowed_targets:\n            orgs: [a]\n")
	out, err := RenderManagedConfigSchema(in, ManagedConfigSchemaVersion)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

// A schema before the harness: block fails on an entry that carries one.
func TestRenderManagedConfigSchemaHarnessBlock(t *testing.T) {
	block := []byte("agents:\n  - name: review\n    harness:\n      timeout_minutes: 30\n")
	plain := []byte("agents:\n  - name: review\n    model: opus\n    harness: {}\n")
	for _, v := range []int{ManagedConfigSchemaTrackerMapping, ManagedConfigSchemaLegacy} {
		_, err := RenderManagedConfigSchema(block, v)
		require.ErrorContains(t, err, `agents entry "review": a harness: block cannot be expressed`)
		out, err := RenderManagedConfigSchema(plain, v)
		require.NoError(t, err)
		assert.Equal(t, plain, out, "an empty block sets nothing")
	}
}

func TestRenderManagedConfigSchemaUnknownVersion(t *testing.T) {
	_, err := RenderManagedConfigSchema([]byte("runtime: claude\n"), ManagedConfigSchemaVersion+1)
	require.ErrorContains(t, err, "unknown managed configuration schema version")
}

func TestRenderManagedConfigSchemaLegacy(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no tracker",
			in:   "runtime: claude\n",
			want: "runtime: claude\n",
		},
		{
			name: "blank",
			in:   "",
			want: "",
		},
		{
			name: "github targets",
			in:   "runtime: claude\ntracker:\n    github:\n        allowed_targets:\n            orgs: [a]\n            repos: [b/c]\nkill_switch: false\n",
			want: "runtime: claude\ntracker: github\ncreate_issues:\n    allow_targets:\n        orgs: [a]\n        repos: [b/c]\nkill_switch: false\n",
		},
		{
			name: "github without targets",
			in:   "tracker:\n    github:\n        allowed_targets: {}\n",
			want: "tracker: github\ncreate_issues:\n    allow_targets: {}\n",
		},
		{
			name: "gitlab repos",
			in:   "tracker:\n    gitlab:\n        allowed_targets:\n            repos: [g/p]\n",
			want: "tracker: gitlab\ncreate_issues:\n    allow_targets:\n        repos: [g/p]\n",
		},
		{
			name: "jira without settings",
			in:   "tracker:\n    jira:\n        allowed_targets: {}\n",
			want: "tracker: jira\ncreate_issues:\n    allow_targets: {}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := RenderManagedConfigSchema([]byte(tt.in), ManagedConfigSchemaLegacy)
			require.NoError(t, err)
			assert.Equal(t, tt.want, normalizeFlow(string(out)))
		})
	}
}

// normalizeFlow rewrites block sequences of scalars to flow form so the
// expectations above stay readable regardless of the encoder's style.
func normalizeFlow(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasSuffix(line, ":") && i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "- ") {
			var items []string
			for i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "- ") {
				items = append(items, strings.TrimPrefix(strings.TrimSpace(lines[i+1]), "- "))
				i++
			}
			line += " [" + strings.Join(items, ", ") + "]"
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func TestRenderManagedConfigSchemaLegacyRoundTrip(t *testing.T) {
	in := []byte("tracker:\n    github:\n        allowed_targets:\n            orgs: [a]\n            repos: [b/c]\n")
	legacy, err := RenderManagedConfigSchema(in, ManagedConfigSchemaLegacy)
	require.NoError(t, err)
	migrated, changed, err := MigrateLegacyTrackingYAML(legacy, TrackingProviderGitLab)
	require.NoError(t, err)
	require.True(t, changed)
	want, err := ParsePerRepoConfigWriter(in)
	require.NoError(t, err)
	got, err := ParsePerRepoConfigWriter(migrated)
	require.NoError(t, err)
	assert.Equal(t, want.IssueTracking(), got.IssueTracking())
}

// TestRenderManagedConfigSchemaLegacyEmptyTargetsResetInherited checks that
// an empty tracker mapping rendered for the legacy schema clears the
// create_issues targets a legacy base layer grants, rather than inheriting
// them.
func TestRenderManagedConfigSchemaLegacyEmptyTargetsResetInherited(t *testing.T) {
	var baseDoc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("tracker: github\ncreate_issues:\n  allow_targets:\n    orgs: [a]\n    repos: [b/c]\n"), &baseDoc))
	base, kind, err := ReadLegacyTracking(baseDoc.Content[0], LegacyTracking{})
	require.NoError(t, err)
	require.Equal(t, TrackingLayerLegacy, kind)

	overlay, err := RenderManagedConfigSchema([]byte("tracker:\n    github:\n        allowed_targets: {}\n"), ManagedConfigSchemaLegacy)
	require.NoError(t, err)
	var overlayDoc yaml.Node
	require.NoError(t, yaml.Unmarshal(overlay, &overlayDoc))
	effective, _, err := ReadLegacyTracking(overlayDoc.Content[0], base)
	require.NoError(t, err)
	got, err := effective.Resolve(TrackingProviderGitHub)
	require.NoError(t, err)
	assert.Empty(t, got.GitHub.AllowedTargets.Orgs)
	assert.Empty(t, got.GitHub.AllowedTargets.Repos)
}

func TestRenderManagedConfigSchemaLegacyUnsupported(t *testing.T) {
	for name, in := range map[string]string{
		"gitlab groups": "tracker:\n    gitlab:\n        allowed_targets:\n            groups: [g]\n",
		"jira settings": "tracker:\n    jira:\n        base_url: https://example.atlassian.net\n        project: P\n        allowed_targets: {}\n",
		"no provider":   "tracker: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := RenderManagedConfigSchema([]byte(in), ManagedConfigSchemaLegacy)
			require.Error(t, err)
			if name == "jira settings" {
				assert.ErrorContains(t, err, "tracker.jira cannot be expressed")
			}
		})
	}
	_, err := RenderManagedConfigSchema([]byte("- a\n"), ManagedConfigSchemaLegacy)
	require.ErrorContains(t, err, "YAML mapping")
	_, err = RenderManagedConfigSchema([]byte("a: [\n"), ManagedConfigSchemaLegacy)
	require.ErrorContains(t, err, "parsing configuration")
}

func TestManagedConfigSchemaDependent(t *testing.T) {
	assert.True(t, ManagedConfigSchemaDependent([]byte("tracker:\n  github: {}\n")))
	assert.True(t, ManagedConfigSchemaDependent([]byte("tracker: github\n")))
	assert.True(t, ManagedConfigSchemaDependent([]byte("create_issues:\n  allow_targets: {}\n")))
	assert.True(t, ManagedConfigSchemaDependent([]byte("agents:\n  - name: review\n    harness: { timeout_minutes: 30 }\n")))
	assert.False(t, ManagedConfigSchemaDependent([]byte("agents:\n  - name: review\n    model: opus\n    harness: {}\n")))
	assert.False(t, ManagedConfigSchemaDependent([]byte("runtime: claude\n")))
	assert.False(t, ManagedConfigSchemaDependent(nil))
	assert.False(t, ManagedConfigSchemaDependent([]byte("a: [\n")))
}

func TestCheckManagedConfigSchemaCompat(t *testing.T) {
	mapping := []byte("# header\ntracker:\n    github:\n        allowed_targets: {}\n")
	legacy := []byte("tracker: github\ncreate_issues:\n    allow_targets:\n        orgs: [a]\n")
	neutral := []byte("runtime: claude\n")

	require.Error(t, CheckManagedConfigSchemaCompat(mapping, ManagedConfigSchemaLegacy))
	require.NoError(t, CheckManagedConfigSchemaCompat(mapping, ManagedConfigSchemaVersion))
	require.NoError(t, CheckManagedConfigSchemaCompat(legacy, ManagedConfigSchemaLegacy))
	require.Error(t, CheckManagedConfigSchemaCompat(legacy, ManagedConfigSchemaVersion))
	require.NoError(t, CheckManagedConfigSchemaCompat(neutral, ManagedConfigSchemaLegacy))
	require.NoError(t, CheckManagedConfigSchemaCompat(neutral, ManagedConfigSchemaVersion))
	require.NoError(t, CheckManagedConfigSchemaCompat([]byte("a: [\n"), ManagedConfigSchemaLegacy))

	require.NoError(t, CheckManagedConfigSchemaCompat(mapping, ManagedConfigSchemaTrackerMapping))
	require.Error(t, CheckManagedConfigSchemaCompat(legacy, ManagedConfigSchemaTrackerMapping))
	block := []byte("agents:\n  - name: review\n    harness: { timeout_minutes: 30 }\n")
	require.NoError(t, CheckManagedConfigSchemaCompat(block, ManagedConfigSchemaVersion))
	require.ErrorContains(t, CheckManagedConfigSchemaCompat(block, ManagedConfigSchemaTrackerMapping), `harness: block on the agents entry "review"`)
	require.Error(t, CheckManagedConfigSchemaCompat(block, ManagedConfigSchemaLegacy))
}
