package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// errGitHubTokenMissing is returned when no GitHub token can be resolved
// from an explicit override, GH_TOKEN, GITHUB_TOKEN, or `gh auth token`.
var errGitHubTokenMissing = errors.New("no GitHub token found: set GH_TOKEN, GITHUB_TOKEN, or run 'gh auth login'")

// testAfterFunc, when non-nil, overrides the afterFunc on clients
// created by newGitHubLiveClient. Only set from test code.
var testAfterFunc func(time.Duration) <-chan time.Time

// envGHToken returns the GH_TOKEN process environment value without
// falling back to GITHUB_TOKEN or the GitHub CLI. The agent runtime
// injects the mint token as GH_TOKEN; diagnostics that must not inspect
// the Actions workflow token use this.
func envGHToken() string {
	return os.Getenv("GH_TOKEN")
}

// envGitHubToken returns GH_TOKEN or GITHUB_TOKEN without spawning the
// GitHub CLI. Callers that only need to inspect credentials already in
// the process environment use this instead of resolveToken.
func envGitHubToken() string {
	if token := envGHToken(); token != "" {
		return token
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token
	}
	return ""
}

// ghAuthTokenFn runs 'gh auth token'. Override in tests to avoid a
// real GitHub CLI subprocess.
var ghAuthTokenFn = func() ([]byte, error) {
	return exec.Command("gh", "auth", "token").Output()
}

// resolveToken finds a GitHub token by checking, in order:
//  1. GH_TOKEN env var
//  2. GITHUB_TOKEN env var
//  3. gh auth token (subprocess call to the GitHub CLI)
//
// This chain allows users who are already authenticated with gh to use
// fullsend without manually exporting tokens. The CLI runs a preflight
// check before each operation and reports exactly which scopes are
// missing, so callers do not need to request all scopes upfront.
//
// Note that gh auth scopes apply to every organization the account
// belongs to. Users who want to limit the blast radius can create a
// fine-grained PAT scoped to a single org and export it as GH_TOKEN.
//
// Command implementations must not read GH_TOKEN / GITHUB_TOKEN or
// invoke `gh auth token` themselves. Use resolveGitHubToken (token
// only) or newAuthenticatedGitHubClient (token + client).
func resolveToken() (string, error) {
	if token := envGitHubToken(); token != "" {
		return token, nil
	}
	out, err := ghAuthTokenFn()
	if err == nil {
		token := strings.TrimSpace(string(out))
		if token != "" {
			return token, nil
		}
	}
	return "", errGitHubTokenMissing
}

// resolveGitHubToken returns a GitHub token from an explicit override
// (CLI flag) or the standard resolution chain. explicit, when non-empty,
// wins over environment variables and `gh auth token`.
func resolveGitHubToken(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return resolveToken()
}

// newAuthenticatedGitHubClient is the CLI boundary for obtaining an
// authenticated GitHub API client. It applies explicit-token precedence,
// the GH_TOKEN → GITHUB_TOKEN → gh auth token chain, client construction
// (including GITHUB_API_URL / GHES base URL), and the shared missing-
// credential error.
//
// Command implementations should call this instead of inspecting GitHub
// credential environment variables or constructing gh.LiveClient
// themselves.
func newAuthenticatedGitHubClient(explicitToken, baseURL string) (*gh.LiveClient, error) {
	token, err := resolveGitHubToken(explicitToken)
	if err != nil {
		return nil, err
	}
	return newGitHubLiveClient(token, baseURL), nil
}

// githubTokenMissingFlagError formats a user-facing missing-token message
// that names the command's explicit-token flag, while preserving
// errGitHubTokenMissing in the error chain so errors.Is still matches it.
type githubTokenMissingFlagError struct {
	flag string
}

func (e *githubTokenMissingFlagError) Error() string {
	return fmt.Sprintf("no GitHub token found: set GH_TOKEN, GITHUB_TOKEN, pass %s, or run 'gh auth login'", e.flag)
}

func (e *githubTokenMissingFlagError) Unwrap() error {
	return errGitHubTokenMissing
}

// githubTokenFlagError wraps errGitHubTokenMissing with the command's
// explicit-token flag so user guidance names every supported source.
func githubTokenFlagError(flag string) error {
	return &githubTokenMissingFlagError{flag: flag}
}

// newGitHubLiveClient builds a GitHub API client. The manifestURL
// parameter, when non-empty, is the forge instance URL from the
// manifest's forge.github.url field. For GitHub.com the default
// API endpoint is used; for GitHub Enterprise Server the API URL
// is derived from the instance URL (<url>/api/v3).
//
// The GITHUB_API_URL environment variable is kept as a fallback for
// callers without a manifest (e.g., repos migrate) and for tests.
func newGitHubLiveClient(token, manifestURL string) *gh.LiveClient {
	client := gh.New(token)
	if apiURL := githubAPIURL(manifestURL); apiURL != "" {
		client = client.WithBaseURL(apiURL)
	} else if base := strings.TrimSpace(os.Getenv("GITHUB_API_URL")); base != "" {
		client = client.WithBaseURL(base)
	}
	if testAfterFunc != nil {
		client = client.WithAfterFunc(testAfterFunc)
	}
	return client
}

// githubAPIURL derives the GitHub REST API base URL from a forge
// instance URL. Returns "" for github.com (the client default) and
// for empty input (let env var or built-in default take over).
func githubAPIURL(instanceURL string) string {
	normalized := strings.TrimRight(instanceURL, "/")
	if normalized == "" || normalized == repos.DefaultGitHubURL {
		return ""
	}
	return normalized + "/api/v3"
}
