package repos

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An explicit empty allowed_remote_resources list denies every remote
// resource; nil inherits. A manifest rewrite must keep the distinction in
// both locations (#8218).
func TestManifestMarshalPreservesEmptyAllowedRemoteResources(t *testing.T) {
	m := newConvergeManifest("acme/api", "acme/web")
	m.Defaults.AllowedRemoteResources = []string{}
	m.GitHub.Repos[0].AllowedRemoteResources = []string{}

	path := filepath.Join(t.TempDir(), "repos.yaml")
	require.NoError(t, writeManifest(path, m))
	reloaded, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)

	require.NotNil(t, reloaded.Defaults.AllowedRemoteResources, "defaults deny-all survives a round trip")
	assert.Empty(t, reloaded.Defaults.AllowedRemoteResources)
	require.NotNil(t, reloaded.GitHub.Repos[0].AllowedRemoteResources, "entry deny-all survives a round trip")
	assert.Empty(t, reloaded.GitHub.Repos[0].AllowedRemoteResources)
	assert.Nil(t, reloaded.GitHub.Repos[1].AllowedRemoteResources, "an unset entry stays unset")

	api, _ := reloaded.ResolveConfig("acme", "api")
	assert.NotNil(t, api.AllowedRemoteResources)
	assert.Empty(t, api.AllowedRemoteResources)
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
	m.GitHub.Repos[1].AllowedRemoteResources = []string{}
	path := writebackManifestFile(t, m)
	fc := newFakeClientForBatch("acme/api", "acme/web")

	names, err := EnsureManagedConfigDefaults(context.Background(), ManagedConfigWritebackConfig{
		Manifest: m, ManifestPath: path, RepoFilter: []string{"acme/api"},
	}, newTestClientFactory(fc), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api"}, names)

	reloaded, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	require.NotNil(t, reloaded.GitHub.Repos[1].AllowedRemoteResources)
	assert.Empty(t, reloaded.GitHub.Repos[1].AllowedRemoteResources)
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
