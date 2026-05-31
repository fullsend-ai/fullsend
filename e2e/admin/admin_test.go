//go:build e2e

package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

// e2eEnv holds the shared state for an e2e test run.
type e2eEnv struct {
	cfg           envConfig
	org           string // the org acquired from the pool
	page          playwright.Page
	client        *gh.LiveClient
	token         string
	runID         string
	screenshotDir string
	binary        string
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

	// Build CLI binary early so we fail fast on compilation errors.
	binary := buildCLIBinary(t)

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
	token, err := createPAT(page, patNote, cfg.password, cfg.totpSecret, screenshotDir, t.Logf)
	require.NoError(t, err, "creating PAT")
	t.Cleanup(func() {
		t.Log("Deleting PAT...")
		if delErr := deletePAT(page, patNote, t.Logf); delErr != nil {
			t.Logf("warning: could not delete PAT: %v", delErr)
		}
	})

	// --- GitHub client ---
	client := newLiveClient(token)

	// Acquire an org from the pool.
	runID := uuid.New().String()
	t.Logf("E2E run ID: %s", runID)

	org, err := acquireOrg(context.Background(), client, token, runID, orgPool, cfg.lockTimeout, t.Logf)
	require.NoError(t, err, "acquiring org from pool")
	t.Logf("Acquired org: %s", org)
	t.Cleanup(func() {
		releaseLock(context.Background(), client, org, runID, t)
	})

	// Teardown-first cleanup.
	cleanupStaleResources(context.Background(), client, token, org, t)

	return &e2eEnv{
		cfg:           cfg,
		org:           org,
		page:          page,
		client:        client,
		token:         token,
		runID:         runID,
		screenshotDir: screenshotDir,
		binary:        binary,
	}
}

func TestAdminInstallUninstall(t *testing.T) {
	env := setupE2ETest(t)
	ctx := context.Background()

	// Phase 1: Install via CLI subprocess.
	t.Log("=== Phase 1: Install ===")
	installArgs := []string{
		"admin", "install", env.org,
		"--skip-app-setup",
		"--skip-mint-check",
		"--mint-url", env.cfg.mintURL,
		"--app-set", e2eAppSet,
		"--enroll-all",
		"--vendor-fullsend-binary",
	}
	if env.cfg.gcpProjectID != "" {
		installArgs = append(installArgs, "--inference-project", env.cfg.gcpProjectID)
	}
	runCLI(t, env.binary, env.token, installArgs...)

	// Verify install artifacts.
	_, err := env.client.GetRepo(ctx, env.org, forge.ConfigRepoName)
	require.NoError(t, err, ".fullsend repo should exist")
	mintURLExists, err := env.client.OrgVariableExists(ctx, env.org, "FULLSEND_MINT_URL")
	require.NoError(t, err)
	require.True(t, mintURLExists, "FULLSEND_MINT_URL org variable should exist")
	cfgData, err := env.client.GetFileContent(ctx, env.org, forge.ConfigRepoName, "config.yaml")
	require.NoError(t, err, "config.yaml should exist")
	parsedCfg, err := config.ParseOrgConfig(cfgData)
	require.NoError(t, err, "config.yaml should parse")
	require.Len(t, parsedCfg.Defaults.Roles, len(defaultRoles), "should have %d roles", len(defaultRoles))
	analyzeOutput := runCLI(t, env.binary, env.token, "admin", "analyze", env.org)
	t.Logf("Analyze output:\n%s", analyzeOutput)

	// Agent runtime files exist (from scaffold).
	// ADR 35: only non-layered, non-upstream-only files are installed.
	// Layered dirs (agents/, skills/, schemas/, harness/, plugins/, policies/,
	// scripts/, env/) and upstream-only dirs (.github/actions/, .github/scripts/) are
	// provided at runtime via sparse checkout in reusable workflows.
	for _, path := range []string{
		".github/workflows/dispatch.yml",
		".github/workflows/repo-maintenance.yml",
		".github/workflows/prioritize.yml",
		".github/workflows/prioritize-scheduler.yml",
		"customized/agents/.gitkeep",
		"customized/skills/.gitkeep",
		"customized/schemas/.gitkeep",
		"customized/harness/.gitkeep",
		"customized/plugins/.gitkeep",
		"customized/policies/.gitkeep",
		"customized/scripts/.gitkeep",
		"customized/env/.gitkeep",
		"templates/shim-workflow-call.yaml",
		"CODEOWNERS",
	} {
		_, err := env.client.GetFileContent(ctx, env.org, forge.ConfigRepoName, path)
		assert.NoError(t, err, "%s should exist in .fullsend", path)
	}

	// Register .fullsend cleanup (in case later phases fail).
	registerRepoCleanup(t, env.client, env.org, forge.ConfigRepoName)

	// Phase 2: Merge enrollment PR.
	t.Log("=== Phase 2: Merge Enrollment PR ===")
	mergeEnrollmentPR(t, env)

	// Phase 3: Triage dispatch smoke test.
	t.Log("=== Phase 3: Triage Dispatch Smoke Test ===")
	runTriageDispatchSmokeTest(t, env)

	// Phase 4: Unenrollment reconciliation.
	t.Log("=== Phase 4: Unenrollment ===")
	runUnenrollmentTest(t, env)

	// Phase 5: Uninstall via CLI subprocess.
	t.Log("=== Phase 5: Uninstall ===")
	runCLI(t, env.binary, env.token,
		"admin", "uninstall", env.org,
		"--yolo",
		"--app-set", e2eAppSet,
	)

	time.Sleep(5 * time.Second)
	_, err = env.client.GetRepo(ctx, env.org, forge.ConfigRepoName)
	require.True(t, forge.IsNotFound(err), ".fullsend repo should be deleted")
	mintURLExists, err = env.client.OrgVariableExists(ctx, env.org, "FULLSEND_MINT_URL")
	require.NoError(t, err)
	require.False(t, mintURLExists, "FULLSEND_MINT_URL should be deleted")

	t.Log("=== E2E test complete ===")
}

