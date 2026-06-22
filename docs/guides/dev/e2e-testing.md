# E2E Testing

Guide for running and debugging fullsend admin e2e tests locally and in CI.

Related ADRs: [0010](../../ADRs/0010-stored-session-for-e2e-browser-auth.md) (browser
session), [0039](../../ADRs/0039-totp-automation-for-e2e-2fa.md) (2FA),
[0040](../../ADRs/0040-org-pool-for-parallel-e2e-tests.md) (org pool),
[0009](../../ADRs/0009-pull-request-target-in-shim-workflows.md) (pull_request_target security model for shims; e2e uses a separate gate pattern documented below).

## Local runs

```bash
# Export a Playwright session (once per session expiry)
make e2e-export-session

# Run tests (uses E2E_GITHUB_SESSION_FILE or credentials from env)
make e2e-test

# Upload session to GitHub repo secret (maintainers)
make e2e-upload-session
```

Required environment variables are documented in the `Makefile` help (`make help`).

Tests acquire an exclusive lock on one org from the pool (`halfsend-01` …
`halfsend-06`) — see [ADR 0040](../../ADRs/0040-org-pool-for-parallel-e2e-tests.md).

## CI authorization

Pull requests trigger e2e via `pull_request_target` in
[`.github/workflows/e2e.yml`](../../../.github/workflows/e2e.yml) so fork PRs can
use repository secrets. Because that exposes credentials to untrusted code, a
**gate job** runs first (see workflow comments for why it is a separate job).

### Who runs automatically

E2E tests run without maintainer action when the PR author is an org/repo
**member** or **collaborator** (`author_association` of `OWNER`, `MEMBER`, or
`COLLABORATOR` on the base repo). The gate uses the frozen
`github.event.pull_request.author_association` from the workflow event — not a
live REST lookup — because `GITHUB_TOKEN` lacks `read:org` and cannot see org
membership for members with private visibility.

### Who needs `ok-to-test`

External contributors and fork PR authors must have a maintainer apply the
**`ok-to-test`** label **after** the latest push. The label must be created once
in GitHub repo settings (Settings → Labels).

### Stale labels

If new commits are pushed after `ok-to-test` was applied, the label is removed
automatically and e2e is skipped until a maintainer re-applies it after
reviewing the latest changes. Freshness compares the label timestamp against
the frozen PR `updated_at` from the workflow event (`PR_UPDATED_AT`); the live
API fallback may over-reject when non-push activity bumped `updated_at`.
Applying the label triggers immediate authorization on `labeled` events.

### Blocked runs

When e2e does not run, a sticky PR comment (marker `<!-- e2e-gate -->`) explains
why and what to do. Re-run the workflow or add/re-apply `ok-to-test` as
appropriate.

## CI architecture

1. **Gate** — authorize the PR author or a fresh `ok-to-test` label (base
   checkout only; never checks out PR head)
2. **E2E** — checkout PR head SHA, authenticate to GCP via WIF, `make e2e-test`

Pushes to `main`, merge queue, and `workflow_dispatch` skip the gate and run e2e
directly.
