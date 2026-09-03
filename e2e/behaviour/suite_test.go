//go:build behaviour

package behaviour_test

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/cucumber/godog"

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

	org := e2etest.BehaviourOrg()
	token, err := e2etest.TokenForOrg(ctx, e2eCfg, org)
	if err != nil {
		t.Fatalf("resolving token for %s: %v", org, err)
	}
	client := e2etest.NewLiveClient(token)

	binary := e2etest.BuildCLIBinary(t)

	driver, err := install.NewCFMintFactory(org, client, token, binary, e2eCfg.GCPProjectID, t.Logf)
	if err != nil {
		t.Fatalf("creating install driver: %v", err)
	}

	t.Cleanup(func() {
		if finalizeErr := driver.Finalize(context.Background()); finalizeErr != nil {
			t.Logf("driver finalize: %v", finalizeErr)
		}
	})

	concurrency := driver.DefaultConcurrency()
	if c := os.Getenv("GODOG_CONCURRENCY"); c != "" {
		n, err := strconv.Atoi(c)
		if err != nil || n < 1 {
			t.Fatalf("GODOG_CONCURRENCY must be a positive integer, got %q", c)
		}
		concurrency = n
	}

	var scmDriver scm.Driver
	switch cfg.SCM {
	case "github":
		scmDriver = scmgh.New(client)
	case "gitlab":
		scmDriver = scmgl.New(client)
	default:
		t.Fatalf("unsupported BEHAVIOUR_SCM %q", cfg.SCM)
	}

	var ciDriver ci.Driver
	switch cfg.CI {
	case "githubactions":
		ciDriver = gaci.New(client, token)
	case "gitlabci":
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
