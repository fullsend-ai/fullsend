//go:build e2e

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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
	agentCreds, orgCfg, enabledRepos, enrolledRepoIDs := runFullInstall(t, env)
	verifyInstalled(t, env, orgCfg, enabledRepos, agentCreds)

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
	stack := buildTestLayerStack(testOrg, env.client, orgCfg, env.printer, user, hasPrivate, enabledRepos, agentCreds, "", enrolledRepoIDs, nil)
	err = stack.InstallAll(ctx)
	require.NoError(t, err, "second InstallAll should succeed")
	verifyInstalled(t, env, orgCfg, enabledRepos, agentCreds)

	// =========================================
	// Phase 2.25: Merge enrollment PR
	// =========================================
	// The enrollment PR must be merged before unenrollment can work (the shim
	// must exist on the default branch for the removal PR to make sense).
	t.Log("=== Phase 2.25: Merge Enrollment PR ===")
	mergeEnrollmentPR(t, env)

	// =========================================
	// Phase 2.5: Triage dispatch smoke test
	// =========================================
	if os.Getenv("E2E_HALFSEND_VERTEX_KEY") != "" {
		t.Log("=== Phase 2.5: Triage Dispatch Smoke Test ===")
		vendorBinaryForE2E(t, env)
		runTriageDispatchSmokeTest(t, env)
	} else {
		t.Log("=== Phase 2.5: Triage Dispatch Smoke Test (SKIPPED — no inference credentials) ===")
	}

	// =========================================
	// Phase 2.75: Unenrollment reconciliation
	// =========================================
	t.Log("=== Phase 2.75: Unenrollment ===")
	runUnenrollmentTest(t, env, orgCfg, agentCreds, enrolledRepoIDs)

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
func runFullInstall(t *testing.T, env *e2eEnv) ([]layers.AgentCredentials, *config.OrgConfig, []string, []int64) {
	t.Helper()
	ctx := context.Background()

	// App setup via manifest flow with Playwright.
	playwrightBrowser := NewPlaywrightBrowserOpener(env.page, t.Logf, env.screenshotDir)
	prompter := AutoPrompter{}
	setup := appsetup.NewSetup(env.client, prompter, playwrightBrowser, env.printer)

	var agentCreds []layers.AgentCredentials
	for _, role := range defaultRoles {
		t.Logf("Setting up app for role: %s", role)
		// Use a per-role timeout so a failed manifest flow doesn't hang
		// until the Go test timeout (which produces an unhelpful panic).
		roleCtx, roleCancel := context.WithTimeout(ctx, 2*time.Minute)
		appCreds, err := setup.Run(roleCtx, testOrg, role)
		roleCancel()
		require.NoError(t, err, "setting up app for role %s", role)

		agentCreds = append(agentCreds, layers.AgentCredentials{
			AgentEntry: config.AgentEntry{
				Role: role,
				Name: appCreds.Name,
				Slug: appCreds.Slug,
			},
			PEM:      appCreds.PEM,
			ClientID: appCreds.ClientID,
		})

		registerAppCleanup(t, env.page, appCreds.Slug, env.screenshotDir)
	}

	// Discover repos and build config.
	allRepos, err := env.client.ListOrgRepos(ctx, testOrg)
	require.NoError(t, err, "listing org repos")

	repoNames := repoNameList(allRepos)
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
		gcpRegion := os.Getenv("E2E_GCP_REGION")
		if gcpRegion == "" {
			gcpRegion = "global"
		}
		inferenceProvider = vertex.New(vertex.Config{
			ProjectID:      gcpProjectID,
			Region:         gcpRegion,
			CredentialJSON: []byte(vertexKey),
		}, nil)
		// Region is stored as a variable, not a secret.
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
	stack := buildTestLayerStack(testOrg, env.client, orgCfg, env.printer, user, hasPrivate, enabledRepos, agentCreds, dispatchToken, enrolledRepoIDs, inferenceProvider)

	err = stack.InstallAll(ctx)
	require.NoError(t, err, "installing layers")

	return agentCreds, orgCfg, enabledRepos, enrolledRepoIDs
}

