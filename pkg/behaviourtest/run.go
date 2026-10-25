//go:build behaviour

package behaviourtest

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
)

// behaviourSuiteName labels this runner in bootstrap failure messages,
// the completion report, and the CI step summary; it matches the godog
// TestSuite name below.
const behaviourSuiteName = "behaviour"

// RunSuite bootstraps and executes the behaviour test suite.
//
// It loads runner and e2e config from the environment, builds the
// shared install driver that leases (org, repo) slots across the pool
// orgs, selects SCM/CI drivers per leased org, builds the fullsend CLI from
// module github.com/fullsend-ai/fullsend, and runs godog with shared
// step registration. Callers supply only FeaturePaths and FixturesRoot;
// tags, concurrency, and driver selection continue to come from env
// vars (GODOG_TAGS, GODOG_CONCURRENCY, BEHAVIOUR_*, ENVIRONMENT).
//
// The run reports its outcome with a failure-class completion report:
// bootstrap failures and the suite status are classified as
// infrastructure or scenario failures, the final failure message names
// the class and per-class counts, and failure-class.json is written
// under BEHAVIOUR_ARTIFACT_DIR (or BEHAVIOUR_FAILURE_CLASS_FILE) when
// either is set — on success as well as failure.
//
// The test file that calls RunSuite must be built with `-tags behaviour`.
func RunSuite(t *testing.T, opts SuiteOptions) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping behaviour tests in short mode")
	}
	if err := opts.validate(); err != nil {
		t.Fatalf("invalid suite options: %v", err)
	}

	cfg := env.LoadRunnerConfig()
	if err := cfg.Validate(); err != nil {
		failRun(t, nil, behaviourSuiteName, fmt.Errorf("invalid behaviour runner config: %w", err))
	}

	e2eCfg := e2etest.LoadEnvConfig(t)
	ctx := context.Background()

	// Build from the fullsend module, not the caller's module root, so
	// external consumers (fullsend-ai/agents) get this repo's cmd/fullsend.
	binary := e2etest.BuildModuleBinary(t, fullsendModulePath)

	// The shared install driver leases (org, repo) slots across the pool
	// orgs; there is no exclusive org lock and no broad startup cleanup.
	// Abandoned slot repos are reclaimed by the allocator's GC.
	pool := poolConfigFor(cfg.Environment, e2eCfg, binary, t.Logf)
	driver, err := installFactoryFor(cfg.Environment)(pool)
	if err != nil {
		failRun(t, nil, behaviourSuiteName, fmt.Errorf("creating install driver (env=%s): %w", cfg.Environment, err))
	}

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

	template := newTemplateWorld(t, behaviourSuiteName, ctx, cfg, pool, driver, opts)

	if outsiderPAT := os.Getenv("TEST_ACTOR_OUTSIDER_PAT"); outsiderPAT != "" {
		outsiderClient := e2etest.NewLiveClient(outsiderPAT)
		outsiderSCM, err := newSCMDriver(cfg.SCM, outsiderClient)
		if err != nil {
			failRun(t, template, behaviourSuiteName, fmt.Errorf("creating outsider SCM driver: %w", err))
		}
		template.OutsiderSCM = outsiderSCM
		login, err := outsiderClient.GetAuthenticatedUser(ctx)
		if err != nil {
			failRun(t, template, behaviourSuiteName, fmt.Errorf("resolving outsider login: %w", err))
		}
		template.OutsiderLogin = login
	}

	if writePAT := os.Getenv("TEST_ACTOR_WRITE_PAT"); writePAT != "" {
		writeClient := e2etest.NewLiveClient(writePAT)
		writeSCM, err := newSCMDriver(cfg.SCM, writeClient)
		if err != nil {
			failRun(t, template, behaviourSuiteName, fmt.Errorf("creating write actor SCM driver: %w", err))
		}
		template.WriteSCM = writeSCM
		template.WriteToken = writePAT
		login, err := writeClient.GetAuthenticatedUser(ctx)
		if err != nil {
			failRun(t, template, behaviourSuiteName, fmt.Errorf("resolving write actor login: %w", err))
		}
		template.WriteLogin = login
	}

	if triagePAT := os.Getenv("TEST_ACTOR_TRIAGE_PAT"); triagePAT != "" {
		triageClient := e2etest.NewLiveClient(triagePAT)
		triageSCM, err := newSCMDriver(cfg.SCM, triageClient)
		if err != nil {
			failRun(t, template, behaviourSuiteName, fmt.Errorf("creating triage actor SCM driver: %w", err))
		}
		template.TriageSCM = triageSCM
		login, err := triageClient.GetAuthenticatedUser(ctx)
		if err != nil {
			failRun(t, template, behaviourSuiteName, fmt.Errorf("resolving triage actor login: %w", err))
		}
		template.TriageLogin = login
	}

	suiteRunner := godog.TestSuite{
		Name:                behaviourSuiteName,
		ScenarioInitializer: func(sc *godog.ScenarioContext) { suite.InitScenario(sc, template) },
		Options: &godog.Options{
			Format:      "pretty",
			Paths:       opts.FeaturePaths,
			TestingT:    t,
			Tags:        os.Getenv("GODOG_TAGS"),
			Concurrency: concurrency,
		},
	}
	st := suiteRunner.Run()
	// The completion report is written for every outcome (design D4): a
	// successful run writes a fresh success marker so a stale marker from
	// an earlier attempt is detectable. godog reports failure through the
	// status, not an error, so runErr is nil and status carries st.
	report, reportPath := writeCompletionReport(template, behaviourSuiteName, nil, st)
	if st != 0 {
		// Design D5: the final failure message names the run class and
		// per-class counts; the report path is appended only when a
		// destination was configured, matching failRun's message.
		failMsg := fmt.Sprintf("%s suite failed with status %d: %s failure (%d infrastructure, %d scenario)",
			behaviourSuiteName, st, report.Class, report.InfrastructureFailures, report.ScenarioFailures)
		if reportPath != "" {
			failMsg += "; report: " + reportPath
		}
		t.Fatalf("%s", failMsg)
	}
}
