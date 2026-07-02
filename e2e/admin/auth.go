//go:build e2e || behaviour

package admin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/cli"
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

// resolveMintURL returns the mint endpoint from FULLSEND_MINT_URL or the hosted
// default (same as fullsend admin --mint-url).
func resolveMintURL() string {
	if u := os.Getenv("FULLSEND_MINT_URL"); u != "" {
		return u
	}
	return cli.DefaultMintURL
}

// DefaultHostedMintGCPProject is the GCP project hosting the public mint service.
// See docs/guides/infrastructure/mint-administration.md.
const DefaultHostedMintGCPProject = "it-gcp-konflux-dev-fullsend"

// MintEnrollProjectID returns the GCP project for `fullsend mint enroll`.
// Inference may use a different project via E2E_GCP_PROJECT_ID; the hosted mint
// always lives in DefaultHostedMintGCPProject unless E2E_GCP_MINT_PROJECT_ID is set.
func MintEnrollProjectID(cfg EnvConfig) string {
	if p := strings.TrimSpace(os.Getenv("E2E_GCP_MINT_PROJECT_ID")); p != "" {
		return p
	}
	mintURL := strings.TrimSpace(cfg.MintURL)
	if mintURL == "" {
		mintURL = cli.DefaultMintURL
	}
	if mintURL == cli.DefaultMintURL {
		return DefaultHostedMintGCPProject
	}
	return strings.TrimSpace(cfg.GCPProjectID)
}

// resolveE2EToken mints a cross-org e2e installation token for targetOrg.
// Repos are omitted so the token covers the full installation (needed to
// create and operate on e2e-lock and .fullsend at runtime).
func resolveE2EToken(ctx context.Context, mintURL, targetOrg string) (string, error) {
	if mintURL == "" {
		return "", fmt.Errorf("mint URL not configured")
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
