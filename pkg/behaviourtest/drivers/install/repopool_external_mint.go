// repopool_external_mint.go implements the RepoPoolExternalMint driver.
// It uses a pre-configured mint URL without deploying anything. This is
// the original install path retained for test configurations that use a
// pre-existing mint endpoint.
package install

import (
	"context"
	"fmt"
	"os"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

// NewRepoPoolExternalMint is a Factory that returns the shared
// slot-leasing Driver backed by a pre-configured mint URL. Every org of
// the pool uses that mint, which must already enroll the pool's slot
// repos; nothing is deployed and teardown is a no-op.
//
// The mint URL is read from FULLSEND_MINT_URL.
func NewRepoPoolExternalMint(pool PoolConfig) (Driver, error) {
	if err := pool.validate(); err != nil {
		return nil, fmt.Errorf("external mint factory: %w", err)
	}
	mintURL := os.Getenv("FULLSEND_MINT_URL")
	if mintURL == "" {
		return nil, fmt.Errorf("external mint: FULLSEND_MINT_URL is required")
	}
	newMint := func(mintGroup, orgCredential) (mintDriver, func(), error) {
		return &externalMintDriver{mintURL: mintURL}, func() {}, nil
	}
	opts := common.DefaultGitHubSetupOpts()
	opts.ConfigPreset = envConfigPreset()
	return buildPoolDriver(pool, nil, newMint, opts, traceStart{})
}

// Compile-time check: NewRepoPoolExternalMint satisfies Factory.
var _ Factory = NewRepoPoolExternalMint

// externalMintDriver uses a pre-configured mint URL.
type externalMintDriver struct {
	mintURL string
}

// Compile-time check that externalMintDriver implements mintDriver.
var _ mintDriver = (*externalMintDriver)(nil)

func (d *externalMintDriver) Install(_ context.Context) (string, error) {
	// The driver only provides the mint URL. Per-repo github setup and
	// post-install validation are handled by the ensurer for each
	// leased pool repo.
	return d.mintURL, nil
}

func (d *externalMintDriver) Teardown(_ context.Context) error {
	// The external mint driver has no mint infrastructure to tear down.
	return nil
}
