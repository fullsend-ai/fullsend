package e2etest

import (
	"context"
	"fmt"
	"net/url"
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

// defaultMintURL is the hosted public mint URL used when FULLSEND_MINT_URL
// is unset. Duplicated from internal/cli.DefaultMintURL so this package
// does not import internal/cli (which pulls the nested mintcore module).
const defaultMintURL = "https://mint.fullsend.sh"

// hostedMintHost is the hostname of the hosted community mint.
const hostedMintHost = "mint.fullsend.sh"

// DefaultPoolOrgInstallMintURL is written into pool orgs as FULLSEND_MINT_URL by
// admin e2e install tests. Distinct from resolveMintURL() / defaultMintURL,
// which CI uses for cross-org e2e org locking.
//
// Admin e2e tests exercise per-org installation; workflows on the installed org
// mint against FULLSEND_MINT_URL. The community hosted mint (mint.fullsend.sh)
// runs in public mode and does not support per-org installs, so org-mode admin
// e2e must keep using the legacy per-org hosted dev mint until that changes.
const DefaultPoolOrgInstallMintURL = "https://fullsend-mint-gljhbkcloq-uc.a.run.app"

// poolOrgMintHost is the hostname of DefaultPoolOrgInstallMintURL, parsed
// once so isPoolOrgMintURL can do a case-insensitive hostname comparison
// (same approach as isHostedMintURL) instead of exact string equality.
var poolOrgMintHost = func() string {
	u, _ := url.Parse(DefaultPoolOrgInstallMintURL)
	return u.Hostname()
}()

func isPoolOrgMintURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), poolOrgMintHost)
}

// isHostedMintURL reports whether raw is the hosted community mint URL
// (mint.fullsend.sh). Duplicated from internal/cli.IsHostedMintURL so this
// package does not import internal/cli (which pulls the nested mintcore module).
func isHostedMintURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), hostedMintHost)
}

// resolveMintURL returns the mint endpoint from FULLSEND_MINT_URL or the hosted
// default (same as fullsend admin --mint-url).
func resolveMintURL() string {
	if u := os.Getenv("FULLSEND_MINT_URL"); u != "" {
		return u
	}
	return defaultMintURL
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
		mintURL = defaultMintURL
	}
	if isPoolOrgMintURL(mintURL) || isHostedMintURL(mintURL) {
		return DefaultHostedMintGCPProject
	}
	return strings.TrimSpace(cfg.GCPProjectID)
}

// e2eMintLevel is the mint privilege level requested for e2e installation
// tokens. Duplicated from internal/mintcore.LevelWrite so this package does
// not import internal/mintcore (which is nested outside this module's
// replace-free dependency graph). Kept in sync by
// TestE2EMintLevelMatchesMintcore.
const e2eMintLevel = "write"

// resolveE2EToken mints a cross-org e2e installation token for targetOrg.
// Repos is set to ["*"] to explicitly request an org-wide token (needed to
// create and operate on e2e-lock and .fullsend at runtime).
func resolveE2EToken(ctx context.Context, mintURL, targetOrg string) (string, error) {
	if mintURL == "" {
		return "", fmt.Errorf("mint URL not configured")
	}
	result, err := mintclient.MintToken(ctx, mintclient.MintRequest{
		MintURL:   mintURL,
		Role:      "e2e",
		Level:     e2eMintLevel,
		Repos:     []string{"*"},
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
