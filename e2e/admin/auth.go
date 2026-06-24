//go:build e2e

package admin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/mintclient"
)

// resolveLocalToken returns a user token from env or gh auth.
func resolveLocalToken() (string, error) {
	if token := os.Getenv("GH_TOKEN"); token != "" {
		return token, nil
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token, nil
	}
	out, err := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "gh", "auth", "token").Output()
	}()
	if err == nil {
		token := strings.TrimSpace(string(out))
		if token != "" {
			return token, nil
		}
	}
	return "", fmt.Errorf("no GitHub token found: set GH_TOKEN, GITHUB_TOKEN, or run 'gh auth login'")
}

// runningInGitHubActions reports whether the test process runs inside GHA.
func runningInGitHubActions() bool {
	return os.Getenv("GITHUB_ACTIONS") == "true"
}

// resolveE2EToken mints a cross-org e2e installation token for targetOrg.
// Repos are omitted so the token covers the full installation (needed to
// create and operate on e2e-lock and .fullsend at runtime).
func resolveE2EToken(ctx context.Context, mintURL, targetOrg string) (string, error) {
	if mintURL == "" {
		return "", fmt.Errorf("E2E_MINT_URL not set")
	}
	result, err := mintclient.MintToken(ctx, mintclient.MintRequest{
		MintURL:   mintURL,
		Role:      "e2e",
		TargetOrg: targetOrg,
	})
	if err != nil {
		return "", err
	}
	return result.Token, nil
}

// tokenForOrg returns an API token for operating on a pool org.
func tokenForOrg(ctx context.Context, cfg envConfig, org string) (string, error) {
	if cfg.useMint {
		return resolveE2EToken(ctx, cfg.mintURL, org)
	}
	return resolveLocalToken()
}

// issueAuthorToken returns a user OAuth token for actions that must appear as a
// human with repository write access. Installation tokens create issues as bots,
// which fail ADR 0054 dispatch authorization (has_write_permission on open).
//
// In pull_request_target CI, the workflow file comes from the base branch until
// this PR merges, so we also accept E2E_GITHUB_PASSWORD (legacy secret name that
// main's e2e.yml already injects; value should be a user PAT with pool-org write).
func issueAuthorToken(cfg envConfig) (string, error) {
	for _, env := range []string{"E2E_ISSUE_AUTHOR_TOKEN", "E2E_GITHUB_PASSWORD"} {
		if token := os.Getenv(env); token != "" {
			return token, nil
		}
	}
	if !cfg.useMint {
		return resolveLocalToken()
	}
	return "", fmt.Errorf("no issue author token: set E2E_ISSUE_AUTHOR_TOKEN or E2E_GITHUB_PASSWORD (user PAT with write on pool org repos)")
}
