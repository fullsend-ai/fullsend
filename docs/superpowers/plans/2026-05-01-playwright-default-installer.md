# Playwright Default Installer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Playwright browser automation the default installer mechanism, with `--no-playwright` as the escape hatch and ephemeral session generation built in.

**Architecture:** The branch `auto-install-with-icons` already has all Playwright infrastructure (PlaywrightBrowserOpener, PAT automation, export-session, --auto/--session-file flags). This plan covers three sequential operations: (1) squash existing work into a clean "opt-in --auto" commit, (2) flip the default so Playwright is on by default with --no-playwright opt-out and ephemeral sessions, (3) write ADR-0026 and update related docs. Icons are a separate PR.

**Tech Stack:** Go, Playwright (playwright-go), Cobra CLI, lipgloss (terminal UI)

---

## File Structure

| File | Role |
|------|------|
| `internal/cli/admin.go` | CLI commands: install, uninstall, export-session. Flag definitions, Playwright lifecycle, briefing gate, session generation/cleanup |
| `internal/appsetup/appsetup.go` | Interfaces: BrowserOpener, RoleAware, LogoUploader, AppDeleter, PATCreator, PATDeleter |
| `internal/appsetup/playwright.go` | PlaywrightBrowserOpener: app creation, logo upload, app deletion |
| `internal/appsetup/playwright_pat.go` | PAT creation/deletion via Playwright |
| `internal/ui/ui.go` | InfoBox method for briefing gate display |
| `docs/ADRs/0026-playwright-default-installer.md` | New ADR |
| `docs/ADRs/0010-stored-session-for-e2e-browser-auth.md` | Forward-reference to ADR-0026 |
| `docs/ADRs/0014-admin-install-github-apps-secrets-v1.md` | Forward-reference to ADR-0026 |
| `docs/superpowers/specs/2026-05-01-auto-install-with-icons-design.md` | Superseded-by header |

---

## Pre-flight: Understanding the branch state

The branch has 12 commits ahead of main plus uncommitted changes. The work breaks into two PRs:

**PR 1 (Icons):** Commits `6fa07cb` through `c2aa1fd` plus `ca1fc34` — icon embedding, `IconForRole`, broken API upload removal. Separate PR.

**PR 2 (Automated Installer):** Everything else. This plan covers PR 2 only. The approach is:
1. Separate the icon commits out (they go to their own PR)
2. Squash all Playwright/CLI work (committed + uncommitted) into one clean commit
3. Add commit 2: flip the default
4. Add commit 3: ADR and doc updates

---

### Task 1: Separate icons into their own branch and reset

**Files:** Git operations only (no code changes)

This task creates the icons PR branch and resets the working branch for the automated installer work.

- [ ] **Step 1: Create the icons branch from main**

```bash
git stash
git checkout main
git checkout -b agent-app-icons
```

- [ ] **Step 2: Cherry-pick icon commits**

Cherry-pick the icon-related commits. These are:
- `6fa07cb` feat: embed agent role icons with go:embed lookup
- `c2aa1fd` feat: add updated icons and future agent icons
- `ca1fc34` refactor: remove broken API-based logo upload

Also cherry-pick the design doc commits if desired:
- `376c90f` docs: add design spec for agent app icons
- `cc26bb8` docs: add implementation plan for agent app icons

```bash
git cherry-pick 376c90f cc26bb8 6fa07cb c2aa1fd ca1fc34
```

If there are conflicts (likely since some intermediate commits touched the same files), resolve them. The goal is: icons embedded, `IconForRole` exists, broken `jwt.go`/`logo.go` deleted.

- [ ] **Step 3: Verify the icons branch builds**

```bash
GOTOOLCHAIN=auto go build ./...
GOTOOLCHAIN=auto go test ./internal/forge/github/...
```

Expected: clean build, icon tests pass.

- [ ] **Step 4: Switch back to the installer branch**

```bash
git checkout auto-install-with-icons
git stash pop
```

- [ ] **Step 5: Commit — do not commit yet, just note this is ready**

The icons branch is ready for a separate PR. We'll come back to push it later.

---

