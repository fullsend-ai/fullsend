package cli

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/repos"
)

// globNoInferenceAuthManifestYAML is a manifest with a glob entry and no
// inference.auth selection at any level.
const globNoInferenceAuthManifestYAML = `version: 1
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/*
`

func TestRunReposInstall_GlobCarveWithoutSelectionFailsBeforeWrite(t *testing.T) {
	manifestPath := writeTestManifest(t, globNoInferenceAuthManifestYAML)
	fc := newInstallFakeClient("acme/api")

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.repoFilter = []string{"acme/api"}
	opts.forge = repos.ForgeGitHub
	opts.runtime = "pi"
	err := runReposInstall(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no inference authentication selected for acme/api")

	data, readErr := os.ReadFile(manifestPath)
	require.NoError(t, readErr)
	assert.Equal(t, globNoInferenceAuthManifestYAML, string(data), "override-driven carving must not persist an entry that fails the selection check")
}

func TestRunReposInstall_UndiscoveredForkWithoutSelectionFailsBeforeWrite(t *testing.T) {
	manifestPath := writeTestManifest(t, globNoInferenceAuthManifestYAML)
	fc := newInstallFakeClient("acme/web", "acme/my-fork")
	// Forks are excluded from ListOrgRepos, so expansion never discovers
	// acme/my-fork even though the acme/* pattern matches it.
	for i := range fc.Repos {
		if fc.Repos[i].Name == "my-fork" {
			fc.Repos[i].Fork = true
		}
	}

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.repoFilter = []string{"acme/my-fork"}
	opts.forge = repos.ForgeGitHub
	err := runReposInstall(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no inference authentication selected for acme/my-fork")

	data, readErr := os.ReadFile(manifestPath)
	require.NoError(t, readErr)
	assert.Equal(t, globNoInferenceAuthManifestYAML, string(data), "carving an undiscovered fork must not persist an entry that fails the selection check")
}

func TestRunReposInstall_GlobCarveWithFlagSatisfiesSelectionCheck(t *testing.T) {
	manifestPath := writeTestManifest(t, globNoInferenceAuthManifestYAML)
	fc := newInstallFakeClient("acme/api")

	opts := useOpenAIInputs(githubManagedInstallOpts(manifestPath, fc))
	opts.repoFilter = []string{"acme/api"}
	opts.forge = repos.ForgeGitHub
	opts.runtime = "pi"
	opts.inferenceAuth = repos.InferenceAuthOpenAIAPIKey
	require.NoError(t, runReposInstall(context.Background(), opts))

	reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	require.Len(t, reloaded.GitHub.Repos, 2)
	assert.Equal(t, "acme/api", reloaded.GitHub.Repos[1].Name)
	assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, reloaded.GitHub.Repos[1].Inference.Auth)
}