// mergeEnrollmentPR finds and merges the enrollment PR for test-repo so the
// shim workflow is active on the default branch.
func mergeEnrollmentPR(t *testing.T, env *e2eEnv) {
	t.Helper()
	ctx := context.Background()

	prs, err := env.client.ListRepoPullRequests(ctx, env.org, testRepo)
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
	err = env.client.MergeChangeProposal(ctx, env.org, testRepo, enrollmentPR.Number)
	require.NoError(t, err, "merging enrollment PR")

	time.Sleep(5 * time.Second)
	t.Log("Enrollment PR merged")
}

func runTriageDispatchSmokeTest(t *testing.T, env *e2eEnv) {
	t.Helper()
	ctx := context.Background()

	// Capture a lower-bound timestamp *before* creating the issue.
	// The shim/dispatch workflows can start very quickly, and we only want to
	// filter out runs from earlier test phases.
	issueCreatedAt := time.Now()

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
	issue, err := env.client.CreateIssue(ctx, env.org, testRepo, issueTitle, issueBody)
	require.NoError(t, err, "creating test issue")
	t.Logf("Created test issue #%d: %s", issue.Number, issue.URL)
	t.Cleanup(func() {
		t.Log("Closing test issue...")
		if closeErr := env.client.CloseIssue(ctx, env.org, testRepo, issue.Number); closeErr != nil {
			t.Logf("warning: could not close test issue: %v", closeErr)
		}
	})

	// Wait for the enrolled-repo shim (fullsend.yaml). Cross-repo workflow_call
	// runs appear on the caller repo, not as separate dispatch.yml runs in .fullsend.
	// Chain: fullsend.yaml → .fullsend/dispatch.yml → reusable-triage (sync).
	t.Log("Waiting for fullsend shim workflow to run...")
	var shimRun *forge.WorkflowRun
	for attempt := 0; attempt < 12; attempt++ {
		time.Sleep(5 * time.Second)
		runs, listErr := env.client.ListWorkflowRuns(ctx, env.org, testRepo, "fullsend.yaml")
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
			shimRun = &r
			break
		}
		if shimRun != nil {
			break
		}
		t.Logf("Attempt %d: no fullsend shim workflow runs found yet", attempt+1)
	}
	require.NotNil(t, shimRun, "fullsend shim workflow should have run in enrolled repo")

	// Wait for the workflow run to complete (up to 12 minutes: 10-minute agent
	// timeout + sandbox setup overhead).
	t.Logf("Waiting for fullsend shim workflow run %d to complete...", shimRun.ID)
	var finalRun *forge.WorkflowRun
	deadline := time.Now().Add(12 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(15 * time.Second)
		run, getErr := env.client.GetWorkflowRun(ctx, env.org, testRepo, shimRun.ID)
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
	require.NotNil(t, finalRun, "fullsend shim workflow run should have completed within deadline")

	// If the run failed, save logs and artifacts for debugging.
	if finalRun.Conclusion != "success" {
		runURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", env.org, testRepo, finalRun.ID)
		fmt.Fprintf(os.Stderr, "::notice::Fullsend shim run %d failed (conclusion: %s). Downloading debug artifacts. Run URL: %s\n", finalRun.ID, finalRun.Conclusion, runURL)

		debugDir := filepath.Join(env.screenshotDir, fmt.Sprintf("fullsend-run-%d", finalRun.ID))
		_ = os.MkdirAll(debugDir, 0o755)

		// Save workflow logs.
		logs, logErr := env.client.GetWorkflowRunLogs(ctx, env.org, testRepo, finalRun.ID)
		if logErr != nil {
			t.Logf("Could not fetch run logs: %v", logErr)
		} else {
			logPath := filepath.Join(debugDir, "workflow-logs.txt")
			if writeErr := os.WriteFile(logPath, []byte(logs), 0o644); writeErr != nil {
				t.Logf("Could not write logs to %s: %v", logPath, writeErr)
			} else {
				fmt.Fprintf(os.Stderr, "::notice file=%s::Fullsend shim run %d workflow logs saved\n", logPath, finalRun.ID)
			}
			t.Logf("Workflow run logs:\n%s", logs)
		}

		// Download run artifacts (transcripts, etc).
		downloadRunArtifacts(ctx, env.token, env.org, testRepo, finalRun.ID, debugDir, t)

		t.Fatalf("Fullsend shim workflow run %d concluded with %q, expected success. Debug artifacts saved to %s", finalRun.ID, finalRun.Conclusion, debugDir)
	}

	// Verify the triage agent posted a comment on the issue.
	t.Log("Verifying triage agent posted a comment...")
	comments, err := env.client.ListIssueComments(ctx, env.org, testRepo, issue.Number)
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
	labelURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d/labels", env.org, testRepo, issue.Number)
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
		if name == "needs-info" || name == "ready-to-code" || name == "duplicate" || name == "blocked" {
			hasTriageLabel = true
			break
		}
	}
	assert.True(t, hasTriageLabel,
		"issue should have a triage label (needs-info, ready-to-code, duplicate, or blocked), got: %v", labelNames)
}

