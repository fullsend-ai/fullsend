# Playwright as Default Installer Mechanism

Supersedes: [2026-05-01-auto-install-with-icons-design.md](2026-05-01-auto-install-with-icons-design.md)

## Problem

GitHub provides no REST API for uploading App logos or creating fine-grained personal access tokens. The original installer opened the system browser and asked the user to perform these steps manually — a multi-step, error-prone process involving 10+ instructions for PAT creation alone. Icons could not be uploaded at all without browser interaction.

This forced the installer into a hybrid model: API calls for what GitHub supports, manual browser interaction for everything else. Users had to context-switch between terminal and browser repeatedly during a single install.

## Decision

Playwright browser automation is the default mechanism for `fullsend admin install` and `fullsend admin uninstall`. The installer generates an ephemeral browser session, performs all GitHub UI interactions automatically, and cleans up the session on exit.

Users who prefer manual interaction can pass `--no-playwright` to fall back to the original flow.

## CLI Flags

### Install

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--no-playwright` | bool | `false` | Fall back to manual browser flow |
| `--session-file` | string | (none) | Use a pre-existing session file; not deleted afterward |
| `--yolo` | bool | `false` | Skip the briefing gate and acknowledgment prompt |

Removed: `--auto` (Playwright is now the default; `--no-playwright` is the opt-out).

### Uninstall

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--no-playwright` | bool | `false` | Fall back to manual browser flow |
| `--session-file` | string | (none) | Use a pre-existing session file; not deleted afterward |
| `--yolo` | bool | `false` | Skip confirmation prompt |

### Export-session

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--session-file` | string | `~/.config/fullsend/session.json` | Path to write session file |

### Flag interaction rules

- `--no-playwright` + `--session-file` = error (contradictory)
- `--yolo` works with both Playwright and `--no-playwright` modes
- `--no-playwright` implies the original manual flow (`DefaultBrowser{}` opener, stdin prompts)

## Session Lifecycle

### Default flow (no `--session-file`)

1. Installer starts, detects no session file provided.
2. Prompts user for GitHub credentials (check `GITHUB_USERNAME`/`GITHUB_PASSWORD` env vars first, then interactive prompt if not set).
3. Launches a headed Playwright browser and logs in.
4. If 2FA is required, the user interacts with the browser window (2-minute timeout).
5. Exports session to a temp file (`os.CreateTemp`).
6. Registers a `defer` that deletes the temp file and logs: `"Ephemeral session file deleted: <path>"`.
7. The defer fires on success AND failure — the session never persists.
8. If session generation fails (bad credentials, 2FA timeout), error out before any install work begins.

### Explicit `--session-file <path>` flow

1. Session file must already exist (hard error if not).
2. Used as-is, no generation step.
3. NOT deleted afterward.
4. At the end (success or failure), print a warning with the path:
   ```
   WARNING: Your session file at <path> contains active GitHub credentials.
   WARNING: Delete it when you no longer need it:  rm <path>
   ```

## Briefing Gate

When Playwright mode is active and `--yolo` is NOT set, the installer pauses after initial validation (org name, token, GCP flags) but before any Playwright or app-setup work begins.

The briefing explains what the installer will do, mentions `--no-playwright` as an alternative, and waits for the user to press Enter. `--yolo` skips the briefing and the wait.

Content (install):
```
Playwright Browser Automation

The installer will open a browser and act on your behalf to set up
GitHub Apps for fullsend. Here's what it will do:

  - Create N GitHub Apps (one per agent role)
  - Upload an icon for each app
  - Install each app on the org
  - Create a fine-grained PAT for workflow dispatch

This is a one-time setup to lay down the app scaffolding. Unfortunately,
GitHub doesn't offer APIs for app logo upload or PAT creation, so
browser automation is the only way to do this without manual steps.

If you'd prefer to do this manually, re-run with --no-playwright
and follow the interactive prompts instead.

Press Enter to continue, or Ctrl-C to abort.
```

The `N` is computed from the `--agents` flag (default 4).

For uninstall, a similar briefing with "delete N GitHub Apps" and "delete dispatch PAT".

## `--no-playwright` Behavior

Falls back to the original manual flow from `origin/main`:

- **App setup:** `DefaultBrowser{}` opener — `xdg-open`/`open` for each URL. User interacts in their own browser.
- **Logo upload:** Skipped. No `LogoUploader` interface satisfied. Apps get GitHub's default identicon.
- **PAT creation:** `promptDispatchToken` flow — opens pre-filled PAT URL in system browser, user configures permissions manually, pastes token into terminal.
- **Uninstall app deletion:** Opens each app's advanced settings page in system browser, user clicks "Delete GitHub App" manually.

No Playwright dependency is loaded. No session file is needed.

## PR and Commit Plan

### PR 1: Icons (separate, independent)

Single commit: embed PNGs via `go:embed`, add `IconForRole` lookup, delete broken API-based upload code (`jwt.go`, `logo.go`, and their tests).

### PR 2: Automated Installer

**Commit 1: `feat: add Playwright-driven app setup, PAT automation, and session export`**

All new Playwright code as opt-in `--auto` mode. Purely additive — no existing behavior changes.

- `internal/appsetup/playwright.go` — `PlaywrightBrowserOpener` implementing `BrowserOpener`, `RoleAware`, `LogoUploader`, `AppDeleter`
- `internal/appsetup/playwright_pat.go` — `CreateDispatchPAT`, `DeleteDispatchPAT` methods
- `internal/appsetup/appsetup.go` — `RoleAware`, `LogoUploader`, `AppDeleter`, `PATCreator`, `PATDeleter` interfaces
- `internal/cli/admin.go` — `--auto`/`--session-file` flags on install and uninstall, `export-session` subcommand, Playwright lifecycle in `RunE`

**Commit 2: `feat: make Playwright the default with ephemeral sessions`**

The behavioral change. If the default-flip causes problems, revert this commit and all automation remains available via `--auto`.

- Remove `--auto` flag, add `--no-playwright`
- Add `--yolo` to install command
- Inline session generation with ephemeral cleanup (`defer` delete + log path)
- Briefing gate before Playwright work
- Session file warning when `--session-file` is explicit
- `export-session` gets default path `~/.config/fullsend/session.json`

**Commit 3: `docs: ADR-0026 Playwright as default installer mechanism`**

- New ADR-0026
- Forward-reference note on ADR-0010: "See also ADR-0026, which extends stored sessions to the production installer (ephemeral by default)."
- Forward-reference note on ADR-0014: "See also ADR-0026, which specifies browser automation for app creation, logo upload, and PAT lifecycle."
- "Superseded by" header on old design spec (2026-05-01-auto-install-with-icons-design.md)

## Related ADRs

- **ADR-0010** (stored session for e2e browser auth) — Supplements. ADR-0010 covers e2e test auth; this design covers production installer auth. Same `storageState` mechanism, different lifecycle (ephemeral by default in production, persistent in e2e).
- **ADR-0014** (GitHub Apps and secrets) — Extends. ADR-0014 specifies the credential surface; this design specifies how browser automation drives app creation, logo upload, and PAT lifecycle.
