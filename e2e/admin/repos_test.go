//go:build e2e

package admin

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

func TestReposLifecycle(t *testing.T) {
	env := setupReposTest(t)
	ctx := context.Background()
	manifestPath := filepath.Join(env.manifestDir, "repos.yaml")

	// Phase 0: Create ephemeral repos.
	t.Log("=== Phase 0: Create ephemeral repos ===")
	ghRepoA := createEphemeralGitHubRepo(t, env.ghClient, env.ghOrg, "gh-a-"+env.runID)
	ghRepoB := createEphemeralGitHubRepo(t, env.ghClient, env.ghOrg, "gh-b-"+env.runID)
	glRepoA := createEphemeralGitLabRepo(t, env.glClient, env.glGroup, "gl-a-"+env.runID)
	glRepoB := createEphemeralGitLabRepo(t, env.glClient, env.glGroup, "gl-b-"+env.runID)

	waitForRepoVisible(t, env.ghClient, env.ghOrg, ghRepoA)
	waitForRepoVisible(t, env.ghClient, env.ghOrg, ghRepoB)
	waitForRepoVisible(t, env.glClient, env.glGroup, glRepoA)
	waitForRepoVisible(t, env.glClient, env.glGroup, glRepoB)

	ghFullA := env.ghOrg + "/" + ghRepoA
	ghFullB := env.ghOrg + "/" + ghRepoB
	glFullA := env.glGroup + "/" + glRepoA
	glFullB := env.glGroup + "/" + glRepoB

	t.Logf("GitHub repos: %s, %s", ghFullA, ghFullB)
	t.Logf("GitLab repos: %s, %s", glFullA, glFullB)

	// Phase 1: repos init (GitHub).
	t.Log("=== Phase 1: repos init (GitHub) ===")
	ghManifest := filepath.Join(env.manifestDir, "gh-repos.yaml")
	e2etest.RunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "init", env.ghOrg,
		"--forge", "github",
		"--repos", fmt.Sprintf("%s,%s", ghFullA, ghFullB),
		"-o", ghManifest,
		"--force",
	)
	ghInit := readTestManifest(t, ghManifest)
	require.Equal(t, 1, ghInit.Version)
	require.Len(t, ghInit.Repos, 2, "init should discover both GitHub repos")
	assert.Equal(t, "github", ghInit.Defaults.Forge)

	// Phase 2: repos init (GitLab).
	t.Log("=== Phase 2: repos init (GitLab) ===")
	glManifest := filepath.Join(env.manifestDir, "gl-repos.yaml")
	e2etest.RunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "init", env.glGroup,
		"--forge", "gitlab",
		"--repos", fmt.Sprintf("%s,%s", glFullA, glFullB),
		"-o", glManifest,
		"--force",
	)
	glInit := readTestManifest(t, glManifest)
	require.Equal(t, 1, glInit.Version)
	require.Len(t, glInit.Repos, 2, "init should discover both GitLab repos")
	assert.Equal(t, "gitlab", glInit.Defaults.Forge)

	// Phase 3: Build combined manifest with one repo per forge.
	t.Log("=== Phase 3: Build combined manifest ===")
	combined := buildCombinedManifest(env.ghOrg, env.glGroup, []string{ghRepoA}, []string{glRepoA})
	writeTestManifest(t, manifestPath, combined)
	t.Logf("Combined manifest at %s with 2 repos", manifestPath)

	// Phase 4: repos add (add second repo from each forge).
	t.Log("=== Phase 4: repos add ===")
	e2etest.RunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "add", ghFullB,
		"--forge", "github",
		"-f", manifestPath,
	)
	e2etest.RunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "add", glFullB,
		"--forge", "gitlab",
		"-f", manifestPath,
	)
	afterAdd := readTestManifest(t, manifestPath)
	require.Len(t, afterAdd.Repos, 4, "manifest should have 4 repos after add")

	repoNames := make([]string, len(afterAdd.Repos))
	for i, r := range afterAdd.Repos {
		repoNames[i] = r.Repo
	}
	assert.Contains(t, repoNames, ghFullA)
	assert.Contains(t, repoNames, ghFullB)
	assert.Contains(t, repoNames, glFullA)
	assert.Contains(t, repoNames, glFullB)

	// Phase 5: repos status (pre-install).
	// Status exits non-zero when repos are not installed.
	t.Log("=== Phase 5: repos status (pre-install) ===")
	statusOut, _ := e2etest.TryRunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "status", "--json", "-f", manifestPath,
	)
	status := parseStatusJSON(t, statusOut)
	require.Len(t, status.Repos, 4, "status should report 4 repos")
	assert.Equal(t, 4, status.Summary.Total)
	assert.Equal(t, 4, status.Summary.NotInstalled, "all repos should be not-installed")
	assert.Equal(t, 0, status.Summary.Installed)

	for _, r := range status.Repos {
		assert.False(t, r.Installed, "%s/%s should not be installed", r.Owner, r.Repo)
		assert.Empty(t, r.Error, "%s/%s should have no errors", r.Owner, r.Repo)
	}

	// Phase 6: repos install --dry-run.
	// Full install requires GCP WIF provisioning. Dry-run validates discovery
	// and config resolution without needing GCP credentials.
	t.Log("=== Phase 6: repos install --dry-run ===")
	installOut := e2etest.RunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "install",
		"--dry-run",
		"--skip-mint-check",
		"-f", manifestPath,
	)
	t.Logf("Install dry-run output:\n%s", installOut)
	for _, name := range []string{ghFullA, ghFullB, glFullA, glFullB} {
		assert.Contains(t, installOut, name, "install dry-run output should mention %s", name)
	}

	// Phase 7: repos diff (pre-install — repos not installed, expect warnings).
	// Diff may exit non-zero when repos are not installed.
	t.Log("=== Phase 7: repos diff ===")
	diffOut, diffErr := e2etest.TryRunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "diff", "--json", "-f", manifestPath,
	)
	if diffErr != nil {
		t.Logf("repos diff exited non-zero (expected for not-installed repos): %v", diffErr)
	}
	diff := parseDiffJSON(t, diffOut)
	t.Logf("Diff: %d changes, %d warnings", len(diff.Changes), len(diff.Warnings))
	assert.True(t, len(diff.Changes) > 0 || len(diff.Warnings) > 0,
		"diff against not-installed repos should report changes or warnings")

	// Phase 8: repos remove (remove one repo from each forge).
	t.Log("=== Phase 8: repos remove ===")
	e2etest.RunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "remove", ghFullB,
		"--yes",
		"-f", manifestPath,
	)
	afterRemoveOne := readTestManifest(t, manifestPath)
	require.Len(t, afterRemoveOne.Repos, 3, "manifest should have 3 repos after removing one")

	e2etest.RunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "remove", glFullB,
		"--yes",
		"-f", manifestPath,
	)
	afterRemoveTwo := readTestManifest(t, manifestPath)
	require.Len(t, afterRemoveTwo.Repos, 2, "manifest should have 2 repos after removing two")

	remainingNames := make([]string, len(afterRemoveTwo.Repos))
	for i, r := range afterRemoveTwo.Repos {
		remainingNames[i] = r.Repo
	}
	assert.Contains(t, remainingNames, ghFullA)
	assert.Contains(t, remainingNames, glFullA)

	// Phase 9: repos add (re-add removed repos).
	t.Log("=== Phase 9: repos add (re-add) ===")
	e2etest.RunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "add", ghFullB,
		"--forge", "github",
		"-f", manifestPath,
	)
	e2etest.RunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "add", glFullB,
		"--forge", "gitlab",
		"-f", manifestPath,
	)
	afterReAdd := readTestManifest(t, manifestPath)
	require.Len(t, afterReAdd.Repos, 4, "manifest should have 4 repos after re-add")

	// Phase 10: Verify forge API interaction — check repo variables are empty.
	t.Log("=== Phase 10: Verify forge API probing ===")
	_, guardExists, err := env.ghClient.GetRepoVariable(ctx, env.ghOrg, ghRepoA, forge.PerRepoGuardVar)
	require.NoError(t, err)
	assert.False(t, guardExists, "guard variable should not exist on fresh repo")

	_, guardExistsGL, err := env.glClient.GetRepoVariable(ctx, env.glGroup, glRepoA, forge.PerRepoGuardVar)
	require.NoError(t, err)
	assert.False(t, guardExistsGL, "guard variable should not exist on fresh GitLab repo")

	// Phase 11: repos upgrade-mint (expected failure — no real mint).
	t.Log("=== Phase 11: repos upgrade-mint ===")
	upgradeOut, upgradeErr := e2etest.TryRunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "upgrade-mint",
		"-f", manifestPath,
	)
	assert.Error(t, upgradeErr, "upgrade-mint should fail without a real mint deployment")
	assert.Contains(t, upgradeOut, "mint", "upgrade-mint error should reference mint in output")

	// Phase 12: repos remove all.
	t.Log("=== Phase 12: repos remove all ===")
	e2etest.RunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "remove",
		ghFullA, ghFullB, glFullA, glFullB,
		"--yes",
		"-f", manifestPath,
	)
	afterRemoveAll := readTestManifest(t, manifestPath)
	assert.Empty(t, afterRemoveAll.Repos, "manifest should have 0 repos after remove all")

	// Phase 13: repos status on empty manifest.
	t.Log("=== Phase 13: repos status (empty manifest) ===")
	emptyStatusOut, _ := e2etest.TryRunCLIWithEnv(t, env.binary, env.cliEnv(),
		"repos", "status", "--json", "-f", manifestPath,
	)
	emptyStatus := parseStatusJSON(t, emptyStatusOut)
	assert.Equal(t, 0, emptyStatus.Summary.Total, "empty manifest should report 0 repos")

	t.Log("=== All phases complete ===")
}
