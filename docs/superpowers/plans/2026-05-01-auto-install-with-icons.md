# Auto Install with Icons Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `--auto --session-file` mode to `fullsend admin install` that drives a Playwright browser through the entire install flow, including uploading role-specific icons as GitHub App logos.

**Architecture:** When `--auto` is set, the CLI launches Playwright with a pre-authenticated session file, constructs a `PlaywrightBrowserOpener` (which implements `BrowserOpener`), and passes it to `appsetup.Setup`. The Playwright opener handles manifest submission, logo upload via the settings page file input, and app installation clicks. The broken API-based logo upload code (jwt.go, logo.go) is deleted.

**Tech Stack:** Go, Playwright (`playwright-go`), `go:embed` icons (already in place)

---

### Task 1: Delete broken API-based logo upload code

**Files:**
- Delete: `internal/appsetup/jwt.go`
- Delete: `internal/appsetup/jwt_test.go`
- Delete: `internal/appsetup/logo.go`
- Delete: `internal/appsetup/logo_test.go`
- Modify: `internal/appsetup/appsetup.go:428-451`

- [ ] **Step 1: Remove the logo upload block from runManifestFlow**

In `internal/appsetup/appsetup.go`, replace lines 428-451 (the entire `select` block at the end of `runManifestFlow`) with the original version without the logo upload:

```go
	select {
	case res := <-resultCh:
		if res.err != nil {
			return nil, res.err
		}
		s.ui.StepDone(fmt.Sprintf("App created: %s", res.creds.Slug))
		return res.creds, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
```

- [ ] **Step 2: Delete jwt.go, jwt_test.go, logo.go, logo_test.go**

```bash
rm internal/appsetup/jwt.go internal/appsetup/jwt_test.go
rm internal/appsetup/logo.go internal/appsetup/logo_test.go
```

- [ ] **Step 3: Run tests to verify nothing broke**

Run: `GOTOOLCHAIN=auto go test ./internal/appsetup/ -v`
Expected: All remaining tests PASS. No references to deleted functions.

- [ ] **Step 4: Commit**

```bash
git add -A internal/appsetup/
git commit -m "refactor: remove broken API-based logo upload

The PATCH /app endpoint doesn't exist. Logo upload will be done
via Playwright browser automation in --auto mode instead."
```

---

### Task 2: Pass role to BrowserOpener via RoleAware interface

The `PlaywrightBrowserOpener` needs to know the current role to look up the correct icon. Rather than changing the `BrowserOpener` interface (which would break `DefaultBrowser`), we add a `RoleAware` interface that `runManifestFlow` type-asserts on.

**Files:**
- Modify: `internal/appsetup/appsetup.go`

- [ ] **Step 1: Add the RoleAware interface and call site**

In `internal/appsetup/appsetup.go`, add the interface after the existing `BrowserOpener` interface (after line 45):

```go
// RoleAware is an optional interface that BrowserOpener implementations
// can satisfy to receive the current agent role before Open is called.
// This allows the opener to perform role-specific actions like uploading
// the correct icon.
type RoleAware interface {
	SetRole(role string)
}
```

Then in `runManifestFlow`, just before the `s.browser.Open(ctx, formURL)` call (around line 420), add:

```go
	// Inform the browser opener of the current role so it can perform
	// role-specific actions (e.g., uploading the correct icon).
	if ra, ok := s.browser.(RoleAware); ok {
		ra.SetRole(role)
	}
```

- [ ] **Step 2: Run tests to verify nothing broke**

Run: `GOTOOLCHAIN=auto go test ./internal/appsetup/ -v`
Expected: All tests PASS. `DefaultBrowser` doesn't implement `RoleAware`, so the type assertion is a no-op.

- [ ] **Step 3: Commit**

```bash
git add internal/appsetup/appsetup.go
git commit -m "feat: add RoleAware interface for role-specific browser actions"
```

---

### Task 3: Create PlaywrightBrowserOpener in production code

Port the browser automation from `e2e/admin/browser.go` into production code, adding logo upload support.

**Files:**
- Create: `internal/appsetup/playwright.go`

