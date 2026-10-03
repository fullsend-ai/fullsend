package cli

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/repos"
)

// noInferenceAuthManifestYAML is a manifest written before inference.auth
// existed: no level selects an inference authentication method.
const noInferenceAuthManifestYAML = `version: 1
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/api
    - name: acme/web
`

func TestReposInstallCmd_InferenceAuthFlagRegistered(t *testing.T) {
	flag := newReposInstallCmd().Flags().Lookup("inference-auth")
	require.NotNil(t, flag)
	assert.Empty(t, flag.DefValue, "there is no implicit default")
	assert.Contains(t, flag.Usage, "vertex-wif")
	assert.Contains(t, flag.Usage, "openai-api-key")
}

func TestRunReposInstall_InferenceAuthInvalidFlag(t *testing.T) {
	manifestPath := writeTestManifest(t, noInferenceAuthManifestYAML)
	fc := newInstallFakeClient("acme/api")

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.repoFilter = []string{"acme/api"}
	opts.inferenceAuth = "vertex"
	err := runReposInstall(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-auth")
	assert.Contains(t, err.Error(), "vertex-wif, openai-api-key")

	data, readErr := os.ReadFile(manifestPath)
	require.NoError(t, readErr)
	assert.Equal(t, noInferenceAuthManifestYAML, string(data), "invalid flag must not touch the manifest")
	assert.Empty(t, fc.VariableValues, "invalid flag must not mutate the forge")
}

func TestRunReposInstall_InferenceAuthPersistsOnExistingEntry(t *testing.T) {
	manifestPath := writeTestManifest(t, noInferenceAuthManifestYAML)
	fc := newInstallFakeClient("acme/api", "acme/web")

	opts := useOpenAIInputs(githubManagedInstallOpts(manifestPath, fc))
	opts.repoFilter = []string{"acme/api"}
	opts.inferenceAuth = repos.InferenceAuthOpenAIAPIKey
	require.NoError(t, runReposInstall(context.Background(), opts))

	reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, reloaded.GitHub.Repos[0].Inference.Auth, "explicit selection is kept at repo scope")
	assert.Empty(t, reloaded.GitHub.Repos[1].Inference.Auth, "unselected repos must not change")
	assert.Empty(t, reloaded.GitHub.Inference.Auth, "forge section must not change")
	assert.Empty(t, reloaded.Defaults.Inference.Auth, "defaults must not change")

	// A later status run resolves the persisted selection for acme/api and
	// still reports the unselected sibling as misconfigured.
	result, statusErr := statusJSON(t, manifestPath, fc)
	require.Error(t, statusErr)
	for _, rs := range result.Repos {
		switch rs.Owner + "/" + rs.Repo {
		case "acme/api":
			assert.NotContains(t, rs.Error, "inference authentication")
		case "acme/web":
			assert.Contains(t, rs.Error, "no inference authentication selected for acme/web")
		}
	}

	// Re-running with the same selection is a no-op for the manifest.
	before, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	require.NoError(t, runReposInstall(context.Background(), opts))
	after, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
}

func TestRunReposInstall_InferenceAuthDryRunDoesNotPersist(t *testing.T) {
	manifestPath := writeTestManifest(t, noInferenceAuthManifestYAML)
	fc := newInstallFakeClient("acme/api")

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.repoFilter = []string{"acme/api"}
	opts.inferenceAuth = repos.InferenceAuthVertexWIF
	opts.dryRun = true
	require.NoError(t, runReposInstall(context.Background(), opts), "dry run resolves the in-memory selection")

	data, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.Equal(t, noInferenceAuthManifestYAML, string(data), "dry run must not write the manifest")
}

func TestRunReposInstall_MissingInferenceAuthReportsPerRepoError(t *testing.T) {
	manifestPath := writeTestManifest(t, noInferenceAuthManifestYAML)
	fc := newInstallFakeClient("acme/api", "acme/web")

	err := runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc))
	require.Error(t, err, "missing selection must fail the install")
	for key := range fc.VariableValues {
		assert.Fail(t, "no variables may be written without an inference selection", key)
	}
	for _, path := range []string{"acme/api/.github/workflows/fullsend.yaml", "acme/web/.github/workflows/fullsend.yaml"} {
		assert.Empty(t, fc.FileContents[path], "no scaffold may be written without an inference selection")
	}

	data, readErr := os.ReadFile(manifestPath)
	require.NoError(t, readErr)
	assert.Equal(t, noInferenceAuthManifestYAML, string(data), "existing manifest must not silently gain a selection")
}

