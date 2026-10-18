package repos

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writebackManifestFile(t *testing.T, m *Manifest) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repos.yaml")
	require.NoError(t, writeManifest(path, m))
	return path
}

func upstreamOnly(t *testing.T) string {
	t.Helper()
	body, err := marshalManagedConfig(mustManagedConfig(t, "create_issues:\n  allow_targets:\n    repos: [fullsend-ai/fullsend]\n").Writer())
	require.NoError(t, err)
	return string(body)
}

func TestEnsureManagedConfigDefaults_ExistingEntryPersistsUpstreamDefault(t *testing.T) {
	m := newConvergeManifest("acme/api", "acme/web")
	path := writebackManifestFile(t, m)
	fc := newFakeClientForBatch("acme/api", "acme/web")

	var msgs []string
	names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, ManifestPath: path, RepoFilter: []string{"acme/api"},
	}, newTestClientFactory(fc), func(repo, _, msg string) { msgs = append(msgs, repo+": "+msg) })
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api"}, names)
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0], "Persisted")

	reloaded, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	api, _ := reloaded.ResolveConfig("acme", "api")
	web, _ := reloaded.ResolveConfig("acme", "web")
	body, _, err := desiredManagedConfig(api)
	require.NoError(t, err)
	assert.Equal(t, managedConfigMarker+upstreamOnly(t), string(body))
	assert.False(t, reloaded.GitHub.Repos[1].Config.IsSet(), "unselected repos are untouched")
	assert.NotNil(t, web.Managed)
}

// A repository install skips because its overlay needs adoption gains no
// manifest edit (#8511).
func TestEnsureManagedConfigDefaults_ExcludedRepoIsNotEdited(t *testing.T) {
	m := newConvergeManifest("acme/api", "acme/web")
	fc := newFakeClientForBatch("acme/api", "acme/web")
	names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, DryRun: true, ExcludeRepos: []string{"acme/web"},
	}, newTestClientFactory(fc), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api"}, names)
	assert.False(t, m.GitHub.Repos[1].Config.IsSet(), "the excluded repo is untouched")
}

func TestEnsureManagedConfigDefaults_NewEntryAndBootstrap(t *testing.T) {
	// A bootstrapped manifest holds only the entries the CLI added in
	// memory; the writeback records the default on them and creates the file.
	m := &Manifest{Version: 1, Defaults: testInferenceDefaults(), GitHub: &PlatformConfig{
		MintURL: "https://mint.example.com", Repos: []RepoEntry{{Name: "acme/new"}},
	}}
	path := filepath.Join(t.TempDir(), "repos.yaml")
	fc := newFakeClientForBatch("acme/new")

	names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, ManifestPath: path, RepoFilter: []string{"acme/new"},
	}, newTestClientFactory(fc), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/new"}, names)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "fullsend-ai/fullsend")
	assert.Contains(t, string(data), "acme/new")
}

func TestEnsureManagedConfigDefaults_GlobCoveredGetsScopedEntry(t *testing.T) {
	m := &Manifest{Version: 1, Defaults: testInferenceDefaults(), GitHub: &PlatformConfig{
		Repos: []RepoEntry{{Name: "acme/*", FullsendRef: "v1.2.3"}},
	}}
	path := writebackManifestFile(t, m)
	fc := newFakeClientForBatch("acme/api", "acme/web")

	names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, ManifestPath: path, RepoFilter: []string{"acme/api"},
	}, newTestClientFactory(fc), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api"}, names)

	require.Len(t, m.GitHub.Repos, 2)
	assert.False(t, m.GitHub.Repos[0].Config.IsSet(), "the glob entry is unchanged so siblings are unaffected")
	assert.Equal(t, "acme/api", m.GitHub.Repos[1].Name)
	assert.Equal(t, "v1.2.3", m.GitHub.Repos[1].FullsendRef, "the carved entry keeps the glob's settings")
	assert.True(t, m.GitHub.Repos[1].Config.IsSet())
}

func TestEnsureManagedConfigDefaults_GlobFilterOnlyTouchesFirstInstalls(t *testing.T) {
	m := &Manifest{Version: 1, Defaults: testInferenceDefaults(), GitHub: &PlatformConfig{
		Repos: []RepoEntry{{Name: "acme/*"}},
	}}
	fc := newFakeClientForBatch("acme/api", "acme/web")
	fc.FileContents["acme/web/"+preset.OverlayPath] = []byte(managedConfigMarker + "{}\n")

	names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, RepoFilter: []string{"acme/*"},
	}, newTestClientFactory(fc), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api"}, names, "a repo with an existing overlay is not a first install")
}

