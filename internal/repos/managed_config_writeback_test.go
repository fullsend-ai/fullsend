package repos

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		Repos: []RepoEntry{{Name: "acme/*", Runtime: "pi"}},
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
	assert.Equal(t, "pi", m.GitHub.Repos[1].Runtime, "the carved entry keeps the glob's settings")
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

func TestEnsureManagedConfigDefaults_RolesWithPreset(t *testing.T) {
	presetPath := writePresetFile(t, testPresetYAML)
	m := newConvergeManifest("acme/api")
	m.Defaults.ConfigBase.Source = presetPath

	names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, Roles: []string{"fix"}, RolesExplicit: true,
	}, newTestClientFactory(newFakeClientForBatch("acme/api")), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api"}, names)
	resolved, _ := m.ResolveConfig("acme", "api")
	assert.Equal(t, []string{"fix"}, resolved.Managed.ConfigRoles())
	assert.Nil(t, resolved.Managed.IssueCreationConfig(), "a declared preset keeps inheriting its create_issues")

	// Roles equal to the effective value are not rewritten.
	names, err = EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, Roles: []string{"fix"}, RolesExplicit: true,
	}, newTestClientFactory(newFakeClientForBatch("acme/api")), nil)
	require.NoError(t, err)
	assert.Empty(t, names)
}

func TestEnsureManagedConfigDefaults_ReadOnlyManifestSuggestsExactEdit(t *testing.T) {
	m := newConvergeManifest("acme/api")
	fc := newFakeClientForBatch("acme/api")

	_, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, ManifestPath: "https://example.com/repos.yaml", Roles: []string{"triage", "fix"}, RolesExplicit: true,
	}, newTestClientFactory(fc), nil)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "read-only")
	assert.Contains(t, msg, `github.repos entry "acme/api"`)
	for _, want := range []string{"config:", "create_issues:", "allow_targets:", "- fullsend-ai/fullsend", "roles:", "- fix"} {
		assert.Contains(t, msg, want)
	}
	assert.False(t, m.GitHub.Repos[0].Config.IsSet(), "a read-only manifest is not edited in memory either")

	m.sourceRemote = true
	_, err = EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{Manifest: m}, newTestClientFactory(fc), nil)
	require.Error(t, err, "a remotely loaded manifest is read-only even without a path")
}

func TestEnsureManagedConfigDefaults_ReadOnlyGlobCoveredSuggestsFullEntry(t *testing.T) {
	m := &Manifest{Version: 1, GitHub: &PlatformConfig{
		Repos: []RepoEntry{{Name: "acme/*", Runtime: "pi", Inference: InferenceSettings{Auth: InferenceAuthVertexWIF}}},
	}}
	fc := newFakeClientForBatch("acme/api")

	_, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, ManifestPath: "https://example.com/repos.yaml",
	}, newTestClientFactory(fc), nil)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "keep the covering glob entry")
	for _, want := range []string{"- name: acme/api", "runtime: pi", "inference:", "auth: vertex-wif", "config:", "- fullsend-ai/fullsend"} {
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

	_, err = EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, Roles: []string{"not-a-role"}, RolesExplicit: true,
	}, newTestClientFactory(newFakeClientForBatch("acme/api")), nil)
	require.Error(t, err, "an invalid persisted value is rejected before the manifest is written")
}

