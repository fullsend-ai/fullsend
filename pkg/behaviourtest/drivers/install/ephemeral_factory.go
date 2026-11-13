// ephemeral_factory.go builds the ephemeral-repo driver (ADR 0114/0115):
// it acquires one lock-pool slot (fixing the run's org and bounding
// cross-run concurrency), deploys a public Cloudflare preview mint for that
// org, resolves the org-scoped WIF provider once, and returns a driver that
// creates ephemeral repos per lease.
package install

import (
	"context"
	"fmt"
	"os"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

// DefaultBehaviourOrg is the org ephemeral behaviour tests run in when
// BEHAVIOUR_ORG is unset and no multi-org lock pool is configured.
const DefaultBehaviourOrg = "fullsend-ai-test"

// envBehaviourOrg returns the single-org default for the lock pool.
func envBehaviourOrg() string {
	if v := os.Getenv("BEHAVIOUR_ORG"); v != "" {
		return v
	}
	return DefaultBehaviourOrg
}

// NewEphemeralCFMint is a Factory for the ephemeral-repo model. It acquires
// a lock slot (which selects the run's org), deploys a public CF preview
// mint for that org, and returns an ephemeralDriver. PEMs come from
// TEST_*_PEM; suite name and app set from BEHAVIOUR_SUITE_NAME /
// BEHAVIOUR_APP_SET; lock-pool membership from BEHAVIOUR_LOCK_POOL /
// BEHAVIOUR_LOCK_SLOTS_PER_ORG (defaulting to the single BEHAVIOUR_ORG).
func NewEphemeralCFMint(pool PoolConfig) (Driver, error) {
	logf := poolLogf(pool)
	if pool.TokenFor == nil || pool.NewClient == nil {
		return nil, fmt.Errorf("ephemeral factory: TokenFor and NewClient are required")
	}
	if !cfmintPEMsConfigured() {
		return nil, fmt.Errorf("ephemeral factory: no PEMs materialized (set the TEST_*_PEM env vars)")
	}

	ctx := context.Background()
	if err := prepareWranglerForMints(ctx, logf); err != nil {
		return nil, fmt.Errorf("ephemeral factory: %w", err)
	}

	// Acquire a lock slot; its org is the run's org.
	lockCfg, err := LockPoolConfigFromEnv(envBehaviourOrg(), os.Getenv, logf)
	if err != nil {
		return nil, fmt.Errorf("ephemeral factory: lock pool config: %w", err)
	}
	clientFor := func(ctx context.Context, org string) (forge.Client, error) {
		tok, err := pool.TokenFor(ctx, org)
		if err != nil {
			return nil, err
		}
		return pool.NewClient(tok), nil
	}
	runID, err := NewRunID()
	if err != nil {
		return nil, fmt.Errorf("ephemeral factory: %w", err)
	}
	lockPool, err := NewLockPool(lockCfg, runID, clientFor, logf)
	if err != nil {
		return nil, fmt.Errorf("ephemeral factory: %w", err)
	}
	held, err := lockPool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("ephemeral factory: acquiring lock: %w", err)
	}
	org := held.Org()
	logf("[ephemeral] run %s holds %s", runID, held.Slot())

	// Credential for the locked org.
	token, err := pool.TokenFor(ctx, org)
	if err != nil {
		_ = held.Release(ctx)
		return nil, fmt.Errorf("ephemeral factory: minting token for %s: %w", org, err)
	}
	client := pool.NewClient(token)

	// Deploy a public preview mint for the org.
	mintURL, mint, err := deployEphemeralMint(ctx, pool, org, token, client, logf)
	if err != nil {
		_ = held.Release(ctx)
		return nil, fmt.Errorf("ephemeral factory: %w", err)
	}

	// Resolve the org-scoped WIF provider once, reused for every repo.
	wif, err := common.ResolveOrgWIFProvider(pool.Binary, token, org, pool.GCPProjectID, e2etest.TryRunCLI, logf)
	if err != nil {
		_ = mint.Teardown(ctx)
		_ = held.Release(ctx)
		return nil, fmt.Errorf("ephemeral factory: resolving org WIF for %s: %w", org, err)
	}

	ens := &ephemeralEnsurer{
		client:        client,
		token:         token,
		binary:        pool.Binary,
		fullsendRef:   common.EnvFullsendRef(),
		mintURL:       mintURL,
		gcpProjectID:  pool.GCPProjectID,
		wifProvider:   wif,
		runID:         runID,
		logf:          logf,
		runCLI:        e2etest.TryRunCLI,
		settle:        awaitWorkflowReady,
		eventDelivery: awaitEventDelivery,
	}

	return &ephemeralDriver{
		org:         org,
		mint:        mint,
		ens:         ens,
		client:      client,
		token:       token,
		mintURL:     mintURL,
		statusGroup: mintStatusGitHubGroup(org),
		lockPool:    lockPool,
		held:        held,
		runID:       runID,
		logf:        logf,
		created:     make(map[string]bool),
	}, nil
}

// Compile-time check: NewEphemeralCFMint satisfies Factory.
var _ Factory = NewEphemeralCFMint

// deployEphemeralMint deploys a public CF preview mint for org and returns
// its URL and the mint driver (for teardown at Finalize). The mint is
// public (PER_REPO_WIF_REPOS=*) with workflow hosts "*", so any ephemeral
// repo may be a caller and workflow host without per-repo enrollment.
func deployEphemeralMint(ctx context.Context, pool PoolConfig, org, token string, client forge.Client, logf func(string, ...any)) (string, mintDriver, error) {
	pemDir, err := setupCFMintPEMDir()
	if err != nil {
		return "", nil, fmt.Errorf("materializing PEMs: %w", err)
	}
	defer func() {
		if pemDir != "" {
			_ = os.RemoveAll(pemDir)
		}
	}()

	cfg := cfmintConfig{
		pemDir:            pemDir,
		suiteName:         envSuiteName(),
		workflowHostRepos: "*",
		appSet:            envAppSet(),
		statusGitHubGroup: mintStatusGitHubGroup(org),
	}
	md, err := newCFMintDriver(client, token, pool.Binary, pool.GCPProjectID, logf, cfg)
	if err != nil {
		return "", nil, fmt.Errorf("creating mint driver: %w", err)
	}
	mintURL, err := md.Install(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("deploying mint for %s: %w", org, err)
	}
	logf("[ephemeral] %s uses preview mint at %s", org, mintURL)
	return mintURL, md, nil
}
