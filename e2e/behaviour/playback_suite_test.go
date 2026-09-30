//go:build playback

package behaviour_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/google/uuid"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	gaci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	glci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/gitlabci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	scmgl "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/gitlab"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"

	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
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

	if org := os.Getenv("PLAYBACK_GITLAB_GROUP"); org != "" {
		return buildGitLabPlaybackWorld(t, org)
	}

	return buildGitHubPlaybackWorld(t)
}

func buildGitHubPlaybackWorld(t *testing.T) (*world.World, *install.PlaybackDriver) {
	t.Helper()

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

	driver := install.NewPlaybackDriver(org, "github", client, token, binary, e2etest.DefaultHostedMintGCPProject, t.Logf)

	cfg := env.RunnerConfig{
		SCM:         "github",
		CI:          "githubactions",
		InstallMode: "per-repo",
		Environment: environment,
	}

	ciDriver := gaci.New(client, token)

	template := &world.World{
		Config:       cfg,
		SCM:          scmgh.New(client),
		CI:           ciDriver,
		Driver:       driver,
		Org:          org,
		Token:        token,
		Logf:         t.Logf,
		FixturesRoot: "e2e/behaviour",
		RepoOwner:    org,
	}

	return template, driver
}

func buildGitLabPlaybackWorld(t *testing.T, group string) (*world.World, *install.PlaybackDriver) {
	t.Helper()

	token, err := resolveGitLabToken()
	if err != nil {
		t.Skipf("no GitLab token available: %v", err)
	}

	var opts []gl.Option
	if baseURL := os.Getenv("GITLAB_BASE_URL"); baseURL != "" {
		opts = append(opts, gl.WithBaseURL(baseURL))
	}

	client, err := gl.New(token, opts...)
	if err != nil {
		t.Fatalf("creating GitLab client: %v", err)
	}

	binary := e2etest.BuildCLIBinary(t)

	driver := install.NewPlaybackDriver(group, "gitlab", client, token, binary, e2etest.DefaultHostedMintGCPProject, t.Logf)

	cfg := env.RunnerConfig{
		SCM:         "gitlab",
		CI:          "gitlabci",
		InstallMode: "per-repo",
		Environment: "dev",
	}

	ciDriver := glci.New(client, token)

	template := &world.World{
		Config:       cfg,
		SCM:          scmgl.New(client),
		CI:           ciDriver,
		Driver:       driver,
		Org:          group,
		Token:        token,
		Logf:         t.Logf,
		FixturesRoot: "e2e/behaviour",
		RepoOwner:    group,
	}

	return template, driver
}

func resolveGitLabToken() (string, error) {
	if token := os.Getenv("GITLAB_TOKEN"); token != "" {
		return token, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "glab", "auth", "token").Output()
	if err == nil {
		if token := strings.TrimSpace(string(out)); token != "" {
			return token, nil
		}
	}
	return "", fmt.Errorf("no GitLab token: set GITLAB_TOKEN or run 'glab auth login'")
}

// Ensure both forge clients satisfy forge.Client at the type level.
var (
	_ = e2etest.NewLiveClient         // *gh.LiveClient
	_ = func() { _ = gh.New }         // compile-time reference
	_ = func() { _, _ = gl.New("x") } // compile-time reference
)
