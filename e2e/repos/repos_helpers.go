//go:build e2e

package repos

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	reposcfg "github.com/fullsend-ai/fullsend/internal/repos"
)

const (
	defaultReposE2EGitHubOrg   = "fullsend-repos-e2e-gh"
	defaultReposE2EGitLabGroup = "fullsend-repos-e2e-gl"

	// ephemeralRepoPrefix tags every repo/project this suite creates. Each run
	// appends a unique UUID (reposTestEnv.runID), so names never collide across
	// concurrent runs and t.Cleanup is the only teardown needed — there is no
	// shared setup-time scan that could delete another run's live repos.
	ephemeralRepoPrefix = "repos-e2e-"
)

func reposE2EGitHubOrg() string {
	if v := os.Getenv("REPOS_E2E_GITHUB_ORG"); v != "" {
		return v
	}
	return defaultReposE2EGitHubOrg
}

func reposE2EGitLabGroup() string {
	if v := os.Getenv("REPOS_E2E_GITLAB_GROUP"); v != "" {
		return v
	}
	return defaultReposE2EGitLabGroup
}

// reposTestEnv holds shared state for a repos lifecycle e2e test run.
type reposTestEnv struct {
	binary  string
	ghToken string
	glToken string
	ghOrg   string
	glGroup string

	ghClient *gh.LiveClient
	glClient *gl.LiveClient

	manifestDir string
	runID       string
}

// cliEnv returns the environment the fullsend CLI needs to reach both forges.
func (e *reposTestEnv) cliEnv() map[string]string {
	return map[string]string{
		"GH_TOKEN":     e.ghToken,
		"GITHUB_TOKEN": e.ghToken,
		"GITLAB_TOKEN": e.glToken,
	}
}

// reposTokenOrSkip reads a required token. Running locally without the token
// skips the suite; in CI (REPOS_E2E_REQUIRE=1, set by the dedicated repos job)
// a missing token fails loudly so a dropped secret cannot silently degrade the
// test to a green no-op.
func reposTokenOrSkip(t *testing.T, envVar string) string {
	t.Helper()
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	if os.Getenv("REPOS_E2E_REQUIRE") == "1" {
		t.Fatalf("%s is required when REPOS_E2E_REQUIRE=1 but is unset", envVar)
	}
	t.Skipf("%s not set, skipping repos e2e test", envVar)
	return ""
}

func setupReposTest(t *testing.T) *reposTestEnv {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping repos e2e test in short mode")
	}

	// Resolve tokens before the (slow) CLI build so a skip costs nothing.
	ghToken := reposTokenOrSkip(t, "REPOS_E2E_GITHUB_TOKEN")
	glToken := reposTokenOrSkip(t, "GITLAB_TOKEN")

	binary := e2etest.BuildCLIBinary(t)

	ghClient := e2etest.NewLiveClient(ghToken)

	glClient, err := gl.New(glToken)
	require.NoError(t, err, "creating GitLab client")

	return &reposTestEnv{
		binary:      binary,
		ghToken:     ghToken,
		glToken:     glToken,
		ghOrg:       reposE2EGitHubOrg(),
		glGroup:     reposE2EGitLabGroup(),
		ghClient:    ghClient,
		glClient:    glClient,
		manifestDir: t.TempDir(),
		runID:       uuid.New().String()[:8],
	}
}

// createEphemeralGitHubRepo creates a throwaway org repo and registers its
// deletion with t.Cleanup. The returned value is the short repo name.
func createEphemeralGitHubRepo(t *testing.T, client forge.Client, org, suffix string) string {
	t.Helper()
	ctx := context.Background()
	name := ephemeralRepoPrefix + suffix

	t.Logf("[setup] Creating GitHub repo %s/%s", org, name)
	_, err := client.CreateRepo(ctx, org, name, "repos e2e test (auto-deleted)", false)
	require.NoError(t, err, "creating GitHub repo %s/%s", org, name)

	t.Cleanup(func() {
		t.Logf("[cleanup] Deleting GitHub repo %s/%s", org, name)
		if delErr := client.DeleteRepo(context.Background(), org, name); delErr != nil {
			t.Logf("[cleanup] failed to delete GitHub repo %s/%s: %v", org, name, delErr)
		}
	})

	return name
}

// createEphemeralGitLabRepo creates a throwaway project inside group and
// registers its deletion with t.Cleanup. The returned value is the short
// project name (its path segment coincides with the name for these slug-safe
// ephemeral names).
func createEphemeralGitLabRepo(t *testing.T, client forge.Client, group, suffix string) string {
	t.Helper()
	ctx := context.Background()
	name := ephemeralRepoPrefix + suffix

	t.Logf("[setup] Creating GitLab project %s/%s", group, name)
	_, err := client.CreateRepo(ctx, group, name, "repos e2e test (auto-deleted)", false)
	require.NoError(t, err, "creating GitLab project %s/%s", group, name)

	t.Cleanup(func() {
		t.Logf("[cleanup] Deleting GitLab project %s/%s", group, name)
		if delErr := client.DeleteRepo(context.Background(), group, name); delErr != nil {
			t.Logf("[cleanup] failed to delete GitLab project %s/%s: %v", group, name, delErr)
		}
	})

	return name
}

func readTestManifest(t *testing.T, path string) *reposcfg.Manifest {
	t.Helper()
	m, err := reposcfg.LoadManifest(context.Background(), path)
	require.NoError(t, err, "reading manifest")
	return m
}

// parseStatus unmarshals `repos status --json` output into the real
// repos.StatusResult. Decoding into the production type (rather than a
// hand-rolled subset) keeps this assertion honest: a future field rename in the
// CLI's JSON schema surfaces here instead of being silently ignored.
func parseStatus(t *testing.T, output string) *reposcfg.StatusResult {
	t.Helper()
	jsonStr := extractJSON(output)
	var s reposcfg.StatusResult
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &s), "parsing status JSON:\n%s", output)
	return &s
}

// extractJSON returns the first complete JSON object embedded in output, so
// any leading log lines the CLI writes before its JSON payload are skipped.
func extractJSON(output string) string {
	start := strings.Index(output, "{")
	if start < 0 {
		return output
	}
	dec := json.NewDecoder(strings.NewReader(output[start:]))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return output[start:]
	}
	return string(raw)
}

const (
	repoVisibilityTimeout   = 30 * time.Second
	repoVisibilityPollDelay = 2 * time.Second
)

// waitForRepoVisible polls until a freshly created repo is readable, tolerating
// only the forge's eventual-consistency not-found window. Any other error
// (auth, rate limit, malformed owner/repo) fails fast rather than hiding behind
// the visibility timeout.
func waitForRepoVisible(t *testing.T, client forge.Client, owner, repo string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), repoVisibilityTimeout)
	defer cancel()
	for {
		_, err := client.GetRepo(ctx, owner, repo)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			t.Fatalf("timed out waiting for %s/%s to become visible: %v", owner, repo, err)
		}
		if !forge.IsNotFound(err) {
			t.Fatalf("checking visibility of %s/%s: %v", owner, repo, err)
		}
		t.Logf("[setup] Waiting for %s/%s to become visible...", owner, repo)
		select {
		case <-time.After(repoVisibilityPollDelay):
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s/%s to become visible", owner, repo)
		}
	}
}
