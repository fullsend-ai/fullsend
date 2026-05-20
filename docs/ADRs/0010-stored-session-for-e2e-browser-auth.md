---
title: "10. Stored browser session for e2e authentication in CI"
status: Accepted
relates_to:
  - testing-agents
topics:
  - e2e
  - ci
  - authentication
  - playwright
---

# 10. Stored browser session for e2e authentication in CI

Date: 2026-04-03

## Status

Accepted

Extended by [ADR 0039](0039-totp-automation-for-e2e-2fa.md).

## Context

The admin CLI e2e tests use Playwright to automate browser interactions with
github.com: creating GitHub Apps via the manifest flow, installing them into the
test org, creating fine-grained PATs, and deleting apps during cleanup. These
operations have no pure REST API equivalent -- the manifest flow inherently
requires a browser-based user interaction.

The tests worked locally but consistently failed in GitHub Actions CI. After
investigation (screenshots, form HTML dumps, CAPTCHA detection, hex-level
password verification), we confirmed:

- The correct password is filled into the login form (25 chars, verified via
  field length and hex dump).
- No CAPTCHA or device verification challenge is presented.
- GitHub responds with "Incorrect username or password" despite the credentials
  being correct.

GitHub blocks password-based login from its own Actions runner IP ranges (Azure
datacenter IPs). This is a known pattern: Google, Auth0, and other auth
providers similarly block or challenge logins from datacenter IPs. The
Playwright project documents this as a common CI failure mode and recommends
`storageState` as the standard solution.

## Decision

Use Playwright's `storageState` mechanism to inject a pre-authenticated browser
session into the e2e test's browser context, bypassing the login form entirely.

**How it works:**

1. A developer logs into github.com as the `botsend` test account locally and
   exports the browser session (cookies, localStorage) to a JSON file using
   Playwright's `browserContext.storageState()`.
2. The JSON is base64-encoded and stored as the `E2E_GITHUB_SESSION` repository
   secret.
3. The e2e workflow decodes the secret, writes it to a file, and the test loads
   it via `browser.NewContext(playwright.BrowserNewContextOptions{StorageStatePath: path})`.
4. The browser context starts already authenticated -- no login form interaction
   needed.

**Why both a stored session AND a password are required in CI:**

GitHub has two distinct authentication gates in the browser, and they behave
differently from datacenter IPs:

1. **Login** (`/login`) -- the initial authentication form. GitHub blocks
   password-based login from Actions runner IPs (Azure datacenter ranges),
   rejecting correct credentials with "Incorrect username or password". The
   stored session bypasses this entirely.

2. **Sudo** (`/sessions/sudo`, titled "Confirm access") -- a re-authentication
   prompt that GitHub presents when an already-authenticated session accesses
   sensitive pages like `/settings/tokens/new` or
   `/settings/personal-access-tokens/new`. Unlike login, sudo confirmation
   *does* accept passwords from datacenter IPs. This makes sense: sudo is
   verifying the identity of an already-authenticated session, not performing
   initial authentication, so it is not subject to the same anti-credential-
   stuffing protections.

The e2e tests need both because:

- The **session** gets past login (which blocks passwords from CI).
- The **password** gets past sudo (which the session alone cannot satisfy,
  since sudo confirmation expires in ~2 hours and cannot be meaningfully
  baked into the stored session).

The `handleSudoIfPresent()` function detects the "Confirm access" page by
title and enters either the password or a TOTP code (see [ADR 0039](0039-totp-automation-for-e2e-2fa.md)).
It is called before PAT creation (both classic and fine-grained).

**Local development:** When `E2E_GITHUB_SESSION_FILE` is not set but
`E2E_GITHUB_USERNAME` and `E2E_GITHUB_PASSWORD` are, `make e2e-test`
automatically generates a session file by logging in via Playwright. This
works from developer machines (non-datacenter IPs) and means developers
don't need to manually export sessions for local testing.

**Make targets:**

- `make e2e-export-session` -- logs into GitHub using `E2E_GITHUB_USERNAME`
  and `E2E_GITHUB_PASSWORD`, exports the session to `.playwright/session.json`.
- `make e2e-upload-session` -- runs `e2e-export-session`, then base64-encodes
  and uploads the session as the `E2E_GITHUB_SESSION` repo secret via `gh`.
- `make e2e-test` -- if `E2E_GITHUB_SESSION_FILE` is unset but username/password
  are available, auto-generates a session before running tests.

**Session expiration:** GitHub's `user_session` cookie uses a rolling expiration
of approximately two weeks. As long as the session is used at least once every
two weeks (which any active repo's CI will do), it stays valid indefinitely. If
it does expire, a developer runs `make e2e-upload-session` to refresh it.

**Alternatives considered:**

1. **Login from CI via Playwright** -- blocked by GitHub's datacenter IP
   restrictions. This is what we tried first. Rejected.
2. **Self-hosted runner** -- would allow login from a non-datacenter IP, but
   requires infrastructure that doesn't exist yet and adds operational burden
   for a test-only concern. Rejected for now; revisit if the stored session
   approach proves too fragile.
3. **Pre-create GitHub Apps manually, skip manifest flow in CI** -- would test
   less of the real user flow. The manifest flow and app installation are core
   to what the admin CLI does; not testing them defeats the purpose of e2e
   tests. Rejected.
4. **GitHub larger runners with static IPs** -- paid feature, may or may not
   bypass the login restriction (untested), and adds cost. Rejected.
5. **Device code flow / OAuth** -- GitHub's device code flow requires manual
   user interaction (entering a code at github.com/login/device) and has been
   restricted due to phishing abuse. Not automatable in CI. Rejected.

## Consequences

- Two repo secrets are required in CI: `E2E_GITHUB_SESSION` (base64-encoded
  storageState JSON for login bypass) and `E2E_GITHUB_PASSWORD` (for sudo
  confirmation on sensitive pages). A third secret, `E2E_GITHUB_TOTP_SECRET`,
  is required when the test account has 2FA enabled (see [ADR 0039](0039-totp-automation-for-e2e-2fa.md)).
- If the test account's password changes, the stored session must be
  re-exported (password change invalidates all sessions) and the
  `E2E_GITHUB_PASSWORD` secret must be updated.
- If the test account has 2FA enabled, set `E2E_GITHUB_TOTP_SECRET` so
  `make e2e-export-session` and sudo confirmation can handle TOTP automatically.
- The login function becomes a session-loading function -- simpler and more
  reliable.
- Session refresh is `make e2e-upload-session`, expected to be needed at most
  every two weeks (and less often on active repos).
- Locally, developers can use username/password directly — `make e2e-test`
  auto-generates a session file when credentials are available.
