package repos

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// overlappingForgeManifest has a GitHub glob and a narrower GitLab glob that
// both textually cover acme/api1, which exists only on GitLab.
func overlappingForgeManifest() (*Manifest, *perForgeClientFactory) {
	m := &Manifest{
		Version:  1,
		Defaults: DefaultsConfig{Inference: InferenceSettings{Auth: InferenceAuthVertexWIF}},
		GitHub:   &PlatformConfig{Repos: []RepoEntry{{Name: "acme/*", FullsendRef: "gh-ref"}}},
		GitLab:   &PlatformConfig{Repos: []RepoEntry{{Name: "acme/api*", FullsendRef: "gl-ref"}}},
	}
	ghClient := forge.NewFakeClient()
	ghClient.Repos = []forge.Repository{{Name: "web", FullName: "acme/web"}}
	glClient := forge.NewFakeClient()
	glClient.Repos = []forge.Repository{{Name: "api1", FullName: "acme/api1"}}
	return m, &perForgeClientFactory{clients: map[string]forge.Client{
		ForgeGitHub: ghClient,
		ForgeGitLab: glClient,
	}}
}

func TestUpdateInferenceAuth_ConcreteFilterKeepsExplicitEntryOnDiscoveringForge(t *testing.T) {
	m, clients := overlappingForgeManifest()

	updated, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, []string{"acme/api1"}, InferenceAuthOpenAIAPIKey, clients)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api1"}, updated)

	require.Len(t, m.GitHub.Repos, 1, "no explicit entry may be created on the forge that did not discover the repo")
	require.Len(t, m.GitLab.Repos, 2)
	carved := m.GitLab.Repos[1]
	assert.Equal(t, "acme/api1", carved.Name)
	assert.Equal(t, InferenceAuthOpenAIAPIKey, carved.Inference.Auth)
	assert.Equal(t, "gl-ref", carved.FullsendRef)

	resolved, err := m.ExpandGlobs(context.Background(), clients)
	require.NoError(t, err)
	found := false
	for _, rr := range resolved {
		if rr.Repo == "api1" {
			found = true
			assert.Equal(t, ForgeGitLab, rr.Forge)
			assert.Equal(t, InferenceAuthOpenAIAPIKey, rr.Entry.Inference.Auth)
		}
	}
	assert.True(t, found)
}

func TestUpdateInferenceAuth_ConcreteFilterUndiscoveredFallsBackToFirstGlob(t *testing.T) {
	m, clients := overlappingForgeManifest()

	updated, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, []string{"acme/api-fork"}, InferenceAuthOpenAIAPIKey, clients)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api-fork"}, updated)
	require.Len(t, m.GitHub.Repos, 2, "an undiscovered target falls back to the first covering glob")
	assert.Equal(t, "acme/api-fork", m.GitHub.Repos[1].Name)
	assert.Len(t, m.GitLab.Repos, 1)
}

func TestCarveOutGlobCovered_ConcreteTargetUsesDiscoveringForge(t *testing.T) {
	m, clients := overlappingForgeManifest()

	var gotForge string
	carved, err := CarveOutGlobCovered(context.Background(), ManifestEditConfig{Manifest: m}, []string{"acme/api1"}, clients,
		func(forgeName string, platform *PlatformConfig, entry *RepoEntry) error {
			gotForge = forgeName
			assert.Same(t, m.GitLab, platform)
			entry.Runtime = "pi"
			return nil
		})
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api1"}, carved)
	assert.Equal(t, ForgeGitLab, gotForge)
	require.Len(t, m.GitHub.Repos, 1)
	require.Len(t, m.GitLab.Repos, 2)
	assert.Equal(t, "gl-ref", m.GitLab.Repos[1].FullsendRef)
	assert.Equal(t, "pi", m.GitLab.Repos[1].Runtime)
}

func TestCarveOutGlobCovered_ApplyErrorLeavesManifestFileUnchanged(t *testing.T) {
	m, clients := overlappingForgeManifest()
	path := filepath.Join(t.TempDir(), "repos.yaml")
	require.NoError(t, writeManifest(path, m))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	_, err = CarveOutGlobCovered(context.Background(), ManifestEditConfig{Manifest: m, ManifestPath: path}, []string{"acme/api1"}, clients,
		func(string, *PlatformConfig, *RepoEntry) error { return fmt.Errorf("rejected") })
	require.Error(t, err)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
}

func TestUpdateInferenceAuth_ConcreteFilterDiscoveryErrorWritesNothing(t *testing.T) {
	m, _ := overlappingForgeManifest()
	failing := &perForgeClientFactory{errs: map[string]error{ForgeGitLab: assert.AnError}}
	path := filepath.Join(t.TempDir(), "repos.yaml")
	require.NoError(t, writeManifest(path, m))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	_, err = UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m, ManifestPath: path}, []string{"acme/api1"}, InferenceAuthOpenAIAPIKey, failing)
	require.Error(t, err)
	assert.Len(t, m.GitHub.Repos, 1, "a failed discovery must not carve an entry on the fallback forge")
	assert.Len(t, m.GitLab.Repos, 1)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))

	// Retrying with a working factory puts the entry on the discovering forge.
	_, clients := overlappingForgeManifest()
	updated, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m, ManifestPath: path}, []string{"acme/api1"}, InferenceAuthOpenAIAPIKey, clients)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api1"}, updated)
	assert.Len(t, m.GitHub.Repos, 1)
	require.Len(t, m.GitLab.Repos, 2)
	assert.Equal(t, "acme/api1", m.GitLab.Repos[1].Name)
}

func TestCarveOutGlobCovered_DiscoveryErrorWritesNothing(t *testing.T) {
	m, _ := overlappingForgeManifest()
	failing := &perForgeClientFactory{errs: map[string]error{ForgeGitLab: assert.AnError}}
	called := false

	_, err := CarveOutGlobCovered(context.Background(), ManifestEditConfig{Manifest: m}, []string{"acme/api1"}, failing,
		func(string, *PlatformConfig, *RepoEntry) error { called = true; return nil })
	require.Error(t, err)
	assert.False(t, called)
	assert.Len(t, m.GitHub.Repos, 1)
	assert.Len(t, m.GitLab.Repos, 1)
}

func TestUpdateInferenceAuth_RepeatedConcreteFiltersCreateOneEntry(t *testing.T) {
	m, clients := overlappingForgeManifest()

	updated, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, []string{"acme/api1", "acme/api1", "ACME/API1"}, InferenceAuthOpenAIAPIKey, clients)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api1"}, updated)
	require.Len(t, m.GitLab.Repos, 2)
	assert.Len(t, m.GitHub.Repos, 1)
	m.GitLab.URL = "https://gitlab.test"
	require.NoError(t, m.Validate(), "duplicate entries would be rejected")
}
