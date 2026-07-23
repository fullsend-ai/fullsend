//go:build behaviour

package behaviour_test

import (
	"context"
	"os"
	"testing"

	"github.com/cucumber/godog"
	"github.com/google/uuid"

	gaci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

func TestBehaviourSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping behaviour tests in short mode")
	}
	if c := os.Getenv("GODOG_CONCURRENCY"); c != "" && c != "1" {
		t.Fatalf("behaviour suite does not support GODOG_CONCURRENCY=%q: per-scenario World isolation is in place but drivers (SCM, CI, Install) are shared by reference and have not been validated under -race with concurrent scenarios yet; see #5441 for parallel support", c)
	}

	cfg := env.LoadRunnerConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid behaviour runner config: %v", err)
	}

	e2eCfg := e2etest.LoadEnvConfig(t)
	ctx := context.Background()

	runID := uuid.New().String()
	org, token, err := e2etest.AcquireOrg(ctx, e2eCfg, runID, e2etest.OrgPool(), e2eCfg.LockTimeout, t.Logf)
	if err != nil {
		t.Fatalf("acquiring org: %v", err)
	}
	client := e2etest.NewLiveClient(token)
	t.Cleanup(func() {
		e2etest.ReleaseLock(context.Background(), client, org, runID, t)
	})

	binary := e2etest.BuildCLIBinary(t)
	installDriver, err := install.NewDriver(cfg, e2eCfg, client, token, binary, t.Logf)
	if err != nil {
		t.Fatalf("creating install driver: %v", err)
	}

	e2etest.CleanupStaleResources(ctx, client, token, org, t)

	installState, err := installDriver.Install(ctx, org)
	if err != nil {
		t.Fatalf("installing fullsend on %s: %v", org, err)
	}

	t.Cleanup(func() {
		teardownCtx := context.Background()
		if teardownErr := installDriver.Teardown(teardownCtx, org, installState); teardownErr != nil {
			t.Logf("install teardown: %v", teardownErr)
		}
	})

	pool, err := world.NewRepoPool(12)
	if err != nil {
		t.Fatalf("creating repo pool: %v", err)
	}

	ensurer := install.NewRepoEnsurer(e2eCfg, client, token, binary, t.Logf)

	testRepo := installState.TestRepo()
	template := &world.World{
		Config:       cfg,
		SCM:          scmgh.New(client),
		CI:           gaci.New(client, token),
		Install:      installState,
		Ensurer:      ensurer,
		Org:          org,
		Token:        token,
		Logf:         t.Logf,
		FixturesRoot: "e2e/behaviour",
		RepoOwner:    org,
		RepoName:     testRepo,
		RepoFull:     org + "/" + testRepo,
	}

	suiteRunner := godog.TestSuite{
		Name:                "behaviour",
		ScenarioInitializer: func(sc *godog.ScenarioContext) { suite.InitScenario(sc, template, pool) },
		Options: &godog.Options{
			Format:      "pretty",
			Paths:       []string{"features"},
			TestingT:    t,
			Tags:        os.Getenv("GODOG_TAGS"),
			Concurrency: 1,
		},
	}
	if st := suiteRunner.Run(); st != 0 {
		t.Fatalf("behaviour suite failed with status %d", st)
	}
}
