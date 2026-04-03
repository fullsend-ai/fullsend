//go:build e2e

package admin

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// e2eEnv holds the shared state for an e2e test run.
type e2eEnv struct {
	cfg           envConfig
	page          playwright.Page
	client        *gh.LiveClient
	token         string
	printer       *ui.Printer
	runID         string
	screenshotDir string
}

// setupE2ETest performs the common Playwright, login, PAT, lock, and cleanup
// steps shared by all e2e test functions. Returns the shared env.
func setupE2ETest(t *testing.T) *e2eEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	cfg := loadEnvConfig(t)
	screenshotDir := t.TempDir()

	// --- Playwright setup ---
	pw, err := playwright.Run()
	require.NoError(t, err, "starting Playwright")
	t.Cleanup(func() {
		if stopErr := pw.Stop(); stopErr != nil {
			t.Logf("warning: could not stop Playwright: %v", stopErr)
		}
	})

	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(os.Getenv("E2E_HEADED") != "true"),
	})
	require.NoError(t, err, "launching Playwright browser")
	t.Cleanup(func() { _ = browser.Close() })

	browserCtx, err := browser.NewContext()
	require.NoError(t, err, "creating browser context")
	t.Cleanup(func() { _ = browserCtx.Close() })

	page, err := browserCtx.NewPage()
	require.NoError(t, err, "creating Playwright page")

	// Log into GitHub programmatically.
	t.Log("Logging into GitHub...")
	err = githubLogin(page, cfg.username, cfg.password, t.Logf)
	require.NoError(t, err, "logging into GitHub")
	t.Logf("Post-login URL: %s", page.URL())

	// Generate a PAT for API access.
	patNote := fmt.Sprintf("fullsend-e2e-%d", time.Now().Unix())
	t.Logf("Creating PAT: %s", patNote)
	token, err := createPAT(page, patNote, t.Logf)
	require.NoError(t, err, "creating PAT")
	t.Cleanup(func() {
		t.Log("Deleting PAT...")
		if delErr := deletePAT(page, patNote, t.Logf); delErr != nil {
			t.Logf("warning: could not delete PAT: %v", delErr)
		}
	})

	// --- GitHub client ---
	client := newLiveClient(token)
	printer := ui.New(os.Stdout)

	// Acquire lock.
	runID := uuid.New().String()
	t.Logf("E2E run ID: %s", runID)

	err = acquireLock(context.Background(), client, token, testOrg, runID, cfg.lockTimeout, t.Logf)
	require.NoError(t, err, "acquiring e2e lock")
	t.Cleanup(func() {
		releaseLock(context.Background(), client, testOrg, runID, t)
	})

	// Teardown-first cleanup.
	cleanupStaleResources(context.Background(), client, page, token, screenshotDir, t)

	return &e2eEnv{
		cfg:           cfg,
		page:          page,
		client:        client,
		token:         token,
		printer:       printer,
		runID:         runID,
		screenshotDir: screenshotDir,
	}
}

// runFullInstall executes the full install flow (app setup + layer stack install)
// and returns the agent credentials and org config for verification.
func runFullInstall(t *testing.T, env *e2eEnv) ([]layers.AgentCredentials, *config.OrgConfig, []string, map[string]string) {
	t.Helper()
	ctx := context.Background()

	// App setup via manifest flow with Playwright.
	playwrightBrowser := NewPlaywrightBrowserOpener(env.page, t.Logf, env.screenshotDir)
	prompter := AutoPrompter{}
	setup := appsetup.NewSetup(env.client, prompter, playwrightBrowser, env.printer)

	var agentCreds []layers.AgentCredentials
	for _, role := range defaultRoles {
		t.Logf("Setting up app for role: %s", role)
		appCreds, err := setup.Run(ctx, testOrg, role)
		require.NoError(t, err, "setting up app for role %s", role)

		agentCreds = append(agentCreds, layers.AgentCredentials{
			AgentEntry: config.AgentEntry{
				Role: role,
				Name: appCreds.Name,
				Slug: appCreds.Slug,
			},
			PEM:   appCreds.PEM,
			AppID: appCreds.AppID,
		})

		registerAppCleanup(t, env.page, appCreds.Slug, env.screenshotDir)
	}

	// Discover repos and build config.
	allRepos, err := env.client.ListOrgRepos(ctx, testOrg)
	require.NoError(t, err, "listing org repos")

	repoNames := repoNameList(allRepos)
	defaultBranches := repoDefaultBranches(allRepos)
	hasPrivate := hasPrivateRepos(allRepos)
	enabledRepos := []string{testRepo}

	agents := make([]config.AgentEntry, len(agentCreds))
	for i, ac := range agentCreds {
		agents[i] = ac.AgentEntry
	}

	orgCfg := config.NewOrgConfig(repoNames, enabledRepos, defaultRoles, agents)

	user, err := env.client.GetAuthenticatedUser(ctx)
	require.NoError(t, err, "getting authenticated user")

	// Build layer stack and install.
	stack := buildTestLayerStack(testOrg, env.client, orgCfg, env.printer, user, hasPrivate, enabledRepos, defaultBranches, agentCreds)
	registerRepoCleanup(t, env.client, testOrg, forge.ConfigRepoName)

	err = stack.InstallAll(ctx)
	require.NoError(t, err, "installing layers")

	return agentCreds, orgCfg, enabledRepos, defaultBranches
}