func TestPreflightManagedConfig(t *testing.T) {
	ctx := context.Background()
	presetPath := writePresetFile(t, testPresetYAML)

	t.Run("clean repos pass", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "{}\n")
		require.NoError(t, PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil))
		require.NoError(t, PreflightManagedConfig(ctx, m, newTestClientFactory(newFakeClientForBatch("acme/api")), []string{"acme/api"}))
	})
	t.Run("markerless overlay needs adoption without a config key", func(t *testing.T) {
		m := newConvergeManifest("acme/api", "acme/web")
		fc := newFakeClientForBatch("acme/api", "acme/web")
		fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("keep_history: true\n")
		fc.FileContents["acme/web/"+preset.OverlayPath] = []byte("keep_history: false\n")
		err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "acme/api")
		assert.Contains(t, err.Error(), "acme/web", "every offending repo is reported")
		assert.Contains(t, err.Error(), "adoption required")
	})
	t.Run("filter limits the check", func(t *testing.T) {
		m := newConvergeManifest("acme/api", "acme/web")
		fc := newFakeClientForBatch("acme/api", "acme/web")
		fc.FileContents["acme/web/"+preset.OverlayPath] = []byte("keep_history: false\n")
		require.NoError(t, PreflightManagedConfig(ctx, m, newTestClientFactory(fc), []string{"acme/api"}))
	})
	t.Run("filter naming a repo that is not in the manifest selects nothing", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		require.NoError(t, PreflightManagedConfig(ctx, m, newTestClientFactory(newFakeClientForBatch("acme/api")), []string{"acme/other"}))
	})
	t.Run("glob-covered filter name is checked", func(t *testing.T) {
		m := &Manifest{Version: 1, Defaults: testInferenceDefaults(), GitHub: &PlatformConfig{Repos: []RepoEntry{{Name: "acme/*"}}}}
		fc := newFakeClientForBatch()
		fc.FileContents["acme/fork/"+preset.OverlayPath] = []byte("keep_history: false\n")
		err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), []string{"acme/fork"})
		require.Error(t, err, "a glob-covered repo ListOrgRepos omits is still checked")
	})
	t.Run("undeclared base", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents["acme/api/"+preset.BasePath] = []byte("version: \"1\"\n")
		err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), preset.BasePath)

		m.Defaults.ConfigBase.Source = presetPath
		require.NoError(t, PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil), "a declared preset accounts for the base")
	})
	t.Run("read error", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := newFakeClientForBatch("acme/api")
		fc.GetFileContentErrors = map[string]error{"acme/api/" + preset.OverlayPath: fmt.Errorf("boom")}
		err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})
	t.Run("filter parse error", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		require.Error(t, PreflightManagedConfig(ctx, m, newTestClientFactory(newFakeClientForBatch("acme/api")), []string{"[bad"}))
	})
}

func TestPreflightManagedConfigNew(t *testing.T) {
	ctx := context.Background()
	m := &Manifest{Version: 1, Defaults: testInferenceDefaults(), GitHub: &PlatformConfig{}}
	fc := newFakeClientForBatch("acme/new")
	require.NoError(t, PreflightManagedConfigNew(ctx, m, newTestClientFactory(fc), ForgeGitHub, []string{"acme/new", "malformed"}))

	fc.FileContents["acme/new/"+preset.OverlayPath] = []byte("keep_history: true\n")
	err := PreflightManagedConfigNew(ctx, m, newTestClientFactory(fc), ForgeGitHub, []string{"acme/new"})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "acme/new") && strings.Contains(err.Error(), "adoption required"))
}

type failingFactory struct{}

func (failingFactory) ConfigFor(string) (ForgeConfig, error) {
	return ForgeConfig{}, fmt.Errorf("no client")
}

func TestWritebackClientFactoryErrors(t *testing.T) {
	m := newConvergeManifest("acme/api")
	_, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{Manifest: m}, failingFactory{}, nil)
	require.Error(t, err)
	err = preflightManagedConfigRepos(context.Background(), m, failingFactory{}, []ResolvedRepo{{Owner: "acme", Repo: "api", Forge: ForgeGitHub}})
	require.Error(t, err)
}

func TestEnsureManagedConfigDefaults_ExplicitRolesWithExistingOverlayOnFirstInstall(t *testing.T) {
	roles := []string{"fix"}
	markedOverlay := []byte(managedConfigMarker + "{}\n")

	t.Run("pending installation records roles only", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		path := writebackManifestFile(t, m)
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents["acme/api/"+preset.OverlayPath] = markedOverlay

		names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
			Manifest: m, ManifestPath: path, Roles: roles, RolesExplicit: true,
		}, newTestClientFactory(fc), nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"acme/api"}, names)

		declared, ok := m.GitHub.Repos[0].Config.ExplicitRoles()
		require.True(t, ok, "the explicit --roles must reach the manifest so convergence renders it")
		assert.Equal(t, roles, declared)
		resolved, _ := m.ResolveConfig("acme", "api")
		assert.Nil(t, resolved.Managed.IssueCreationConfig(),
			"an existing overlay never gets the automatic create_issues injection")
	})
	t.Run("installed repository is left alone", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents["acme/api/"+preset.OverlayPath] = markedOverlay
		fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte("shim")

		names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
			Manifest: m, Roles: roles, RolesExplicit: true,
		}, newTestClientFactory(fc), nil)
		require.NoError(t, err)
		assert.Empty(t, names, "an established installation does not take --roles through the first-install path")
		assert.False(t, m.GitHub.Repos[0].Config.IsSet())
	})
	t.Run("read-only manifest suggests the roles edit", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents["acme/api/"+preset.OverlayPath] = markedOverlay

		_, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
			Manifest: m, ManifestPath: "https://example.com/repos.yaml", Roles: roles, RolesExplicit: true,
		}, newTestClientFactory(fc), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "roles")
		assert.Contains(t, err.Error(), "read-only")
	})
}
