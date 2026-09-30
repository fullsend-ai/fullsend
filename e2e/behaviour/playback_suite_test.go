//go:build playback

package behaviour_test

import (
	"context"
	"os"
	"testing"

	"github.com/cucumber/godog"
	"github.com/google/uuid"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	gaci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestPlaybackSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping playback tests in short mode")
	}

	template, driver := buildPlaybackWorld(t)

	t.Cleanup(func() {
		if finalizeErr := driver.Finalize(context.Background()); finalizeErr != nil {
			t.Logf("driver finalize: %v", finalizeErr)
		}
	})

	suiteRunner := godog.TestSuite{
		Name:                "playback",
		ScenarioInitializer: func(sc *godog.ScenarioContext) { suite.InitScenario(sc, template) },
		Options: &godog.Options{
			Format:      "pretty",
			Paths:       []string{"features"},
			TestingT:    t,
			Tags:        "@playback",
			Concurrency: driver.Capacity(),
		},
	}
	if st := suiteRunner.Run(); st != 0 {
		t.Fatalf("playback suite failed with status %d", st)
	}
}

func buildPlaybackWorld(t *testing.T) (*world.World, *install.PlaybackDriver) {
	t.Helper()

	return buildGitHubPlaybackWorld(t)
}

func buildGitHubPlaybackWorld(t *testing.T) (*world.World, *install.PlaybackDriver) {
	t.Helper()
	t.Setenv("PLAYBACK_RUNTIME", "dummy-playback")

	e2eCfg := e2etest.LoadEnvConfig(t)
	runID := uuid.NewString()
	ctx := context.Background()
	environment := os.Getenv("ENVIRONMENT")
	if environment == "" {
		environment = "dev"
	}
	orgPool := e2etest.OrgPool()
	if environment == "stage" {
		orgPool = []string{install.StageOrg}
	}
	org, token, err := e2etest.AcquireOrg(ctx, e2eCfg, runID, orgPool, e2eCfg.LockTimeout, t.Logf)
	if err != nil {
		t.Fatalf("acquiring playback org: %v", err)
	}
	t.Cleanup(func() { e2etest.ReleaseLock(context.Background(), e2etest.NewLiveClient(token), org, runID, t) })

	client := e2etest.NewLiveClient(token)
	binary := e2etest.BuildModuleBinary(t, "github.com/fullsend-ai/fullsend")
	e2etest.CleanupStaleResources(ctx, client, token, org, t)

	var baseDriver install.Driver
	if environment == "stage" {
		baseDriver, err = install.NewRepoPoolCFMintStage(org, client, token, binary, e2eCfg.GCPProjectID, t.Logf)
	} else {
		baseDriver, err = install.NewRepoPoolCFMintPreviews(org, client, token, binary, e2eCfg.GCPProjectID, t.Logf)
	}
	if err != nil {
		t.Fatalf("creating install driver (env=%s): %v", environment, err)
	}
	driver := install.NewPlaybackDriver(baseDriver, org, client, t.Logf)

	cfg := env.RunnerConfig{
		SCM:         "github",
		CI:          "githubactions",
		InstallMode: "per-repo",
		Environment: environment,
	}

	ciDriver := gaci.New(client, token)
	issueSCM := scmgh.New(client)
	if triageToken := os.Getenv("TEST_ACTOR_TRIAGE_PAT"); triageToken != "" {
		issueSCM = scmgh.New(e2etest.NewLiveClient(triageToken))
	}

	template := &world.World{
		Config:       cfg,
		SCM:          scmgh.New(client),
		CI:           ciDriver,
		IssueSCM:     issueSCM,
		Driver:       driver,
		Org:          org,
		Token:        token,
		Logf:         t.Logf,
		FixturesRoot: "e2e/behaviour",
		RepoOwner:    org,
	}

	return template, driver
}
