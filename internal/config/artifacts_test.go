package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStricterArtifactContent(t *testing.T) {
	tests := []struct{ a, b, want string }{
		{"", "", ArtifactContentAll},
		{ArtifactContentAll, ArtifactContentMetadata, ArtifactContentMetadata},
		{ArtifactContentMetadata, ArtifactContentAll, ArtifactContentMetadata},
		{ArtifactContentMetadata, ArtifactContentNone, ArtifactContentNone},
		{ArtifactContentNone, ArtifactContentMetadata, ArtifactContentNone},
		{ArtifactContentNone, "", ArtifactContentNone},
		{"", ArtifactContentMetadata, ArtifactContentMetadata},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, StricterArtifactContent(tc.a, tc.b), "%q vs %q", tc.a, tc.b)
	}
	assert.Equal(t, []string{"all", "metadata", "none"}, ValidArtifactContentLevels())
}

func TestValidateArtifacts(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{name: "unset", yaml: "version: \"1\"\n"},
		{name: "content levels", yaml: "artifacts:\n  content: metadata\n"},
		{name: "open source keys", yaml: "artifacts:\n  by_source:\n    jira: metadata\n    linear: none\n"},
		{name: "unknown content", yaml: "artifacts:\n  content: private\n", wantErr: `invalid artifacts.content "private"`},
		{name: "unknown by_source level", yaml: "artifacts:\n  by_source:\n    jira: hidden\n", wantErr: `invalid artifacts.by_source.jira "hidden"`},
		{name: "empty by_source level", yaml: "artifacts:\n  by_source:\n    jira: \"\"\n", wantErr: `invalid artifacts.by_source.jira ""`},
		{name: "empty source key", yaml: "artifacts:\n  by_source:\n    \"\": none\n", wantErr: "source key must not be empty"},
		{name: "blank source key", yaml: "artifacts:\n  by_source:\n    \"  \": none\n", wantErr: "source key must not be empty"},
		{name: "uppercase source key", yaml: "artifacts:\n  by_source:\n    Jira: none\n", wantErr: `invalid artifacts.by_source key "Jira": source keys are lowercase with no surrounding spaces; use "jira"`},
		{name: "source key with spaces", yaml: "artifacts:\n  by_source:\n    \" jira \": none\n", wantErr: `use "jira"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParsePerRepoConfigWriter([]byte(tc.yaml))
			require.NoError(t, err)
			err = cfg.ValidateGlobal()
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestResolveArtifactContent(t *testing.T) {
	tests := []struct {
		name   string
		yaml   string
		source string
		want   string
	}{
		{name: "unset is all", yaml: "version: \"1\"\n", source: "github", want: "all"},
		{name: "content applies to every source", yaml: "artifacts:\n  content: metadata\n", source: "github", want: "metadata"},
		{name: "by_source entry for the run source", yaml: "artifacts:\n  by_source:\n    jira: metadata\n", source: "jira", want: "metadata"},
		{name: "other source gets content", yaml: "artifacts:\n  by_source:\n    jira: metadata\n", source: "github", want: "all"},
		{name: "unknown source gets stricter content", yaml: "artifacts:\n  content: none\n  by_source:\n    jira: metadata\n", source: "", want: "none"},
		{name: "unknown source gets strictest by_source entry", yaml: "artifacts:\n  by_source:\n    jira: metadata\n    linear: none\n    github: all\n", source: "", want: "none"},
		{name: "unknown source without entries gets content", yaml: "artifacts:\n  content: metadata\n", source: "", want: "metadata"},
		{name: "stricter content wins", yaml: "artifacts:\n  content: none\n  by_source:\n    jira: metadata\n", source: "jira", want: "none"},
		{name: "stricter source wins", yaml: "artifacts:\n  content: metadata\n  by_source:\n    jira: none\n", source: "jira", want: "none"},
		{name: "looser source entry cannot relax content", yaml: "artifacts:\n  content: metadata\n  by_source:\n    github: all\n", source: "github", want: "metadata"},
		{name: "source is lowercased and trimmed", yaml: "artifacts:\n  by_source:\n    jira: none\n", source: " Jira ", want: "none"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParsePerRepoConfig([]byte(tc.yaml))
			require.NoError(t, err)
			got, err := ResolveArtifactContent(cfg, tc.source)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolveArtifactContent_NilConfigIsAll(t *testing.T) {
	got, err := ResolveArtifactContent(nil, "jira")
	require.NoError(t, err)
	assert.Equal(t, ArtifactContentAll, got)
}

func TestResolveArtifactContent_InvalidLevelFails(t *testing.T) {
	for _, yml := range []string{
		"artifacts:\n  content: secret\n",
		"artifacts:\n  by_source:\n    github: secret\n",
	} {
		cfg, err := ParsePerRepoConfig([]byte(yml))
		require.NoError(t, err)
		_, err = ResolveArtifactContent(cfg, "jira")
		assert.Error(t, err, yml)
	}
}

// The run-time config load does not run validateArtifacts, so an
// uppercase or padded by_source key must fail here, not resolve to "all".
func TestResolveArtifactContent_NonLowercaseKeyFails(t *testing.T) {
	for _, yml := range []string{
		"artifacts:\n  by_source:\n    Jira: metadata\n",
		"artifacts:\n  by_source:\n    \"jira \": none\n",
	} {
		cfg, err := ParsePerRepoConfig([]byte(yml))
		require.NoError(t, err)
		_, err = ResolveArtifactContent(cfg, "jira")
		assert.Error(t, err, yml)
	}
}

func TestArtifacts_LayerMerge(t *testing.T) {
	dir := t.TempDir()
	base := "version: \"1\"\nartifacts:\n  content: metadata\n  by_source:\n    jira: none\n    gitlab: metadata\n"
	overlay := "artifacts:\n  by_source:\n    gitlab: none\n    schedule: metadata\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, BaseConfigFile), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, OverlayConfigFile), []byte(overlay), 0o644))

	cfg, err := LoadConfig(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)
	r, ok := cfg.(PerRepoConfigReader)
	require.True(t, ok)
	assert.Equal(t, "metadata", r.ConfigArtifactsContent(), "content falls through to the base layer")
	assert.Equal(t, map[string]string{"jira": "none", "gitlab": "none", "schedule": "metadata"}, r.ConfigArtifactsBySource(),
		"by_source merges per key")

	// An explicit overlay content overrides the base.
	require.NoError(t, os.WriteFile(filepath.Join(dir, OverlayConfigFile), []byte("artifacts:\n  content: all\n"), 0o644))
	cfg, err = LoadConfig(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)
	r = cfg.(PerRepoConfigReader)
	assert.Equal(t, "all", r.ConfigArtifactsContent())
	assert.Equal(t, map[string]string{"jira": "none", "gitlab": "metadata"}, r.ConfigArtifactsBySource())
}

func TestArtifacts_ContentSet(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, BaseConfigFile), []byte("version: \"1\"\nartifacts:\n  content: all\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, OverlayConfigFile), []byte("artifacts:\n  by_source:\n    jira: all\n"), 0o644))
	cfg, err := LoadConfig(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)
	r := cfg.(PerRepoConfigReader)
	assert.True(t, r.ConfigArtifactsContentSet(), "an explicit content in the base layer counts")

	unset, err := ParsePerRepoConfig([]byte("artifacts:\n  by_source:\n    jira: all\n"))
	require.NoError(t, err)
	assert.False(t, unset.ConfigArtifactsContentSet())
	assert.False(t, NewEmptyPerRepoOverlay().ConfigArtifactsContentSet())
	assert.False(t, (&perRepoDefaults{}).ConfigArtifactsContentSet())
}

func TestArtifacts_DefaultsAndMarshal(t *testing.T) {
	empty := NewEmptyPerRepoOverlay()
	assert.Equal(t, ArtifactContentAll, empty.ConfigArtifactsContent())
	assert.Nil(t, empty.ConfigArtifactsBySource())

	out, err := empty.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(out), "artifacts")

	cfg, err := ParsePerRepoConfigWriter([]byte("artifacts:\n  content: metadata\n  by_source:\n    jira: none\n"))
	require.NoError(t, err)
	out, err = cfg.Marshal()
	require.NoError(t, err)
	round, err := ParsePerRepoConfig(out)
	require.NoError(t, err)
	assert.Equal(t, "metadata", round.ConfigArtifactsContent())
	assert.Equal(t, map[string]string{"jira": "none"}, round.ConfigArtifactsBySource())
}

func TestArtifacts_MergeManaged(t *testing.T) {
	parent, err := ParsePerRepoConfigWriter([]byte("artifacts:\n  content: metadata\n  by_source:\n    jira: none\n"))
	require.NoError(t, err)
	child, err := ParsePerRepoConfigWriter([]byte("artifacts:\n  by_source:\n    gitlab: metadata\n"))
	require.NoError(t, err)

	merged := MergeManaged(parent, child)
	assert.Equal(t, "metadata", merged.ConfigArtifactsContent())
	assert.Equal(t, map[string]string{"jira": "none", "gitlab": "metadata"}, merged.ConfigArtifactsBySource())

	// The merge copies: changing the result leaves the inputs alone.
	asPerRepo(merged).Artifacts.BySource["jira"] = "all"
	assert.Equal(t, "none", parent.ConfigArtifactsBySource()["jira"])

	noArtifacts, err := ParsePerRepoConfigWriter([]byte("kill_switch: true\n"))
	require.NoError(t, err)
	merged = MergeManaged(parent, noArtifacts)
	assert.Equal(t, map[string]string{"jira": "none"}, merged.ConfigArtifactsBySource(),
		"a layer without artifacts keeps the parent's")

	override, err := ParsePerRepoConfigWriter([]byte("artifacts:\n  content: none\n"))
	require.NoError(t, err)
	merged = MergeManaged(noArtifacts, override)
	assert.Equal(t, "none", merged.ConfigArtifactsContent())
}

func TestArtifacts_EffectiveYAML(t *testing.T) {
	plain, err := EffectiveYAML(NewEmptyPerRepoOverlay())
	require.NoError(t, err)
	assert.NotContains(t, string(plain), "artifacts", "the default level is not rendered")

	cfg, err := ParsePerRepoConfig([]byte("artifacts:\n  by_source:\n    jira: metadata\n"))
	require.NoError(t, err)
	out, err := EffectiveYAML(cfg)
	require.NoError(t, err)
	assert.Contains(t, string(out), "artifacts:\n    by_source:\n        jira: metadata\n    content: all\n")
}

func TestCheckManagedSafetyGate_Artifacts(t *testing.T) {
	tests := []struct {
		name      string
		current   string
		candidate string
		base      string
		wantKeys  []string
	}{
		{
			name:      "tightening is not a relaxation",
			current:   "",
			candidate: "artifacts:\n  content: none\n  by_source:\n    jira: none\n",
		},
		{
			name:      "declared content lowering is allowed",
			current:   "artifacts:\n  content: metadata\n",
			candidate: "artifacts:\n  content: all\n",
		},
		{
			name:      "undeclared content lowering through the base",
			current:   "",
			candidate: "",
			base:      "artifacts:\n  content: metadata\n",
			wantKeys:  []string{"artifacts.content"},
		},
		{
			name:      "dropping a source entry",
			current:   "artifacts:\n  by_source:\n    jira: metadata\n",
			candidate: "kill_switch: false\n",
			wantKeys:  []string{"artifacts.by_source.jira"},
		},
		{
			name:      "declared source lowering is allowed",
			current:   "artifacts:\n  by_source:\n    jira: none\n",
			candidate: "artifacts:\n  by_source:\n    jira: metadata\n",
		},
		{
			name:      "dropping an entry the content level still covers",
			current:   "artifacts:\n  content: none\n  by_source:\n    jira: metadata\n",
			candidate: "artifacts:\n  content: none\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// base sits under the current layer only, so a candidate
			// without it loses whatever the base set.
			current := mustLayer(t, tc.current, tc.base)
			candidate := mustLayer(t, tc.candidate, "")
			got := SafetyRelaxationKeys(CheckManagedSafetyGate(current, candidate))
			if len(tc.wantKeys) == 0 {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tc.wantKeys, got)
		})
	}
}
