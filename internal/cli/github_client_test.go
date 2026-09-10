package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvGHToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "from-gh")
	t.Setenv("GITHUB_TOKEN", "from-github")
	assert.Equal(t, "from-gh", envGHToken())
}

func TestEnvGHToken_Empty(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	assert.Empty(t, envGHToken())
}

func TestEnvGitHubToken_GHToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "from-gh")
	t.Setenv("GITHUB_TOKEN", "from-github")
	assert.Equal(t, "from-gh", envGitHubToken())
}

func TestEnvGitHubToken_GitHubTokenFallback(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "from-github")
	assert.Equal(t, "from-github", envGitHubToken())
}

func TestEnvGitHubToken_Empty(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	assert.Empty(t, envGitHubToken())
}

func TestResolveToken_EnvVar(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token-123")
	t.Setenv("GITHUB_TOKEN", "")

	token, err := resolveToken()
	require.NoError(t, err)
	assert.Equal(t, "test-token-123", token)
}

func TestResolveToken_GitHubTokenFallback(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "github-token-456")

	token, err := resolveToken()
	require.NoError(t, err)
	assert.Equal(t, "github-token-456", token)
}

func TestResolveToken_GHTokenTakesPrecedence(t *testing.T) {
	t.Setenv("GH_TOKEN", "gh-wins")
	t.Setenv("GITHUB_TOKEN", "github-ignored")

	token, err := resolveToken()
	require.NoError(t, err)
	assert.Equal(t, "gh-wins", token)
}

func TestResolveToken_Missing(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", "/nonexistent")

	_, err := resolveToken()
	require.Error(t, err)
	assert.ErrorIs(t, err, errGitHubTokenMissing)
	assert.Contains(t, err.Error(), "GH_TOKEN")
	assert.Contains(t, err.Error(), "GITHUB_TOKEN")
	assert.Contains(t, err.Error(), "gh auth login")
}

func TestResolveToken_GhAuthToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	old := ghAuthTokenFn
	t.Cleanup(func() { ghAuthTokenFn = old })
	ghAuthTokenFn = func() ([]byte, error) {
		return []byte("  gho_from_gh_cli\n"), nil
	}

	token, err := resolveToken()
	require.NoError(t, err)
	assert.Equal(t, "gho_from_gh_cli", token)
}

func TestResolveToken_GhAuthTokenEmpty(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	old := ghAuthTokenFn
	t.Cleanup(func() { ghAuthTokenFn = old })
	ghAuthTokenFn = func() ([]byte, error) {
		return []byte("   \n"), nil
	}

	_, err := resolveToken()
	require.Error(t, err)
	assert.ErrorIs(t, err, errGitHubTokenMissing)
}

func TestResolveGitHubToken_ExplicitWins(t *testing.T) {
	t.Setenv("GH_TOKEN", "from-env")
	t.Setenv("GITHUB_TOKEN", "from-github")

	token, err := resolveGitHubToken("from-flag")
	require.NoError(t, err)
	assert.Equal(t, "from-flag", token)
}

func TestResolveGitHubToken_FallsBackToChain(t *testing.T) {
	t.Setenv("GH_TOKEN", "from-env")
	t.Setenv("GITHUB_TOKEN", "")

	token, err := resolveGitHubToken("")
	require.NoError(t, err)
	assert.Equal(t, "from-env", token)
}

func TestNewAuthenticatedGitHubClient_Explicit(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", "/nonexistent")

	client, err := newAuthenticatedGitHubClient("ghp-explicit", "")
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestNewAuthenticatedGitHubClient_FromEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp-from-env")
	t.Setenv("GITHUB_TOKEN", "")

	client, err := newAuthenticatedGitHubClient("", "")
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestNewAuthenticatedGitHubClient_Missing(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", "/nonexistent")

	_, err := newAuthenticatedGitHubClient("", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, errGitHubTokenMissing)
}

func TestNewAuthenticatedGitHubClient_WithBaseURL(t *testing.T) {
	client, err := newAuthenticatedGitHubClient("ghp-test", "https://ghes.example.com")
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, "https://ghes.example.com/api/v3", client.BaseURL())
}

func TestGitHubTokenFlagError(t *testing.T) {
	err := githubTokenFlagError("--token")
	assert.Contains(t, err.Error(), "GH_TOKEN")
	assert.Contains(t, err.Error(), "GITHUB_TOKEN")
	assert.Contains(t, err.Error(), "--token")
	assert.Contains(t, err.Error(), "gh auth login")
	assert.ErrorIs(t, err, errGitHubTokenMissing)
}

// TestCLIGitHubAuth_NoDirectCredentialReads is the regression guard for
// #7090: command implementations must obtain GitHub credentials through
// resolveGitHubToken / newAuthenticatedGitHubClient, not by reading
// GH_TOKEN / GITHUB_TOKEN or invoking `gh auth token` themselves.
func TestCLIGitHubAuth_NoDirectCredentialReads(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	forbidden := []string{
		`os.Getenv("GH_TOKEN")`,
		`os.Getenv("GITHUB_TOKEN")`,
		`os.LookupEnv("GH_TOKEN")`,
		`os.LookupEnv("GITHUB_TOKEN")`,
		`exec.Command("gh", "auth", "token")`,
		`ghAuthTokenFn(`,
		`envGitHubToken(`,
		`envGHToken(`,
	}
	allowed := map[string]bool{
		"github_client.go": true,
	}
	// allowedPattern grants narrow, reviewed exceptions: a specific file
	// may contain a specific forbidden pattern without failing the scan,
	// while every other forbidden pattern in that file still fails it.
	allowedPattern := map[string]map[string]bool{
		// run.go saves and restores the caller's pre-existing GH_TOKEN
		// around minting an agent token; it does not read the credential
		// to authenticate a GitHub client. It also calls envGHToken()
		// for a documented token-scope diagnostic (see run.go:1155),
		// not to authenticate a GitHub client.
		"run.go": {
			`os.LookupEnv("GH_TOKEN")`: true,
			`envGHToken(`:              true,
		},
	}

	var violations []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if allowed[name] {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(".", name))
		require.NoError(t, readErr)
		src := string(data)
		for _, pat := range forbidden {
			if strings.Contains(src, pat) {
				if allowedPattern[name][pat] {
					continue
				}
				violations = append(violations, name+": "+pat)
			}
		}
	}
	assert.Empty(t, violations,
		"CLI command files must not read GitHub credentials directly; use resolveGitHubToken or newAuthenticatedGitHubClient")
}
