package cli

import (
	"context"
	"encoding/json"
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

// inference.auth is declared in repos.yaml only; the install flag that
// persisted it was removed (#8219).
func TestReposInstallCmd_InferenceAuthFlagRemoved(t *testing.T) {
	assert.Nil(t, newReposInstallCmd().Flags().Lookup("inference-auth"))
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

// A selection declared on the repository entry wins over the forge section
// and defaults; install never edits it.
func TestRunReposInstall_DeclaredEntrySelectionWinsAndIsNotRewritten(t *testing.T) {
	manifest := `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/api
      inference:
        auth: openai-api-key
`
	manifestPath := writeTestManifest(t, manifest)
	fc := newInstallFakeClient("acme/api")

	opts := useOpenAIInputs(githubManagedInstallOpts(manifestPath, fc))
	require.NoError(t, runReposInstall(context.Background(), opts))

	assert.True(t, fc.Secrets["acme/api/FULLSEND_OPENAI_API_KEY"], "the entry selection decides which credentials are written")
	reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, reloaded.GitHub.Repos[0].Inference.Auth)
	assert.Equal(t, repos.InferenceAuthVertexWIF, reloaded.Defaults.Inference.Auth, "defaults must not change")
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
	require.NoError(t, runReposInstall(context.Background(), opts))

	// The first install records the upstream create_issues default on an
	// explicit entry for that repo only (#8218). The entry is copied from
	// the glob so the glob selection is kept, and the glob is unchanged.
	reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	require.Len(t, reloaded.GitHub.Repos, 2)
	assert.Equal(t, "acme/*", reloaded.GitHub.Repos[0].Name)
	assert.False(t, reloaded.GitHub.Repos[0].Config.IsSet(), "the glob entry must not gain the per-repo default")
	assert.Equal(t, "acme/api", reloaded.GitHub.Repos[1].Name)
	assert.True(t, reloaded.GitHub.Repos[1].Config.IsSet())
	resolved, ok := reloaded.ResolveConfig("acme", "api")
	require.True(t, ok)
	assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, resolved.InferenceAuth, "the carved entry keeps the glob selection")
}

func TestRunReposInstall_GlobSelectionWinsOverConflictingDefaults(t *testing.T) {
	manifestPath := writeTestManifest(t, globInferenceAuthManifestYAML)
	fc := newInstallFakeClient("acme/api")

	opts := useOpenAIInputs(githubManagedInstallOpts(manifestPath, fc))
	opts.repoFilter = []string{"acme/api"}
	require.NoError(t, runReposInstall(context.Background(), opts))

	reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	require.Len(t, reloaded.GitHub.Repos, 2, "the glob is retained; the repo gets an explicit copy carrying the upstream create_issues default (#8218)")
	resolved, ok := reloaded.ResolveConfigWithGlobs("acme", "api")
	require.True(t, ok)
	assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, resolved.InferenceAuth, "the glob selection must beat the conflicting default")
}

// A glob pattern selector only filters declared repositories; it applies
// their manifest settings and never edits the glob entry.
func TestRunReposInstall_GlobSelectorFiltersDeclaredRepos(t *testing.T) {
	manifestPath := writeTestManifest(t, globInferenceAuthManifestYAML)
	fc := newInstallFakeClient("acme/api", "acme/api-gateway", "acme/web")

	opts := useOpenAIInputs(githubManagedInstallOpts(manifestPath, fc))
	opts.repoFilter = []string{"acme/api*"}
	require.NoError(t, runReposInstall(context.Background(), opts))

	assert.True(t, fc.Secrets["acme/api/FULLSEND_OPENAI_API_KEY"])
	assert.True(t, fc.Secrets["acme/api-gateway/FULLSEND_OPENAI_API_KEY"])
	assert.False(t, fc.Secrets["acme/web/FULLSEND_OPENAI_API_KEY"], "unselected repositories are not converged")

	reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	assert.Equal(t, "acme/*", reloaded.GitHub.Repos[0].Name)
	assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, reloaded.GitHub.Repos[0].Inference.Auth, "the glob entry is unchanged")
	assert.Empty(t, reloaded.GitHub.Repos[0].AppSet)
	assert.Empty(t, reloaded.GitHub.Repos[0].FullsendRef)
}

