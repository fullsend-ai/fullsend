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
