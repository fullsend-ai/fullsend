# Admin E2E Tests Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** End-to-end tests that exercise the full `fullsend admin install` and `fullsend admin uninstall` flows against a real GitHub organization (`halfsend`), including browser-automated GitHub App creation via Playwright.

**Architecture:** The e2e test injects Playwright-backed implementations of the existing `appsetup.BrowserOpener` and `appsetup.Prompter` interfaces. It reproduces the wiring logic from `admin.go` to call public APIs (`appsetup.Setup.Run()`, `layers.Stack.InstallAll()`, etc.) directly. A distributed lock via a GitHub repo prevents concurrent test runs from colliding.

**Tech Stack:** Go 1.25, `playwright-go`, `testify`, GitHub REST API, `//go:build e2e` tag

---

## File Structure

```
e2e/
  admin/
    testutil.go          # Constants, env var loading, client setup, getRepoCreatedAt()
    lock.go              # acquireLock(), releaseLock() — distributed lock via e2e-lock repo
    lock_test.go         # Unit tests for lock logic (using forge.FakeClient)
    prompter.go          # AutoPrompter implementing appsetup.Prompter
    browser.go           # PlaywrightBrowserOpener implementing appsetup.BrowserOpener
    cleanup.go           # cleanupStaleResources(), deleteAppViaPlaywright()
    admin_test.go        # TestAdminInstallUninstall — the main e2e test
    cmd/
      setup-auth/
        main.go          # One-time interactive auth state bootstrap tool
.github/
  workflows/
    e2e.yml              # CI workflow for e2e tests
Makefile                 # Add e2e-test target
```

All files under `e2e/admin/` (except `cmd/setup-auth/main.go`) use `//go:build e2e`.
`cmd/setup-auth/main.go` has no build tag (it's a standalone tool).

---

### Task 1: Add playwright-go dependency

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add playwright-go and uuid dependency**

```bash
go get github.com/playwright-community/playwright-go
go get github.com/google/uuid
```

- [ ] **Step 2: Tidy modules**

```bash
go mod tidy
```

- [ ] **Step 3: Verify existing tests still pass**

Run: `go test -race ./...`
Expected: All existing tests pass (playwright-go is not imported by non-e2e code).

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add playwright-go and uuid dependencies for e2e tests

Assisted-by: OpenCode claude-opus-4-6@default"
```

---

### Task 2: Test utilities — constants, env loading, API helpers

**Files:**
- Create: `e2e/admin/testutil.go`

- [ ] **Step 1: Create the e2e/admin directory**

```bash
mkdir -p e2e/admin
```

- [ ] **Step 2: Write testutil.go**

```go
//go:build e2e

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

const (
	// testOrg is the dedicated GitHub org for e2e tests.
	testOrg = "halfsend"

	// testRepo is a pre-existing repo in the test org for enrollment testing.
	testRepo = "test-repo"

	// lockRepo is the name of the distributed lock repo.
	lockRepo = "e2e-lock"

	// defaultLockTimeout is how long to wait for the lock before giving up.
	defaultLockTimeout = 30 * time.Minute

	// lockPollInterval is how often to poll while waiting for the lock.
	lockPollInterval = 1 * time.Minute

	// freshLockThreshold is the age below which a lock is considered
	// "just acquired" and we reset the wait timer.
	freshLockThreshold = 2 * time.Minute
)

// defaultRoles is the standard set of agent roles.
var defaultRoles = []string{"fullsend", "triage", "coder", "review"}

// envConfig holds required environment configuration.
type envConfig struct {
	token          string
	browserStateDir string
	lockTimeout    time.Duration
}

// loadEnvConfig reads and validates required env vars. Calls t.Skip if
// E2E_GITHUB_TOKEN is not set (allows running `go test -tags e2e` without
// credentials to check compilation).
func loadEnvConfig(t *testing.T) envConfig {
	t.Helper()

	token := os.Getenv("E2E_GITHUB_TOKEN")
	if token == "" {
		t.Skip("E2E_GITHUB_TOKEN not set, skipping e2e test")
	}

	browserStateDir := os.Getenv("E2E_BROWSER_STATE_DIR")
	if browserStateDir == "" {
		t.Skip("E2E_BROWSER_STATE_DIR not set, skipping e2e test")
	}

	lockTimeout := defaultLockTimeout
	if v := os.Getenv("E2E_LOCK_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("invalid E2E_LOCK_TIMEOUT %q: %v", v, err)
		}
		lockTimeout = d
	}

	return envConfig{
		token:          token,
		browserStateDir: browserStateDir,
		lockTimeout:    lockTimeout,
	}
}