func runUninstall(t *testing.T, env *e2eEnv) {
	t.Helper()
	emptyCfg := config.NewOrgConfig(nil, nil, nil, nil, "")
	stack := layers.NewStack(
		layers.NewConfigRepoLayer(testOrg, env.client, emptyCfg, env.printer, false),
		layers.NewWorkflowsLayer(testOrg, env.client, env.printer, ""),
		layers.NewSecretsLayer(testOrg, env.client, nil, env.printer),
		layers.NewInferenceLayer(testOrg, env.client, nil, env.printer),
		layers.NewDispatchTokenLayer(testOrg, env.client, "", nil, env.printer, nil),
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
		layers.NewDispatchTokenLayer(testOrg, env.client, "", nil, env.printer, nil),
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
func verifyInstalled(t *testing.T, env *e2eEnv, orgCfg *config.OrgConfig, enabledRepos []string, agentCreds []layers.AgentCredentials) {
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

	// Agent runtime files exist (from scaffold).
	for _, path := range []string{
		".github/workflows/triage.yml",
		".github/workflows/code.yml",
		".github/workflows/review.yml",
		".github/workflows/repo-maintenance.yml",
		".github/actions/fullsend/action.yml",
		".github/scripts/setup-agent-env.sh",
		"agents/triage.md",
		"agents/code.md",
		"harness/triage.yaml",
		"harness/code.yaml",
		"policies/triage.yaml",
		"policies/code.yaml",
		"env/triage.env",
		"env/code-agent.env",
		"env/gcp-vertex.env",
		"scripts/scan-secrets",
		"scripts/pre-code.sh",
		"scripts/post-code.sh",
		"scripts/post-triage.sh",
		"scripts/reconcile-repos.sh",
		"skills/code-implementation/SKILL.md",
		"skills/fix-review/SKILL.md",
		".github/workflows/fix.yml",
		"agents/fix.md",
		"harness/fix.yaml",
		"policies/fix.yaml",
		"env/fix-agent.env",
		"schemas/fix-result.schema.json",
		"scripts/pre-fix.sh",
		"scripts/post-fix.sh",
		"scripts/process-fix-result.py",
		"templates/shim-workflow.yaml",
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

		varName := fmt.Sprintf("FULLSEND_%s_CLIENT_ID", strings.ToUpper(role))
		exists, err = env.client.RepoVariableExists(ctx, testOrg, forge.ConfigRepoName, varName)
		assert.NoError(t, err, "checking variable %s", varName)
		assert.True(t, exists, "variable %s should exist", varName)
	}

	// Inference secrets exist if vertex key was provided.
	if os.Getenv("E2E_HALFSEND_VERTEX_KEY") != "" {
		for _, secretName := range []string{"FULLSEND_GCP_SA_KEY_JSON", "FULLSEND_GCP_PROJECT_ID"} {
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

	analyzeStack := buildTestLayerStack(testOrg, env.client, orgCfg, env.printer, user, hasPrivate, enabledRepos, agentCreds, "", nil, nil)
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
		layers.NewDispatchTokenLayer(testOrg, env.client, "", nil, env.printer, nil),
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

// vendorBinaryForE2E builds the fullsend binary for the current platform
// (which is linux/amd64 in CI) and uploads it to the config repo so the
// triage workflow uses the code under test rather than a released version.
func vendorBinaryForE2E(t *testing.T, env *e2eEnv) {
	t.Helper()

	tmpBinary, err := os.CreateTemp("", "fullsend-e2e-*")
	require.NoError(t, err)
	tmpBinary.Close()
	t.Cleanup(func() { os.Remove(tmpBinary.Name()) })

	// Find the module root (go test runs with cwd set to the test package dir).
	modRoot, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	require.NoError(t, err, "finding module root")

	t.Log("Building fullsend binary for vendoring...")
	cmd := exec.Command("go", "build", "-o", tmpBinary.Name(), "./cmd/fullsend/")
	cmd.Dir = strings.TrimSpace(string(modRoot))
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building fullsend binary: %s", string(out))

	t.Log("Uploading vendored binary to .fullsend/bin/fullsend...")
	err = layers.VendorBinary(context.Background(), env.client, testOrg, tmpBinary.Name())
	require.NoError(t, err, "vendoring binary")
	t.Log("Vendored binary uploaded successfully")
}

// mergeEnrollmentPR finds and merges the enrollment PR for test-repo so the
// shim workflow is active on the default branch. This must run before both
// the triage smoke test and the unenrollment test.
func mergeEnrollmentPR(t *testing.T, env *e2eEnv) {
	t.Helper()
	ctx := context.Background()

	prs, err := env.client.ListRepoPullRequests(ctx, testOrg, testRepo)
	require.NoError(t, err, "listing PRs for %s", testRepo)

	var enrollmentPR *forge.ChangeProposal
	for _, pr := range prs {
		if strings.Contains(pr.Title, "fullsend") {
			cp := pr
			enrollmentPR = &cp
			break
		}
	}
	require.NotNil(t, enrollmentPR, "enrollment PR should exist for %s", testRepo)

	t.Logf("Merging enrollment PR #%d: %s", enrollmentPR.Number, enrollmentPR.URL)
	err = env.client.MergeChangeProposal(ctx, testOrg, testRepo, enrollmentPR.Number)
	require.NoError(t, err, "merging enrollment PR")

	// Wait for GitHub to process the merge.
	time.Sleep(5 * time.Second)
	t.Log("Enrollment PR merged")
}

func runTriageDispatchSmokeTest(t *testing.T, env *e2eEnv) {
	t.Helper()
	ctx := context.Background()

	// File a test issue to trigger the shim workflow.
	issueTitle := fmt.Sprintf("e2e-triage-test-%s", env.runID)
	issueBody := `## Bug Report

**What happened:**
The application crashes with a segmentation fault when saving a file larger than 64KB
that contains UTF-8 multibyte characters (e.g., emoji or CJK characters).

**Expected behavior:**
The file should save successfully regardless of size or character encoding.

**Steps to reproduce:**
1. Open the application (v2.3.1)
2. Create a new document
3. Paste approximately 70KB of text containing emoji characters
4. Click File > Save
5. Application crashes immediately

**Environment:**
- OS: Ubuntu 22.04 LTS
- Application version: 2.3.1 (installed via apt)
- RAM: 16GB

**Error output:**
` + "```" + `
Segmentation fault (core dumped)
` + "```" + `

**Additional context:**
This started happening after the v2.3.0 -> v2.3.1 upgrade. Files under 64KB save fine.
Files over 64KB save fine if they contain only ASCII characters.`
	issue, err := env.client.CreateIssue(ctx, testOrg, testRepo, issueTitle, issueBody)
	require.NoError(t, err, "creating test issue")
	t.Logf("Created test issue #%d: %s", issue.Number, issue.URL)
	t.Cleanup(func() {
		t.Log("Closing test issue...")
		if closeErr := env.client.CloseIssue(ctx, testOrg, testRepo, issue.Number); closeErr != nil {
			t.Logf("warning: could not close test issue: %v", closeErr)
		}
	})

	// Wait for the triage workflow to be dispatched in .fullsend.
	// The shim fires on issues:opened and dispatches to triage.yml.
	// The shim typically fires within ~5s of the issue being created,
	// so 12 attempts at 5s intervals (60s total) is generous.
	// Filter by CreatedAt to avoid false positives from previous runs.
	issueCreatedAt := time.Now()
	t.Log("Waiting for triage workflow to be dispatched...")
	var triageRun *forge.WorkflowRun
	for attempt := 0; attempt < 12; attempt++ {
		time.Sleep(5 * time.Second)
		runs, listErr := env.client.ListWorkflowRuns(ctx, testOrg, forge.ConfigRepoName, "triage.yml")
		if listErr != nil {
			t.Logf("Attempt %d: error listing workflow runs: %v", attempt+1, listErr)
			continue
		}
		for _, run := range runs {
			runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
			if parseErr != nil {
				t.Logf("Attempt %d: run %d has unparseable CreatedAt %q: %v", attempt+1, run.ID, run.CreatedAt, parseErr)
				continue
			}
			if runTime.Before(issueCreatedAt) {
				t.Logf("Attempt %d: run %d created at %s is from before our issue, skipping", attempt+1, run.ID, run.CreatedAt)
				continue
			}
			t.Logf("Attempt %d: found run %d (status: %s, conclusion: %s, created: %s)", attempt+1, run.ID, run.Status, run.Conclusion, run.CreatedAt)
			r := run // avoid loop variable capture
			triageRun = &r
			break
		}
		if triageRun != nil {
			break
		}
		t.Logf("Attempt %d: no triage workflow runs found yet", attempt+1)
	}
	require.NotNil(t, triageRun, "triage workflow should have been dispatched in .fullsend repo")

	// Wait for the workflow run to complete (up to 12 minutes: 10-minute agent
	// timeout + sandbox setup overhead).
	t.Logf("Waiting for triage workflow run %d to complete...", triageRun.ID)
	var finalRun *forge.WorkflowRun
	deadline := time.Now().Add(12 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(15 * time.Second)
		run, getErr := env.client.GetWorkflowRun(ctx, testOrg, forge.ConfigRepoName, triageRun.ID)
		if getErr != nil {
			t.Logf("Error polling workflow run: %v", getErr)
			continue
		}
		t.Logf("Run %d: status=%s conclusion=%s", run.ID, run.Status, run.Conclusion)
		if run.Status == "completed" {
			finalRun = run
			break
		}
	}
	require.NotNil(t, finalRun, "triage workflow run should have completed within deadline")

	// If the run failed, fetch logs for debugging.
	if finalRun.Conclusion != "success" {
		logs, logErr := env.client.GetWorkflowRunLogs(ctx, testOrg, forge.ConfigRepoName, finalRun.ID)
		if logErr != nil {
			t.Logf("Could not fetch run logs: %v", logErr)
		} else {
			t.Logf("Workflow run logs:\n%s", logs)
		}
		t.Fatalf("Triage workflow run %d concluded with %q, expected success", finalRun.ID, finalRun.Conclusion)
	}

	// Verify the triage agent posted a comment on the issue.
	t.Log("Verifying triage agent posted a comment...")
	comments, err := env.client.ListIssueComments(ctx, testOrg, testRepo, issue.Number)
	require.NoError(t, err, "listing issue comments")
	assert.NotEmpty(t, comments, "triage agent should have posted at least one comment on the issue")

	if len(comments) > 0 {
		lastComment := comments[len(comments)-1]
		t.Logf("Triage comment by %s (first 200 chars): %.200s", lastComment.Author, lastComment.Body)

		// The comment should be from the bot (ends with [bot]).
		assert.True(t, strings.HasSuffix(lastComment.Author, "[bot]"),
			"triage comment should be from a bot, got author %q", lastComment.Author)
	}

	// Verify labels: either needs-info (insufficient) or ready-to-code (sufficient).
	t.Log("Verifying triage labels...")
	labelURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d/labels", testOrg, testRepo, issue.Number)
	labelReq, err := http.NewRequestWithContext(ctx, http.MethodGet, labelURL, nil)
	require.NoError(t, err)
	labelReq.Header.Set("Authorization", "Bearer "+env.token)
	labelReq.Header.Set("Accept", "application/vnd.github+json")
	labelResp, err := http.DefaultClient.Do(labelReq)
	require.NoError(t, err)
	defer labelResp.Body.Close()

	var labels []struct {
		Name string `json:"name"`
	}
	err = json.NewDecoder(labelResp.Body).Decode(&labels)
	require.NoError(t, err, "decoding labels response")

	labelNames := make([]string, len(labels))
	for i, l := range labels {
		labelNames[i] = l.Name
	}
	t.Logf("Issue labels after triage: %v", labelNames)

	hasTriageLabel := false
	for _, name := range labelNames {
		if name == "needs-info" || name == "ready-to-code" || name == "duplicate" {
			hasTriageLabel = true
			break
		}
	}
	assert.True(t, hasTriageLabel,
		"issue should have a triage label (needs-info, ready-to-code, or duplicate), got: %v", labelNames)
}

// runUnenrollmentTest disables test-repo in config.yaml, runs install to
// dispatch reconciliation, verifies the removal PR, merges it, and confirms
// the shim is gone from the default branch.
func runUnenrollmentTest(t *testing.T, env *e2eEnv, orgCfg *config.OrgConfig, agentCreds []layers.AgentCredentials, enrolledRepoIDs []int64) {
	t.Helper()
	ctx := context.Background()

	// Update config.yaml to disable test-repo.
	orgCfg.Repos[testRepo] = config.RepoConfig{Enabled: false}
	cfgData, err := orgCfg.Marshal()
	require.NoError(t, err, "marshaling updated config")

	err = env.client.CreateOrUpdateFile(ctx, testOrg, forge.ConfigRepoName, "config.yaml", "chore: disable test-repo for unenrollment test", cfgData)
	require.NoError(t, err, "updating config.yaml with disabled repo")
	t.Logf("Set %s to enabled: false in config.yaml", testRepo)

	// Wait for GitHub to process the push.
	time.Sleep(5 * time.Second)

	// Run install with no enabled repos and test-repo as disabled.
	user, err := env.client.GetAuthenticatedUser(ctx)
	require.NoError(t, err)
	allRepos, err := env.client.ListOrgRepos(ctx, testOrg)
	require.NoError(t, err)
	hasPrivate := hasPrivateRepos(allRepos)

	stack := buildTestLayerStack(testOrg, env.client, orgCfg, env.printer, user, hasPrivate, nil, agentCreds, "", enrolledRepoIDs, nil)
	err = stack.InstallAll(ctx)
	require.NoError(t, err, "install with disabled repo should succeed")

	// Verify removal PR exists.
	prs, err := env.client.ListRepoPullRequests(ctx, testOrg, testRepo)
	require.NoError(t, err, "listing PRs for %s", testRepo)

	var removalPR *forge.ChangeProposal
	for _, pr := range prs {
		if pr.Title == "chore: disconnect from fullsend agent pipeline" {
			cp := pr
			removalPR = &cp
			break
		}
	}
	require.NotNil(t, removalPR, "removal PR should exist for %s", testRepo)
	t.Logf("Found removal PR #%d: %s", removalPR.Number, removalPR.URL)

	// Merge the removal PR.
	err = env.client.MergeChangeProposal(ctx, testOrg, testRepo, removalPR.Number)
	require.NoError(t, err, "merging removal PR")
	t.Logf("Merged removal PR #%d", removalPR.Number)

	// Wait for merge to propagate.
	time.Sleep(5 * time.Second)

	// Verify shim no longer exists on the default branch.
	_, err = env.client.GetFileContent(ctx, testOrg, testRepo, ".github/workflows/fullsend.yaml")
	assert.True(t, forge.IsNotFound(err), "shim workflow should be removed from %s after merging removal PR", testRepo)
	t.Logf("Verified shim is gone from %s", testRepo)

	// Re-enable the repo in config for subsequent test phases.
	orgCfg.Repos[testRepo] = config.RepoConfig{Enabled: true}
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
		layers.NewDispatchTokenLayer(org, client, dispatchToken, enrolledRepoIDs, printer, nil),
		layers.NewEnrollmentLayer(org, client, enabledRepos, cfg.DisabledRepos(), printer),
	)
}

func repoNameList(repos []forge.Repository) []string {
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}
	return names
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
