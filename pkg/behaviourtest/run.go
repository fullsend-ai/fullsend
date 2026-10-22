//go:build behaviour

package behaviourtest

import (
	"context"
	"os"
	"testing"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
)

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
		t.Fatalf("invalid behaviour runner config: %v", err)
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
		t.Fatalf("creating install driver (env=%s): %v", cfg.Environment, err)
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

	template := newTemplateWorld(t, ctx, cfg, pool, driver, opts)

	if outsiderPAT := os.Getenv("TEST_ACTOR_OUTSIDER_PAT"); outsiderPAT != "" {
		outsiderClient := e2etest.NewLiveClient(outsiderPAT)
		outsiderSCM, err := newSCMDriver(cfg.SCM, outsiderClient)
		if err != nil {
			t.Fatal(err)
		}
		template.OutsiderSCM = outsiderSCM
		login, err := outsiderClient.GetAuthenticatedUser(ctx)
		if err != nil {
			t.Fatalf("resolving outsider login: %v", err)
		}
		template.OutsiderLogin = login
	}

	if writePAT := os.Getenv("TEST_ACTOR_WRITE_PAT"); writePAT != "" {
		writeClient := e2etest.NewLiveClient(writePAT)
		writeSCM, err := newSCMDriver(cfg.SCM, writeClient)
		if err != nil {
			t.Fatal(err)
		}
		template.WriteSCM = writeSCM
		template.WriteToken = writePAT
		login, err := writeClient.GetAuthenticatedUser(ctx)
		if err != nil {
			t.Fatalf("resolving write actor login: %v", err)
		}
		template.WriteLogin = login
	}

	if triagePAT := os.Getenv("TEST_ACTOR_TRIAGE_PAT"); triagePAT != "" {
		triageClient := e2etest.NewLiveClient(triagePAT)
		triageSCM, err := newSCMDriver(cfg.SCM, triageClient)
		if err != nil {
			t.Fatal(err)
		}
		template.TriageSCM = triageSCM
		login, err := triageClient.GetAuthenticatedUser(ctx)
		if err != nil {
			t.Fatalf("resolving triage actor login: %v", err)
		}
		template.TriageLogin = login
	}

	suiteRunner := godog.TestSuite{
		Name:                "behaviour",
		ScenarioInitializer: func(sc *godog.ScenarioContext) { suite.InitScenario(sc, template) },
		Options: &godog.Options{
			Format:      "pretty",
			Paths:       opts.FeaturePaths,
			TestingT:    t,
			Tags:        os.Getenv("GODOG_TAGS"),
			Concurrency: concurrency,
		},
	}
	if st := suiteRunner.Run(); st != 0 {
		t.Fatalf("behaviour suite failed with status %d", st)
	}
}