func TestEnsureManagedConfigDefaults_DryRunUpdatesMemoryOnly(t *testing.T) {
	m := newConvergeManifest("acme/api")
	path := writebackManifestFile(t, m)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	fc := newFakeClientForBatch("acme/api")

	var msgs []string
	_, err = EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, ManifestPath: path, DryRun: true,
	}, newTestClientFactory(fc), func(_, _, msg string) { msgs = append(msgs, msg) })
	require.NoError(t, err)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "dry run never writes the manifest")
	assert.True(t, m.GitHub.Repos[0].Config.IsSet(), "dry run renders from the updated in-memory manifest")
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0], "Would persist")
}

func TestEnsureManagedConfigDefaults_DoesNotOverrideDeclaredValues(t *testing.T) {
	presetPath := writePresetFile(t, testPresetYAML)

	t.Run("declared preset", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.Defaults.ConfigBase.Source = presetPath
		names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{Manifest: m},
			newTestClientFactory(newFakeClientForBatch("acme/api")), nil)
		require.NoError(t, err)
		assert.Empty(t, names)
		assert.False(t, m.GitHub.Repos[0].Config.IsSet())
	})
	t.Run("explicit repo create_issues", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.GitHub.Repos[0].Config = mustManagedConfig(t, "create_issues:\n  allow_targets:\n    orgs: [acme]\n")
		names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{Manifest: m},
			newTestClientFactory(newFakeClientForBatch("acme/api")), nil)
		require.NoError(t, err)
		assert.Empty(t, names)
	})
	t.Run("explicit defaults create_issues", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.Defaults.Config = mustManagedConfig(t, "create_issues:\n  allow_targets:\n    orgs: [acme]\n")
		names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{Manifest: m},
			newTestClientFactory(newFakeClientForBatch("acme/api")), nil)
		require.NoError(t, err)
		assert.Empty(t, names)
	})
	t.Run("existing overlay", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "{}\n")
		names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{Manifest: m},
			newTestClientFactory(fc), nil)
		require.NoError(t, err)
		assert.Empty(t, names)
	})
	t.Run("repo without inference auth", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.Defaults.Inference = InferenceSettings{}
		names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{Manifest: m},
			newTestClientFactory(newFakeClientForBatch("acme/api")), nil)
		require.NoError(t, err)
		assert.Empty(t, names, "install fails validation first; the manifest must not change")
	})
}

func TestEnsureManagedConfigDefaults_ReadOnlyManifestSuggestsExactEdit(t *testing.T) {
	m := newConvergeManifest("acme/api")
	fc := newFakeClientForBatch("acme/api")

	_, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, ManifestPath: "https://example.com/repos.yaml",
	}, newTestClientFactory(fc), nil)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "read-only")
	assert.Contains(t, msg, `github.repos entry "acme/api"`)
	for _, want := range []string{"config:", "create_issues:", "allow_targets:", "- fullsend-ai/fullsend"} {
		assert.Contains(t, msg, want)
	}
	assert.False(t, m.GitHub.Repos[0].Config.IsSet(), "a read-only manifest is not edited in memory either")

	m.sourceRemote = true
	_, err = EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{Manifest: m}, newTestClientFactory(fc), nil)
	require.Error(t, err, "a remotely loaded manifest is read-only even without a path")
}

func TestEnsureManagedConfigDefaults_ReadOnlyGlobCoveredSuggestsFullEntry(t *testing.T) {
	m := &Manifest{Version: 1, GitHub: &PlatformConfig{
		Repos: []RepoEntry{{Name: "acme/*", FullsendRef: "v1.2.3", Inference: InferenceSettings{Auth: InferenceAuthVertexWIF}}},
	}}
	fc := newFakeClientForBatch("acme/api")

	_, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, ManifestPath: "https://example.com/repos.yaml",
	}, newTestClientFactory(fc), nil)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "keep the covering glob entry")
	for _, want := range []string{"- name: acme/api", "fullsend_ref: v1.2.3", "inference:", "auth: vertex-wif", "config:", "- fullsend-ai/fullsend"} {
		assert.Contains(t, msg, want, "the suggested entry must carry the glob's settings")
	}
	require.Len(t, m.GitHub.Repos, 1, "a read-only manifest is not edited in memory either")
	assert.False(t, m.GitHub.Repos[0].Config.IsSet())
}

