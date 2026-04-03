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
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestAdminInstallUninstall(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	cfg := loadEnvConfig(t)
	ctx := context.Background()

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
	err = githubLogin(page, cfg.username, cfg.password)
	require.NoError(t, err, "logging into GitHub")

	// Generate a PAT for API access.
	patNote := fmt.Sprintf("fullsend-e2e-%d", time.Now().Unix())
	t.Logf("Creating PAT: %s", patNote)
	token, err := createPAT(page, patNote)
	require.NoError(t, err, "creating PAT")
	t.Cleanup(func() {
		t.Log("Deleting PAT...")
		if delErr := deletePAT(page, patNote); delErr != nil {
			t.Logf("warning: could not delete PAT: %v", delErr)
		}
	})

	// --- GitHub client ---
	client := newLiveClient(token)
	printer := ui.New(os.Stdout)

	// ======================
	// Phase 0: Acquire lock
	// ======================
	runID := uuid.New().String()
	t.Logf("E2E run ID: %s", runID)

	err = acquireLock(ctx, client, token, testOrg, runID, cfg.lockTimeout)
	require.NoError(t, err, "acquiring e2e lock")
	t.Cleanup(func() {
		releaseLock(context.Background(), client, testOrg, runID, t)
	})

	// ======================
	// Phase 1: Teardown-first cleanup
	// ======================
	cleanupStaleResources(ctx, client, page, t)

	// ======================
	// Phase 2: Install
	// ======================
	t.Log("=== Phase 2: Install ===")

	// 2a. App setup via manifest flow with Playwright.
	playwrightBrowser := NewPlaywrightBrowserOpener(page)
	prompter := AutoPrompter{}

	setup := appsetup.NewSetup(client, prompter, playwrightBrowser, printer)

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

		// Register cleanup for this app.
		registerAppCleanup(t, page, appCreds.Slug)
	}

	// 2b. Discover repos and build config.
	allRepos, err := client.ListOrgRepos(ctx, testOrg)
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

	user, err := client.GetAuthenticatedUser(ctx)
	require.NoError(t, err, "getting authenticated user")

	// 2c. Build layer stack and install.
	stack := buildTestLayerStack(testOrg, client, orgCfg, printer, user, hasPrivate, enabledRepos, defaultBranches, agentCreds)

	registerRepoCleanup(t, client, testOrg, forge.ConfigRepoName)

	err = stack.InstallAll(ctx)
	require.NoError(t, err, "installing layers")

	// ======================
	// Phase 3: Verify install
	// ======================
	t.Log("=== Phase 3: Verify Install ===")

	// 3a. .fullsend repo exists.
	repo, err := client.GetRepo(ctx, testOrg, forge.ConfigRepoName)
	require.NoError(t, err, ".fullsend repo should exist")
	assert.Equal(t, forge.ConfigRepoName, repo.Name)

	// 3b. config.yaml exists and parses.
	cfgData, err := client.GetFileContent(ctx, testOrg, forge.ConfigRepoName, "config.yaml")
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
		_, err := client.GetFileContent(ctx, testOrg, forge.ConfigRepoName, path)
		assert.NoError(t, err, "%s should exist in .fullsend", path)
	}

	// 3d. Secrets and variables exist for each role.
	for _, role := range defaultRoles {
		secretName := fmt.Sprintf("FULLSEND_%s_APP_PRIVATE_KEY", strings.ToUpper(role))
		exists, err := client.RepoSecretExists(ctx, testOrg, forge.ConfigRepoName, secretName)
		assert.NoError(t, err, "checking secret %s", secretName)
		assert.True(t, exists, "secret %s should exist", secretName)

		varName := fmt.Sprintf("FULLSEND_%s_APP_ID", strings.ToUpper(role))
		exists, err = client.RepoVariableExists(ctx, testOrg, forge.ConfigRepoName, varName)
		assert.NoError(t, err, "checking variable %s", varName)
		assert.True(t, exists, "variable %s should exist", varName)
	}

	// 3e. Enrollment PR exists for test-repo.
	prs, err := client.ListRepoPullRequests(ctx, testOrg, testRepo)
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

	analyzeStack := buildTestLayerStack(testOrg, client, orgCfg, printer, user, hasPrivate, enabledRepos, defaultBranches, agentCreds)
	reports, err := analyzeStack.AnalyzeAll(ctx)
	require.NoError(t, err, "analyzing layers")
	for _, report := range reports {
		assert.Equal(t, layers.StatusInstalled, report.Status,
			"layer %s should be installed, got %s (details: %v)",
			report.Name, report.Status, report.Details)
	}

	// ======================
	// Phase 5: Uninstall
	// ======================
	t.Log("=== Phase 5: Uninstall ===")

	emptyCfg := config.NewOrgConfig(nil, nil, nil, nil)
	uninstallStack := layers.NewStack(
		layers.NewConfigRepoLayer(testOrg, client, emptyCfg, printer, false),
		layers.NewWorkflowsLayer(testOrg, client, printer, ""),
		layers.NewSecretsLayer(testOrg, client, nil, printer),
		layers.NewEnrollmentLayer(testOrg, client, nil, nil, printer),
	)

	errs := uninstallStack.UninstallAll(ctx)
	assert.Empty(t, errs, "uninstall should complete without errors")

	// ======================
	// Phase 6: Verify uninstall
	// ======================
	t.Log("=== Phase 6: Verify Uninstall ===")

	_, err = client.GetRepo(ctx, testOrg, forge.ConfigRepoName)
	assert.True(t, forge.IsNotFound(err), ".fullsend repo should be deleted after uninstall")

	t.Log("=== E2E test complete ===")
}

// --- Helper functions (duplicated from cli/admin.go for decoupling) ---

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
