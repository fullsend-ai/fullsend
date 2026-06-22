//go:build e2e

package admin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
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
	out, err := exec.Command("gh", "auth", "token").Output()
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

// poolMintRepos returns repo names for cross-org e2e mint tokens.
// test-repo is first for installation lookup; .fullsend is required for admin install tests.
func poolMintRepos() []string {
	return []string{testRepo, forge.ConfigRepoName, lockRepo}
}

// resolveE2EToken mints a cross-org e2e installation token for targetOrg.
func resolveE2EToken(ctx context.Context, mintURL, targetOrg string) (string, error) {
	if mintURL == "" {
		return "", fmt.Errorf("E2E_MINT_URL not set")
	}
	result, err := mintclient.MintToken(ctx, mintclient.MintRequest{
		MintURL:   mintURL,
		Role:      "e2e",
		TargetOrg: targetOrg,
		Repos:     poolMintRepos(),
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
