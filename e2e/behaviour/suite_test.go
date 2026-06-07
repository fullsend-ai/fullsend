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
	scmgh "github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/env"
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

	token := resolveToken(t)
	client := admin.NewLiveClient(token)
	ctx := context.Background()

	runID := uuid.New().String()
	org, err := admin.AcquireOrg(ctx, client, token, runID, admin.OrgPool(), 10*time.Minute, t.Logf)
	if err != nil {
		t.Fatalf("acquiring org: %v", err)
	}
	t.Cleanup(func() {
		admin.ReleaseLock(context.Background(), client, org, runID, t)
	})

	setup := env.NewPerOrg(client)
	if err := setup.Validate(ctx, org); err != nil {
		t.Skipf("org %s not ready for behaviour tests: %v", org, err)
	}

	w := &world.World{
		Config:    cfg,
		SCM:       scmgh.New(client, token),
		CI:        gaci.New(client, token),
		Env:       setup,
		Org:       org,
		Token:     token,
		RepoOwner: org,
		RepoName:  setup.TestRepo(),
		RepoFull:  org + "/" + setup.TestRepo(),
	}

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
		w.DummyExpectations = nil
		w.OutputExpectations = nil
		w.IssueNumber = 0
		w.IssueTitle = ""
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

func resolveToken(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(key)); token != "" {
			return token
		}
	}
	t.Skip("GITHUB_TOKEN or GH_TOKEN required for behaviour tests")
	return ""
}
