//go:build e2e

package admin

import (
	"context"
	"encoding/json"
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
	"github.com/fullsend-ai/fullsend/internal/inference"
	"github.com/fullsend-ai/fullsend/internal/inference/vertex"
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
// steps. Returns the shared env.
func setupE2ETest(t *testing.T) *e2eEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	cfg := loadEnvConfig(t)
	screenshotDir := os.Getenv("E2E_SCREENSHOT_DIR")
	if screenshotDir == "" {
		screenshotDir = ".playwright"
	}
	_ = os.MkdirAll(screenshotDir, 0o755)

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

	// Load pre-authenticated session via storageState (ADR 0010).
	t.Logf("Loading browser session from %s", cfg.sessionFile)
	browserCtx, err := browser.NewContext(playwright.BrowserNewContextOptions{
		StorageStatePath: playwright.String(cfg.sessionFile),
	})
	require.NoError(t, err, "creating browser context with storageState")
	t.Cleanup(func() { _ = browserCtx.Close() })

	page, err := browserCtx.NewPage()
	require.NoError(t, err, "creating Playwright page")

	// Verify the session is valid by navigating to a page that requires auth.
	err = verifyGitHubSession(page, screenshotDir, t.Logf)
	require.NoError(t, err, "verifying GitHub session — session may be expired, re-export it locally")

	// Generate a PAT for API access.
	patNote := fmt.Sprintf("fullsend-e2e-%d", time.Now().Unix())
	t.Logf("Creating PAT: %s", patNote)
	token, err := createPAT(page, patNote, cfg.password, screenshotDir, t.Logf)
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

func TestAdminInstallUninstall(t *testing.T) {
	env := setupE2ETest(t)
	ctx := context.Background()

	// =========================================
	// Phase 1: First install (creates resources)
	// =========================================
	t.Log("=== Phase 1: First Install ===")
	agentCreds, orgCfg, enabledRepos, defaultBranches, enrolledRepoIDs := runFullInstall(t, env)
	verifyInstalled(t, env, orgCfg, enabledRepos, defaultBranches, agentCreds)

	// =========================================
	// Phase 2: Second install (idempotent no-op)
	// =========================================
	t.Log("=== Phase 2: Second Install (idempotent) ===")
	user, err := env.client.GetAuthenticatedUser(ctx)
	require.NoError(t, err)
	allRepos, err := env.client.ListOrgRepos(ctx, testOrg)
	require.NoError(t, err)
	hasPrivate := hasPrivateRepos(allRepos)

	// Second install should reuse existing dispatch token (empty string).
	// Inference provider is nil for idempotent re-install (already provisioned).
	stack := buildTestLayerStack(testOrg, env.client, orgCfg, env.printer, user, hasPrivate, enabledRepos, defaultBranches, agentCreds, "", enrolledRepoIDs, nil)
	err = stack.InstallAll(ctx)
	require.NoError(t, err, "second InstallAll should succeed")
	verifyInstalled(t, env, orgCfg, enabledRepos, defaultBranches, agentCreds)

	// =========================================
	// Phase 3: First uninstall (deletes resources)
	// =========================================
	t.Log("=== Phase 3: First Uninstall ===")
	runUninstall(t, env)
	// Wait for repo deletion to propagate (GitHub returns 409 if checked too soon).
	time.Sleep(5 * time.Second)
	verifyNotInstalled(t, env)

	// =========================================
	// Phase 4: Second uninstall (idempotent no-op)
	// =========================================
	t.Log("=== Phase 4: Second Uninstall (idempotent) ===")
	runUninstallAllowNotFound(t, env)
	time.Sleep(3 * time.Second)
	verifyNotInstalled(t, env)

	t.Log("=== E2E test complete ===")
}

// --- Install/uninstall helpers ---

// runFullInstall executes the full install flow (app setup + layer stack install)
// and returns the agent credentials and org config for verification.
func runFullInstall(t *testing.T, env *e2eEnv) ([]layers.AgentCredentials, *config.OrgConfig, []string, map[string]string, []int64) {
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

	// Build inference provider if vertex key is available (mode 3).
	var inferenceProvider inference.Provider
	var inferenceProviderName string
	if vertexKey := os.Getenv("E2E_HALFSEND_VERTEX_KEY"); vertexKey != "" {
		gcpProjectID := os.Getenv("E2E_GCP_PROJECT_ID")
		if gcpProjectID == "" {
			// Try to extract project_id from the key JSON.
			gcpProjectID = extractProjectID(t, vertexKey)
		}
		inferenceProvider = vertex.New(vertex.Config{
			ProjectID:      gcpProjectID,
			CredentialJSON: vertexKey,
		}, nil)
		inferenceProviderName = "vertex"
		t.Logf("Inference provider: vertex (project: %s)", gcpProjectID)
	} else {
		t.Log("E2E_HALFSEND_VERTEX_KEY not set, skipping inference layer")
	}

	orgCfg := config.NewOrgConfig(repoNames, enabledRepos, defaultRoles, agents, inferenceProviderName)

	user, err := env.client.GetAuthenticatedUser(ctx)
	require.NoError(t, err, "getting authenticated user")

	// Collect repo IDs for enrolled repos (needed by DispatchTokenLayer).
	var enrolledRepoIDs []int64
	for _, repoName := range enabledRepos {
		repo, repoErr := env.client.GetRepo(ctx, testOrg, repoName)
		require.NoError(t, repoErr, "getting repo %s for ID", repoName)
		enrolledRepoIDs = append(enrolledRepoIDs, repo.ID)
	}

	// Install config-repo and workflows layers first so .fullsend repo exists.
	// This mirrors the real CLI which creates the repo before prompting for
	// the dispatch token (so the user can scope the fine-grained PAT to it).
	configLayer := layers.NewConfigRepoLayer(testOrg, env.client, orgCfg, env.printer, hasPrivate)
	err = configLayer.Install(ctx)
	require.NoError(t, err, "pre-installing config-repo layer")
	registerRepoCleanup(t, env.client, testOrg, forge.ConfigRepoName)

	workflowsLayer := layers.NewWorkflowsLayer(testOrg, env.client, env.printer, user)
	err = workflowsLayer.Install(ctx)
	require.NoError(t, err, "pre-installing workflows layer")

	// Create a fine-grained PAT for dispatch via Playwright.
	// This mirrors the real CLI flow: the user creates a fine-grained PAT
	// scoped to .fullsend with actions:write, then pastes it back.
	t.Log("Creating fine-grained dispatch PAT via Playwright...")
	dispatchToken, err := createDispatchPAT(env.page, testOrg, env.cfg.password, env.screenshotDir, t.Logf)
	require.NoError(t, err, "creating dispatch PAT")
	t.Cleanup(func() {
		t.Log("Deleting dispatch PAT...")
		if delErr := deleteDispatchPAT(env.page, testOrg, env.screenshotDir, t.Logf); delErr != nil {
			t.Logf("warning: could not delete dispatch PAT: %v", delErr)
		}
	})

	// Build full layer stack with the dispatch token and install all layers.
	// Config-repo and workflows are idempotent, so re-running them is harmless.
	stack := buildTestLayerStack(testOrg, env.client, orgCfg, env.printer, user, hasPrivate, enabledRepos, defaultBranches, agentCreds, dispatchToken, enrolledRepoIDs, inferenceProvider)

	err = stack.InstallAll(ctx)
	require.NoError(t, err, "installing layers")

	return agentCreds, orgCfg, enabledRepos, defaultBranches, enrolledRepoIDs
}

func runUninstall(t *testing.T, env *e2eEnv) {
	t.Helper()
	emptyCfg := config.NewOrgConfig(nil, nil, nil, nil, "")
	stack := layers.NewStack(
		layers.NewConfigRepoLayer(testOrg, env.client, emptyCfg, env.printer, false),
		layers.NewWorkflowsLayer(testOrg, env.client, env.printer, ""),
		layers.NewSecretsLayer(testOrg, env.client, nil, env.printer),
		layers.NewInferenceLayer(testOrg, env.client, nil, env.printer),
		layers.NewDispatchTokenLayer(testOrg, env.client, "", nil, env.printer),
		layers.NewEnrollmentLayer(testOrg, env.client, nil, nil, env.printer),
	)
	errs := stack.UninstallAll(context.Background())
	assert.Empty(t, errs, "uninstall should complete without errors")
}

// runUninstallAllowNotFound runs uninstall but accepts not-found errors
// (expected when resources are already deleted).
func runUninstallAllowNotFound(t *testing.T, env *e2eEnv) {
	t.Helper()
	emptyCfg := config.NewOrgConfig(nil, nil, nil, nil, "")
	stack := layers.NewStack(
		layers.NewConfigRepoLayer(testOrg, env.client, emptyCfg, env.printer, false),
		layers.NewWorkflowsLayer(testOrg, env.client, env.printer, ""),
		layers.NewSecretsLayer(testOrg, env.client, nil, env.printer),
		layers.NewInferenceLayer(testOrg, env.client, nil, env.printer),
		layers.NewDispatchTokenLayer(testOrg, env.client, "", nil, env.printer),
		layers.NewEnrollmentLayer(testOrg, env.client, nil, nil, env.printer),
	)
	errs := stack.UninstallAll(context.Background())
	for _, e := range errs {
		if !forge.IsNotFound(e) {
			t.Errorf("unexpected uninstall error (not a not-found): %v", e)
		}
	}
}

// --- Verification helpers ---

// verifyInstalled checks that all resources exist and analyze reports installed.
func verifyInstalled(t *testing.T, env *e2eEnv, orgCfg *config.OrgConfig, enabledRepos []string, defaultBranches map[string]string, agentCreds []layers.AgentCredentials) {
	t.Helper()
	ctx := context.Background()

	// .fullsend repo exists.
	repo, err := env.client.GetRepo(ctx, testOrg, forge.ConfigRepoName)
	require.NoError(t, err, ".fullsend repo should exist")
	assert.Equal(t, forge.ConfigRepoName, repo.Name)

	// config.yaml exists and parses.
	cfgData, err := env.client.GetFileContent(ctx, testOrg, forge.ConfigRepoName, "config.yaml")
	require.NoError(t, err, "config.yaml should exist")
	parsedCfg, err := config.ParseOrgConfig(cfgData)
	require.NoError(t, err, "config.yaml should parse")
	assert.Equal(t, "1", parsedCfg.Version)
	assert.Len(t, parsedCfg.Agents, len(defaultRoles))

	// Workflow files exist.
	for _, path := range []string{
		".github/workflows/agent.yaml",
		".github/workflows/repo-onboard.yaml",
		"CODEOWNERS",
	} {
		_, err := env.client.GetFileContent(ctx, testOrg, forge.ConfigRepoName, path)
		assert.NoError(t, err, "%s should exist in .fullsend", path)
	}

	// Secrets and variables exist for each role.
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

	// Inference secrets exist if vertex key was provided.
	if os.Getenv("E2E_HALFSEND_VERTEX_KEY") != "" {
		for _, secretName := range []string{"FULLSEND_GCP_SA_KEY_JSON", "GCP_PROJECT_ID"} {
			exists, secErr := env.client.RepoSecretExists(ctx, testOrg, forge.ConfigRepoName, secretName)
			assert.NoError(t, secErr, "checking inference secret %s", secretName)
			assert.True(t, exists, "inference secret %s should exist", secretName)
		}
	}

	// Dispatch token org secret exists.
	dispatchExists, err := env.client.OrgSecretExists(ctx, testOrg, "FULLSEND_DISPATCH_TOKEN")
	assert.NoError(t, err, "checking dispatch token org secret")
	assert.True(t, dispatchExists, "FULLSEND_DISPATCH_TOKEN org secret should exist")

	// Enrollment PR exists for test-repo.
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

	// Analyze reports installed.
	user, err := env.client.GetAuthenticatedUser(ctx)
	require.NoError(t, err)
	allRepos, err := env.client.ListOrgRepos(ctx, testOrg)
	require.NoError(t, err)
	hasPrivate := hasPrivateRepos(allRepos)

	analyzeStack := buildTestLayerStack(testOrg, env.client, orgCfg, env.printer, user, hasPrivate, enabledRepos, defaultBranches, agentCreds, "", nil, nil)
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

// verifyNotInstalled checks that the config repo is gone and analyze reports
// not-installed for layers with concrete artifacts.
func verifyNotInstalled(t *testing.T, env *e2eEnv) {
	t.Helper()
	ctx := context.Background()

	_, err := env.client.GetRepo(ctx, testOrg, forge.ConfigRepoName)
	assert.True(t, forge.IsNotFound(err), ".fullsend repo should be deleted")

	// Dispatch token org secret should be deleted.
	dispatchExists, err := env.client.OrgSecretExists(ctx, testOrg, "FULLSEND_DISPATCH_TOKEN")
	assert.NoError(t, err, "checking dispatch token after uninstall")
	assert.False(t, dispatchExists, "FULLSEND_DISPATCH_TOKEN org secret should be deleted")

	emptyCfg := config.NewOrgConfig(nil, nil, nil, nil, "")
	stack := layers.NewStack(
		layers.NewConfigRepoLayer(testOrg, env.client, emptyCfg, env.printer, false),
		layers.NewWorkflowsLayer(testOrg, env.client, env.printer, ""),
		layers.NewSecretsLayer(testOrg, env.client, nil, env.printer),
		layers.NewInferenceLayer(testOrg, env.client, nil, env.printer),
		layers.NewDispatchTokenLayer(testOrg, env.client, "", nil, env.printer),
		layers.NewEnrollmentLayer(testOrg, env.client, nil, nil, env.printer),
	)
	reports, err := stack.AnalyzeAll(ctx)
	require.NoError(t, err, "analyzing layers after uninstall")
	for _, report := range reports {
		switch report.Name {
		case "config-repo", "workflows", "dispatch-token":
			assert.Equal(t, layers.StatusNotInstalled, report.Status,
				"layer %s should be not-installed, got %s",
				report.Name, report.Status)
		default:
			// Layers with empty config may report "installed" (nothing to track).
			t.Logf("layer %s status: %s (accepted)", report.Name, report.Status)
		}
	}
}

// --- Utility functions ---

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
	dispatchToken string,
	enrolledRepoIDs []int64,
	inferenceProvider inference.Provider,
) *layers.Stack {
	return layers.NewStack(
		layers.NewConfigRepoLayer(org, client, cfg, printer, hasPrivate),
		layers.NewWorkflowsLayer(org, client, printer, user),
		layers.NewSecretsLayer(org, client, agentCreds, printer),
		layers.NewInferenceLayer(org, client, inferenceProvider, printer),
		layers.NewDispatchTokenLayer(org, client, dispatchToken, enrolledRepoIDs, printer),
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

// extractProjectID attempts to extract project_id from a GCP service account
// key JSON string. Falls back to "unknown" if parsing fails.
func extractProjectID(t *testing.T, keyJSON string) string {
	t.Helper()
	var key struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal([]byte(keyJSON), &key); err != nil {
		t.Logf("warning: could not parse project_id from vertex key: %v", err)
		return "unknown"
	}
	if key.ProjectID == "" {
		t.Log("warning: vertex key has empty project_id")
		return "unknown"
	}
	return key.ProjectID
}
