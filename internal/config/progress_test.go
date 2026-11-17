package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStricterProgressVerbosity(t *testing.T) {
	tests := []struct{ a, b, want string }{
		{"", "", ProgressVerbosityFull},
		{ProgressVerbosityFull, ProgressVerbosityTools, ProgressVerbosityTools},
		{ProgressVerbosityTools, ProgressVerbosityFull, ProgressVerbosityTools},
		{ProgressVerbosityTools, ProgressVerbosityQuiet, ProgressVerbosityQuiet},
		{ProgressVerbosityQuiet, ProgressVerbosityTools, ProgressVerbosityQuiet},
		{ProgressVerbosityQuiet, "", ProgressVerbosityQuiet},
		{"", ProgressVerbosityTools, ProgressVerbosityTools},
		{ProgressVerbosityTools, "loud", "loud"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, StricterProgressVerbosity(tc.a, tc.b), "%q vs %q", tc.a, tc.b)
	}
	assert.Equal(t, []string{"full", "tools", "quiet"}, ValidProgressVerbosities())
}

// An empty level ranks as full, like every other progress helper; an
// unknown level ranks strictest.
func TestProgressVerbosityRank(t *testing.T) {
	assert.Equal(t, 0, progressVerbosityRank(""))
	assert.Equal(t, 0, progressVerbosityRank(ProgressVerbosityFull))
	assert.Equal(t, 1, progressVerbosityRank(ProgressVerbosityTools))
	assert.Equal(t, 2, progressVerbosityRank(ProgressVerbosityQuiet))
	assert.Equal(t, 2, progressVerbosityRank("loud"))
}