### Task 2: Squash all Playwright/installer work into one commit

**Files:** Git operations, then verify build

This task takes all the committed + uncommitted Playwright work and squashes it into a single clean commit on a fresh branch.

- [ ] **Step 1: Create a fresh branch from main**

```bash
git checkout auto-install-with-icons
git stash  # save any uncommitted work
git checkout main
git checkout -b playwright-default-installer
git stash pop
```

- [ ] **Step 2: Cherry-pick the icon prerequisite commits**

The Playwright code depends on icons being present (LogoUploader uses `IconForRole`). Cherry-pick the icon commits first:

```bash
git cherry-pick 6fa07cb c2aa1fd ca1fc34
```

- [ ] **Step 3: Stage all Playwright/installer files**

The uncommitted changes from `auto-install-with-icons` should now be in the working tree. Stage them along with any Playwright files that were in separate commits:

```bash
# Check what needs staging
git status

# Stage the Playwright and CLI files
git add internal/appsetup/appsetup.go
git add internal/appsetup/playwright.go
git add internal/appsetup/playwright_pat.go
git add internal/cli/admin.go
git add .gitignore
```

If `playwright.go` and `playwright_pat.go` are showing as "new file" (untracked), that's expected. If `playwright.go` was committed in earlier commits on the old branch, you may need to also cherry-pick those commits or manually copy the files.

- [ ] **Step 4: Verify everything builds**

```bash
GOTOOLCHAIN=auto go build ./cmd/fullsend/
GOTOOLCHAIN=auto go test ./internal/appsetup/...
GOTOOLCHAIN=auto go vet ./...
```

Expected: clean build, tests pass, no vet issues.

- [ ] **Step 5: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat: add Playwright-driven app setup, PAT automation, and session export

Add PlaywrightBrowserOpener that automates GitHub App creation, logo
upload, app installation, and app deletion via Playwright browser
automation. Add PAT creation/deletion for dispatch tokens.

New CLI additions:
- fullsend admin export-session: generate a Playwright session file
- --auto flag on install and uninstall: use Playwright instead of
  manual browser interaction
- --session-file flag: provide pre-generated session file

Optional interfaces (RoleAware, LogoUploader, AppDeleter, PATCreator,
PATDeleter) allow the PlaywrightBrowserOpener to extend BrowserOpener
without breaking the manual flow.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Flip the default — Playwright on, `--no-playwright` opt-out

**Files:**
- Modify: `internal/cli/admin.go`
- Modify: `internal/ui/ui.go`

This is the behavioral change. All edits are in `admin.go` except adding `InfoBox` to `ui.go`.

- [ ] **Step 1: Add `InfoBox` to ui.go**

The briefing gate needs a styled box. Add an `InfoBox` method alongside the existing `ErrorBox`:

In `internal/ui/ui.go`, add after the `ErrorBox` method (~line 133):

```go
// InfoBox prints an info-styled bordered box with title and detail.
func (p *Printer) InfoBox(title, detail string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	heading := lipgloss.NewStyle().Bold(true).Foreground(ColorBrand).Render(title)
	body := heading + "\n" + detail

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBrand).
		Padding(0, 1).
		Render(body)

	fmt.Fprintln(p.w, box)
}
```

- [ ] **Step 2: Replace `--auto` with `--no-playwright` on install**

In `internal/cli/admin.go`, in `newInstallCmd()`:

Replace the flag variable declarations:
```go
// Old:
var autoMode bool
var sessionFile string

// New:
var noPlaywright bool
var sessionFile string
var yolo bool
```

Replace the flag registrations (near line 272):
```go
// Old:
cmd.Flags().BoolVar(&autoMode, "auto", false, "fully automated install using Playwright browser automation (requires --session-file)")
cmd.Flags().StringVar(&sessionFile, "session-file", "", "path to Playwright session file for --auto mode")

// New:
cmd.Flags().BoolVar(&noPlaywright, "no-playwright", false, "fall back to manual browser flow (no Playwright)")
cmd.Flags().StringVar(&sessionFile, "session-file", "", "path to pre-existing Playwright session file (skips login, not deleted afterward)")
cmd.Flags().BoolVar(&yolo, "yolo", false, "skip the briefing and acknowledgment prompt")
```

