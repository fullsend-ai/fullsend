# E2E Testing

Guide for running and debugging fullsend admin e2e tests locally and in CI.

Related ADRs: [0040](../../ADRs/0040-org-pool-for-parallel-e2e-tests.md) (org pool),
[0051](../../ADRs/0051-cross-org-mint-authorization-via-org-variables.md) (cross-org mint),
[0009](../../ADRs/0009-pull-request-target-in-shim-workflows.md) (pull_request_target security model for shims; e2e uses a separate gate pattern documented below).

Historical ADRs [0010](../../ADRs/0010-stored-session-for-e2e-browser-auth.md) (browser session) and
[0039](../../ADRs/0039-totp-automation-for-e2e-2fa.md) (2FA) are superseded for CI by cross-org mint
auth ([#2155](https://github.com/fullsend-ai/fullsend/issues/2155)); local runs no longer use
Playwright or stored sessions.

## Local runs

```bash
# Authenticate as an admin on the pool orgs
gh auth login --web

# Run tests (uses gh auth token, GH_TOKEN, or GITHUB_TOKEN)
make e2e-test
```

Optional environment variables:

| Variable | Purpose |
|----------|---------|
| `GH_TOKEN` / `GITHUB_TOKEN` | Override token source for local runs |
| `E2E_LOCK_TIMEOUT` | Max wait for a free pool org (default 10m) |
| `E2E_GCP_PROJECT_ID` | GCP project for inference-related setup (if needed) |

Tests acquire an exclusive lock on one org from the pool (`halfsend-01` …
`halfsend-06`) — see [ADR 0040](../../ADRs/0040-org-pool-for-parallel-e2e-tests.md).

## CI runs

In GitHub Actions, tests mint a cross-org installation token via the mint service:

1. Workflow requests a GHA OIDC token (`id-token: write`)
2. `mintclient.MintToken` POSTs to `E2E_MINT_URL/v1/token` with `{role: "e2e", target_org: "<pool org>", repos: ["test-repo", ".fullsend", "e2e-lock"]}`
3. Mint verifies the caller against `FULLSEND_FOREIGN_E2E_REPOS` on the target org ([ADR 0051](../../ADRs/0051-cross-org-mint-authorization-via-org-variables.md))

Required repository secrets:

| Secret | Purpose |
|--------|---------|
| `E2E_MINT_URL` | Mint service base URL |
| `E2E_GCP_WIF_PROVIDER` | GCP WIF provider (inference / auxiliary GCP access) |
| `E2E_GCP_SERVICE_ACCOUNT` | GCP service account for WIF |
| `E2E_GCP_PROJECT_ID` | GCP project ID |

If `E2E_MINT_URL` is unset, the e2e job skips with a warning.

## Pool org provisioning

Each pool org must be provisioned before e2e can use it:

1. Org exists with `botsend` as owner
2. `test-repo` and `e2e-lock` repos (lock created at runtime)
3. All role apps installed, including `fullsend-ai-e2e`
4. `FULLSEND_FOREIGN_E2E_REPOS` includes `fullsend-ai/fullsend` (authorizes CI workflows)
5. Mint enrolled: org in `ALLOWED_ORGS`, `${ORG}/e2e` in `ROLE_APP_IDS`, e2e app PEM enrolled

Use the idempotent setup script:

```bash
MINT_PROJECT=... MINT_FUNCTION=... hack/setup-new-e2e-org.sh 07
```

Verify foreign authorization:

```bash
fullsend admin foreign list --org halfsend-01
# expect e2e → fullsend-ai/fullsend
```

Existing pool orgs (`halfsend-01` … `halfsend-06`) need a one-time operator pass: install the e2e app (if missing) and run:

```bash
fullsend admin foreign allow --org halfsend-NN --role e2e --caller fullsend-ai/fullsend
```

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
2. **E2E** — checkout PR head SHA, authenticate to GCP via WIF, mint cross-org
   tokens per pool org, `make e2e-test`

Pushes to `main`, merge queue, and `workflow_dispatch` skip the gate and run e2e
directly.
