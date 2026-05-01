# Auto Install with Icons (`--auto` mode)

Add `--auto` mode to `fullsend admin install` that drives a Playwright browser through the entire install flow, including uploading role-specific icons as GitHub App logos.

## Problem

GitHub has no REST API for uploading GitHub App logos. The only way to set an app's logo is through the GitHub web UI. Our earlier attempt to use `PATCH /app` with multipart form data returned 404 — that endpoint doesn't exist.

The manifest flow's confirmation page (where the user clicks "Create GitHub App") has a logo upload field, but the current CLI just opens the URL and waits for the user to interact manually. We need browser automation to programmatically set the file input before the form is submitted.

## Design

### New flag: `--auto --session-file <path>`

Add `--auto` and `--session-file` flags to the `install` command. When `--auto` is set:

- `--session-file` is required (hard error if missing)
- The CLI launches a Playwright browser with the provided session's storage state
- All browser interactions (app creation, logo upload, app installation) are automated
- No human interaction needed

Without `--auto`, behavior is unchanged: the CLI opens URLs with `xdg-open`/`open` and the user clicks through manually. Logos are not uploaded in this mode.

### PlaywrightBrowserOpener in production code

Move browser automation from `e2e/admin/browser.go` (build-tagged `e2e`) into `internal/appsetup/playwright.go` (no build tag). This implements the existing `BrowserOpener` interface:

```go
type PlaywrightBrowserOpener struct {
    page playwright.Page
    ui   *ui.Printer
}

func (b *PlaywrightBrowserOpener) Open(ctx context.Context, url string) error
```

The `Open` method handles three URL patterns:
1. **Local manifest form** (`127.0.0.1`) — fetch HTML, extract manifest, submit from GitHub's origin (same as current e2e code)
2. **App creation page** (`/settings/apps/new` or `/settings/apps/manifest`) — upload the logo via the file input, then click "Create GitHub App"
3. **Installation page** (`/installations/new`) — click "Install", retry on 404

### Logo upload via file input

On the app creation confirmation page, before clicking "Create GitHub App":

1. Locate the logo file input (`input[type="file"]` in the display information section)
2. Write the embedded PNG to a temp file
3. Set the file input via Playwright's `SetInputFiles`
4. Proceed with clicking "Create GitHub App"

The role name is passed through to the `PlaywrightBrowserOpener` so it can look up the right icon via `ghTypes.IconForRole(role)`. This requires extending the `BrowserOpener` interface or passing the role as context. The simplest approach: add a `SetRole(role string)` method to the `PlaywrightBrowserOpener` that the appsetup code calls before `Open`. The `BrowserOpener` interface stays unchanged; `SetRole` is type-asserted when available.

### CLI wiring

In `internal/cli/admin.go`, the `newInstallCmd` function:

1. Add `--auto` (bool) and `--session-file` (string) flags
2. If `--auto` is set, validate `--session-file` is provided and the file exists
3. Launch Playwright, load the session, create a page
4. Construct `PlaywrightBrowserOpener` instead of `DefaultBrowser`
5. Pass it to `appsetup.NewSetup` as usual

Playwright lifecycle (start, browser launch, context, page) is managed in the CLI command's `RunE`. Cleanup via deferred calls.

### Error handling

- Logo upload failure (file input not found, icon missing): **hard error**, halts install
- Transient errors (page load timeout, 404 on fresh app): retry with backoff
- Non-transient errors (session expired, element missing): halt with clear message
- `--auto` without `--session-file`: hard error at flag validation

### Remove broken API-based logo upload

Delete the files and code from the earlier approach that used `PATCH /app`:

- Delete `internal/appsetup/jwt.go` and `jwt_test.go`
- Delete `internal/appsetup/logo.go` and `logo_test.go`
- Remove the logo upload block from `runManifestFlow` in `appsetup.go`

### Testing

**Unit tests:**
- `internal/appsetup/playwright_test.go` — test URL routing logic (which handler is called for which URL pattern)

**E2e tests:**
- Existing test (`TestAdminInstallUninstall`): unchanged, continues to use the e2e `PlaywrightBrowserOpener` in `e2e/admin/browser.go` to test the manual flow
- New test (`TestAdminInstallAuto`): runs the `fullsend` binary as a subprocess with `--auto --session-file`, verifies apps are created with correct logos by checking the app's `avatar_url` via the API. No Playwright assistance from the test driver — the binary drives its own browser.

## Files changed

| File | Change |
|------|--------|
| `internal/appsetup/playwright.go` | New — production PlaywrightBrowserOpener with logo upload |
| `internal/appsetup/playwright_test.go` | New — unit tests for URL routing |
| `internal/appsetup/appsetup.go` | Modified — remove broken logo upload from runManifestFlow, add SetRole type assertion |
| `internal/cli/admin.go` | Modified — add --auto and --session-file flags, Playwright lifecycle |
| `internal/appsetup/jwt.go` | Deleted |
| `internal/appsetup/jwt_test.go` | Deleted |
| `internal/appsetup/logo.go` | Deleted |
| `internal/appsetup/logo_test.go` | Deleted |
| `e2e/admin/admin_test.go` | Modified — add TestAdminInstallAuto |

## Out of scope

- Headless vs headed mode toggle (always headed for now; headless can be added later)
- Sudo/2FA handling in `--auto` mode (session file should already be fully authenticated)
- Removing Playwright from e2e tests (the existing e2e browser code stays; it tests the non-auto path)
