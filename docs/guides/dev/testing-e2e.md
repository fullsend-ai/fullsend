# End-to-end tests

The e2e test suite exercises the full admin install/uninstall flow against a
live GitHub organization using [Playwright](https://playwright.dev/) browser
automation. This includes creating GitHub Apps via the manifest flow, installing
them into a test org, creating fine-grained PATs, and cleaning up afterwards.

These tests run from `e2e/admin/` with the `e2e` build tag.

## Prerequisites

You need a GitHub test account with owner access to at least one test
organization. The tests require credentials supplied via environment variables:

| Variable | Required | Description |
|---|---|---|
| `E2E_GITHUB_USERNAME` | Yes (local) | GitHub username for the test account |
| `E2E_GITHUB_PASSWORD` | Yes | Account password (or use `E2E_GITHUB_PASSWORD_FILE`) |
| `E2E_GITHUB_PASSWORD_FILE` | Alt | Path to a file containing the password (useful in environments where secrets are mounted as files) |
| `E2E_GITHUB_SESSION_FILE` | CI | Path to a pre-exported Playwright session file (bypasses login) |
| `E2E_GITHUB_TOTP_SECRET` | 2FA | Base32-encoded TOTP secret for 2FA-enabled accounts |

When `E2E_GITHUB_SESSION_FILE` is set, the tests skip login and load the stored
session directly. When only username and password are provided, `make e2e-test`
auto-generates a session file before running.

## Running locally

**Run the tests:**

```bash
make e2e-test
```

This target:

1. Installs Playwright Chromium if not already cached.
2. If no session file is set but credentials are available, generates one
   via `make e2e-export-session`.
3. Runs `go test -tags e2e -v -count=1 -timeout 30m ./e2e/admin/`.

**Export a session file manually:**

```bash
make e2e-export-session
```

Logs into GitHub using `E2E_GITHUB_USERNAME` and `E2E_GITHUB_PASSWORD` (and
`E2E_GITHUB_TOTP_SECRET` if 2FA is enabled), then saves the Playwright
session to `.playwright/session.json`.

**Upload a session to CI:**

```bash
make e2e-upload-session
```

Runs `e2e-export-session`, then base64-encodes the session file and uploads
it as the `E2E_GITHUB_SESSION` repository secret via `gh`.

Run `make help` for all available targets.

## How CI runs e2e tests

GitHub Actions cannot log in to github.com with a password from datacenter
IPs -- GitHub blocks this as an anti-credential-stuffing measure. CI uses a
stored Playwright session to bypass the login form entirely.

**Authentication flow in CI:**

1. The `E2E_GITHUB_SESSION` repo secret (base64-encoded Playwright
   `storageState` JSON) is decoded and written to a file.
2. The test loads it via Playwright's `StorageStatePath` option, starting the
   browser already authenticated.
3. For sudo prompts (e.g., creating PATs), the test enters the password or a
   TOTP code. Unlike login, sudo confirmation works from datacenter IPs.

**Session maintenance:** GitHub's `user_session` cookie uses a rolling
expiration of approximately two weeks. As long as CI runs at least once every
two weeks, the session stays valid. If it expires, a developer runs
`make e2e-upload-session` to refresh it.

**Required CI secrets:**

- `E2E_GITHUB_SESSION` -- base64-encoded storageState JSON
- `E2E_GITHUB_PASSWORD` -- for sudo confirmation on sensitive pages
- `E2E_GITHUB_TOTP_SECRET` -- required when the test account has 2FA enabled

## Parallel execution with org pools

To avoid serializing CI runs, e2e tests use a pool of identically-configured
GitHub organizations (e.g., `halfsend-01` through `halfsend-06`). Each run
acquires exclusive access to one org using a lightweight distributed lock
(an `e2e-lock` repo created atomically in the target org).

- **Acquisition:** The test runner shuffles the pool, attempts to create the
  `e2e-lock` repo in each org. If all are locked, it polls every 30 seconds
  up to `E2E_LOCK_TIMEOUT` (default 10 minutes).
- **Release:** On completion (pass or fail), the runner deletes the lock repo
  after verifying ownership via a UUID stored in the repo.
- **Stale locks:** Locks older than 15 minutes are considered stale and
  force-acquired, so crashed runs self-heal.

Adding orgs to the pool requires provisioning the org (with the test account
as owner and a `test-repo`) and appending its name to the `orgPool` slice.

## Architectural decisions

The e2e testing infrastructure is documented in these ADRs:

- [ADR 0010: Stored browser session for e2e authentication in CI](../../ADRs/0010-stored-session-for-e2e-browser-auth.md) --
  why stored sessions are used and how they work
- [ADR 0039: TOTP automation for e2e 2FA](../../ADRs/0039-totp-automation-for-e2e-2fa.md) --
  automated TOTP entry for 2FA-enabled test accounts
- [ADR 0040: Org pool for parallel e2e tests](../../ADRs/0040-org-pool-for-parallel-e2e-tests.md) --
  distributed locking across a pool of test organizations