// newLiveClient creates a GitHub API client from the token.
func newLiveClient(token string) *gh.LiveClient {
	return gh.New(token)
}

// getRepoCreatedAt fetches a repo's created_at timestamp directly from the
// GitHub REST API. This is intentionally NOT added to forge.Client since it's
// only needed for e2e lock management.
func getRepoCreatedAt(ctx context.Context, token, org, repo string) (time.Time, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", org, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return time.Time{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("fetching repo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return time.Time{}, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return time.Time{}, fmt.Errorf("decoding response: %w", err)
	}

	return result.CreatedAt, nil
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build -tags e2e ./e2e/admin/`
Expected: No errors (package has no main, build checks compilation only — may need `go vet -tags e2e ./e2e/admin/` instead).

Actually, since this is a test package, verify with:
```bash
go vet -tags e2e ./e2e/admin/
```
Expected: No errors.

- [ ] **Step 4: Commit**

```bash
git add e2e/admin/testutil.go
git commit -m "feat(e2e): add test utilities for admin e2e tests

Constants, env config loading, live client factory, and
getRepoCreatedAt() helper for the distributed lock.

Assisted-by: OpenCode claude-opus-4-6@default"
```

---

### Task 3: Distributed lock

**Files:**
- Create: `e2e/admin/lock.go`
- Create: `e2e/admin/lock_test.go`

- [ ] **Step 1: Write the failing lock test**

Create `e2e/admin/lock_test.go`:

```go
//go:build e2e

package admin