- [ ] **Step 3: Replace validation logic in install RunE**

Replace the `--auto` validation block (around lines 153-164) with:

```go
// Validate flag interactions.
if noPlaywright && sessionFile != "" {
	return fmt.Errorf("--no-playwright and --session-file are contradictory")
}
if noPlaywright && yolo {
	// --yolo with --no-playwright is fine — it just skips the
	// manual-mode confirmation if we ever add one.
}
```

- [ ] **Step 4: Add session generation and briefing gate to install RunE**

Replace the browser setup block (around lines 211-245) with the new flow. This goes after the `dryRun` early return and before app setup:

```go
// Set up browser opener.
var installBrowser appsetup.BrowserOpener
if noPlaywright {
	installBrowser = appsetup.DefaultBrowser{}
} else {
	// Determine session file path.
	ephemeralSession := sessionFile == ""
	activeSessionFile := sessionFile

	if ephemeralSession {
		// Generate ephemeral session inline.
		username := os.Getenv("GITHUB_USERNAME")
		password := os.Getenv("GITHUB_PASSWORD")
		if username == "" {
			printer.StepInfo("Enter GitHub username:")
			scanner := bufio.NewScanner(os.Stdin)
			if !scanner.Scan() {
				return fmt.Errorf("no username provided")
			}
			username = strings.TrimSpace(scanner.Text())
		}
		if password == "" {
			printer.StepInfo("Enter GitHub password:")
			scanner := bufio.NewScanner(os.Stdin)
			if !scanner.Scan() {
				return fmt.Errorf("no password provided")
			}
			password = strings.TrimSpace(scanner.Text())
		}

		tmpFile, tmpErr := os.CreateTemp("", "fullsend-session-*.json")
		if tmpErr != nil {
			return fmt.Errorf("creating temp session file: %w", tmpErr)
		}
		activeSessionFile = tmpFile.Name()
		tmpFile.Close()

		// Always clean up ephemeral session.
		defer func() {
			if rmErr := os.Remove(activeSessionFile); rmErr == nil {
				printer.StepDone("Ephemeral session file deleted: " + activeSessionFile)
			} else if !os.IsNotExist(rmErr) {
				printer.StepWarn("Could not delete ephemeral session file: " + activeSessionFile)
			}
		}()

		if err := generateSession(printer, username, password, activeSessionFile); err != nil {
			return fmt.Errorf("generating session: %w", err)
		}
	} else {
		// Explicit session file — validate it exists.
		if _, err := os.Stat(sessionFile); err != nil {
			return fmt.Errorf("session file %s: %w", sessionFile, err)
		}
	}

	// Show briefing gate unless --yolo.
	if !yolo {
		numApps := len(strings.Split(agents, ","))
		briefing := fmt.Sprintf(
			"The installer will open a browser and act on your behalf to set up\n"+
				"GitHub Apps for fullsend. Here's what it will do:\n\n"+
				"  - Create %d GitHub Apps (one per agent role)\n"+
				"  - Upload an icon for each app\n"+
				"  - Install each app on the org\n"+
				"  - Create a fine-grained PAT for workflow dispatch\n\n"+
				"This is a one-time setup to lay down the app scaffolding. Unfortunately,\n"+
				"GitHub doesn't offer APIs for app logo upload or PAT creation, so\n"+
				"browser automation is the only way to do this without manual steps.\n\n"+
				"If you'd prefer to do this manually, re-run with --no-playwright\n"+
				"and follow the interactive prompts instead.\n\n"+
				"Press Enter to continue, or Ctrl-C to abort.",
			numApps,
		)
		printer.InfoBox("Playwright Browser Automation", briefing)
		printer.Blank()
		fmt.Scanln()
	}

	// Launch Playwright.
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
		StorageStatePath: playwright.String(activeSessionFile),
	})
	if pwErr != nil {
		return fmt.Errorf("creating browser context: %w", pwErr)
	}
	defer func() { _ = browserCtx.Close() }()

	page, pwErr := browserCtx.NewPage()
	if pwErr != nil {
		return fmt.Errorf("creating browser page: %w", pwErr)
	}

	installBrowser = appsetup.NewPlaywrightBrowserOpener(page, printer)
	printer.StepDone("Playwright browser ready")

	// Warn about persistent session file at the end.
	if !ephemeralSession {
		defer func() {
			printer.Blank()
			printer.StepWarn("WARNING: Your session file at " + sessionFile + " contains active GitHub credentials.")
			printer.StepWarn("Delete it when you no longer need it:  rm " + sessionFile)
		}()
	}
}
```