// downloadRunArtifacts fetches all artifacts from a workflow run and extracts
// them into destDir. Each artifact is extracted into a subdirectory named after
// the artifact. This captures transcripts, screenshots, and other debug data
// that the agent run uploads.
func downloadRunArtifacts(ctx context.Context, token, org, repo string, runID int, destDir string, t *testing.T) {
	t.Helper()

	// List artifacts for the run.
	listURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runs/%d/artifacts", org, repo, runID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		t.Logf("[artifacts] Could not create request: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("[artifacts] Could not list artifacts: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Logf("[artifacts] List artifacts returned HTTP %d", resp.StatusCode)
		return
	}

	var result struct {
		Artifacts []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"artifacts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Logf("[artifacts] Could not decode artifact list: %v", err)
		return
	}

	if len(result.Artifacts) == 0 {
		t.Log("[artifacts] No artifacts found for this run")
		return
	}

	t.Logf("[artifacts] Found %d artifact(s), downloading...", len(result.Artifacts))
	for _, art := range result.Artifacts {
		downloadAndExtractArtifact(ctx, token, org, repo, art.ID, art.Name, destDir, t)
	}
}

// downloadAndExtractArtifact downloads a single artifact zip and extracts it.
func downloadAndExtractArtifact(ctx context.Context, token, org, repo string, artifactID int, name, destDir string, t *testing.T) {
	t.Helper()

	dlURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/artifacts/%d/zip", org, repo, artifactID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		t.Logf("[artifacts] Could not create download request for %s: %v", name, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("[artifacts] Could not download %s: %v", name, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Logf("[artifacts] Download %s returned HTTP %d", name, resp.StatusCode)
		return
	}

	// Read the zip into memory (artifacts are typically small).
	zipData, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20)) // 50 MB limit
	if err != nil {
		t.Logf("[artifacts] Could not read %s: %v", name, err)
		return
	}

	artDir := filepath.Join(destDir, name)
	_ = os.MkdirAll(artDir, 0o755)

	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		// Not a zip — save raw content.
		rawPath := filepath.Join(destDir, name+".bin")
		_ = os.WriteFile(rawPath, zipData, 0o644)
		t.Logf("[artifacts] %s is not a zip, saved raw to %s", name, rawPath)
		return
	}

	for _, f := range zr.File {
		outPath := filepath.Join(artDir, f.Name)

		// Prevent zip slip.
		if !strings.HasPrefix(filepath.Clean(outPath), filepath.Clean(artDir)+string(os.PathSeparator)) {
			t.Logf("[artifacts] Skipping suspicious path in %s: %s", name, f.Name)
			continue
		}

		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(outPath, 0o755)
			continue
		}

		_ = os.MkdirAll(filepath.Dir(outPath), 0o755)
		rc, err := f.Open()
		if err != nil {
			t.Logf("[artifacts] Could not open %s/%s: %v", name, f.Name, err)
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Logf("[artifacts] Could not read %s/%s: %v", name, f.Name, err)
			continue
		}
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			t.Logf("[artifacts] Could not write %s: %v", outPath, err)
			continue
		}
	}

	fmt.Fprintf(os.Stderr, "::notice::Extracted artifact %q (%d files) to %s\n", name, len(zr.File), artDir)
	t.Logf("[artifacts] Extracted %s (%d files) to %s", name, len(zr.File), artDir)
}

