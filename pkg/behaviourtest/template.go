//go:build behaviour || playback

package behaviourtest

import (
	"context"
	"fmt"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// fullsendModulePath is the module the suite runners build the CLI from.
const fullsendModulePath = "github.com/fullsend-ai/fullsend"

// newTemplateWorld builds the template World shared by RunSuite and
// RunPlaybackSuite. suiteName ("behaviour" or "playback") labels the
// bootstrap failure report failRun writes when the driver cannot supply
// its default org. A scenario that leases a slot is rebound to the slot's
// org (Org, Token, MintURL, and SCM/CI through OrgDrivers; see
// steps.bindLease). Until then the World points at the driver's default
// org (install.SuiteBootstrap.DefaultOrg), with drivers built from that
// org's client and token, so steps a scenario runs without a lease still
// have working drivers. The runner never reads the driver's pool layout
// or credentials itself: a driver without a pool (forgejo-dev) leaves the
// org and token empty and the client nil.
func newTemplateWorld(
	t *testing.T,
	suiteName string,
	ctx context.Context,
	cfg env.RunnerConfig,
	binary string,
	driver install.Driver,
	opts SuiteOptions,
) *world.World {
	t.Helper()
	access, err := install.BootstrapFor(driver).DefaultOrg(ctx)
	if err != nil {
		// The World is built below from the default org's credential, so
		// none exists here yet; failRun handles a nil World by installing
		// a fresh recorder for the bootstrap record.
		failRun(t, nil, suiteName, err)
	}
	orgDrivers := orgDriversFor(cfg.SCM, cfg.CI)
	scmDriver, ciDriver, err := orgDrivers(access.Client, access.Token)
	if err != nil {
		failRun(t, nil, suiteName, fmt.Errorf("creating SCM and CI drivers: %w", err))
	}
	return &world.World{
		Config:       cfg,
		CLIBinary:    binary,
		SCM:          scmDriver,
		CI:           ciDriver,
		OrgDrivers:   orgDrivers,
		Driver:       driver,
		Failures:     &behaviourfail.Recorder{},
		Org:          access.Org,
		Token:        access.Token,
		Logf:         t.Logf,
		FixturesRoot: opts.FixturesRoot,
		RepoOwner:    access.Org,
	}
}