- [ ] **Step 5: Extract `generateSession` helper**

Add this function to `admin.go`. It's extracted from the existing `export-session` command logic:

```go
// generateSession logs into GitHub via Playwright and exports the session.
// The browser is launched headed so the user can complete 2FA if required.
func generateSession(printer *ui.Printer, username, password, outputPath string) error {
	printer.StepStart("Generating browser session")

	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("starting Playwright: %w", err)
	}
	defer func() { _ = pw.Stop() }()

	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(false),
	})
	if err != nil {
		return fmt.Errorf("launching browser: %w", err)
	}
	defer func() { _ = browser.Close() }()

	browserCtx, err := browser.NewContext()
	if err != nil {
		return fmt.Errorf("creating browser context: %w", err)
	}
	defer func() { _ = browserCtx.Close() }()

	page, err := browserCtx.NewPage()
	if err != nil {
		return fmt.Errorf("creating page: %w", err)
	}

	if _, err := page.Goto("https://github.com/login", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	}); err != nil {
		return fmt.Errorf("navigating to login: %w", err)
	}

	// Check if already logged in.
	if !strings.Contains(page.URL(), "/login") && !strings.Contains(page.URL(), "/session") {
		printer.StepDone("Already logged in")
		if _, err := browserCtx.StorageState(outputPath); err != nil {
			return fmt.Errorf("exporting session: %w", err)
		}
		printer.StepDone("Session exported to " + outputPath)
		return nil
	}

	if err := page.Locator("#login_field").Fill(username); err != nil {
		return fmt.Errorf("filling username: %w", err)
	}
	if err := page.Locator("#password").Fill(password); err != nil {
		return fmt.Errorf("filling password: %w", err)
	}
	if err := page.Locator("input[type='submit'], button[type='submit']").First().Click(); err != nil {
		return fmt.Errorf("clicking submit: %w", err)
	}

	if err := page.WaitForURL("https://github.com/**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(15000),
	}); err != nil {
		return fmt.Errorf("post-login navigation: %w (url: %s)", err, page.URL())
	}

	// Handle 2FA.
	currentURL := page.URL()
	if strings.Contains(currentURL, "/sessions/") || strings.Contains(currentURL, "/two-factor") {
		printer.StepInfo("2FA required — complete it in the browser window")
		if err := page.WaitForURL("https://github.com/", playwright.PageWaitForURLOptions{
			Timeout: playwright.Float(120000),
		}); err != nil {
			return fmt.Errorf("timed out waiting for 2FA (url: %s)", page.URL())
		}
	}

	currentURL = page.URL()
	if strings.Contains(currentURL, "/login") || strings.Contains(currentURL, "/session") {
		return fmt.Errorf("login failed, still at: %s", currentURL)
	}

	printer.StepDone("Logged in")

	if _, err := browserCtx.StorageState(outputPath); err != nil {
		return fmt.Errorf("exporting session: %w", err)
	}

	printer.StepDone("Session generated: " + outputPath)
	return nil
}
```

- [ ] **Step 6: Update `export-session` to use `generateSession` and default path**

In `newExportSessionCmd()`, replace the inline login logic with a call to `generateSession`, and set the default `--session-file` path:

```go
func newExportSessionCmd() *cobra.Command {
	var sessionFile string

	cmd := &cobra.Command{
		Use:   "export-session",
		Short: "Export a GitHub browser session for use with --session-file",
		Long:  "Logs into GitHub via Playwright and exports the browser session as a storage state JSON file. Set GITHUB_USERNAME and GITHUB_PASSWORD environment variables, or enter credentials interactively.",
		RunE: func(cmd *cobra.Command, args []string) error {
			username := os.Getenv("GITHUB_USERNAME")
			password := os.Getenv("GITHUB_PASSWORD")

			printer := ui.New(os.Stdout)
			printer.Banner()
			printer.Blank()
			printer.Header("Exporting GitHub session")
			printer.Blank()

			if username == "" {
				printer.StepInfo("Enter GitHub username:")
				scanner := bufio.NewScanner(os.Stdin)
				if !scanner.Scan() {
					return fmt.Errorf("no username provided")
				}
				username = strings.TrimSpace(scanner.Text())
			}
			if password == "" {
				printer.StepInfo("Enter GitHub password:")
				scanner := bufio.NewScanner(os.Stdin)
				if !scanner.Scan() {
					return fmt.Errorf("no password provided")
				}
				password = strings.TrimSpace(scanner.Text())
			}

			if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
				return fmt.Errorf("creating output directory: %w", err)
			}

			if err := generateSession(printer, username, password, sessionFile); err != nil {
				return err
			}

			printer.Blank()
			printer.StepInfo("Use with: fullsend admin install <org> --session-file " + sessionFile)
			printer.Blank()
			printer.StepWarn("WARNING: This file contains active GitHub credentials.")
			printer.StepWarn("Delete it when you no longer need it:  rm " + sessionFile)
			return nil
		},
	}

	defaultPath := filepath.Join(os.Getenv("HOME"), ".config", "fullsend", "session.json")
	cmd.Flags().StringVar(&sessionFile, "session-file", defaultPath, "path to write the session file")

	return cmd
}
```

- [ ] **Step 7: Replace `--auto` with `--no-playwright` on uninstall**

In `newUninstallCmd()`, replace flag variables:

```go
// Old:
var autoMode bool
var sessionFile string

// New:
var noPlaywright bool
var sessionFile string
```

Replace flag registrations:
```go
// Old:
cmd.Flags().BoolVar(&autoMode, "auto", false, "fully automated uninstall using Playwright browser automation (requires --session-file)")
cmd.Flags().StringVar(&sessionFile, "session-file", "", "path to Playwright session file for --auto mode")

// New:
cmd.Flags().BoolVar(&noPlaywright, "no-playwright", false, "fall back to manual browser flow (no Playwright)")
cmd.Flags().StringVar(&sessionFile, "session-file", "", "path to pre-existing Playwright session file (skips login, not deleted afterward)")
```

Replace validation and browser setup in the uninstall `RunE` with the same pattern as install:
- `noPlaywright` → `DefaultBrowser{}`
- No `--session-file` → ephemeral session (prompt for creds, defer delete)
- Explicit `--session-file` → validate exists, use as-is, defer warning
- Briefing gate (uninstall version): "delete N GitHub Apps" and "delete dispatch PAT"
- `--yolo` skips both the briefing gate AND the org-name confirmation prompt

The uninstall briefing text:

```go
briefing := fmt.Sprintf(
	"The uninstaller will open a browser and act on your behalf to\n"+
		"clean up GitHub Apps for fullsend. Here's what it will do:\n\n"+
		"  - Delete %d GitHub Apps\n"+
		"  - Delete the dispatch PAT\n"+
		"  - Delete the .fullsend config repo and secrets\n\n"+
		"If you'd prefer to do this manually, re-run with --no-playwright.\n\n"+
		"Press Enter to continue, or Ctrl-C to abort.",
	len(agentSlugs),
)
```

Note: the uninstall briefing needs to come after we compute `agentSlugs` but before we start any destructive work. Move the slug computation before the briefing gate.

- [ ] **Step 8: Build and verify**

```bash
GOTOOLCHAIN=auto go build ./cmd/fullsend/
GOTOOLCHAIN=auto go vet ./...
```

Expected: clean build, no vet issues.

- [ ] **Step 9: Commit**