import (
	"context"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquireLock_NoExistingLock(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	runID := "test-uuid-1234"
	err := acquireLock(ctx, fake, "", testOrg, runID, 5*time.Minute)
	require.NoError(t, err)

	// Verify the lock repo was created with our UUID.
	content, err := fake.GetFileContent(ctx, testOrg, lockRepo, "README.md")
	require.NoError(t, err)
	assert.Equal(t, runID, string(content))
}

func TestReleaseLock_OwnedByUs(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	runID := "test-uuid-1234"
	// Pre-create the lock repo with our UUID.
	_, err := fake.CreateRepo(ctx, testOrg, lockRepo, "E2E test lock", false)
	require.NoError(t, err)
	err = fake.CreateFile(ctx, testOrg, lockRepo, "README.md", "acquire lock", []byte(runID))
	require.NoError(t, err)

	releaseLock(ctx, fake, testOrg, runID, t)

	// Verify repo was deleted.
	_, err = fake.GetRepo(ctx, testOrg, lockRepo)
	assert.True(t, forge.IsNotFound(err))
}

func TestReleaseLock_OwnedBySomeoneElse(t *testing.T) {
	fake := forge.NewFakeClient()
	ctx := context.Background()

	// Pre-create the lock repo with a different UUID.
	_, err := fake.CreateRepo(ctx, testOrg, lockRepo, "E2E test lock", false)
	require.NoError(t, err)
	err = fake.CreateFile(ctx, testOrg, lockRepo, "README.md", "acquire lock", []byte("other-uuid"))
	require.NoError(t, err)

	releaseLock(ctx, fake, testOrg, "our-uuid", t)

	// Repo should NOT have been deleted (not our lock).
	_, err = fake.GetRepo(ctx, testOrg, lockRepo)
	assert.NoError(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags e2e -run TestAcquireLock -v ./e2e/admin/`
Expected: FAIL — `acquireLock` is not defined.

- [ ] **Step 3: Write lock.go**

Create `e2e/admin/lock.go`:

```go
//go:build e2e

package admin

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// acquireLock attempts to acquire the distributed e2e lock by creating an
// e2e-lock repo in the test org. If the lock is already held, it polls
// until the lock is released or the timeout expires.
//
// The token parameter is needed for getRepoCreatedAt (direct API call).
// Pass "" if using a fake client (skips age checks).
func acquireLock(ctx context.Context, client forge.Client, token, org, runID string, timeout time.Duration) error {
	// Try to create the lock repo.
	acquired, err := tryCreateLock(ctx, client, org, runID)
	if err != nil {
		return fmt.Errorf("trying to create lock: %w", err)
	}
	if acquired {
		return nil
	}

	// Lock exists. Poll until released or timeout.
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Check if lock was released.
		content, err := client.GetFileContent(ctx, org, lockRepo, "README.md")
		if forge.IsNotFound(err) {
			// Lock was released — try to acquire.
			acquired, err := tryCreateLock(ctx, client, org, runID)
			if err != nil {
				return fmt.Errorf("retrying lock creation: %w", err)
			}
			if acquired {
				return nil
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("reading lock file: %w", err)
		}

		holder := strings.TrimSpace(string(content))
		if holder == runID {
			return nil // We hold it.
		}

		// Check lock age if we have a token (skip for fake clients).
		if token != "" {
			createdAt, ageErr := getRepoCreatedAt(ctx, token, org, lockRepo)
			if ageErr == nil {
				age := time.Since(createdAt)

				// Stale lock recovery.
				if age > timeout {
					fmt.Printf("[e2e-lock] Lock appears stale (age: %s), force-acquiring\n", age)
					_ = client.DeleteRepo(ctx, org, lockRepo)
					acquired, err := tryCreateLock(ctx, client, org, runID)
					if err != nil {
						return fmt.Errorf("force-acquiring stale lock: %w", err)
					}
					if acquired {
						return nil
					}
					continue
				}

				// Fresh lock — reset deadline.
				if age < freshLockThreshold {
					fmt.Printf("[e2e-lock] Lock recently acquired by another run (age: %s), resetting timer\n", age)
					deadline = time.Now().Add(timeout)
				}

				fmt.Printf("[e2e-lock] Lock held by %s (age: %s), waiting...\n", truncateUUID(holder), age.Round(time.Second))
			}
		} else {
			fmt.Printf("[e2e-lock] Lock held by %s, waiting...\n", truncateUUID(holder))
		}

		select {
		case <-time.After(lockPollInterval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return fmt.Errorf("timed out waiting for e2e lock after %s", timeout)
}

// tryCreateLock attempts to create the lock repo and write our UUID.
// Returns (true, nil) if the lock was successfully acquired.
func tryCreateLock(ctx context.Context, client forge.Client, org, runID string) (bool, error) {
	_, err := client.CreateRepo(ctx, org, lockRepo, "E2E test lock — do not delete manually", false)
	if err != nil {
		// Repo already exists (409 or similar) — someone else got it.
		return false, nil
	}

	err = client.CreateFile(ctx, org, lockRepo, "README.md", "acquire lock", []byte(runID))
	if err != nil {
		// Failed to write our UUID — cleanup and report failure.
		_ = client.DeleteRepo(ctx, org, lockRepo)
		return false, fmt.Errorf("writing lock file: %w", err)
	}

	// Verify we actually got the lock (handle race between two creators).
	content, err := client.GetFileContent(ctx, org, lockRepo, "README.md")
	if err != nil {
		return false, fmt.Errorf("verifying lock: %w", err)
	}
	if strings.TrimSpace(string(content)) == runID {
		fmt.Printf("[e2e-lock] Lock acquired (run: %s)\n", truncateUUID(runID))
		return true, nil
	}

	// Lost the race.
	return false, nil
}

// releaseLock deletes the lock repo, but only if we still hold it.
func releaseLock(ctx context.Context, client forge.Client, org, runID string, t *testing.T) {
	content, err := client.GetFileContent(ctx, org, lockRepo, "README.md")
	if err != nil {
		t.Logf("[e2e-lock] Could not read lock file during release: %v", err)
		return
	}

	if strings.TrimSpace(string(content)) != runID {
		t.Logf("[e2e-lock] Lock is held by someone else (%s), not releasing", truncateUUID(string(content)))
		return
	}

	if err := client.DeleteRepo(ctx, org, lockRepo); err != nil {
		t.Logf("[e2e-lock] Failed to release lock: %v", err)
		return
	}
	t.Logf("[e2e-lock] Lock released (run: %s)", truncateUUID(runID))
}

// truncateUUID returns the first 8 chars of a UUID for log readability.
func truncateUUID(uuid string) string {
	if len(uuid) > 8 {
		return uuid[:8]
	}
	return uuid
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -tags e2e -run TestAcquireLock -v ./e2e/admin/`
Expected: PASS

Run: `go test -tags e2e -run TestReleaseLock -v ./e2e/admin/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add e2e/admin/lock.go e2e/admin/lock_test.go
git commit -m "feat(e2e): add distributed lock via GitHub repo

Implements acquireLock/releaseLock using an e2e-lock repo in the
test org. Handles races, stale lock recovery, and fresh lock
detection with timer reset.

Assisted-by: OpenCode claude-opus-4-6@default"
```

---

### Task 4: AutoPrompter

**Files:**
- Create: `e2e/admin/prompter.go`

- [ ] **Step 1: Write prompter.go**

```go
//go:build e2e

package admin

// AutoPrompter implements appsetup.Prompter for non-interactive e2e use.
// It automatically accepts all prompts without human input.
type AutoPrompter struct{}

// WaitForEnter returns immediately. In e2e tests, Playwright handles
// the browser interactions that the prompt would normally gate on.
func (AutoPrompter) WaitForEnter(_ string) error {
	return nil
}

// Confirm always returns true, accepting any confirmation prompt
// (e.g., reuse existing app).
func (AutoPrompter) Confirm(_ string) (bool, error) {
	return true, nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet -tags e2e ./e2e/admin/`
Expected: No errors.

- [ ] **Step 3: Commit**

```bash
git add e2e/admin/prompter.go
git commit -m "feat(e2e): add AutoPrompter for non-interactive app setup

Implements appsetup.Prompter that auto-accepts all prompts,
removing the need for human interaction during e2e tests.

Assisted-by: OpenCode claude-opus-4-6@default"
```

---

### Task 5: PlaywrightBrowserOpener

**Files:**
- Create: `e2e/admin/browser.go`

This is the most complex piece. The `PlaywrightBrowserOpener` must handle three
distinct page types that `appsetup` opens:

1. **Local manifest form** (`http://127.0.0.1:PORT/`) — auto-submitting form
   that redirects to GitHub.
2. **GitHub "Register new GitHub App" page** — needs to click the create button.
3. **GitHub app install page** (`/apps/SLUG/installations/new`) — needs to click
   "Install".

- [ ] **Step 1: Write browser.go**

```go
//go:build e2e

package admin

import (
	"context"
	"fmt"
	"strings"

	"github.com/playwright-community/playwright-go"
)

// PlaywrightBrowserOpener implements appsetup.BrowserOpener using a
// Playwright browser page with a pre-authenticated persistent context.
type PlaywrightBrowserOpener struct {
	page playwright.Page
}

// NewPlaywrightBrowserOpener creates a new PlaywrightBrowserOpener
// using the given Playwright page.
func NewPlaywrightBrowserOpener(page playwright.Page) *PlaywrightBrowserOpener {
	return &PlaywrightBrowserOpener{page: page}
}

// Open navigates the Playwright page to the given URL and handles the
// expected interactions based on the page type.
//
// The manifest flow calls Open twice per app role:
//  1. With the local form URL — auto-submits to GitHub, we click "Create"
//  2. With the installation URL — we click "Install"
func (b *PlaywrightBrowserOpener) Open(_ context.Context, url string) error {
	if _, err := b.page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		return fmt.Errorf("navigating to %s: %w", url, err)
	}

	pageURL := b.page.URL()

	switch {
	case strings.Contains(pageURL, "/settings/apps/new"):
		// GitHub "Register new GitHub App" confirmation page.
		// The local form auto-submitted the manifest; we're now on GitHub.
		return b.handleCreateAppPage()

	case strings.Contains(pageURL, "/installations/new"):
		// GitHub App installation page.
		return b.handleInstallAppPage()

	case strings.Contains(url, "127.0.0.1"):
		// Local manifest form — it auto-submits via JS.
		// Wait for the redirect to GitHub, then handle the create page.
		if err := b.page.WaitForURL("**/settings/apps/new**", playwright.PageWaitForURLOptions{
			Timeout: playwright.Float(30000),
		}); err != nil {
			// The auto-submit may have already redirected. Check current URL.
			pageURL = b.page.URL()
			if strings.Contains(pageURL, "/settings/apps/new") {
				return b.handleCreateAppPage()
			}
			// May have gone all the way through to callback.
			if strings.Contains(pageURL, "/callback") {
				return nil // Success — callback already handled.
			}
			return fmt.Errorf("waiting for GitHub app creation page: %w", err)
		}
		return b.handleCreateAppPage()

	default:
		return fmt.Errorf("unexpected URL: %s", pageURL)
	}
}

// handleCreateAppPage clicks the "Create GitHub App" button on GitHub's
// app registration confirmation page.
func (b *PlaywrightBrowserOpener) handleCreateAppPage() error {
	// GitHub's "Create GitHub App" button is the form submit button.
	btn := b.page.Locator("button[type='submit']:has-text('Create GitHub App')")
	if err := btn.Click(); err != nil {
		return fmt.Errorf("clicking 'Create GitHub App' button: %w", err)
	}

	// Wait for GitHub to process and redirect back to our callback URL.
	// The callback URL is on 127.0.0.1, so wait for that navigation.
	if err := b.page.WaitForURL("**/callback**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(60000),
	}); err != nil {
		// Check if we ended up on the success page already.
		pageURL := b.page.URL()
		if strings.Contains(pageURL, "/callback") || strings.Contains(pageURL, "127.0.0.1") {
			return nil
		}
		return fmt.Errorf("waiting for callback after app creation: %w", err)
	}

	return nil
}

// handleInstallAppPage clicks through GitHub's app installation UI.
func (b *PlaywrightBrowserOpener) handleInstallAppPage() error {
	// Click "Install" button on the installation page.
	// GitHub shows a page where the user selects repos and clicks Install.
	btn := b.page.Locator("button[type='submit']:has-text('Install')")
	if err := btn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(15000),
	}); err != nil {
		return fmt.Errorf("clicking 'Install' button: %w", err)
	}

	// Wait for the installation to process.
	// GitHub redirects to the app's settings page after installation.
	if err := b.page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	}); err != nil {
		return fmt.Errorf("waiting for install to complete: %w", err)
	}

	return nil
}

// deleteAppViaPlaywright navigates to the GitHub App settings page and
// clicks through the deletion flow.
func deleteAppViaPlaywright(page playwright.Page, slug string) error {
	url := fmt.Sprintf("https://github.com/settings/apps/%s/advanced", slug)
	if _, err := page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		return fmt.Errorf("navigating to app settings for %s: %w", slug, err)
	}

	// Click "Delete GitHub App" in the danger zone.
	deleteBtn := page.Locator("button:has-text('Delete GitHub App')")
	if err := deleteBtn.Click(); err != nil {
		return fmt.Errorf("clicking 'Delete GitHub App' for %s: %w", slug, err)
	}

	// Confirm deletion in the modal dialog.
	// GitHub shows a confirmation dialog with a button to confirm.
	confirmBtn := page.Locator("button.btn-danger:has-text('Delete this GitHub App')")
	if err := confirmBtn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		// Try alternative selector for the confirmation.
		altBtn := page.Locator("dialog button:has-text('delete'), .js-confirm-button")
		if altErr := altBtn.Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(5000),
		}); altErr != nil {
			return fmt.Errorf("confirming app deletion for %s: primary=%w, alt=%v", slug, err, altErr)
		}
	}

	// Wait for deletion to process.
	if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	}); err != nil {
		return fmt.Errorf("waiting for app deletion to complete for %s: %w", slug, err)
	}

	fmt.Printf("[cleanup] Deleted GitHub App: %s\n", slug)
	return nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet -tags e2e ./e2e/admin/`
Expected: No errors.

- [ ] **Step 3: Commit**

```bash
git add e2e/admin/browser.go
git commit -m "feat(e2e): add PlaywrightBrowserOpener for manifest flow automation

Handles three page types: local manifest form auto-submit,
GitHub App creation confirmation, and app installation.
Also includes deleteAppViaPlaywright for cleanup.

Assisted-by: OpenCode claude-opus-4-6@default"
```

---

### Task 6: Cleanup helpers

**Files:**
- Create: `e2e/admin/cleanup.go`

- [ ] **Step 1: Write cleanup.go**

```go
//go:build e2e

package admin

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/playwright-community/playwright-go"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// cleanupStaleResources removes leftover resources from previous test runs.
// This is the "teardown-first" part of the dual cleanup strategy.
func cleanupStaleResources(ctx context.Context, client forge.Client, page playwright.Page, t *testing.T) {
	t.Helper()
	t.Log("[cleanup] Scanning for stale resources from previous runs...")

	// 1. Delete .fullsend repo if it exists.
	_, err := client.GetRepo(ctx, testOrg, forge.ConfigRepoName)
	if err == nil {
		t.Logf("[cleanup] Deleting stale %s repo", forge.ConfigRepoName)
		if delErr := client.DeleteRepo(ctx, testOrg, forge.ConfigRepoName); delErr != nil {
			t.Logf("[cleanup] Warning: could not delete %s: %v", forge.ConfigRepoName, delErr)
		}
	}

	// 2. Delete any fullsend-halfsend* GitHub Apps via Playwright.
	installations, err := client.ListOrgInstallations(ctx, testOrg)
	if err != nil {
		t.Logf("[cleanup] Warning: could not list installations: %v", err)
	} else {
		for _, inst := range installations {
			if strings.HasPrefix(inst.AppSlug, "fullsend-"+testOrg) {
				t.Logf("[cleanup] Deleting stale app: %s", inst.AppSlug)
				if delErr := deleteAppViaPlaywright(page, inst.AppSlug); delErr != nil {
					t.Logf("[cleanup] Warning: could not delete app %s: %v", inst.AppSlug, delErr)
				}
			}
		}
	}

	// 3. Close any open enrollment PRs in test-repo.
	prs, err := client.ListRepoPullRequests(ctx, testOrg, testRepo)
	if err != nil {
		t.Logf("[cleanup] Warning: could not list PRs: %v", err)
	} else {
		for _, pr := range prs {
			if strings.Contains(pr.Title, "fullsend") {
				t.Logf("[cleanup] Found stale enrollment PR #%d: %s", pr.Number, pr.Title)
				// Note: forge.Client doesn't have ClosePR; log for manual cleanup.
				t.Logf("[cleanup] Manual cleanup needed: close PR %s", pr.URL)
			}
		}
	}

	t.Log("[cleanup] Stale resource scan complete")
}

// registerAppCleanup registers a t.Cleanup that deletes the given app slug.
func registerAppCleanup(t *testing.T, page playwright.Page, slug string) {
	t.Helper()
	t.Cleanup(func() {
		fmt.Printf("[cleanup] Deleting app %s via Playwright\n", slug)
		if err := deleteAppViaPlaywright(page, slug); err != nil {
			t.Logf("[cleanup] Warning: could not delete app %s: %v", slug, err)
		}
	})
}

// registerRepoCleanup registers a t.Cleanup that deletes a repo.
func registerRepoCleanup(t *testing.T, client forge.Client, org, repo string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, err := client.GetRepo(ctx, org, repo)
		if err != nil {
			return // Already gone.
		}
		fmt.Printf("[cleanup] Deleting repo %s/%s\n", org, repo)
		if delErr := client.DeleteRepo(ctx, org, repo); delErr != nil {
			t.Logf("[cleanup] Warning: could not delete %s/%s: %v", org, repo, delErr)
		}
	})
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet -tags e2e ./e2e/admin/`
Expected: No errors.

- [ ] **Step 3: Commit**

```bash
git add e2e/admin/cleanup.go
git commit -m "feat(e2e): add cleanup helpers for teardown-first and deferred cleanup

cleanupStaleResources scans for leftover GitHub resources.
registerAppCleanup and registerRepoCleanup register t.Cleanup
handlers for mid-test failure recovery.

Assisted-by: OpenCode claude-opus-4-6@default"
```

---

### Task 7: Main e2e test — TestAdminInstallUninstall

**Files:**
- Create: `e2e/admin/admin_test.go`

This is the main test that ties everything together.

- [ ] **Step 1: Write admin_test.go**

```go
//go:build e2e

package admin

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

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

	browser, err := pw.Chromium.LaunchPersistentContext(cfg.browserStateDir, playwright.BrowserTypeLaunchPersistentContextOptions{
		Headless: playwright.Bool(os.Getenv("E2E_HEADED") != "true"),
	})
	require.NoError(t, err, "launching Playwright browser")
	t.Cleanup(func() { _ = browser.Close() })

	page, err := browser.NewPage()
	require.NoError(t, err, "creating Playwright page")

	// --- GitHub client ---
	client := newLiveClient(cfg.token)
	printer := ui.New(os.Stdout)

	// ======================
	// Phase 0: Acquire lock
	// ======================
	runID := uuid.New().String()
	t.Logf("E2E run ID: %s", runID)

	err = acquireLock(ctx, client, cfg.token, testOrg, runID, cfg.lockTimeout)
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
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet -tags e2e ./e2e/admin/`
Expected: No errors.

- [ ] **Step 3: Commit**

```bash
git add e2e/admin/admin_test.go
git commit -m "feat(e2e): add TestAdminInstallUninstall e2e test

Full lifecycle test: lock acquisition, teardown-first cleanup,
app creation via Playwright, layer stack install, verification,
analyze, uninstall, and verification.

Assisted-by: OpenCode claude-opus-4-6@default"
```

---

### Task 8: Auth state bootstrap tool

**Files:**
- Create: `e2e/admin/cmd/setup-auth/main.go`

- [ ] **Step 1: Create the directory**

```bash
mkdir -p e2e/admin/cmd/setup-auth
```

- [ ] **Step 2: Write main.go**

```go
// Command setup-auth launches a headed Playwright browser for manual GitHub
// login. The resulting session state is saved to E2E_BROWSER_STATE_DIR for
// use by e2e tests.
//
// Usage:
//
//	E2E_BROWSER_STATE_DIR=/tmp/pw-state go run ./e2e/admin/cmd/setup-auth/
package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/playwright-community/playwright-go"
)

func main() {
	stateDir := os.Getenv("E2E_BROWSER_STATE_DIR")
	if stateDir == "" {
		fmt.Fprintln(os.Stderr, "E2E_BROWSER_STATE_DIR must be set")
		os.Exit(1)
	}

	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "creating state dir: %v\n", err)
		os.Exit(1)
	}

	if err := playwright.Install(&playwright.RunOptions{
		Browsers: []string{"chromium"},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "installing Playwright browsers: %v\n", err)
		os.Exit(1)
	}

	pw, err := playwright.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting Playwright: %v\n", err)
		os.Exit(1)
	}
	defer pw.Stop()

	// Launch in headed mode so the user can log in.
	browser, err := pw.Chromium.LaunchPersistentContext(stateDir, playwright.BrowserTypeLaunchPersistentContextOptions{
		Headless: playwright.Bool(false),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "launching browser: %v\n", err)
		os.Exit(1)
	}

	page, err := browser.NewPage()
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating page: %v\n", err)
		os.Exit(1)
	}

	if _, err := page.Goto("https://github.com/login"); err != nil {
		fmt.Fprintf(os.Stderr, "navigating to GitHub login: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== GitHub Login ===")
	fmt.Println("Log in as the 'botsend' user in the browser window.")
	fmt.Println("Complete any 2FA prompts.")
	fmt.Println("")
	fmt.Println("When you're fully logged in, press Enter here to save the session.")

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()

	// Close the browser — this saves the persistent context state.
	if err := browser.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "closing browser: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("")
	fmt.Printf("Session state saved to: %s\n", stateDir)
	fmt.Println("")
	fmt.Println("To convert to a CI secret:")
	fmt.Printf("  tar czf - -C %s . | base64 > state.b64\n", stateDir)
	fmt.Println("  gh secret set E2E_BROWSER_STATE < state.b64")
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./e2e/admin/cmd/setup-auth/`
Expected: Binary is built (won't run without a display).

- [ ] **Step 4: Commit**

```bash
git add e2e/admin/cmd/setup-auth/main.go
git commit -m "feat(e2e): add setup-auth tool for Playwright browser state bootstrap

One-time interactive tool that opens a headed browser for manual
GitHub login, then saves the session state for e2e test reuse.

Assisted-by: OpenCode claude-opus-4-6@default"
```

---

### Task 9: CI workflow

**Files:**
- Create: `.github/workflows/e2e.yml`

- [ ] **Step 1: Write the workflow file**

```yaml
name: E2E Tests

on:
  push:
    branches: [main]
    paths: ['**/*.go', 'go.mod', 'go.sum']
  pull_request:
    paths: ['**/*.go', 'go.mod', 'go.sum']
  workflow_dispatch:

concurrency:
  group: e2e-halfsend
  cancel-in-progress: false

jobs:
  e2e:
    runs-on: ubuntu-latest
    timeout-minutes: 20
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Install Playwright browsers
        run: go run github.com/playwright-community/playwright-go/cmd/playwright install --with-deps chromium

      - name: Restore browser auth state
        run: |
          mkdir -p /tmp/pw-state
          echo "$E2E_BROWSER_STATE" | base64 -d | tar xzf - -C /tmp/pw-state
        env:
          E2E_BROWSER_STATE: ${{ secrets.E2E_BROWSER_STATE }}

      - name: Run e2e tests
        run: go test -tags e2e -v -timeout 15m ./e2e/admin/
        env:
          E2E_GITHUB_TOKEN: ${{ secrets.E2E_GITHUB_TOKEN }}
          E2E_BROWSER_STATE_DIR: /tmp/pw-state
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/e2e.yml
git commit -m "ci: add e2e test workflow for admin commands

Runs on Go file changes with concurrency group to prevent
parallel runs against the shared halfsend test org.

Assisted-by: OpenCode claude-opus-4-6@default"
```

---

### Task 10: Makefile target

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Add e2e-test target to Makefile**

Add `e2e-test` to the `.PHONY` line and add the target after `go-tidy`.
Also add it to the help output.

In the help section, add:
```
@echo "  e2e-test             - Run admin e2e tests (requires E2E_GITHUB_TOKEN)"
```

Add the target:
```makefile
e2e-test:
	go test -tags e2e -v -timeout 15m ./e2e/admin/
```

- [ ] **Step 2: Verify the target works (dry run)**

Run: `make -n e2e-test`
Expected: Prints the `go test -tags e2e ...` command.

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "chore: add e2e-test Makefile target

Assisted-by: OpenCode claude-opus-4-6@default"
```

---

### Task 11: Verify full compilation

**Files:** (none — verification only)

- [ ] **Step 1: Verify e2e package compiles**

Run: `go vet -tags e2e ./e2e/...`
Expected: No errors.

- [ ] **Step 2: Verify existing tests still pass**

Run: `go test -race ./...`
Expected: All existing tests pass. The e2e tests are NOT run (no `-tags e2e`).

- [ ] **Step 3: Verify build still works**

Run: `make go-build`
Expected: Binary built successfully.

- [ ] **Step 4: Run linter**

Run: `go vet ./...`
Expected: No errors (e2e files are excluded without the tag).

---

### Task 12: Final commit — all files together if not already committed

If any files were not committed in previous tasks, commit them now.

- [ ] **Step 1: Check for uncommitted changes**

Run: `git status`
Expected: Clean working tree (all changes committed in previous tasks).

- [ ] **Step 2: Verify commit history**

Run: `git log --oneline -15`
Expected: See all task commits in order.