func TestRunReposInstall_NewEntryInheritsForgeSelection(t *testing.T) {
	manifestPath := writeTestManifest(t, `version: 1
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  inference:
    auth: vertex-wif
  repos:
    - name: acme/api
`)
	fc := newInstallFakeClient("acme/api", "acme/new")

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.repoFilter = []string{"acme/new"}
	opts.forge = repos.ForgeGitHub
	require.NoError(t, runReposInstall(context.Background(), opts))

	reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	var found bool
	for _, entry := range reloaded.GitHub.Repos {
		if entry.Name == "acme/new" {
			found = true
			assert.Empty(t, entry.Inference.Auth, "without the flag the new entry inherits instead of pinning a value")
		}
	}
	require.True(t, found)
	resolved, ok := reloaded.ResolveConfig("acme", "new")
	require.True(t, ok)
	assert.Equal(t, repos.InferenceAuthVertexWIF, resolved.InferenceAuth)
}

func TestRunReposInstall_NewEntryFlagOverridesInheritedSelection(t *testing.T) {
	manifestPath := writeTestManifest(t, `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/api
`)
	fc := newInstallFakeClient("acme/api", "acme/new")

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.repoFilter = []string{"acme/new"}
	opts.forge = repos.ForgeGitHub
	opts.inferenceAuth = repos.InferenceAuthVertexWIF
	require.NoError(t, runReposInstall(context.Background(), opts))

	reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	for _, entry := range reloaded.GitHub.Repos {
		switch entry.Name {
		case "acme/new":
			assert.Equal(t, repos.InferenceAuthVertexWIF, entry.Inference.Auth, "an explicit flag is pinned even when it equals the inherited value")
		case "acme/api":
			assert.Empty(t, entry.Inference.Auth)
		}
	}
}

func TestRunReposInstall_NewGitLabEntryWithoutSelectionFailsBeforeWrite(t *testing.T) {
	manifest := `version: 1
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: group/existing
`
	manifestPath := writeTestManifest(t, manifest)
	fc := newInstallFakeClient("group/project")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		repoFilter:  []string{"group/project"},
		forge:       repos.ForgeGitLab,
		testClient:  fc,
	})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "no inference authentication selected for group/project"), err.Error())
	assert.Contains(t, err.Error(), "gitlab section")

	data, readErr := os.ReadFile(manifestPath)
	require.NoError(t, readErr)
	assert.Equal(t, manifest, string(data), "manifest must not be written before the selection error")
}

const globInferenceAuthManifestYAML = `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/*
      inference:
        auth: openai-api-key
`

func TestRunReposInstall_GlobOnlySelectionIsKept(t *testing.T) {
	manifest := strings.Replace(globInferenceAuthManifestYAML, "defaults:\n  inference:\n    auth: vertex-wif\n", "", 1)
	manifestPath := writeTestManifest(t, manifest)
	fc := newInstallFakeClient("acme/api")

	opts := useOpenAIInputs(githubManagedInstallOpts(manifestPath, fc))
	opts.repoFilter = []string{"acme/api"}
	opts.forge = repos.ForgeGitHub
	require.NoError(t, runReposInstall(context.Background(), opts))

	data, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.Equal(t, manifest, string(data), "a glob-covered repo must not gain an entry that drops the glob selection")
}

func TestRunReposInstall_GlobSelectionWinsOverConflictingDefaults(t *testing.T) {
	manifestPath := writeTestManifest(t, globInferenceAuthManifestYAML)
	fc := newInstallFakeClient("acme/api")

	opts := useOpenAIInputs(githubManagedInstallOpts(manifestPath, fc))
	opts.repoFilter = []string{"acme/api"}
	opts.forge = repos.ForgeGitHub
	require.NoError(t, runReposInstall(context.Background(), opts))

	reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	require.Len(t, reloaded.GitHub.Repos, 1, "the glob is retained without an explicit copy")
	resolved, ok := reloaded.ResolveConfigWithGlobs("acme", "api")
	require.True(t, ok)
	assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, resolved.InferenceAuth, "the glob selection must beat the conflicting default")
}

