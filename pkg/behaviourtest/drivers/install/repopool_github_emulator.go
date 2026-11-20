// repopool_github_emulator.go implements the RepoPoolGitHubEmulator driver: the
// external-mint pool, run against a GitHub emulator (ENVIRONMENT=github-emulator).
package install

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
)

// githubEmulatorOrgSetupTimeout bounds creating the pool orgs on the emulator.
const githubEmulatorOrgSetupTimeout = time.Minute

// NewRepoPoolGitHubEmulator is a Factory for a pool on a GitHub emulator. It is
// the external-mint pool (mint at FULLSEND_MINT_URL, nothing deployed, each
// slot installed vendored, App set from BEHAVIOUR_APP_SET) with one
// addition: the emulator starts empty, so the pool's orgs are created
// first when they do not exist, owned by the token's user.
//
// The emulator's orgs belong to this suite alone, so the shared e2e-lock
// bridge to legacy runners is left out (PoolConfig.PrivateOrgs).
// Everything else is the live code path: slot creation, Actions-readiness
// polling, slot GC and the rate-limit gate run unchanged.
func NewRepoPoolGitHubEmulator(pool PoolConfig) (Driver, error) {
	if err := pool.validate(); err != nil {
		return nil, fmt.Errorf("github-emulator factory: %w", err)
	}
	mintURL := os.Getenv("FULLSEND_MINT_URL")
	if mintURL == "" {
		return nil, fmt.Errorf("github-emulator factory: FULLSEND_MINT_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), githubEmulatorOrgSetupTimeout)
	defer cancel()
	for _, org := range pool.Layout.Orgs() {
		token, err := pool.TokenFor(ctx, org)
		if err != nil {
			return nil, fmt.Errorf("github-emulator factory: token for %s: %w", org, err)
		}
		viewer := func(ctx context.Context) (string, error) { return e2etest.GitHubEmulatorViewerLogin(ctx, token) }
		if err := e2etest.EnsureGitHubEmulatorOrg(ctx, token, org, viewer); err != nil {
			return nil, fmt.Errorf("github-emulator factory: %w", err)
		}
	}
	newMint := func(mintGroup, orgCredential) (mintDriver, func(), error) {
		return &externalMintDriver{mintURL: mintURL}, func() {}, nil
	}
	// Setup names the App set the emulator was seeded with, as the
	// preview mints do, so bot logins match AppSet(). playbackSetupOpts
	// also selects the playback runtime when PLAYBACK_RUNTIME is set, so
	// this factory backs the playback suite too (see NewPlaybackDriver).
	opts := playbackSetupOpts()
	opts.AppSet = envAppSet()
	pool.PrivateOrgs = true
	return buildPoolDriver(pool, nil, newMint, opts, traceStart{})
}

// Compile-time check: NewRepoPoolGitHubEmulator satisfies Factory.
var _ Factory = NewRepoPoolGitHubEmulator
