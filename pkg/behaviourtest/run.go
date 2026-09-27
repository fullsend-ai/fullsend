//go:build behaviour

package behaviourtest

import (
	"context"
	"os"
	"testing"

	"github.com/cucumber/godog"
	"github.com/google/uuid"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

const fullsendModulePath = "github.com/fullsend-ai/fullsend"

// RunSuite bootstraps and executes the behaviour test suite.
//
// It loads runner and e2e config from the environment, acquires a pool
// org, selects SCM/CI/install drivers, builds the fullsend CLI from
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
	binary := e2etest.BuildModuleBinary(t, fullsendModulePath)

	e2etest.CleanupStaleResources(ctx, client, token, org, t)

	driverFactory := installFactoryFor(cfg.Environment)
	driver, err := driverFactory(org, client, token, binary, e2eCfg.GCPProjectID, t.Logf)
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
		login, err := writeClient.GetAuthenticatedUser(ctx)
		if err != nil {
			t.Fatalf("resolving write actor login: %v", err)
		}
		template.WriteLogin = login
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
