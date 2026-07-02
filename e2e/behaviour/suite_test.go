//go:build behaviour

package behaviour_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/google/uuid"

	"github.com/fullsend-ai/fullsend/e2e/admin"
	gaci "github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/ci/githubactions"
	"github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/env"
	"github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/install"
	scmgh "github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/e2e/behaviour/steps"
	"github.com/fullsend-ai/fullsend/e2e/behaviour/world"
)

func TestBehaviourSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping behaviour tests in short mode")
	}

	cfg := env.LoadRunnerConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid behaviour runner config: %v", err)
	}

	e2eCfg := admin.LoadEnvConfig(t)
	ctx := context.Background()

	runID := uuid.New().String()
	org, token, err := admin.AcquireOrg(ctx, e2eCfg, runID, admin.OrgPool(), e2eCfg.LockTimeout, t.Logf)
	if err != nil {
		t.Fatalf("acquiring org: %v", err)
	}
	client := admin.NewLiveClient(token)

	binary := admin.BuildCLIBinary(t)
	installDriver, err := install.NewDriver(cfg, e2eCfg, client, token, binary, t.Logf)
	if err != nil {
		t.Fatalf("creating install driver: %v", err)
	}

	admin.CleanupStaleResources(ctx, client, token, org, t)

	installState, err := installDriver.Install(ctx, org)
	if err != nil {
		t.Fatalf("installing fullsend on %s: %v", org, err)
	}

	t.Cleanup(func() {
		teardownCtx := context.Background()
		if teardownErr := installDriver.Teardown(teardownCtx, org, installState); teardownErr != nil {
			t.Logf("install teardown: %v", teardownErr)
		}
		admin.ReleaseLock(teardownCtx, client, org, runID, t)
	})

	testRepo := installState.TestRepo()
	w := &world.World{
		Config:    cfg,
		SCM:       scmgh.New(client),
		CI:        gaci.New(client, token),
		Install:   installState,
		Org:       org,
		Token:     token,
		Logf:      t.Logf,
		RepoOwner: org,
		RepoName:  testRepo,
		RepoFull:  org + "/" + testRepo,
	}

	// World is shared across scenarios; godog runs serially by default. Do not
	// pass --concurrency without giving each scenario its own World in Before.
	suite := godog.TestSuite{
		Name:                "behaviour",
		ScenarioInitializer: func(sc *godog.ScenarioContext) { initializeScenario(sc, w) },
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features"},
			TestingT: t,
			Tags:     os.Getenv("GODOG_TAGS"),
		},
	}
	if st := suite.Run(); st != 0 {
		t.Fatalf("behaviour suite failed with status %d", st)
	}
}

func initializeScenario(sc *godog.ScenarioContext, w *world.World) {
	sc.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		for _, tag := range sc.Tags {
			name := strings.TrimPrefix(tag.Name, "@")
			switch {
			case name == "skip:per-org" && w.Config.InstallMode == "per-org":
				return ctx, godog.ErrSkip
			case name == "skip:per-repo" && w.Config.InstallMode == "per-repo":
				return ctx, godog.ErrSkip
			case name == "requires:per-repo" && w.Config.InstallMode != "per-repo":
				return ctx, godog.ErrSkip
			case name == "skip:gitlab" && w.Config.SCM == "gitlab":
				return ctx, godog.ErrSkip
			}
		}
		w.ScenarioStart = time.Now()
		w.DummyOps = nil
		w.IssueNumber = 0
		w.IssueTitle = ""
		w.TriageWorkflow = ""
		w.TriageTriggerEvent = ""
		w.WorkflowRun = nil
		w.ArtifactDir = ""
		return ctx, nil
	})
	sc.After(func(ctx context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		steps.CleanupScenario(w)
		return ctx, err
	})
	steps.Register(sc, w)
}