- [ ] **Step 1: Create the PlaywrightBrowserOpener**

Create `internal/appsetup/playwright.go`:

```go
package appsetup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	ghTypes "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/playwright-community/playwright-go"
	xhtml "golang.org/x/net/html"
)

// PlaywrightBrowserOpener implements BrowserOpener and RoleAware using
// a Playwright browser page with a pre-authenticated session.
type PlaywrightBrowserOpener struct {
	page playwright.Page
	ui   *ui.Printer
	role string
}

// NewPlaywrightBrowserOpener creates a new PlaywrightBrowserOpener.
func NewPlaywrightBrowserOpener(page playwright.Page, printer *ui.Printer) *PlaywrightBrowserOpener {
	return &PlaywrightBrowserOpener{page: page, ui: printer}
}

// SetRole sets the current agent role for icon lookup.
func (b *PlaywrightBrowserOpener) SetRole(role string) {
	b.role = role
}

// Open navigates the Playwright page to the given URL and handles
// interactions based on the page type.
func (b *PlaywrightBrowserOpener) Open(_ context.Context, url string) error {
	// Local manifest form — fetch via HTTP, submit from GitHub's origin.
	if strings.Contains(url, "127.0.0.1") {
		return b.handleLocalFormSubmission(url)
	}

	if _, err := b.page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(15000),
	}); err != nil {
		return fmt.Errorf("navigating to %s: %w", url, err)
	}

	pageURL := b.page.URL()

	switch {
	case strings.Contains(pageURL, "/settings/apps/new"),
		strings.Contains(pageURL, "/settings/apps/manifest"):
		return b.handleCreateAppPage()
	case strings.Contains(pageURL, "/installations/new"):
		return b.handleInstallAppPage()
	default:
		return fmt.Errorf("unexpected URL: %s", pageURL)
	}
}

// handleLocalFormSubmission fetches the local form via HTTP, extracts the
// manifest, then submits from GitHub's origin so session cookies are included.
func (b *PlaywrightBrowserOpener) handleLocalFormSubmission(localURL string) error {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Get(localURL)
	if err != nil {
		return fmt.Errorf("fetching local form page: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading local form page: %w", err)
	}
	content := string(body)

	manifest, err := extractInputValue(content, "manifest")
	if err != nil {
		return fmt.Errorf("extracting manifest from form: %w", err)
	}
	actionURL, err := extractFormAction(content)
	if err != nil {
		return fmt.Errorf("extracting form action: %w", err)
	}

	// Ensure hook_attributes exists in the manifest.
	var manifestMap map[string]any
	if jsonErr := json.Unmarshal([]byte(manifest), &manifestMap); jsonErr != nil {
		return fmt.Errorf("parsing manifest JSON: %w", jsonErr)
	}
	if _, ok := manifestMap["hook_attributes"]; !ok {
		manifestMap["hook_attributes"] = map[string]any{
			"url":    "https://example.com/webhook",
			"active": false,
		}
		patched, jsonErr := json.Marshal(manifestMap)
		if jsonErr != nil {
			return fmt.Errorf("re-marshaling manifest: %w", jsonErr)
		}
		manifest = string(patched)
	}

	b.ui.StepInfo(fmt.Sprintf("Submitting manifest (%d bytes) to %s", len(manifest), actionURL))

	// Navigate to GitHub's origin first so session cookies are sent.
	if _, err := b.page.Goto("https://github.com/settings", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(15000),
	}); err != nil {
		b.ui.StepWarn(fmt.Sprintf("Pre-navigate to GitHub failed: %v", err))
	}

	// Submit the form via JS.
	_, err = b.page.Evaluate(`([action, manifest]) => {
		const form = document.createElement('form');
		form.method = 'post';
		form.action = action;
		const m = document.createElement('input');
		m.type = 'hidden'; m.name = 'manifest'; m.value = manifest;
		form.appendChild(m);
		document.body.appendChild(form);
		form.submit();
	}`, []string{actionURL, manifest})
	if err != nil {
		return fmt.Errorf("submitting manifest form via JS: %w", err)
	}

	// Wait for navigation to app creation page.
	if err := b.page.WaitForURL("**/settings/apps/**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(15000),
	}); err != nil {
		pageURL := b.page.URL()
		if strings.Contains(pageURL, "/settings/apps/") {
			// We're there.
		} else if strings.Contains(pageURL, "/callback") {
			return nil
		} else {
			return fmt.Errorf("waiting for manifest page: %w (URL: %s)", err, pageURL)
		}
	}

	return b.handleCreateAppPage()
}

// handleCreateAppPage uploads the role icon and clicks "Create GitHub App".
func (b *PlaywrightBrowserOpener) handleCreateAppPage() error {
	b.ui.StepInfo(fmt.Sprintf("On app creation page: %s", b.page.URL()))

	// Upload the role-specific icon if available.
	if b.role != "" {
		if err := b.uploadLogo(); err != nil {
			return fmt.Errorf("uploading logo for role %s: %w", b.role, err)
		}
	}

	// Click "Create GitHub App".
	btn := b.page.Locator("button:has-text('Create GitHub App'), input[type='submit'][value*='Create GitHub App']")
	if err := btn.First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		return fmt.Errorf("waiting for 'Create GitHub App' button: %w", err)
	}
	if err := btn.First().Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		return fmt.Errorf("clicking 'Create GitHub App': %w", err)
	}

	// Wait for redirect back to callback URL.
	if err := b.page.WaitForURL("**/callback**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(30000),
	}); err != nil {
		pageURL := b.page.URL()
		if strings.Contains(pageURL, "/callback") || strings.Contains(pageURL, "127.0.0.1") {
			return nil
		}
		return fmt.Errorf("waiting for callback: %w", err)
	}

	return nil
}

// uploadLogo sets the role-specific icon on the app creation form's file input.
func (b *PlaywrightBrowserOpener) uploadLogo() error {
	icon, ok := ghTypes.IconForRole(b.role)
	if !ok {
		b.ui.StepInfo(fmt.Sprintf("No icon available for role %s, skipping logo upload", b.role))
		return nil
	}

	// Write the icon to a temp file for Playwright's SetInputFiles.
	tmpFile, err := os.CreateTemp("", fmt.Sprintf("fullsend-icon-%s-*.png", b.role))
	if err != nil {
		return fmt.Errorf("creating temp file for icon: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(icon); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing icon to temp file: %w", err)
	}
	tmpFile.Close()

	// Find the file input for the logo upload.
	// The app creation/edit page has an input[type=file] for the logo.
	fileInput := b.page.Locator("input[type='file']")
	count, err := fileInput.Count()
	if err != nil || count == 0 {
		return fmt.Errorf("logo file input not found on page %s", b.page.URL())
	}

	if err := fileInput.First().SetInputFiles(tmpFile.Name()); err != nil {
		return fmt.Errorf("setting logo file: %w", err)
	}

	// Wait for the avatar preview to update (GitHub processes the upload client-side).
	time.Sleep(2 * time.Second)

	b.ui.StepDone(fmt.Sprintf("Logo set for role %s", b.role))
	return nil
}

// handleInstallAppPage clicks "Install" on the GitHub App installation page.
// Retries on 404 since GitHub needs time to provision the app.
func (b *PlaywrightBrowserOpener) handleInstallAppPage() error {
	pageURL := b.page.URL()
	b.ui.StepInfo(fmt.Sprintf("On installation page: %s", pageURL))

	for attempt := range 5 {
		// Check for 404.
		is404, _ := b.page.Locator("img[alt='404'], h1:has-text('404')").Count()
		if is404 > 0 {
			b.ui.StepInfo(fmt.Sprintf("Got 404, retrying in %ds (attempt %d/5)", (attempt+1)*2, attempt+1))
			time.Sleep(time.Duration((attempt+1)*2) * time.Second)
			if _, err := b.page.Goto(pageURL, playwright.PageGotoOptions{
				WaitUntil: playwright.WaitUntilStateDomcontentloaded,
				Timeout:   playwright.Float(15000),
			}); err != nil {
				continue
			}
			continue
		}

		btn := b.page.Locator("button[type='submit']:has-text('Install')")
		if err := btn.Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(5000),
		}); err != nil {
			if attempt < 4 {
				b.ui.StepInfo(fmt.Sprintf("Install button not found, retrying (attempt %d/5)", attempt+1))
				time.Sleep(time.Duration((attempt+1)*2) * time.Second)
				if _, navErr := b.page.Goto(pageURL, playwright.PageGotoOptions{
					WaitUntil: playwright.WaitUntilStateDomcontentloaded,
					Timeout:   playwright.Float(15000),
				}); navErr != nil {
					continue
				}
				continue
			}
			return fmt.Errorf("clicking 'Install': %w", err)
		}

		break
	}

	// Wait for navigation away from installations/new.
	if err := b.page.WaitForURL("!**/installations/new**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		b.ui.StepWarn(fmt.Sprintf("WaitForURL after install timed out: %v", err))
	}
	if err := b.page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	}); err != nil {
		return fmt.Errorf("waiting for install to complete: %w", err)
	}

	return nil
}

// extractInputValue extracts the value attribute of a hidden input with the
// given name from raw HTML using proper HTML parsing.
func extractInputValue(rawHTML, name string) (string, error) {
	doc, err := xhtml.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return "", fmt.Errorf("parsing HTML: %w", err)
	}
	var value string
	var found bool
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if found {
			return
		}
		if n.Type == xhtml.ElementNode && n.Data == "input" {
			var nameAttr, valueAttr string
			for _, a := range n.Attr {
				if a.Key == "name" {
					nameAttr = a.Val
				}
				if a.Key == "value" {
					valueAttr = a.Val
				}
			}
			if nameAttr == name {
				value = valueAttr
				found = true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if !found {
		return "", fmt.Errorf("input %q not found in HTML", name)
	}
	return value, nil
}

// extractFormAction extracts the action URL from the first form element.
func extractFormAction(rawHTML string) (string, error) {
	doc, err := xhtml.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return "", fmt.Errorf("parsing HTML: %w", err)
	}
	var action string
	var found bool
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if found {
			return
		}
		if n.Type == xhtml.ElementNode && n.Data == "form" {
			for _, a := range n.Attr {
				if a.Key == "action" {
					action = a.Val
					found = true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if !found {
		return "", fmt.Errorf("form action not found in HTML")
	}
	return action, nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `GOTOOLCHAIN=auto go build ./internal/appsetup/`
Expected: Compiles successfully.

- [ ] **Step 3: Commit**

```bash
git add internal/appsetup/playwright.go
git commit -m "feat: add PlaywrightBrowserOpener for --auto install mode

