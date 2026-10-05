//go:build playback

package behaviourtest

import (
	"context"
	"os"
	"testing"

	"github.com/cucumber/godog"
	"github.com/google/uuid"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// RunPlaybackSuite bootstraps and executes the dummy-playback behaviour
// suite. It mirrors RunSuite (pool org acquisition, driver/SCM/CI
// selection, binary build — see select.go for the shared helpers both
// use) but installs pool repos with the "dummy-playback" runtime
// (PLAYBACK_RUNTIME, read by the install factories — see
// drivers/install/playback_driver.go) and wraps the resulting Driver in
// install.NewPlaybackDriver so World.IsPlaybackMode reports true and the
// playback-only step definitions (pkg/behaviourtest/steps/playback.go)
// activate.
//
// Only @playback-tagged scenarios are run — the tag filter is fixed
// rather than read from GODOG_TAGS, since this suite has a single
// purpose and does not need caller-supplied tag expressions.
//
// The test file that calls RunPlaybackSuite must be built with
// `-tags playback`. This lives in a separate build-tagged file (rather
// than behind a flag on RunSuite) because RunSuite itself is compiled
// only under `-tags behaviour`; see run.go.
func RunPlaybackSuite(t *testing.T, opts SuiteOptions) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping playback tests in short mode")
	}
	if err := opts.validate(); err != nil {
		t.Fatalf("invalid suite options: %v", err)
	}

	// Causes the install factories (NewRepoPoolCFMintPreviews /
	// NewRepoPoolCFMintStage) to install pool repos with the
	// dummy-playback runtime and its tracking-issue AfterInstall hook,
	// instead of the normal "dummy" runtime.
	t.Setenv("PLAYBACK_RUNTIME", "dummy-playback")

	cfg := env.LoadRunnerConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid behaviour runner config: %v", err)
	}

	e2eCfg := e2etest.LoadEnvConfig(t)
	ctx := context.Background()

	runID := uuid.New().String()
	orgPool := orgPoolForEnvironment(cfg.Environment)
	org, token, err := e2etest.AcquireOrg(ctx, e2eCfg, runID, orgPool, e2eCfg.LockTimeout, t.Logf)
	if err != nil {
		t.Fatalf("acquiring org (env=%s): %v", cfg.Environment, err)
	}
	client := e2etest.NewLiveClient(token)
	t.Cleanup(func() {
		e2etest.ReleaseLock(context.Background(), client, org, runID, t)
	})

	// Build from the fullsend module, not the caller's module root, so
	// external consumers (fullsend-ai/agents) get this repo's cmd/fullsend.
	binary := e2etest.BuildModuleBinary(t, "github.com/fullsend-ai/fullsend")

	e2etest.CleanupStaleResources(ctx, client, token, org, t)

	baseFactory := installFactoryFor(cfg.Environment)
	baseDriver, err := baseFactory(org, client, token, binary, e2eCfg.GCPProjectID, t.Logf)
	if err != nil {
		t.Fatalf("creating install driver (env=%s): %v", cfg.Environment, err)
	}
	driver := install.NewPlaybackDriver(baseDriver)

	// Register Finalize as cleanup so mint resources are torn down
	// even if the suite fails partway through. Finalize also reclaims
	// any outstanding leases, logging them as errors.
	t.Cleanup(func() {
		if finalizeErr := driver.Finalize(context.Background()); finalizeErr != nil {
			t.Logf("driver finalize: %v", finalizeErr)
		}
	})

	concurrency, overCapacity, err := resolveConcurrency(os.Getenv("GODOG_CONCURRENCY"), driver.Capacity())
	if err != nil {
		t.Fatal(err)
	}
	if overCapacity {
		t.Logf("WARNING: GODOG_CONCURRENCY=%d exceeds driver capacity %d; excess workers will block in AllocateRepo", concurrency, driver.Capacity())
	}

	scmDriver, err := newSCMDriver(cfg.SCM, client)
	if err != nil {
		t.Fatal(err)
	}
	ciDriver, err := newCIDriver(cfg.CI, client, token)
	if err != nil {
		t.Fatal(err)
	}

	template := &world.World{
		Config:       cfg,
		SCM:          scmDriver,
		CI:           ciDriver,
		Driver:       driver,
		Org:          org,
		Token:        token,
		Logf:         t.Logf,
		FixturesRoot: opts.FixturesRoot,
		RepoOwner:    org,
	}

	suiteRunner := godog.TestSuite{
		Name:                "playback",
		ScenarioInitializer: func(sc *godog.ScenarioContext) { suite.InitScenario(sc, template) },
		Options: &godog.Options{
			Format:      "pretty",
			Paths:       opts.FeaturePaths,
			TestingT:    t,
			Tags:        "@playback",
			Concurrency: concurrency,
		},
	}
	if st := suiteRunner.Run(); st != 0 {
		t.Fatalf("playback suite failed with status %d", st)
	}
}