```bash
git add internal/cli/admin.go internal/ui/ui.go
git commit -m "$(cat <<'EOF'
feat: make Playwright the default installer with ephemeral sessions

Playwright browser automation is now the default for install and
uninstall. The installer generates an ephemeral browser session
inline (prompting for GitHub credentials), uses it for all UI
automation, and deletes it on exit.

Key changes:
- Remove --auto flag; add --no-playwright to opt out
- Add --yolo to install (skip briefing gate)
- Inline session generation with ephemeral cleanup via defer
- Briefing gate explains what Playwright will do before proceeding
- --session-file uses pre-existing session (not deleted, warns)
- export-session defaults to ~/.config/fullsend/session.json
- Extract generateSession helper (shared by install and export-session)

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Write ADR-0026 and update related docs

**Files:**
- Create: `docs/ADRs/0026-playwright-default-installer.md`
- Modify: `docs/ADRs/0010-stored-session-for-e2e-browser-auth.md`
- Modify: `docs/ADRs/0014-admin-install-github-apps-secrets-v1.md`
- Modify: `docs/superpowers/specs/2026-05-01-auto-install-with-icons-design.md`

- [ ] **Step 1: Check the next available ADR number**

```bash
ls docs/ADRs/ | sort -n | tail -5
```

The plan assumes 0026 but verify no collision.

- [ ] **Step 2: Write ADR-0026**

Create `docs/ADRs/0026-playwright-default-installer.md`:

```markdown
# 26. Playwright browser automation as default installer mechanism

Date: 2026-05-01

## Status

Proposed

Supplements [ADR-0010](0010-stored-session-for-e2e-browser-auth.md).
Extends [ADR-0014](0014-admin-install-github-apps-secrets-v1.md).

## Context

GitHub provides no REST API for uploading GitHub App logos or creating
fine-grained personal access tokens (PATs). The original installer
opened the system browser and asked the user to perform these steps
manually — a multi-step, error-prone process involving 10+ instructions
for PAT creation alone.

This forced the installer into a hybrid model: API calls for what
GitHub supports, manual browser interaction for everything else. Users
had to context-switch between terminal and browser repeatedly.

ADR-0010 established Playwright with `storageState` for e2e test
authentication. The same mechanism works for production installer
automation.

## Decision

Playwright browser automation is the default mechanism for
`fullsend admin install` and `fullsend admin uninstall`.

When no `--session-file` is provided, the installer:
1. Prompts for GitHub credentials (env vars or interactive).
2. Generates an ephemeral browser session.
3. Performs all GitHub UI interactions automatically (app creation,
   logo upload, app installation, PAT creation/deletion).
4. Deletes the ephemeral session on exit (success or failure).

Users who prefer manual interaction pass `--no-playwright` to fall
back to the original xdg-open/stdin flow.

An explicit `--session-file <path>` reuses a pre-generated session
(not deleted afterward; the user is warned to clean it up).

## Consequences

- Playwright becomes a runtime dependency for the default install path.
  Users on headless systems must use `--no-playwright`.
- Session files contain active GitHub credentials. Ephemeral sessions
  are deleted automatically; explicit sessions require user cleanup.
- Logo upload and PAT creation are now automated, reducing a 10+ step
  manual process to a single command.
- The `--no-playwright` escape hatch preserves the original flow for
  users who cannot or prefer not to use browser automation.
- ADR-0010's stored-session pattern extends from e2e tests to
  production installer use, with a different lifecycle (ephemeral
  by default in production, persistent in e2e).
```

- [ ] **Step 3: Add forward reference to ADR-0010**

At the end of `docs/ADRs/0010-stored-session-for-e2e-browser-auth.md`, add:

```markdown

---

See also [ADR-0026](0026-playwright-default-installer.md), which extends
stored sessions to the production installer (ephemeral by default).
```

- [ ] **Step 4: Add forward reference to ADR-0014**

At the end of `docs/ADRs/0014-admin-install-github-apps-secrets-v1.md`, add:

```markdown

---

See also [ADR-0026](0026-playwright-default-installer.md), which specifies
browser automation for app creation, logo upload, and PAT lifecycle.
```

- [ ] **Step 5: Mark old design spec as superseded**

At the top of `docs/superpowers/specs/2026-05-01-auto-install-with-icons-design.md`, add:

```markdown
> **Superseded by:** [2026-05-01-playwright-default-installer-design.md](2026-05-01-playwright-default-installer-design.md)

