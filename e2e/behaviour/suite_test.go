//go:build behaviour

package behaviour_test

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/cucumber/godog"
	"github.com/google/uuid"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	gaci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	glci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/gitlabci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	scmgl "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/gitlab"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

func TestBehaviourSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping behaviour tests in short mode")
	}

	cfg := env.LoadRunnerConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid behaviour runner config: %v", err)
	}

	e2eCfg := e2etest.LoadEnvConfig(t)
	ctx := context.Background()

	// For STAGE, use the halfsend org directly (not the numbered pool).
	// For DEV, acquire a pool org as usual.
	runID := uuid.New().String()
	var orgPool []string
	if cfg.Environment == "stage" {
		orgPool = []string{install.StageOrg}
	} else {
		orgPool = e2etest.OrgPool()
	}
	org, token, err := e2etest.AcquireOrg(ctx, e2eCfg, runID, orgPool, e2eCfg.LockTimeout, t.Logf)
	if err != nil {
		t.Fatalf("acquiring org (env=%s): %v", cfg.Environment, err)
	}
	client := e2etest.NewLiveClient(token)
	t.Cleanup(func() {
		e2etest.ReleaseLock(context.Background(), client, org, runID, t)
	})

	binary := e2etest.BuildCLIBinary(t)

	e2etest.CleanupStaleResources(ctx, client, token, org, t)

	// Select the install driver based on the ENVIRONMENT:
	//   dev   → CF preview mint (ephemeral per run)
	//   stage → durable CF mint at stage-mint.fullsend.sh
	var driverFactory install.Factory
	if cfg.Environment == "stage" {
		driverFactory = install.NewRepoPoolCFMintStage
	} else {
		driverFactory = install.NewRepoPoolCFMintPreviews
	}

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

	// Default concurrency to driver capacity. Honor GODOG_CONCURRENCY
	// when set; warn (do not fail) if it exceeds capacity.
	concurrency := driver.Capacity()
	if c := os.Getenv("GODOG_CONCURRENCY"); c != "" {
		n, err := strconv.Atoi(c)
		if err != nil || n < 1 {
			t.Fatalf("GODOG_CONCURRENCY must be a positive integer, got %q", c)
		}
		concurrency = n
	}
	if concurrency > driver.Capacity() {
		t.Logf("WARNING: GODOG_CONCURRENCY=%d exceeds driver capacity %d; excess workers will block in AllocateRepo", concurrency, driver.Capacity())
	}

	var scmDriver scm.Driver
	switch cfg.SCM {
	case "github":
		scmDriver = scmgh.New(client)
	case "gitlab":
		// TODO: client is a GitHub forge.Client (from e2etest.NewLiveClient).
		// When BEHAVIOUR_SCM=gitlab is used in CI, this must be replaced with
		// a GitLab-backed forge.Client. Currently latent: no CI job sets
		// BEHAVIOUR_SCM=gitlab and @skip:gitlab tag removal is still pending.
		scmDriver = scmgl.New(client)
	default:
		t.Fatalf("unsupported BEHAVIOUR_SCM %q", cfg.SCM)
	}

	var ciDriver ci.Driver
	switch cfg.CI {
	case "githubactions":
		ciDriver = gaci.New(client, token)
	case "gitlabci":
		// TODO: client is a GitHub forge.Client (from e2etest.NewLiveClient).
		// When BEHAVIOUR_CI=gitlabci is used in CI, this must be replaced with
		// a GitLab-backed forge.Client. Currently latent: no CI job sets
		// BEHAVIOUR_CI=gitlabci.
		ciDriver = glci.New(client, token)
	default:
		t.Fatalf("unsupported BEHAVIOUR_CI %q", cfg.CI)
	}

	template := &world.World{
		Config:       cfg,
		SCM:          scmDriver,
		CI:           ciDriver,
		Driver:       driver,
		Org:          org,
		Token:        token,
		Logf:         t.Logf,
		FixturesRoot: "e2e/behaviour",
		RepoOwner:    org,
	}

	suiteRunner := godog.TestSuite{
		Name:                "behaviour",
		ScenarioInitializer: func(sc *godog.ScenarioContext) { suite.InitScenario(sc, template) },
		Options: &godog.Options{
			Format:      "pretty",
			Paths:       []string{"features"},
			TestingT:    t,
			Tags:        os.Getenv("GODOG_TAGS"),
			Concurrency: concurrency,
		},
	}
	if st := suiteRunner.Run(); st != 0 {
		t.Fatalf("behaviour suite failed with status %d", st)
	}
}