func TestRunReposInstall_FlagOverridePreservesGlobOverrides(t *testing.T) {
	manifestPath := writeTestManifest(t, globInferenceAuthManifestYAML)
	fc := newInstallFakeClient("acme/api", "acme/web")

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.repoFilter = []string{"acme/api"}
	opts.forge = repos.ForgeGitHub
	opts.inferenceAuth = repos.InferenceAuthVertexWIF
	require.NoError(t, runReposInstall(context.Background(), opts))

	reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	require.Len(t, reloaded.GitHub.Repos, 2)
	assert.Equal(t, "acme/*", reloaded.GitHub.Repos[0].Name)
	assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, reloaded.GitHub.Repos[0].Inference.Auth, "the glob keeps its selection")
	assert.Equal(t, "acme/api", reloaded.GitHub.Repos[1].Name)
	assert.Equal(t, repos.InferenceAuthVertexWIF, reloaded.GitHub.Repos[1].Inference.Auth)
	assert.Equal(t, "v1.0.0", reloaded.GitHub.FullsendRef)

	web, ok := reloaded.ResolveConfigWithGlobs("acme", "web")
	require.True(t, ok)
	assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, web.InferenceAuth, "sibling repos keep the glob selection")
}

func TestRunReposInstall_NarrowerGlobFilterAppliesFlagToMatchingRepos(t *testing.T) {
	manifestPath := writeTestManifest(t, globInferenceAuthManifestYAML)
	fc := newInstallFakeClient("acme/api", "acme/api-gateway", "acme/web")

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.repoFilter = []string{"acme/api*"}
	opts.forge = repos.ForgeGitHub
	opts.inferenceAuth = repos.InferenceAuthVertexWIF
	require.NoError(t, runReposInstall(context.Background(), opts))

	reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	for name, want := range map[string]string{
		"api":         repos.InferenceAuthVertexWIF,
		"api-gateway": repos.InferenceAuthVertexWIF,
		"web":         repos.InferenceAuthOpenAIAPIKey,
	} {
		resolved, ok := reloaded.ResolveConfigWithGlobs("acme", name)
		require.True(t, ok, name)
		assert.Equal(t, want, resolved.InferenceAuth, name)
	}
	assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, reloaded.GitHub.Repos[0].Inference.Auth, "the glob entry is unchanged")
}

func TestRunReposInstall_AppSetOnGlobCoveredRepoCopiesGlobEntry(t *testing.T) {
	for _, withAuth := range []bool{false, true} {
		name := "without-inference-auth"
		if withAuth {
			name = "with-inference-auth"
		}
		t.Run(name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, globInferenceAuthManifestYAML)
			fc := newInstallFakeClient("acme/api", "acme/web")

			opts := githubManagedInstallOpts(manifestPath, fc)
			opts.repoFilter = []string{"acme/api"}
			opts.forge = repos.ForgeGitHub
			opts.appSet = "custom-set"
			wantAuth := repos.InferenceAuthOpenAIAPIKey
			if !withAuth {
				useOpenAIInputs(opts)
			}
			if withAuth {
				opts.inferenceAuth = repos.InferenceAuthVertexWIF
				wantAuth = repos.InferenceAuthVertexWIF
			}
			require.NoError(t, runReposInstall(context.Background(), opts))

			reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
			require.NoError(t, err)
			require.Len(t, reloaded.GitHub.Repos, 2)
			assert.Equal(t, "acme/*", reloaded.GitHub.Repos[0].Name)
			assert.Empty(t, reloaded.GitHub.Repos[0].AppSet, "the glob entry is unchanged")
			assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, reloaded.GitHub.Repos[0].Inference.Auth)

			api := reloaded.GitHub.Repos[1]
			assert.Equal(t, "acme/api", api.Name)
			assert.Equal(t, "custom-set", api.AppSet)
			assert.Equal(t, wantAuth, api.Inference.Auth, "the explicit entry keeps the glob's inference.auth unless overridden")

			web, ok := reloaded.ResolveConfigWithGlobs("acme", "web")
			require.True(t, ok)
			assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, web.InferenceAuth, "siblings keep the glob selection")
		})
	}
}