func TestAdminInstallUninstall(t *testing.T) {
	env := setupE2ETest(t)
	ctx := context.Background()

	// ======================
	// Phase 2: Install
	// ======================
	t.Log("=== Phase 2: Install ===")
	agentCreds, orgCfg, enabledRepos, defaultBranches := runFullInstall(t, env)

	// ======================
	// Phase 3: Verify install
	// ======================
	t.Log("=== Phase 3: Verify Install ===")

	// 3a. .fullsend repo exists.
	repo, err := env.client.GetRepo(ctx, testOrg, forge.ConfigRepoName)
	require.NoError(t, err, ".fullsend repo should exist")
	assert.Equal(t, forge.ConfigRepoName, repo.Name)

	// 3b. config.yaml exists and parses.
	cfgData, err := env.client.GetFileContent(ctx, testOrg, forge.ConfigRepoName, "config.yaml")
	require.NoError(t, err, "config.yaml should exist")
	parsedCfg, err := config.ParseOrgConfig(cfgData)
	require.NoError(t, err, "config.yaml should parse")
	assert.Equal(t, "1", parsedCfg.Version)
	assert.Len(t, parsedCfg.Agents, len(defaultRoles))

	// 3c. Workflow files exist.
	for _, path := range []string{
		".github/workflows/agent.yaml",
		".github/workflows/repo-onboard.yaml",
		"CODEOWNERS",
	} {
		_, err := env.client.GetFileContent(ctx, testOrg, forge.ConfigRepoName, path)
		assert.NoError(t, err, "%s should exist in .fullsend", path)
	}

	// 3d. Secrets and variables exist for each role.
	for _, role := range defaultRoles {
		secretName := fmt.Sprintf("FULLSEND_%s_APP_PRIVATE_KEY", strings.ToUpper(role))
		exists, err := env.client.RepoSecretExists(ctx, testOrg, forge.ConfigRepoName, secretName)
		assert.NoError(t, err, "checking secret %s", secretName)
		assert.True(t, exists, "secret %s should exist", secretName)

		varName := fmt.Sprintf("FULLSEND_%s_APP_ID", strings.ToUpper(role))
		exists, err = env.client.RepoVariableExists(ctx, testOrg, forge.ConfigRepoName, varName)
		assert.NoError(t, err, "checking variable %s", varName)
		assert.True(t, exists, "variable %s should exist", varName)
	}

	// 3e. Enrollment PR exists for test-repo.
	prs, err := env.client.ListRepoPullRequests(ctx, testOrg, testRepo)
	require.NoError(t, err, "listing PRs for %s", testRepo)
	found := false
	for _, pr := range prs {
		if strings.Contains(pr.Title, "fullsend") {
			found = true
			t.Logf("Found enrollment PR: %s", pr.URL)
			break
		}
	}
	assert.True(t, found, "enrollment PR should exist for %s", testRepo)

	// ======================
	// Phase 4: Analyze
	// ======================
	t.Log("=== Phase 4: Analyze ===")
	verifyAnalyze(t, env, orgCfg, enabledRepos, defaultBranches, agentCreds)

	// ======================
	// Phase 5: Uninstall
	// ======================
	t.Log("=== Phase 5: Uninstall ===")
	runUninstall(t, env)

	// ======================
	// Phase 6: Verify uninstall
	// ======================
	t.Log("=== Phase 6: Verify Uninstall ===")
	verifyUninstall(t, env)

	t.Log("=== E2E test complete ===")
}