// runUnenrollmentTest disables test-repo in config.yaml, runs install to
// dispatch reconciliation, verifies the removal PR, merges it, and confirms
// the shim is gone from the default branch.
func runUnenrollmentTest(t *testing.T, env *e2eEnv) {
	t.Helper()
	ctx := context.Background()

	// Disable the test repo via CLI (updates config.yaml). The push to
	// config.yaml on main triggers repo-maintenance, which creates the
	// removal PR.
	runCLI(t, env.binary, env.token,
		"admin", "disable", "repos", env.org, testRepo, "--yolo")

	// Wait for the removal PR to be created by repo-maintenance.
	t.Log("Waiting for removal PR...")
	var removalPR *forge.ChangeProposal
	for attempt := 0; attempt < 24; attempt++ {
		time.Sleep(5 * time.Second)
		prs, err := env.client.ListRepoPullRequests(ctx, env.org, testRepo)
		if err != nil {
			t.Logf("Attempt %d: error listing PRs: %v", attempt+1, err)
			continue
		}
		for _, pr := range prs {
			if pr.Title == "chore: disconnect from fullsend agent pipeline" {
				cp := pr
				removalPR = &cp
				break
			}
		}
		if removalPR != nil {
			break
		}
		t.Logf("Attempt %d: removal PR not yet created", attempt+1)
	}
	require.NotNil(t, removalPR, "removal PR should exist for %s", testRepo)
	t.Logf("Found removal PR #%d: %s", removalPR.Number, removalPR.URL)
	err := env.client.MergeChangeProposal(ctx, env.org, testRepo, removalPR.Number)
	require.NoError(t, err, "merging removal PR")
	time.Sleep(5 * time.Second)
	_, err = env.client.GetFileContent(ctx, env.org, testRepo, ".github/workflows/fullsend.yaml")
	require.True(t, forge.IsNotFound(err), "shim should be removed from %s after unenrollment", testRepo)
	t.Log("Verified shim is gone")
}
