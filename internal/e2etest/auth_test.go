package e2etest

import (
	"context"
	"os"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMintEnrollProjectID(t *testing.T) {
	t.Setenv("E2E_GCP_MINT_PROJECT_ID", "")

	// Pool-org install mint (DefaultPoolOrgInstallMintURL) → hosted project.
	cfg := EnvConfig{
		MintURL:      DefaultPoolOrgInstallMintURL,
		GCPProjectID: "inference-only-project",
	}
	assert.Equal(t, DefaultHostedMintGCPProject, MintEnrollProjectID(cfg))

	// Pool-org URL with trailing slash still matches via hostname comparison.
	cfg.MintURL = DefaultPoolOrgInstallMintURL + "/"
	assert.Equal(t, DefaultHostedMintGCPProject, MintEnrollProjectID(cfg))

	// Community mint (cli.DefaultMintURL / mint.fullsend.sh) → hosted project
	// via IsHostedMintURL.
	cfg.MintURL = cli.DefaultMintURL
	assert.Equal(t, DefaultHostedMintGCPProject, MintEnrollProjectID(cfg))

	// Env override takes precedence.
	t.Setenv("E2E_GCP_MINT_PROJECT_ID", "override-mint-project")
	assert.Equal(t, "override-mint-project", MintEnrollProjectID(cfg))

	// Custom (non-hosted) mint → inference project.
	t.Setenv("E2E_GCP_MINT_PROJECT_ID", "")
	cfg.MintURL = "https://mint.example.com"
	assert.Equal(t, "inference-only-project", MintEnrollProjectID(cfg))
}

func TestMintEnrollProjectID_EmptyMintURLFallsBackToHosted(t *testing.T) {
	t.Setenv("E2E_GCP_MINT_PROJECT_ID", "")
	cfg := EnvConfig{GCPProjectID: "custom-project"}
	assert.Equal(t, DefaultHostedMintGCPProject, MintEnrollProjectID(cfg))
}

func TestMintEnrollProjectID_EmptyWithoutHostedMint(t *testing.T) {
	t.Setenv("E2E_GCP_MINT_PROJECT_ID", "")
	cfg := EnvConfig{
		MintURL:      "https://mint.example.com",
		GCPProjectID: "",
	}
	assert.Empty(t, MintEnrollProjectID(cfg))
}

func TestIsPoolOrgMintURL(t *testing.T) {
	assert.True(t, isPoolOrgMintURL(DefaultPoolOrgInstallMintURL))
	assert.True(t, isPoolOrgMintURL(DefaultPoolOrgInstallMintURL+"/"))
	assert.False(t, isPoolOrgMintURL("https://other-mint.run.app"))
	assert.False(t, isPoolOrgMintURL("://"))
}

func TestMintEnrollProjectID_RespectsEnvOverride(t *testing.T) {
	t.Setenv("E2E_GCP_MINT_PROJECT_ID", "from-env")
	cfg := EnvConfig{MintURL: DefaultPoolOrgInstallMintURL}
	assert.Equal(t, "from-env", MintEnrollProjectID(cfg))
	_ = os.Unsetenv("E2E_GCP_MINT_PROJECT_ID")
}

func TestResolveE2EToken_EmptyMintURL(t *testing.T) {
	_, err := resolveE2EToken(context.Background(), "", "test-org")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint URL not configured")
}

func TestResolveE2EToken_MintTokenError(t *testing.T) {
	// With a valid URL but no OIDC env vars, MintToken fails at the
	// OIDC step. This exercises the struct literal (including Repos)
	// and the error-return path.
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	_, err := resolveE2EToken(context.Background(), "https://mint.example.com", "test-org")
	require.Error(t, err)
}

func TestRunningInGitHubActions(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	assert.True(t, runningInGitHubActions())

	t.Setenv("GITHUB_ACTIONS", "false")
	assert.False(t, runningInGitHubActions())

	t.Setenv("GITHUB_ACTIONS", "")
	assert.False(t, runningInGitHubActions())
}

func TestResolveMintURL(t *testing.T) {
	t.Setenv("FULLSEND_MINT_URL", "https://custom-mint.example.com")
	assert.Equal(t, "https://custom-mint.example.com", resolveMintURL())

	t.Setenv("FULLSEND_MINT_URL", "")
	assert.Equal(t, cli.DefaultMintURL, resolveMintURL())
}

func TestResolveLocalToken_FromGHToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "gh-token-value")
	t.Setenv("GITHUB_TOKEN", "")
	token, err := resolveLocalToken()
	require.NoError(t, err)
	assert.Equal(t, "gh-token-value", token)
}

func TestResolveLocalToken_FromGitHubToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "github-token-value")
	token, err := resolveLocalToken()
	require.NoError(t, err)
	assert.Equal(t, "github-token-value", token)
}

func TestResolveLocalToken_GHTokenTakesPrecedence(t *testing.T) {
	t.Setenv("GH_TOKEN", "gh-token-value")
	t.Setenv("GITHUB_TOKEN", "github-token-value")
	token, err := resolveLocalToken()
	require.NoError(t, err)
	assert.Equal(t, "gh-token-value", token)
}

func TestTokenForOrg_WithMint(t *testing.T) {
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	cfg := envConfig{
		useMint: true,
		mintURL: "https://mint.example.com",
	}
	_, err := tokenForOrg(context.Background(), cfg, "test-org")
	// Fails because OIDC env vars are not set, but exercises the mint path.
	require.Error(t, err)
}

func TestTokenForOrg_WithoutMint(t *testing.T) {
	t.Setenv("GH_TOKEN", "local-token")
	t.Setenv("GITHUB_TOKEN", "")

	cfg := envConfig{useMint: false}
	token, err := tokenForOrg(context.Background(), cfg, "test-org")
	require.NoError(t, err)
	assert.Equal(t, "local-token", token)
}