func TestUninstallBeforeInstall(t *testing.T) {
	env := setupE2ETest(t)
	ctx := context.Background()

	// Verify layers are in a clean state. Layers with empty config (secrets,
	// enrollment) report "installed" when there's nothing to track, so we
	// only check config-repo and workflows which have concrete artifacts.
	t.Log("=== Verify nothing is installed ===")
	emptyCfg := config.NewOrgConfig(nil, nil, nil, nil)
	stack := layers.NewStack(
		layers.NewConfigRepoLayer(testOrg, env.client, emptyCfg, env.printer, false),
		layers.NewWorkflowsLayer(testOrg, env.client, env.printer, ""),
		layers.NewSecretsLayer(testOrg, env.client, nil, env.printer),
		layers.NewEnrollmentLayer(testOrg, env.client, nil, nil, env.printer),
	)
	reports, err := stack.AnalyzeAll(ctx)
	require.NoError(t, err, "analyzing layers")
	for _, report := range reports {
		switch report.Name {
		case "config-repo", "workflows":
			assert.Equal(t, layers.StatusNotInstalled, report.Status,
				"layer %s should be not-installed before install, got %s",
				report.Name, report.Status)
		default:
			// Layers with empty config may report "installed" (nothing to do).
			t.Logf("layer %s status: %s (accepted)", report.Name, report.Status)
		}
	}

	// Uninstall should be a no-op — errors about missing resources are acceptable.
	t.Log("=== Uninstall (no-op) ===")
	errs := stack.UninstallAll(ctx)
	for _, e := range errs {
		if !forge.IsNotFound(e) {
			t.Errorf("unexpected uninstall error (not a not-found): %v", e)
		}
	}

	// Still not installed.
	reports, err = stack.AnalyzeAll(ctx)
	require.NoError(t, err, "analyzing layers after no-op uninstall")
	for _, report := range reports {
		switch report.Name {
		case "config-repo", "workflows":
			assert.Equal(t, layers.StatusNotInstalled, report.Status,
				"layer %s should still be not-installed, got %s",
				report.Name, report.Status)
		default:
			t.Logf("layer %s status: %s (accepted)", report.Name, report.Status)
		}
	}

	t.Log("=== TestUninstallBeforeInstall complete ===")
}

func TestInstallIsIdempotent(t *testing.T) {
	env := setupE2ETest(t)
	ctx := context.Background()

	// First install.
	t.Log("=== First install ===")
	agentCreds, orgCfg, enabledRepos, defaultBranches := runFullInstall(t, env)

	// Verify installed.
	t.Log("=== Verify first install ===")
	verifyAnalyze(t, env, orgCfg, enabledRepos, defaultBranches, agentCreds)

	// Second install — should be idempotent.
	t.Log("=== Second install (idempotent) ===")
	user, err := env.client.GetAuthenticatedUser(ctx)
	require.NoError(t, err)
	hasPrivate := false // doesn't matter for second install
	stack := buildTestLayerStack(testOrg, env.client, orgCfg, env.printer, user, hasPrivate, enabledRepos, defaultBranches, agentCreds)
	err = stack.InstallAll(ctx)
	require.NoError(t, err, "second InstallAll should succeed")

	// Verify still installed.
	t.Log("=== Verify second install ===")
	verifyAnalyze(t, env, orgCfg, enabledRepos, defaultBranches, agentCreds)

	// config.yaml still parses.
	cfgData, err := env.client.GetFileContent(ctx, testOrg, forge.ConfigRepoName, "config.yaml")
	require.NoError(t, err, "config.yaml should still exist")
	_, err = config.ParseOrgConfig(cfgData)
	require.NoError(t, err, "config.yaml should still parse")

	// Secrets still exist.
	for _, role := range defaultRoles {
		secretName := fmt.Sprintf("FULLSEND_%s_APP_PRIVATE_KEY", strings.ToUpper(role))
		exists, err := env.client.RepoSecretExists(ctx, testOrg, forge.ConfigRepoName, secretName)
		assert.NoError(t, err)
		assert.True(t, exists, "secret %s should still exist after idempotent install", secretName)
	}

	// Cleanup.
	t.Log("=== Uninstall ===")
	runUninstall(t, env)
	verifyUninstall(t, env)

	t.Log("=== TestInstallIsIdempotent complete ===")
}