func TestValidateProgress(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{name: "unset", yaml: "version: \"1\"\n"},
		{name: "verbosity levels", yaml: "progress:\n  verbosity: tools\n"},
		{name: "open source keys", yaml: "progress:\n  by_source:\n    jira: quiet\n    linear: tools\n"},
		{name: "unknown verbosity", yaml: "progress:\n  verbosity: loud\n", wantErr: `invalid progress.verbosity "loud"`},
		{name: "unknown by_source level", yaml: "progress:\n  by_source:\n    jira: hidden\n", wantErr: `invalid progress.by_source.jira "hidden"`},
		{name: "empty by_source level", yaml: "progress:\n  by_source:\n    jira: \"\"\n", wantErr: `invalid progress.by_source.jira ""`},
		{name: "empty source key", yaml: "progress:\n  by_source:\n    \"\": quiet\n", wantErr: "source key must not be empty"},
		{name: "uppercase source key", yaml: "progress:\n  by_source:\n    Jira: quiet\n", wantErr: `invalid progress.by_source key "Jira": source keys are lowercase with no surrounding spaces; use "jira"`},
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

func TestResolveProgressVerbosity(t *testing.T) {
	tests := []struct {
		name   string
		yaml   string
		source string
		want   string
	}{
		{name: "unset is full", yaml: "version: \"1\"\n", source: "github", want: "full"},
		{name: "verbosity applies to every source", yaml: "progress:\n  verbosity: tools\n", source: "github", want: "tools"},
		{name: "by_source entry for the run source", yaml: "progress:\n  verbosity: full\n  by_source:\n    jira: quiet\n", source: "jira", want: "quiet"},
		{name: "other source gets verbosity", yaml: "progress:\n  verbosity: full\n  by_source:\n    jira: quiet\n", source: "github", want: "full"},
		{name: "unknown source gets strictest by_source entry", yaml: "progress:\n  by_source:\n    jira: tools\n    linear: quiet\n    github: full\n", source: "", want: "quiet"},
		{name: "unknown source without entries gets verbosity", yaml: "progress:\n  verbosity: tools\n", source: "", want: "tools"},
		{name: "stricter verbosity wins", yaml: "progress:\n  verbosity: quiet\n  by_source:\n    jira: tools\n", source: "jira", want: "quiet"},
		{name: "stricter source wins", yaml: "progress:\n  verbosity: tools\n  by_source:\n    jira: quiet\n", source: "jira", want: "quiet"},
		{name: "looser source entry cannot relax verbosity", yaml: "progress:\n  verbosity: tools\n  by_source:\n    github: full\n", source: "github", want: "tools"},
		{name: "source is lowercased and trimmed", yaml: "progress:\n  by_source:\n    jira: quiet\n", source: " Jira ", want: "quiet"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParsePerRepoConfig([]byte(tc.yaml))
			require.NoError(t, err)
			got, err := ResolveProgressVerbosity(cfg, tc.source)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolveProgressVerbosity_NilConfigIsFull(t *testing.T) {
	got, err := ResolveProgressVerbosity(nil, "jira")
	require.NoError(t, err)
	assert.Equal(t, ProgressVerbosityFull, got)
}

// The run-time config load does not run validateProgress, so an unknown
// level or a non-lowercase key must fail here; the caller then logs at
// quiet.
func TestResolveProgressVerbosity_InvalidFails(t *testing.T) {
	for _, yml := range []string{
		"progress:\n  verbosity: loud\n",
		"progress:\n  by_source:\n    github: loud\n",
		"progress:\n  by_source:\n    Jira: tools\n",
	} {
		cfg, err := ParsePerRepoConfig([]byte(yml))
		require.NoError(t, err)
		_, err = ResolveProgressVerbosity(cfg, "jira")
		assert.Error(t, err, yml)
	}
}

func TestProgress_LayerMerge(t *testing.T) {
	dir := t.TempDir()
	base := "version: \"1\"\nprogress:\n  verbosity: tools\n  by_source:\n    jira: quiet\n    gitlab: tools\n"
	overlay := "progress:\n  by_source:\n    gitlab: quiet\n    schedule: tools\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, BaseConfigFile), []byte(base), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, OverlayConfigFile), []byte(overlay), 0o644))

	cfg, err := LoadConfig(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)
	r, ok := cfg.(PerRepoConfigReader)
	require.True(t, ok)
	assert.Equal(t, "tools", r.ConfigProgressVerbosity(), "verbosity falls through to the base layer")
	assert.Equal(t, map[string]string{"jira": "quiet", "gitlab": "quiet", "schedule": "tools"}, r.ConfigProgressBySource(),
		"by_source merges per key")

	// An explicit overlay verbosity overrides the base.
	require.NoError(t, os.WriteFile(filepath.Join(dir, OverlayConfigFile), []byte("progress:\n  verbosity: full\n"), 0o644))
	cfg, err = LoadConfig(dir, LoadOpts{MissingOK: false})
	require.NoError(t, err)
	r = cfg.(PerRepoConfigReader)
	assert.Equal(t, "full", r.ConfigProgressVerbosity())
	assert.Equal(t, map[string]string{"jira": "quiet", "gitlab": "tools"}, r.ConfigProgressBySource())
}

func TestProgress_DefaultsAndMarshal(t *testing.T) {
	empty := NewEmptyPerRepoOverlay()
	assert.Equal(t, ProgressVerbosityFull, empty.ConfigProgressVerbosity())
	assert.Nil(t, empty.ConfigProgressBySource())
	assert.Equal(t, ProgressVerbosityFull, (&perRepoDefaults{}).ConfigProgressVerbosity())
	assert.Nil(t, (&perRepoDefaults{}).ConfigProgressBySource())

	out, err := empty.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(out), "progress")

	cfg, err := ParsePerRepoConfigWriter([]byte("progress:\n  verbosity: tools\n  by_source:\n    jira: quiet\n"))
	require.NoError(t, err)
	out, err = cfg.Marshal()
	require.NoError(t, err)
	round, err := ParsePerRepoConfig(out)
	require.NoError(t, err)
	assert.Equal(t, "tools", round.ConfigProgressVerbosity())
	assert.Equal(t, map[string]string{"jira": "quiet"}, round.ConfigProgressBySource())
}

func TestProgress_MergeManaged(t *testing.T) {
	parent, err := ParsePerRepoConfigWriter([]byte("progress:\n  verbosity: tools\n  by_source:\n    jira: quiet\n"))
	require.NoError(t, err)
	child, err := ParsePerRepoConfigWriter([]byte("progress:\n  by_source:\n    gitlab: tools\n"))
	require.NoError(t, err)

	merged := MergeManaged(parent, child)
	assert.Equal(t, "tools", merged.ConfigProgressVerbosity())
	assert.Equal(t, map[string]string{"jira": "quiet", "gitlab": "tools"}, merged.ConfigProgressBySource())

	// The merge copies: changing the result leaves the inputs alone.
	asPerRepo(merged).Progress.BySource["jira"] = "full"
	assert.Equal(t, "quiet", parent.ConfigProgressBySource()["jira"])

	noProgress, err := ParsePerRepoConfigWriter([]byte("kill_switch: true\n"))
	require.NoError(t, err)
	merged = MergeManaged(parent, noProgress)
	assert.Equal(t, map[string]string{"jira": "quiet"}, merged.ConfigProgressBySource(),
		"a layer without progress keeps the parent's")

	override, err := ParsePerRepoConfigWriter([]byte("progress:\n  verbosity: quiet\n"))
	require.NoError(t, err)
	merged = MergeManaged(noProgress, override)
	assert.Equal(t, "quiet", merged.ConfigProgressVerbosity())
}

func TestProgress_EffectiveYAML(t *testing.T) {
	plain, err := EffectiveYAML(NewEmptyPerRepoOverlay())
	require.NoError(t, err)
	assert.NotContains(t, string(plain), "progress", "the default level is not rendered")

	cfg, err := ParsePerRepoConfig([]byte("progress:\n  by_source:\n    jira: quiet\n"))
	require.NoError(t, err)
	out, err := EffectiveYAML(cfg)
	require.NoError(t, err)
	assert.Contains(t, string(out), "progress:\n    by_source:\n        jira: quiet\n    verbosity: full\n")
}

func TestCheckManagedSafetyGate_Progress(t *testing.T) {
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
			candidate: "progress:\n  verbosity: quiet\n  by_source:\n    jira: quiet\n",
		},
		{
			name:      "declared verbosity lowering is allowed",
			current:   "progress:\n  verbosity: tools\n",
			candidate: "progress:\n  verbosity: full\n",
		},
		{
			name:      "undeclared verbosity lowering through the base",
			current:   "",
			candidate: "",
			base:      "progress:\n  verbosity: tools\n",
			wantKeys:  []string{"progress.verbosity"},
		},
		{
			name:      "dropping a source entry",
			current:   "progress:\n  by_source:\n    jira: quiet\n",
			candidate: "kill_switch: false\n",
			wantKeys:  []string{"progress.by_source.jira"},
		},
		{
			name:      "declared source lowering is allowed",
			current:   "progress:\n  by_source:\n    jira: quiet\n",
			candidate: "progress:\n  by_source:\n    jira: tools\n",
		},
		{
			name:      "dropping an entry the verbosity level still covers",
			current:   "progress:\n  verbosity: quiet\n  by_source:\n    jira: tools\n",
			candidate: "progress:\n  verbosity: quiet\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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