Implements BrowserOpener and RoleAware interfaces. Handles manifest
form submission, logo upload via file input, and app installation.
Adapted from e2e/admin/browser.go for production use."
```

---

### Task 4: Add --auto and --session-file flags to install command

**Files:**
- Modify: `internal/cli/admin.go`

- [ ] **Step 1: Add flag variables**

In `internal/cli/admin.go`, in the `newInstallCmd` function, add two new variables after the existing flag variables (after line 93):

```go
	var autoMode bool
	var sessionFile string
```

- [ ] **Step 2: Add flag validation**

In the `RunE` function, after the GCP flag validation block (after line 142), add:

```go
			// Validate --auto flag dependencies.
			if autoMode && sessionFile == "" {
				return fmt.Errorf("--session-file is required when --auto is set")
			}
			if sessionFile != "" && !autoMode {
				return fmt.Errorf("--session-file requires --auto")
			}
```

- [ ] **Step 3: Modify runAppSetup to accept a BrowserOpener parameter**

Change the `runAppSetup` function signature from:

```go
func runAppSetup(ctx context.Context, client forge.Client, printer *ui.Printer, org string, roles []string) ([]layers.AgentCredentials, error) {
```

to:

```go
func runAppSetup(ctx context.Context, client forge.Client, printer *ui.Printer, org string, roles []string, browser appsetup.BrowserOpener) ([]layers.AgentCredentials, error) {
```

And change line 391 from:

```go
	setup := appsetup.NewSetup(client, appsetup.StdinPrompter{}, appsetup.DefaultBrowser{}, printer)
```

to:

```go
	setup := appsetup.NewSetup(client, appsetup.StdinPrompter{}, browser, printer)
```

- [ ] **Step 4: Add Playwright setup in RunE and update call site**

In the `RunE` function, replace the `runAppSetup` call block (lines 189-197) with:

```go
			// Collect agent credentials via app setup.
			var agentCreds []layers.AgentCredentials
			if !skipAppSetup {
				var browser appsetup.BrowserOpener
				if autoMode {
					pw, pwErr := playwright.Run()
					if pwErr != nil {
						return fmt.Errorf("starting Playwright: %w", pwErr)
					}
					defer func() { _ = pw.Stop() }()

					pwBrowser, pwErr := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
						Headless: playwright.Bool(false),
					})
					if pwErr != nil {
						return fmt.Errorf("launching browser: %w", pwErr)
					}
					defer func() { _ = pwBrowser.Close() }()

					browserCtx, pwErr := pwBrowser.NewContext(playwright.BrowserNewContextOptions{
						StorageStatePath: playwright.String(sessionFile),
					})
					if pwErr != nil {
						return fmt.Errorf("creating browser context: %w", pwErr)
					}
					defer func() { _ = browserCtx.Close() }()

					page, pwErr := browserCtx.NewPage()
					if pwErr != nil {
						return fmt.Errorf("creating browser page: %w", pwErr)
					}

					browser = appsetup.NewPlaywrightBrowserOpener(page, printer)
					printer.StepDone("Playwright browser ready (auto mode)")
				} else {
					browser = appsetup.DefaultBrowser{}
				}

				creds, err := runAppSetup(ctx, client, printer, org, roles, browser)
				if err != nil {
					return err
				}
				agentCreds = creds
			}