func TestEnsureManagedConfigDefaults_WriteFailureSuggestsEdit(t *testing.T) {
	m := newConvergeManifest("acme/api")
	_, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, ManifestPath: filepath.Join(t.TempDir(), "missing-dir", "repos.yaml"),
	}, newTestClientFactory(newFakeClientForBatch("acme/api")), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "writing manifest")
	assert.Contains(t, err.Error(), "- fullsend-ai/fullsend")
}

func TestEnsureManagedConfigDefaults_Errors(t *testing.T) {
	_, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{}, nil, nil)
	require.Error(t, err)

	m := newConvergeManifest("acme/api")
	fc := newFakeClientForBatch("acme/api")
	fc.GetFileContentErrors = map[string]error{"acme/api/" + preset.OverlayPath: fmt.Errorf("boom")}
	_, err = EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{Manifest: m}, newTestClientFactory(fc), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")

	_, err = EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, RepoFilter: []string{"[bad"},
	}, newTestClientFactory(newFakeClientForBatch("acme/api")), nil)
	require.Error(t, err)
}

func TestPreflightManagedConfig(t *testing.T) {
	ctx := context.Background()
	presetPath := writePresetFile(t, testPresetYAML)

	t.Run("clean repos pass", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "{}\n")
		adoption, err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
		require.NoError(t, err)
		assert.Empty(t, adoption)
		adoption, err = PreflightManagedConfig(ctx, m, newTestClientFactory(newFakeClientForBatch("acme/api")), []string{"acme/api"})
		require.NoError(t, err)
		assert.Empty(t, adoption)
	})
	t.Run("markerless overlay needs adoption without a config key", func(t *testing.T) {
		m := newConvergeManifest("acme/api", "acme/web")
		fc := newFakeClientForBatch("acme/api", "acme/web")
		fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("keep_history: true\n")
		fc.FileContents["acme/web/"+preset.OverlayPath] = []byte("keep_history: false\n")
		adoption, err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
		require.NoError(t, err, "adoption is a per-repository failure, not a run-level one (#8511)")
		require.Len(t, adoption, 2, "every offending repo is reported")
		assert.Equal(t, "acme/api", adoption[0].Name())
		assert.Equal(t, "acme/web", adoption[1].Name())
		for _, a := range adoption {
			assert.Contains(t, a.Guidance, "adoption required")
			assert.Contains(t, a.Guidance, "proposed repos.yaml entry")
			assert.NotContains(t, a.Guidance, a.Name()+":", "the guidance does not repeat the repository name")
		}
	})
	t.Run("one repo needs adoption and the other is clean", func(t *testing.T) {
		m := newConvergeManifest("acme/api", "acme/web")
		fc := newFakeClientForBatch("acme/api", "acme/web")
		fc.FileContents["acme/web/"+preset.OverlayPath] = []byte("keep_history: false\n")
		adoption, err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
		require.NoError(t, err)
		require.Len(t, adoption, 1)
		assert.Equal(t, AdoptionRequiredRepo{Owner: "acme", Repo: "web", Guidance: adoption[0].Guidance}, adoption[0])
	})
	t.Run("a hard problem on another repo stops the run and keeps the adoption guidance", func(t *testing.T) {
		m := newConvergeManifest("acme/api", "acme/web")
		fc := newFakeClientForBatch("acme/api", "acme/web")
		fc.FileContents["acme/api/"+preset.BasePath] = []byte("version: \"1\"\n")
		fc.FileContents["acme/web/"+preset.OverlayPath] = []byte("keep_history: false\n")
		adoption, err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
		require.Error(t, err)
		assert.Nil(t, adoption)
		assert.Contains(t, err.Error(), "acme/api: "+preset.BasePath)
		assert.Contains(t, err.Error(), "acme/web: "+preset.OverlayPath+" exists without the managed-configuration ownership marker")
	})
	t.Run("filter limits the check", func(t *testing.T) {
		m := newConvergeManifest("acme/api", "acme/web")
		fc := newFakeClientForBatch("acme/api", "acme/web")
		fc.FileContents["acme/web/"+preset.OverlayPath] = []byte("keep_history: false\n")
		adoption, err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), []string{"acme/api"})
		require.NoError(t, err)
		assert.Empty(t, adoption)
	})
	t.Run("filter naming a repo that is not in the manifest selects nothing", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		adoption, err := PreflightManagedConfig(ctx, m, newTestClientFactory(newFakeClientForBatch("acme/api")), []string{"acme/other"})
		require.NoError(t, err)
		assert.Empty(t, adoption)
	})
	t.Run("glob-covered filter name is checked", func(t *testing.T) {
		m := &Manifest{Version: 1, Defaults: testInferenceDefaults(), GitHub: &PlatformConfig{Repos: []RepoEntry{{Name: "acme/*"}}}}
		fc := newFakeClientForBatch()
		fc.FileContents["acme/fork/"+preset.OverlayPath] = []byte("keep_history: false\n")
		adoption, err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), []string{"acme/fork"})
		require.NoError(t, err)
		require.Len(t, adoption, 1, "a glob-covered repo ListOrgRepos omits is still checked")
		assert.Equal(t, "acme/fork", adoption[0].Name())
	})
	t.Run("undeclared base", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents["acme/api/"+preset.BasePath] = []byte("version: \"1\"\n")
		_, err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), preset.BasePath)

		m.Defaults.ConfigBase.Source = presetPath
		_, err = PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
		require.NoError(t, err, "a declared preset accounts for the base")
	})
	t.Run("read error", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := newFakeClientForBatch("acme/api")
		fc.GetFileContentErrors = map[string]error{"acme/api/" + preset.OverlayPath: fmt.Errorf("boom")}
		_, err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})
	t.Run("filter parse error", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		_, err := PreflightManagedConfig(ctx, m, newTestClientFactory(newFakeClientForBatch("acme/api")), []string{"[bad"})
		require.Error(t, err)
	})
}