func TestRunReposInstall_OverridesOnGlobCoveredRepoCopyGlobEntry(t *testing.T) {
	for _, withAuth := range []bool{false, true} {
		name := "without-inference-auth"
		if withAuth {
			name = "with-inference-auth"
		}
		t.Run(name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, globInferenceAuthManifestYAML)
			fc := newInstallFakeClient("acme/api", "acme/web")

			opts := githubManagedInstallOpts(manifestPath, fc)
			opts.repoFilter = []string{"acme/api"}
			opts.forge = repos.ForgeGitHub
			opts.fullsendRef = "v2.0.0"
			opts.mintURL = "https://mint-override.example.com"
			opts.allowedRemoteResources = []string{"https://github.com/acme/"}
			wantAuth := repos.InferenceAuthOpenAIAPIKey
			if !withAuth {
				useOpenAIInputs(opts)
			}
			if withAuth {
				opts.inferenceAuth = repos.InferenceAuthVertexWIF
				wantAuth = repos.InferenceAuthVertexWIF
			}
			require.NoError(t, runReposInstall(context.Background(), opts))

			reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
			require.NoError(t, err)
			require.Len(t, reloaded.GitHub.Repos, 2)
			glob := reloaded.GitHub.Repos[0]
			assert.Equal(t, "acme/*", glob.Name)
			assert.Empty(t, glob.FullsendRef, "the glob entry is unchanged")
			assert.Empty(t, glob.MintURL, "the glob entry is unchanged")
			assert.Empty(t, glob.AllowedRemoteResources, "the glob entry is unchanged")
			assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, glob.Inference.Auth)

			api := reloaded.GitHub.Repos[1]
			assert.Equal(t, "acme/api", api.Name)
			assert.Equal(t, "v2.0.0", api.FullsendRef)
			assert.Equal(t, "https://mint-override.example.com", api.MintURL)
			assert.Equal(t, []string{"https://github.com/acme/"}, api.AllowedRemoteResources)
			assert.Equal(t, wantAuth, api.Inference.Auth, "the explicit entry keeps the glob's inference.auth unless overridden")

			web, ok := reloaded.ResolveConfigWithGlobs("acme", "web")
			require.True(t, ok)
			assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, web.InferenceAuth, "siblings keep the glob selection")
			assert.Equal(t, "v1.0.0", reloaded.GitHub.FullsendRef, "forge section is unchanged")
		})
	}
}

func TestRunReposInstall_GlobCoveredRepoMissingFromExpansionGetsExplicitEntry(t *testing.T) {
	manifestPath := writeTestManifest(t, globInferenceAuthManifestYAML)
	fc := newInstallFakeClient("acme/web", "acme/my-fork")
	// Forks are excluded from ListOrgRepos, so glob expansion never
	// discovers acme/my-fork even though the acme/* pattern matches it.
	for i := range fc.Repos {
		if fc.Repos[i].Name == "my-fork" {
			fc.Repos[i].Fork = true
		}
	}

	opts := useOpenAIInputs(githubManagedInstallOpts(manifestPath, fc))
	opts.repoFilter = []string{"acme/my-fork"}
	opts.forge = repos.ForgeGitHub
	require.NoError(t, runReposInstall(context.Background(), opts))

	reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	require.Len(t, reloaded.GitHub.Repos, 2)
	assert.Equal(t, "acme/*", reloaded.GitHub.Repos[0].Name)
	fork := reloaded.GitHub.Repos[1]
	assert.Equal(t, "acme/my-fork", fork.Name)
	assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, fork.Inference.Auth, "the explicit entry keeps the glob's inference.auth")
}

// noAuthGlobManifestYAML covers repos with a glob entry and no inference.auth
// selection at any level.
const noAuthGlobManifestYAML = `version: 1
github:
  fullsend_ref: v1.0.0
  repos:
    - name: acme/*
`

func TestRunReposInstall_AppSetOnGlobCoveredRepoMissingSelectionLeavesManifestUnchanged(t *testing.T) {
	manifestPath := writeTestManifest(t, noAuthGlobManifestYAML)
	fc := newInstallFakeClient("acme/api", "acme/web")

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.repoFilter = []string{"acme/api"}
	opts.forge = repos.ForgeGitHub
	opts.appSet = "custom-set"
	err := runReposInstall(context.Background(), opts)
	require.Error(t, err, "missing selection must fail the install")

	data, readErr := os.ReadFile(manifestPath)
	require.NoError(t, readErr)
	assert.Equal(t, noAuthGlobManifestYAML, string(data), "--app-set must not persist an entry without an effective inference.auth")
}