// --- Shared verification helpers ---

func verifyAnalyze(t *testing.T, env *e2eEnv, orgCfg *config.OrgConfig, enabledRepos []string, defaultBranches map[string]string, agentCreds []layers.AgentCredentials) {
	t.Helper()
	ctx := context.Background()

	user, err := env.client.GetAuthenticatedUser(ctx)
	require.NoError(t, err)

	allRepos, err := env.client.ListOrgRepos(ctx, testOrg)
	require.NoError(t, err)
	hasPrivate := hasPrivateRepos(allRepos)

	analyzeStack := buildTestLayerStack(testOrg, env.client, orgCfg, env.printer, user, hasPrivate, enabledRepos, defaultBranches, agentCreds)
	reports, err := analyzeStack.AnalyzeAll(ctx)
	require.NoError(t, err, "analyzing layers")
	for _, report := range reports {
		if report.Name == "enrollment" {
			// Enrollment creates a PR but doesn't merge it, so the shim
			// workflow file doesn't exist on the default branch yet.
			assert.Contains(t, []layers.LayerStatus{layers.StatusInstalled, layers.StatusNotInstalled},
				report.Status, "layer %s status: %s (details: %v)",
				report.Name, report.Status, report.Details)
			continue
		}
		assert.Equal(t, layers.StatusInstalled, report.Status,
			"layer %s should be installed, got %s (details: %v)",
			report.Name, report.Status, report.Details)
	}
}

func runUninstall(t *testing.T, env *e2eEnv) {
	t.Helper()
	ctx := context.Background()
	emptyCfg := config.NewOrgConfig(nil, nil, nil, nil)
	uninstallStack := layers.NewStack(
		layers.NewConfigRepoLayer(testOrg, env.client, emptyCfg, env.printer, false),
		layers.NewWorkflowsLayer(testOrg, env.client, env.printer, ""),
		layers.NewSecretsLayer(testOrg, env.client, nil, env.printer),
		layers.NewEnrollmentLayer(testOrg, env.client, nil, nil, env.printer),
	)
	errs := uninstallStack.UninstallAll(ctx)
	assert.Empty(t, errs, "uninstall should complete without errors")
}

func verifyUninstall(t *testing.T, env *e2eEnv) {
	t.Helper()
	_, err := env.client.GetRepo(context.Background(), testOrg, forge.ConfigRepoName)
	assert.True(t, forge.IsNotFound(err), ".fullsend repo should be deleted after uninstall")
}

// --- Helper functions ---

func buildTestLayerStack(
	org string,
	client forge.Client,
	cfg *config.OrgConfig,
	printer *ui.Printer,
	user string,
	hasPrivate bool,
	enabledRepos []string,
	defaultBranches map[string]string,
	agentCreds []layers.AgentCredentials,
) *layers.Stack {
	return layers.NewStack(
		layers.NewConfigRepoLayer(org, client, cfg, printer, hasPrivate),
		layers.NewWorkflowsLayer(org, client, printer, user),
		layers.NewSecretsLayer(org, client, agentCreds, printer),
		layers.NewEnrollmentLayer(org, client, enabledRepos, defaultBranches, printer),
	)
}

func repoNameList(repos []forge.Repository) []string {
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}
	return names
}

func repoDefaultBranches(repos []forge.Repository) map[string]string {
	branches := make(map[string]string, len(repos))
	for _, r := range repos {
		branches[r.Name] = r.DefaultBranch
	}
	return branches
}

func hasPrivateRepos(repos []forge.Repository) bool {
	for _, r := range repos {
		if r.Private {
			return true
		}
	}
	return false
}
