package e2etest

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/internal/cli"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
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

	// Community mint (defaultMintURL / mint.fullsend.sh) → hosted project
	// via isHostedMintURL.
	cfg.MintURL = defaultMintURL
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
	// PR #8801 review: only transient mint failures (deadline, transport,
	// HTTP 429/5xx) stay infrastructure-marked; the missing-OIDC-env
	// failure is deterministic harness misconfiguration, like the
	// mint-URL guard, so it surfaces unmarked and scenario-classified.
	// The transient classes are pinned in TestIsTransientMintError and
	// auth_infraclass_ext_test.go; failclass cannot be imported here
	// because it pulls drivers/install, which imports this package.
	assert.False(t, behaviourfail.Is(err),
		"a missing-OIDC-env mint failure is configuration validation and stays unmarked")
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
	assert.Equal(t, defaultMintURL, resolveMintURL())
}

func TestIsHostedMintURL(t *testing.T) {
	t.Parallel()
	assert.True(t, isHostedMintURL(defaultMintURL))
	assert.True(t, isHostedMintURL(defaultMintURL+"/v1/token"))
	assert.True(t, isHostedMintURL("https://Mint.Fullsend.SH"))
	assert.False(t, isHostedMintURL("https://evil.example.com"))
	assert.False(t, isHostedMintURL("https://fullsend-mint-abc123.run.app"))
	assert.False(t, isHostedMintURL(""))
	assert.False(t, isHostedMintURL("://"))
}

func TestDefaultMintURLMatchesCLI(t *testing.T) {
	t.Parallel()
	assert.Equal(t, cli.DefaultMintURL, defaultMintURL)
	assert.Equal(t, cli.IsHostedMintURL(defaultMintURL), isHostedMintURL(defaultMintURL))
}

func TestE2EMintLevelMatchesMintcore(t *testing.T) {
	t.Parallel()
	assert.Equal(t, mintcore.LevelWrite, e2eMintLevel)
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
	// tokenForOrg surfaces resolveE2EToken's classification unchanged
	// (the mark decision is made once at the source, never re-wrapped
	// here): the missing-OIDC-env failure is deterministic configuration
	// validation, so it stays unmarked and scenario-classified (PR #8801
	// review). The transient classes stay marked, pinned in
	// auth_infraclass_ext_test.go.
	assert.False(t, behaviourfail.Is(err),
		"tokenForOrg's mint path surfaces resolveE2EToken's unmarked config failure unchanged")
}

func TestTokenForOrg_WithoutMint(t *testing.T) {
	t.Setenv("GH_TOKEN", "local-token")
	t.Setenv("GITHUB_TOKEN", "")

	cfg := envConfig{useMint: false}
	token, err := tokenForOrg(context.Background(), cfg, "test-org")
	require.NoError(t, err)
	assert.Equal(t, "local-token", token)
}

func TestTokenForOrg_Exported(t *testing.T) {
	t.Setenv("GH_TOKEN", "local-token")
	t.Setenv("GITHUB_TOKEN", "")

	token, err := TokenForOrg(context.Background(), EnvConfig{UseMint: false}, "halfsend-03")
	require.NoError(t, err)
	assert.Equal(t, "local-token", token)
}

// timeoutErr is a minimal net.Error whose transport failed.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "transport timed out" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }

// TestIsTransientMintError pins the mint path's transient boundary
// (PR #8801 review): a context deadline, a transport failure, or an
// HTTP 429/5xx from the mint or OIDC endpoints is a retryable
// environment condition, while deterministic rejections and a cancelled
// context stay unmarked so a retry cannot mask them.
func TestIsTransientMintError(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err  error
		want bool
	}{
		"context deadline": {
			err:  fmt.Errorf("calling mint service: %w", context.DeadlineExceeded),
			want: true,
		},
		"transport timeout": {
			err:  fmt.Errorf("calling mint service: requesting token: %w", timeoutErr{}),
			want: true,
		},
		"mint 429": {
			err:  errors.New("calling mint service: mint returned HTTP 429: rate limited"),
			want: true,
		},
		"mint 500 after retries": {
			err:  fmt.Errorf("calling mint service: giving up after 5 attempts: mint returned HTTP 500: unavailable"),
			want: true,
		},
		"oidc 503 after retries": {
			err:  errors.New("fetching OIDC JWT: giving up after 15 attempts: OIDC endpoint returned HTTP 503: unavailable"),
			want: true,
		},
		"mint 403 not enrolled": {
			err:  errors.New("calling mint service: mint returned HTTP 403: org not enrolled"),
			want: false,
		},
		"mint 401 unauthorized": {
			err:  errors.New("calling mint service: mint returned HTTP 401: unauthorized"),
			want: false,
		},
		"empty token": {
			err:  errors.New("calling mint service: mint returned empty token"),
			want: false,
		},
		"non-status text after prefix": {
			err:  errors.New("calling mint service: mint returned HTTP unavailable: oops"),
			want: false,
		},
		"malformed response": {
			err:  fmt.Errorf("calling mint service: parsing response: %w", errors.New("bad json")),
			want: false,
		},
		"context cancelled": {
			err:  fmt.Errorf("calling mint service: %w", context.Canceled),
			want: false,
		},
		"transport-wrapped cancellation": {
			err: fmt.Errorf("calling mint service: %w", &url.Error{
				Op:  "Get",
				URL: "https://mint.example.test/v1/token",
				Err: context.Canceled,
			}),
			want: false,
		},
		"malformed mint URL": {
			err: fmt.Errorf("calling mint service: %w", &url.Error{
				Op:  "parse",
				URL: "https://mint example.test/v1/token",
				Err: errors.New("invalid character \" \" in host name"),
			}),
			want: false,
		},
		"transport-wrapped connection failure": {
			err: fmt.Errorf("calling mint service: %w", &url.Error{
				Op:  "Get",
				URL: "https://mint.example.test/v1/token",
				Err: timeoutErr{},
			}),
			want: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, isTransientMintError(tc.err))
		})
	}
}