// A concrete repository covered by a glob is installed with the glob's
// settings even when the organization listing omits it (a fork). The only
// manifest change is the pre-existing first-install create_issues writeback.
func TestRunReposInstall_GlobCoveredRepoMissingFromExpansionIsInstalled(t *testing.T) {
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
	require.NoError(t, runReposInstall(context.Background(), opts))
	assert.True(t, fc.Secrets["acme/my-fork/FULLSEND_OPENAI_API_KEY"], "the glob's inference.auth selects the credentials")
	assert.False(t, fc.Secrets["acme/web/FULLSEND_OPENAI_API_KEY"])

	reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	resolved, ok := reloaded.ResolveConfigWithGlobs("acme", "my-fork")
	require.True(t, ok)
	assert.Equal(t, repos.InferenceAuthOpenAIAPIKey, resolved.InferenceAuth)
	assert.Equal(t, "acme/*", reloaded.GitHub.Repos[0].Name, "the glob entry is unchanged")
}

// noAuthGlobManifestYAML covers repos with a glob entry and no inference.auth
// selection at any level.
const noAuthGlobManifestYAML = `version: 1
github:
  fullsend_ref: v1.0.0
  repos:
    - name: acme/*
`

func TestRunReposInstall_GlobCoveredRepoMissingSelectionLeavesManifestUnchanged(t *testing.T) {
	manifestPath := writeTestManifest(t, noAuthGlobManifestYAML)
	fc := newInstallFakeClient("acme/api", "acme/web")

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.repoFilter = []string{"acme/api"}
	err := runReposInstall(context.Background(), opts)
	require.Error(t, err, "missing selection must fail the install")

	data, readErr := os.ReadFile(manifestPath)
	require.NoError(t, readErr)
	assert.Equal(t, noAuthGlobManifestYAML, string(data), "a repository without an effective inference.auth gains no entry")
	assert.Empty(t, fc.Variables)
	assert.Empty(t, fc.CreatedSecrets)
}

// mixedInferenceAuthManifestYAML has one repository with a valid selection
// and one whose entry carries an invalid inference.auth value.
const mixedInferenceAuthManifestYAML = `version: 1
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/api
      inference:
        auth: vertex-wif
    - name: acme/web
      inference:
        auth: bogus
`

func runStatusJSONFiltered(t *testing.T, manifestPath string, filter []string) (repos.StatusResult, error) {
	t.Helper()
	cmd := newReposStatusCmd()
	cmd.SetContext(context.Background())
	var buf strings.Builder
	cmd.SetOut(&buf)
	err := runReposStatus(cmd, &reposStatusConfig{
		manifest:    manifestPath,
		jsonOutput:  true,
		concurrency: 4,
		repoFilter:  filter,
		testClient:  newInstallFakeClient("acme/api", "acme/web"),
	})
	var result repos.StatusResult
	require.NotEmpty(t, buf.String(), "status must render output despite the invalid entry")
	require.NoError(t, json.Unmarshal([]byte(buf.String()), &result))
	return result, err
}

func TestRunReposStatus_InvalidInferenceAuthIsPerRepoError(t *testing.T) {
	manifestPath := writeTestManifest(t, mixedInferenceAuthManifestYAML)

	result, err := runStatusJSONFiltered(t, manifestPath, nil)
	require.Error(t, err, "an errored repository still fails the command")
	assert.NotContains(t, err.Error(), "manifest validation failed")
	require.Len(t, result.Repos, 2, "healthy repositories are still evaluated")
	assert.Equal(t, 1, result.Summary.Errored)

	byRepo := map[string]repos.RepoStatus{}
	for _, rs := range result.Repos {
		byRepo[rs.Owner+"/"+rs.Repo] = rs
	}
	assert.Empty(t, byRepo["acme/api"].Error)
	assert.Contains(t, byRepo["acme/web"].Error, "acme/web")
	assert.Contains(t, byRepo["acme/web"].Error, "bogus")
}

func TestRunReposStatus_InvalidInferenceAuthFilteredToHealthyRepo(t *testing.T) {
	manifestPath := writeTestManifest(t, mixedInferenceAuthManifestYAML)

	result, err := runStatusJSONFiltered(t, manifestPath, []string{"acme/api"})
	require.Len(t, result.Repos, 1)
	assert.Equal(t, "api", result.Repos[0].Repo)
	assert.Empty(t, result.Repos[0].Error)
	assert.Zero(t, result.Summary.Errored)
	if err != nil {
		assert.NotContains(t, err.Error(), "manifest validation failed")
	}
}

func TestRunReposStatus_StructuralManifestErrorsStillFail(t *testing.T) {
	manifestPath := writeTestManifest(t, "version: 2\n")
	cmd := newReposStatusCmd()
	cmd.SetContext(context.Background())
	err := runReposStatus(cmd, &reposStatusConfig{manifest: manifestPath, concurrency: 4, testClient: newInstallFakeClient()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "manifest validation failed")
}
