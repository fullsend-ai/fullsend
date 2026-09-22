package cli

import (
	"testing"

	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveGitLabToken_FromEnv(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "glpat-test-token")
	token, err := resolveGitLabToken()
	require.NoError(t, err)
	assert.Equal(t, "glpat-test-token", token)
}

func TestResolveGitLabToken_Missing(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "")
	_, err := resolveGitLabToken()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no GitLab token found")
}

func TestNewForgeClient_GitLab_WithToken(t *testing.T) {
	client, err := newForgeClient(repos.ForgeGitLab, "glpat-direct-token", "")
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestNewForgeClient_GitLab_FromEnv(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "glpat-env-token")
	client, err := newForgeClient(repos.ForgeGitLab, "", "")
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestNewForgeClient_GitLab_NoToken(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "")
	_, err := newForgeClient(repos.ForgeGitLab, "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no GitLab token found")
}

func TestNewForgeClient_GitLab_WithBaseURL(t *testing.T) {
	t.Setenv("GITLAB_API_URL", "https://gitlab.example.com/api/v4")
	client, err := newForgeClient(repos.ForgeGitLab, "glpat-test", "")
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestNewForgeClient_GitLab_ManifestURLTakesPrecedence(t *testing.T) {
	t.Setenv("GITLAB_API_URL", "https://should-not-use.example.com")
	client, err := newForgeClient(repos.ForgeGitLab, "glpat-test", "https://gitlab.self-hosted.example.com")
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestNewForgeClient_GitLab_FullsendGitLabURLFallback(t *testing.T) {
	t.Setenv("FULLSEND_GITLAB_URL", "https://gitlab.self-hosted.example.com")
	t.Setenv("GITLAB_API_URL", "https://should-not-use.example.com")
	t.Setenv("CI_SERVER_URL", "https://should-not-use-either.example.com")
	client, err := newForgeClient(repos.ForgeGitLab, "glpat-test", "")
	require.NoError(t, err)
	require.NotNil(t, client)
	glClient, ok := client.(*gl.LiveClient)
	require.True(t, ok)
	assert.Equal(t, "https://gitlab.self-hosted.example.com", glClient.BaseURL())
}

func TestNewForgeClient_GitLab_CIServerURLFallback(t *testing.T) {
	t.Setenv("FULLSEND_GITLAB_URL", "")
	t.Setenv("GITLAB_API_URL", "")
	t.Setenv("CI_SERVER_URL", "https://gitlab.ci-server.example.com")
	client, err := newForgeClient(repos.ForgeGitLab, "glpat-test", "")
	require.NoError(t, err)
	require.NotNil(t, client)
	glClient, ok := client.(*gl.LiveClient)
	require.True(t, ok)
	assert.Equal(t, "https://gitlab.ci-server.example.com", glClient.BaseURL())
}

func TestNewForgeClient_GitHub(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp-test-token")
	client, err := newForgeClient(repos.ForgeGitHub, "", "")
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestNewForgeClient_EmptyDefaultsToGitHub(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp-test-token")
	client, err := newForgeClient("", "", "")
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestNewForgeClient_Unsupported(t *testing.T) {
	_, err := newForgeClient("bitbucket", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported forge")
}

func TestNewForgeClientFactory_GitHub(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp-test-token")
	factory := newForgeClientFactory("", nil)
	cfg, err := factory.ConfigFor(repos.ForgeGitHub)
	require.NoError(t, err)
	assert.NotNil(t, cfg.Client)
}

func TestNewForgeClientFactory_EmptyForgeDefaultsToGitHub(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp-test-token")
	factory := newForgeClientFactory("", nil)
	cfg, err := factory.ConfigFor("")
	require.NoError(t, err)
	assert.NotNil(t, cfg.Client)
}

func TestNewForgeClientFactory_GitLab(t *testing.T) {
	factory := newForgeClientFactory("glpat-direct", nil)
	cfg, err := factory.ConfigFor(repos.ForgeGitLab)
	require.NoError(t, err)
	assert.NotNil(t, cfg.Client)
}

func TestNewForgeClientFactory_WithManifestURLs(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp-test-token")
	m := &repos.Manifest{
		Version: 1,
		GitHub:  &repos.PlatformConfig{URL: "https://github.com"},
		GitLab:  &repos.PlatformConfig{URL: "https://gitlab.self-hosted.example.com"},
	}
	factory := newForgeClientFactory("glpat-test", m)

	ghCfg, err := factory.ConfigFor(repos.ForgeGitHub)
	require.NoError(t, err)
	assert.NotNil(t, ghCfg.Client)

	glCfg, err := factory.ConfigFor(repos.ForgeGitLab)
	require.NoError(t, err)
	assert.NotNil(t, glCfg.Client)
}

func TestNewForgeClientFactory_CapturesManifestGitLabURL(t *testing.T) {
	// Verify the factory captures the GitLab URL from the manifest at
	// creation time, so that clients use the manifest-configured URL
	// rather than falling back to env vars.
	m := &repos.Manifest{
		Version: 1,
		GitLab: &repos.PlatformConfig{
			URL:   "https://gitlab.self-hosted.example.com",
			Repos: []repos.RepoEntry{{Name: "group/project"}},
		},
	}
	factory := newForgeClientFactory("glpat-test", m)
	f := factory.(*forgeClientFactory)
	assert.Equal(t, "https://gitlab.self-hosted.example.com", f.gitlabURL,
		"factory should capture the GitLab URL from the manifest")
}

func TestGetGitLabToken_FromFlag(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("gitlab-token", "", "")
	require.NoError(t, cmd.Flags().Set("gitlab-token", "glpat-from-flag"))
	assert.Equal(t, "glpat-from-flag", getGitLabToken(cmd))
}

func TestGetGitLabToken_FromParentFlag(t *testing.T) {
	parent := &cobra.Command{}
	parent.PersistentFlags().String("gitlab-token", "", "")
	require.NoError(t, parent.PersistentFlags().Set("gitlab-token", "glpat-inherited"))

	child := &cobra.Command{}
	parent.AddCommand(child)

	assert.Equal(t, "glpat-inherited", getGitLabToken(child))
}

func TestGetGitLabToken_Empty(t *testing.T) {
	cmd := &cobra.Command{}
	assert.Equal(t, "", getGitLabToken(cmd))
}

func TestNewForgeClientFactory_Caching(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp-test-token")
	factory := newForgeClientFactory("", nil)

	cfg1, err := factory.ConfigFor(repos.ForgeGitHub)
	require.NoError(t, err)

	cfg2, err := factory.ConfigFor(repos.ForgeGitHub)
	require.NoError(t, err)

	assert.Same(t, cfg1.Client, cfg2.Client, "same forge should return the same cached client instance")
}

func TestNewForgeClientFactory_MixedForge(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp-test-token")
	factory := newForgeClientFactory("glpat-test-token", nil)

	ghCfg, err := factory.ConfigFor(repos.ForgeGitHub)
	require.NoError(t, err)

	glCfg, err := factory.ConfigFor(repos.ForgeGitLab)
	require.NoError(t, err)

	assert.NotSame(t, ghCfg.Client, glCfg.Client, "different forges should return different clients")
	assert.NotNil(t, ghCfg.Client)
	assert.NotNil(t, glCfg.Client)
}

func TestGitHubAPIURL(t *testing.T) {
	tests := []struct {
		name        string
		instanceURL string
		want        string
	}{
		{"empty returns empty", "", ""},
		{"github.com returns empty (use default)", "https://github.com", ""},
		{"github.com trailing slash returns empty", "https://github.com/", ""},
		{"GHES derives API URL", "https://ghes.example.com", "https://ghes.example.com/api/v3"},
		{"trailing slash stripped", "https://ghes.example.com/", "https://ghes.example.com/api/v3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := githubAPIURL(tt.instanceURL)
			assert.Equal(t, tt.want, got)
		})
	}
}
