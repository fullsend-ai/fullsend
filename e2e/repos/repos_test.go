//go:build e2e

package repos_test

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// TestReposLifecycle exercises the `fullsend repos` CLI (set, status,
// install --dry-run, uninstall) end to end against ephemeral GitHub and GitLab
// repositories. It is opt-in: it skips unless REPOS_E2E_GITHUB_TOKEN and
// GITLAB_TOKEN are set (and fails instead of skipping when REPOS_E2E_REQUIRE=1,
// which the dedicated CI job sets).
func TestReposLifecycle(t *testing.T) {
	env := setupReposTest(t)
	ctx := context.Background()
	manifestPath := filepath.Join(env.manifestDir, "repos.yaml")

	// Phase 0: create one ephemeral repo per forge.
	t.Log("=== Phase 0: Create ephemeral repos ===")
	ghRepo := createEphemeralGitHubRepo(t, env.ghClient, env.ghOrg, "gh-"+env.runID)
	glRepo := createEphemeralGitLabRepo(t, env.glClient, env.glGroup, "gl-"+env.runID)

	waitForRepoVisible(t, env.ghClient, env.ghOrg, ghRepo)
	waitForRepoVisible(t, env.glClient, env.glGroup, glRepo)

	ghFull := env.ghOrg + "/" + ghRepo
	glFull := env.glGroup + "/" + glRepo
	t.Logf("GitHub repo: %s", ghFull)
	t.Logf("GitLab repo: %s", glFull)

	// Phase 1: build the manifest with `repos set`.
	// gitlab.url must precede the GitLab repo entry (a GitLab section with
	// repos requires a url), and inference auth is required for every repo.
	t.Log("=== Phase 1: repos set (build manifest) ===")
	reposSet := func(path, value string) {
		e2etest.RunCLIWithEnv(t, env.binary, env.cliEnv(),
			"repos", "set", path, value, "-f", manifestPath)
	}
	reposSet("defaults.inference.auth", "vertex-wif")
	reposSet("gitlab.url", "https://gitlab.com")
	reposSet(fmt.Sprintf("github.repos[%q]", ghFull), "{}")
	reposSet(fmt.Sprintf("gitlab.repos[%q]", glFull), "{}")

	m := readTestManifest(t, manifestPath)
	require.Equal(t, 1, m.Version)
	require.NotNil(t, m.GitHub, "manifest should have a github section")
	require.NotNil(t, m.GitLab, "manifest should have a gitlab section")
	require.Len(t, m.GitHub.Repos, 1, "github section should have one repo")
	require.Len(t, m.GitLab.Repos, 1, "gitlab section should have one repo")
	assert.Equal(t, ghFull, m.GitHub.Repos[0].Name)
	assert.Equal(t, glFull, m.GitLab.Repos[0].Name)

	// Phase 2: repos status (pre-install). Freshly created repos have no
	// fullsend installed, so status reports them not-installed and exits
	// non-zero — expected, hence TryRunCLIWithEnv.
	t.Log("=== Phase 2: repos status (pre-install) ===")
	statusOut, statusErr := e2etest.TryRunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "status", "--json", "-f", manifestPath)
	if statusErr != nil {
		t.Logf("repos status exited non-zero (expected for not-installed repos): %v", statusErr)
	}
	status := parseStatus(t, statusOut)
	require.Len(t, status.Repos, 2, "status should report both repos")
	assert.Equal(t, 2, status.Summary.Total)
	assert.Equal(t, 2, status.Summary.NotInstalled, "both repos should be not-installed")
	assert.Equal(t, 0, status.Summary.Installed)
	for _, r := range status.Repos {
		assert.False(t, r.Installed, "%s/%s should not be installed", r.Owner, r.Repo)
		assert.Empty(t, r.Error, "%s/%s should have no status error", r.Owner, r.Repo)
	}

	// Phase 3: repos install --dry-run. A full install provisions GCP WIF and
	// mint access; the dry run validates manifest resolution and planning
	// without touching either. --vertex-project supplies the project the
	// vertex-wif plan references, and an explicit --vertex-wif-provider skips
	// the live GCP project-number lookup that would otherwise fail the run
	// (the job has no GCP credentials). Neither value is contacted in a dry run.
	t.Log("=== Phase 3: repos install --dry-run ===")
	installOut, installErr := e2etest.TryRunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "install", "--dry-run",
		"--vertex-project", "dummy-project",
		"--vertex-wif-provider", dummyWIFProvider,
		"-f", manifestPath)
	t.Logf("install dry-run output:\n%s", installOut)
	require.NoError(t, installErr, "install dry-run should succeed")
	// Each repo is named in manifest-loading output too, so require the
	// per-repo dry-run planning line ("[owner/repo] Would ...") instead.
	assert.Regexp(t, `\[`+regexp.QuoteMeta(ghFull)+`\] Would `, installOut,
		"install dry-run should plan the GitHub repo")
	assert.Regexp(t, `\[`+regexp.QuoteMeta(glFull)+`\] Would `, installOut,
		"install dry-run should plan the GitLab repo")

	// Phase 4: forge API probe — the per-repo install guard variable must be
	// absent on these fresh repos, confirming the dry run installed nothing.
	t.Log("=== Phase 4: forge API probe ===")
	_, ghGuard, err := env.ghClient.GetRepoVariable(ctx, env.ghOrg, ghRepo, forge.PerRepoGuardVar)
	require.NoError(t, err)
	assert.False(t, ghGuard, "guard variable should not exist on fresh GitHub repo")

	_, glGuard, err := env.glClient.GetRepoVariable(ctx, env.glGroup, glRepo, forge.PerRepoGuardVar)
	require.NoError(t, err)
	assert.False(t, glGuard, "guard variable should not exist on fresh GitLab project")

	// Phase 5: repos uninstall --manifest-only. The repos were never installed,
	// so --manifest-only drops entries without a teardown. Remove the GitHub
	// repo and confirm the GitLab repo remains.
	t.Log("=== Phase 5: repos uninstall --manifest-only (GitHub) ===")
	e2etest.RunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "uninstall", ghFull, "--manifest-only", "--yes", "-f", manifestPath)
	afterGH := readTestManifest(t, manifestPath)
	assert.Equal(t, 0, sectionRepoCount(afterGH.GitHub), "github section should have no repos left")
	require.Equal(t, 1, sectionRepoCount(afterGH.GitLab), "gitlab repo should remain")
	assert.Equal(t, glFull, afterGH.GitLab.Repos[0].Name)

	// Phase 6: repos status on the reduced manifest reports a single repo.
	t.Log("=== Phase 6: repos status (reduced manifest) ===")
	reducedOut, _ := e2etest.TryRunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "status", "--json", "-f", manifestPath)
	reduced := parseStatus(t, reducedOut)
	assert.Equal(t, 1, reduced.Summary.Total, "reduced manifest should report one repo")

	// Phase 7: remove the last repo; the manifest ends empty.
	t.Log("=== Phase 7: repos uninstall --manifest-only (GitLab) ===")
	e2etest.RunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "uninstall", glFull, "--manifest-only", "--yes", "-f", manifestPath)
	afterAll := readTestManifest(t, manifestPath)
	assert.Equal(t, 0, sectionRepoCount(afterAll.GitHub), "github section should be empty")
	assert.Equal(t, 0, sectionRepoCount(afterAll.GitLab), "gitlab section should be empty")

	t.Log("=== All phases complete ===")
}

// dummyWIFProvider is a syntactically valid WIF provider resource name. It is
// never contacted: passing it makes `repos install --dry-run` skip the GCP
// project-number lookup.
const dummyWIFProvider = "projects/123456789012/locations/global/workloadIdentityPools/dummy-pool/providers/dummy-provider"

// sectionRepoCount returns the number of repo entries in a forge section,
// treating a nil (absent) section as zero.
func sectionRepoCount(p *repos.PlatformConfig) int {
	if p == nil {
		return 0
	}
	return len(p.Repos)
}
