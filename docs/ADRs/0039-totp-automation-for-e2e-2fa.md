---
title: "39. TOTP automation for e2e 2FA"
status: Accepted
relates_to:
  - testing-agents
topics:
  - e2e
  - ci
  - authentication
  - 2fa
---

# 39. TOTP automation for e2e 2FA

Date: 2026-05-19

## Status

Accepted

Extends [ADR 0010](0010-stored-session-for-e2e-browser-auth.md).

## Context

[ADR 0010](0010-stored-session-for-e2e-browser-auth.md) established stored
Playwright sessions for e2e CI authentication. When that ADR was written, the
test account did not have 2FA enabled. With 2FA on, two new authentication
prompts appear that the existing infrastructure could not handle:

1. A TOTP challenge during `make e2e-export-session` (local login).
2. A TOTP challenge on sudo pages in CI (GitHub may present TOTP instead of
   a password field for 2FA-enabled accounts).

## Decision

Automate TOTP entry in Playwright using a shared `e2e/internal/otp` package
that wraps `pquerna/otp` to generate time-based codes. The TOTP secret is
supplied via the `E2E_GITHUB_TOTP_SECRET` environment variable (base32-encoded).

- `handleTOTPIfPresent()` in the login helpers detects TOTP form fields and
  enters the current code. `handleSudoIfPresent()` now handles both password
  and TOTP sudo prompts.
- The `export-session` command detects and completes the 2FA prompt after
  password login.
- `E2E_GITHUB_TOTP_SECRET` is optional — omitting it preserves the existing
  non-2FA flow.

## Consequences

- Three repo secrets are now required in CI when the test account has 2FA:
  `E2E_GITHUB_SESSION`, `E2E_GITHUB_PASSWORD`, and `E2E_GITHUB_TOTP_SECRET`.
- Session export and sudo confirmation work automatically for 2FA accounts.
- The TOTP secret is a long-lived credential that must be protected like the
  password.
- If GitHub changes its TOTP form markup, the Playwright selectors will need
  updating.
