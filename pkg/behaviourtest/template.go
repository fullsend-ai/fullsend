package behaviourtest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// fullsendModulePath is the module the suite runners build the CLI from.
const fullsendModulePath = "github.com/fullsend-ai/fullsend"

// newTemplateWorld builds the template World shared by RunSuite and
// RunPlaybackSuite. A scenario that leases a slot is rebound to the
// slot's org (Org, Token, MintURL, and SCM/CI through OrgDrivers; see
// steps.bindLease). Until then the World points at the first pool org
// whose token can be minted, with drivers built from that token, so steps
// a scenario runs without a lease still have working drivers.
func newTemplateWorld(
	t *testing.T,
	ctx context.Context,
	cfg env.RunnerConfig,
	pool install.PoolConfig,
	driver install.Driver,
	opts SuiteOptions,
) *world.World {
	t.Helper()
	// Use the first org whose credential can be minted: one unhealthy org
	// must not abort the suite while the allocator can still lease slots
	// from the others.
	var defaultOrg, token string
	var mintErrs []error
	for _, org := range pool.Layout.Orgs() {
		tok, err := pool.TokenFor(ctx, org)
		if err != nil {
			t.Logf("minting token for %s (trying the next org): %v", org, err)
			mintErrs = append(mintErrs, fmt.Errorf("%s: %w", org, err))
			continue
		}
		defaultOrg, token = org, tok
		break
	}
	if defaultOrg == "" {
		t.Fatalf("minting a token for any pool org: %v", errors.Join(mintErrs...))
	}
	orgDrivers := orgDriversFor(cfg.SCM, cfg.CI)
	scmDriver, ciDriver, err := orgDrivers(pool.NewClient(token), token)
	if err != nil {
		t.Fatal(err)
	}
	return &world.World{
		Config:       cfg,
		CLIBinary:    pool.Binary,
		SCM:          scmDriver,
		CI:           ciDriver,
		OrgDrivers:   orgDrivers,
		Driver:       driver,
		Org:          defaultOrg,
		Token:        token,
		Logf:         t.Logf,
		FixturesRoot: opts.FixturesRoot,
		RepoOwner:    defaultOrg,
	}
}
