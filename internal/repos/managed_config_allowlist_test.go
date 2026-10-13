package repos

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An explicit empty allowed_remote_resources list denies every remote
// resource; nil inherits. A manifest rewrite must keep the distinction in
// every config layer (#8218, #8330).
func TestManifestMarshalPreservesEmptyAllowedRemoteResources(t *testing.T) {
	m := newConvergeManifest("acme/api", "acme/web")
	m.Defaults.Config = mustManagedConfig(t, "allowed_remote_resources: []\n")
	m.GitHub.Config = mustManagedConfig(t, "allowed_remote_resources: []\n")
	m.GitHub.Repos[0].Config = mustManagedConfig(t, "allowed_remote_resources: []\n")

	path := filepath.Join(t.TempDir(), "repos.yaml")
	require.NoError(t, writeManifest(path, m))
	reloaded, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)

	for name, layer := range map[string]config.ManagedConfig{
		"defaults.config": reloaded.Defaults.Config,
		"github.config":   reloaded.GitHub.Config,
		"entry config":    reloaded.GitHub.Repos[0].Config,
	} {
		require.True(t, layer.IsSet(), name)
		require.NotNil(t, layer.Writer().AllowedResources(), "%s deny-all survives a round trip", name)
		assert.Empty(t, layer.Writer().AllowedResources(), name)
	}
	assert.False(t, reloaded.GitHub.Repos[1].Config.IsSet(), "an unset entry stays unset")

	api, _ := reloaded.ResolveConfig("acme", "api")
	assert.NotNil(t, api.Managed.AllowedResources())
	assert.Empty(t, api.Managed.AllowedResources())
}

func TestManifestMarshalOmitsUnsetAllowedRemoteResources(t *testing.T) {
	m := newConvergeManifest("acme/api")
	data, err := MarshalWithHeader(m)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "allowed_remote_resources")
}

// Writeback must not erase a deny-all declaration on an unselected,
// not-yet-installed repository when it rewrites the manifest.
func TestEnsureManagedConfigDefaults_KeepsDenyAllOnUnselectedRepo(t *testing.T) {
	m := newConvergeManifest("acme/api", "acme/web")
	m.GitHub.Repos[1].Config = mustManagedConfig(t, "allowed_remote_resources: []\n")
	path := writebackManifestFile(t, m)
	fc := newFakeClientForBatch("acme/api", "acme/web")

	names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, ManifestPath: path, RepoFilter: []string{"acme/api"},
	}, newTestClientFactory(fc), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api"}, names)

	reloaded, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	web := reloaded.GitHub.Repos[1].Config.Writer()
	require.NotNil(t, web)
	require.NotNil(t, web.AllowedResources())
	assert.Empty(t, web.AllowedResources())
}

// The upstream create_issues grant is only a default for a pristine
// repository; one with an existing base layer needs an operator declaration.
func TestEnsureManagedConfigDefaults_NonPristineRepoGetsNoUpstreamGrant(t *testing.T) {
	m := newConvergeManifest("acme/api")
	path := writebackManifestFile(t, m)
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/"+preset.BasePath] = []byte("version: \"1\"\n")

	names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, ManifestPath: path,
	}, newTestClientFactory(fc), nil)
	require.NoError(t, err)
	assert.Empty(t, names, "no implicit grant for a repository with an existing layer")
	assert.False(t, m.GitHub.Repos[0].Config.IsSet())
}

func TestPreflightManagedConfig_PresetLoadFailureIsReportedInGuidance(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.ConfigBase.Source = filepath.Join(t.TempDir(), "missing-preset.yaml")
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("keep_history: true\n")

	err := PreflightManagedConfig(context.Background(), m, newTestClientFactory(fc), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "adoption required")
	assert.Contains(t, err.Error(), "loading the declared preset")
}

// pristineInstallation is the single predicate convergence, status and
// writeback share for "no established installation".
func TestPristineInstallation(t *testing.T) {
	present := func(names ...string) []ComponentStatus {
		out := make([]ComponentStatus, len(names))
		for i, n := range names {
			out[i] = ComponentStatus{Name: n, Present: true}
		}
		return out
	}
	cases := map[string]struct {
		components []ComponentStatus
		want       bool
	}{
		"no components":        {nil, true},
		"credentials only":     {present("var:FULLSEND_MINT_URL", "secret:FULLSEND_OPENAI_API_KEY"), true},
		"cli-created schedule": {present("var:FULLSEND_MINT_URL", "schedule:slash-poll", "schedule:event-poll"), true},
		"schedule with shim":   {present("schedule:slash-poll", "workflow"), false},
		"identifiers":          {present(openAIWIFComponent), true},
		"residual diagnostic":  {present("secret:FULLSEND_OPENAI_API_KEY", openAIWIFResidualComponent), true},
		"thin caller":          {present("var:FULLSEND_MINT_URL", "thin-caller:.github/workflows/triage.yml"), false},
		"scaffold file":        {present("scaffold:.fullsend/x"), false},
		"absent component":     {[]ComponentStatus{{Name: "workflow", Present: false}}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, pristineInstallation(tc.components))
		})
	}
}

// A repository holding only pre-provisioned credentials is pristine for
// writeback as it is for convergence, so the first install records the
// upstream issue-target default.
func TestEnsureManagedConfigDefaults_CredentialOnlyRepoIsPristine(t *testing.T) {
	m := newConvergeManifest("acme/api")
	path := writebackManifestFile(t, m)
	fc := newFakeClientForBatch("acme/api")
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"

	names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, ManifestPath: path,
	}, newTestClientFactory(fc), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api"}, names)
	assert.True(t, m.GitHub.Repos[0].Config.IsSet())
}

// An explicit empty allowlist resets inherited prefixes, but the sparse
// managed file flattens layers and cannot record the reset. A non-empty list
// below an empty one is rejected rather than silently re-admitting the
// preset and code-default prefixes.
func TestValidate_RejectsAllowlistBelowExplicitEmptyList(t *testing.T) {
	const empty = "allowed_remote_resources: []\n"
	const list = "allowed_remote_resources: [\"https://specific.example.com/\"]\n"
	cases := map[string]struct {
		defaults, platform, entry string
		wantErr                   string
	}{
		"defaults empty, platform non-empty":   {empty, list, "", "github.config.allowed_remote_resources"},
		"defaults empty, repository non-empty": {empty, "", list, "github.repos[acme/api].config.allowed_remote_resources"},
		"platform empty, repository non-empty": {"", empty, list, "github.repos[acme/api].config.allowed_remote_resources"},
		"defaults empty, platform unset, repository non-empty": {empty, "allowed_remote_resources: null\n", list,
			"github.repos[acme/api].config.allowed_remote_resources"},
		"empty at every layer":                  {empty, empty, empty, ""},
		"non-empty then empty":                  {list, "", empty, ""},
		"non-empty then non-empty":              {list, list, list, ""},
		"empty only at the most specific layer": {"", "", empty, ""},
		"empty, non-empty, empty":               {empty, list, empty, ""},
		"empty, non-empty, unset":               {empty, list, "", "github.config.allowed_remote_resources"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := newConvergeManifest("acme/api")
			if tc.defaults != "" {
				m.Defaults.Config = mustManagedConfig(t, tc.defaults)
			}
			if tc.platform != "" {
				m.GitHub.Config = mustManagedConfig(t, tc.platform)
			}
			if tc.entry != "" {
				m.GitHub.Repos[0].Config = mustManagedConfig(t, tc.entry)
			}
			err := m.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), "cannot record that reset")
		})
	}
}