```

- [ ] **Step 6: Commit**

```bash
git add docs/ADRs/0026-playwright-default-installer.md \
       docs/ADRs/0010-stored-session-for-e2e-browser-auth.md \
       docs/ADRs/0014-admin-install-github-apps-secrets-v1.md \
       docs/superpowers/specs/2026-05-01-auto-install-with-icons-design.md \
       docs/superpowers/specs/2026-05-01-playwright-default-installer-design.md
git commit -m "$(cat <<'EOF'
docs: ADR-0026 Playwright as default installer mechanism

Record the decision to make Playwright browser automation the default
for install and uninstall. Add forward references from ADR-0010 and
ADR-0014. Mark old auto-install design spec as superseded.

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Lint, test, and verify

**Files:** None (verification only)

- [ ] **Step 1: Run linter**

```bash
make lint
```

Expected: no failures.

- [ ] **Step 2: Run unit tests**

```bash
GOTOOLCHAIN=auto go test ./...
```

Expected: all tests pass.

- [ ] **Step 3: Run go vet**

```bash
GOTOOLCHAIN=auto go vet ./...
```

Expected: no issues.

- [ ] **Step 4: Verify final commit history**

```bash
git log --oneline origin/main..HEAD
```

Expected (approximately):
```
<sha> docs: ADR-0026 Playwright as default installer mechanism
<sha> feat: make Playwright the default installer with ephemeral sessions
<sha> feat: add Playwright-driven app setup, PAT automation, and session export
<sha> refactor: remove broken API-based logo upload
<sha> feat: add updated icons and future agent icons
<sha> feat: embed agent role icons with go:embed lookup
```

The icon commits (bottom 3) will be separated into their own PR. The top 3 are the automated installer PR.

- [ ] **Step 5: Manual smoke test**

Build and test the briefing gate:

```bash
GOTOOLCHAIN=auto go build -o fullsend ./cmd/fullsend/
./fullsend admin install appdumpster --yolo --session-file /path/to/session.json
```

Verify: skips briefing, uses session file, warns at end.

```bash
./fullsend admin install appdumpster
```

Verify: prompts for credentials, shows briefing, waits for Enter.

---

### Task 6: Push and create PRs

**Files:** Git operations only

- [ ] **Step 1: Push icons branch and create PR**

```bash
git checkout agent-app-icons
git push -u origin agent-app-icons
gh pr create --title "feat: embed agent role icons" --body "$(cat <<'EOF'
## Summary
- Embed per-role PNG icons via `go:embed` with `IconForRole` lookup
- Delete broken API-based logo upload code (`jwt.go`, `logo.go`)
- Icons: bootstrap, triage, coder, review, plus future agent roles

## Test plan
- [ ] `go test ./internal/forge/github/...` passes
- [ ] `go build ./...` clean

Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 2: Push installer branch and create PR**

```bash
git checkout playwright-default-installer
git push -u origin playwright-default-installer
gh pr create --title "feat: Playwright as default installer mechanism" --body "$(cat <<'EOF'
## Summary
- Playwright browser automation is the default for install/uninstall
- Ephemeral session generation (prompted inline, deleted on exit)
- `--session-file` for pre-existing sessions (not deleted, warns)
- `--no-playwright` falls back to manual browser flow
- `--yolo` skips briefing gate on install
- Automated PAT creation/deletion via Playwright
- ADR-0026 records the decision

## Test plan
- [ ] `go build ./cmd/fullsend/` clean
- [ ] `go test ./...` passes
- [ ] `go vet ./...` clean
- [ ] Manual: `fullsend admin install <org>` shows briefing, prompts for creds
- [ ] Manual: `fullsend admin install <org> --yolo --session-file <path>` skips briefing
- [ ] Manual: `fullsend admin install <org> --no-playwright` uses manual flow
- [ ] Manual: `fullsend admin uninstall <org>` deletes apps and PAT via Playwright

Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```
