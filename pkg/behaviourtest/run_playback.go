//go:build playback

package behaviourtest

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
)

// playbackSuiteName labels this runner in bootstrap failure messages,
// the completion report, and the CI step summary; it matches the godog
// TestSuite name below.
const playbackSuiteName = "playback"

// RunPlaybackSuite bootstraps and executes the dummy-playback behaviour
// suite. It mirrors RunSuite (cross-org slot leasing through the shared
// install driver, per-org SCM/CI selection, binary build — see select.go
// and template.go for the shared helpers both use) but installs pool
// repos with the "dummy-playback" runtime
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
// The run reports its outcome with a failure-class completion report:
// bootstrap failures and the suite status are classified as
// infrastructure or scenario failures, the final failure message names
// the class and per-class counts, and failure-class.json is written
// under BEHAVIOUR_ARTIFACT_DIR (or BEHAVIOUR_FAILURE_CLASS_FILE) when
// either is set — on success as well as failure.
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
		failRun(t, nil, playbackSuiteName, fmt.Errorf("invalid behaviour runner config: %w", err))
	}

	e2eCfg := e2etest.LoadEnvConfig(t)
	ctx := context.Background()

	// Build from the fullsend module, not the caller's module root, so
	// external consumers (fullsend-ai/agents) get this repo's cmd/fullsend.
	binary := e2etest.BuildModuleBinary(t, fullsendModulePath)

	// Same slot allocation and GC rules as RunSuite: the shared install
	// driver leases (org, repo) slots across the pool orgs, with no
	// exclusive org lock and no broad startup cleanup.
	pool := poolConfigFor(cfg.Environment, e2eCfg, binary, t.Logf)
	baseDriver, err := installFactoryFor(cfg.Environment)(pool)
	if err != nil {
		failRun(t, nil, playbackSuiteName, fmt.Errorf("creating install driver (env=%s): %w", cfg.Environment, err))
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

	concurrency, overCapacity, err := resolveConcurrency(os.Getenv("GODOG_CONCURRENCY"), pool.Layout.SlotsPerOrg, driver.Capacity())
	if err != nil {
		t.Fatal(err)
	}
	if overCapacity {
		t.Logf("WARNING: GODOG_CONCURRENCY=%d exceeds driver capacity %d; excess workers will block while allocating", concurrency, driver.Capacity())
	}
	t.Logf("leasing repo slots across %d org(s) %v (%d slot(s)), godog concurrency %d", pool.Layout.OrgCount, pool.Layout.Orgs(), driver.Capacity(), concurrency)

	template := newTemplateWorld(t, playbackSuiteName, ctx, cfg, pool, driver, opts)

	suiteRunner := godog.TestSuite{
		Name:                playbackSuiteName,
		ScenarioInitializer: func(sc *godog.ScenarioContext) { suite.InitScenario(sc, template) },
		Options: &godog.Options{
			Format:      "pretty",
			Paths:       opts.FeaturePaths,
			TestingT:    t,
			Tags:        "@playback",
			Concurrency: concurrency,
		},
	}
	st := suiteRunner.Run()
	// The completion report is written for every outcome (design D4): a
	// successful run writes a fresh success marker so a stale marker from
	// an earlier attempt is detectable. godog reports failure through the
	// status, not an error, so runErr is nil and status carries st.
	report, reportPath := writeCompletionReport(template, playbackSuiteName, nil, st)
	if st != 0 {
		// Design D5: the final failure message names the run class and
		// per-class counts; the report path is appended only when a
		// destination was configured, matching failRun's message.
		failMsg := fmt.Sprintf("%s suite failed with status %d: %s failure (%d infrastructure, %d scenario)",
			playbackSuiteName, st, report.Class, report.InfrastructureFailures, report.ScenarioFailures)
		if reportPath != "" {
			failMsg += "; report: " + reportPath
		}
		t.Fatalf("%s", failMsg)
	}
}