type failingFactory struct{}

func (failingFactory) ConfigFor(string) (ForgeConfig, error) {
	return ForgeConfig{}, fmt.Errorf("no client")
}

func TestWritebackClientFactoryErrors(t *testing.T) {
	m := newConvergeManifest("acme/api")
	_, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{Manifest: m}, failingFactory{}, nil)
	require.Error(t, err)
	_, err = preflightManagedConfigRepos(context.Background(), m, failingFactory{}, []ResolvedRepo{{Owner: "acme", Repo: "api", Forge: ForgeGitHub}})
	require.Error(t, err)
}

// The create_issues default is a first-install grant: an existing overlay
// or a repository that is not pristine never receives it, so nothing is
// written to the manifest and a read-only manifest is not an error.
func TestEnsureManagedConfigDefaults_NoWritebackUnlessPristineFirstInstall(t *testing.T) {
	markedOverlay := []byte(managedConfigMarker + "{}\n")
	const readOnlyURL = "https://example.com/repos.yaml"

	cases := []struct {
		name  string
		files map[string][]byte
	}{
		{"existing overlay", map[string][]byte{"acme/api/" + preset.OverlayPath: markedOverlay}},
		{"existing overlay on an installed repository", map[string][]byte{
			"acme/api/" + preset.OverlayPath:           markedOverlay,
			"acme/api/.github/workflows/fullsend.yaml": []byte("shim"),
		}},
		{"installed repository without overlay", map[string][]byte{
			"acme/api/.github/workflows/fullsend.yaml": []byte("shim"),
		}},
		{"existing base without overlay", map[string][]byte{
			"acme/api/" + preset.BasePath: []byte("version: \"1\"\n"),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newConvergeManifest("acme/api")
			fc := newFakeClientForBatch("acme/api")
			for k, v := range tc.files {
				fc.FileContents[k] = v
			}

			names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
				Manifest: m, ManifestPath: writebackManifestFile(t, m),
			}, newTestClientFactory(fc), nil)
			require.NoError(t, err)
			assert.Empty(t, names)
			assert.False(t, m.GitHub.Repos[0].Config.IsSet(), "the manifest entry is not edited")

			_, err = EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
				Manifest: m, ManifestPath: readOnlyURL,
			}, newTestClientFactory(fc), nil)
			require.NoError(t, err, "nothing to persist, so a read-only manifest is not an error")
		})
	}
}