```

- [ ] **Step 5: Add the flag definitions**

After the existing flag definitions (after line 213), add:

```go
	cmd.Flags().BoolVar(&autoMode, "auto", false, "fully automated install using Playwright browser automation (requires --session-file)")
	cmd.Flags().StringVar(&sessionFile, "session-file", "", "path to Playwright session file for --auto mode")
```

- [ ] **Step 6: Add the playwright import**

Add to the import block in `admin.go`:

```go
	"github.com/playwright-community/playwright-go"
```

- [ ] **Step 7: Run tests and verify compilation**

Run: `GOTOOLCHAIN=auto go build ./cmd/fullsend/`
Expected: Compiles successfully.

Run: `GOTOOLCHAIN=auto go test ./internal/cli/ -v`
Expected: All tests PASS.

Run: `GOTOOLCHAIN=auto go test ./internal/appsetup/ -v`
Expected: All tests PASS.

- [ ] **Step 8: Run lint**

Run: `GOTOOLCHAIN=auto make lint`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/cli/admin.go
git commit -m "feat: add --auto and --session-file flags to install command

When --auto is set, the CLI launches a Playwright browser with a
pre-authenticated session and automates the entire install flow,
including uploading role-specific icons as GitHub App logos."
```

---

### Task 5: Verify end-to-end with a manual test

This is a manual verification step — not automated.

- [ ] **Step 1: Build the binary**

```bash
GOTOOLCHAIN=auto go build -o ./fullsend ./cmd/fullsend/
```

- [ ] **Step 2: Export a session file (if not already available)**

Use the existing session export tooling:

```bash
GOTOOLCHAIN=auto go run ./e2e/cmd/export-session/
```

Or reuse an existing session file.

- [ ] **Step 3: Run install with --auto**

```bash
./fullsend admin install appdumpster --auto --session-file /path/to/session.json
```

Expected: The CLI launches a browser, creates each app, uploads the logo, and installs — all without human interaction. Each app should show its role-specific icon on the GitHub app page.

- [ ] **Step 4: Verify icons**

Check each app's page:
- `https://github.com/apps/appdumpster-fullsend` — should show the teal seedling icon
- `https://github.com/apps/appdumpster-triage` — should show the orange clipboard icon
- `https://github.com/apps/appdumpster-coder` — should show the cyan code brackets icon
- `https://github.com/apps/appdumpster-review` — should show the pink magnifying glass icon
