# E2E Testing

Guide for running and debugging fullsend admin e2e tests locally and in CI.

Related ADRs: [0040](../../ADRs/0040-org-pool-for-parallel-e2e-tests.md) (org pool),
[0060](../../ADRs/0060-cross-org-mint-authorization-via-org-variables.md) (cross-org mint),
[0009](../../ADRs/0009-pull-request-target-in-shim-workflows.md) (pull_request_target security model for shims; e2e uses a separate gate pattern documented below).

Historical ADRs [0010](../../ADRs/0010-stored-session-for-e2e-browser-auth.md) (browser session) and
[0039](../../ADRs/0039-totp-automation-for-e2e-2fa.md) (2FA) are superseded for CI by cross-org mint
auth ([#2155](https://github.com/fullsend-ai/fullsend/issues/2155)); local runs no longer use
Playwright or stored sessions.

## Prerequisites

Before running e2e locally or in CI:

1. **Pool orgs** (`halfsend-01` … `halfsend-12`) provisioned per [Pool org provisioning](#pool-org-provisioning) below
2. **Mint** deployed with `e2e` role enrolled and `ALLOWED_ORGS` including `fullsend-ai`
3. **CI only:** pool orgs with `FULLSEND_FOREIGN_E2E_REPOS` authorizing `fullsend-ai/fullsend`
4. **Local only:** `gh auth login` (or `GH_TOKEN` / `GITHUB_TOKEN`) with admin access on pool orgs

## Local runs

1. Authenticate as an admin on the pool orgs (`gh auth login --web`, or export `GH_TOKEN`).
2. Run tests (uses `gh auth token`, `GH_TOKEN`, or `GITHUB_TOKEN`):

```bash
make e2e-test
```

Optional environment variables:

| Variable | Purpose |
|----------|---------|
| `GH_TOKEN` / `GITHUB_TOKEN` | Override token source for local runs |
| `FULLSEND_MINT_URL` | Override mint endpoint (default: hosted public mint, same as `fullsend admin --mint-url`) |
| `E2E_LOCK_TIMEOUT` | Max wait for a free pool org (default 10m) |
| `E2E_GCP_PROJECT_ID` | GCP project for inference setup (`github setup --inference-project`) |

Behaviour tests use the same pool orgs but install via `fullsend github setup` (per-repo) instead of `fullsend admin install`. See [behaviour-testing.md](behaviour-testing.md) and [behaviour-drivers.md](behaviour-drivers.md).

Tests acquire an exclusive lock on one org from the pool (`halfsend-01` …
`halfsend-12`) — see [ADR 0040](../../ADRs/0040-org-pool-for-parallel-e2e-tests.md).

## CI runs

In GitHub Actions, tests mint a cross-org installation token via the mint service:

1. Workflow requests a GHA OIDC token (`id-token: write`)
2. `mintclient.MintToken` POSTs to `{FULLSEND_MINT_URL or hosted default}/v1/token` with `{role: "e2e", target_org: "<pool org>"}` (repos omitted for installation-wide access)
3. Mint verifies the caller against `FULLSEND_FOREIGN_E2E_REPOS` on the target org ([ADR 0060](../../ADRs/0060-cross-org-mint-authorization-via-org-variables.md))

Required repository secrets:

| Secret | Purpose |
|--------|---------|
| `E2E_GCP_WIF_PROVIDER` | GCP WIF provider (inference / auxiliary GCP access) |
| `E2E_GCP_SERVICE_ACCOUNT` | GCP service account for WIF |
| `E2E_GCP_PROJECT_ID` | GCP project ID for inference secrets (`github setup --inference-project`) |

Mint URL uses the hosted public endpoint by default (same as `fullsend admin --mint-url`). Override with org/repo variable `FULLSEND_MINT_URL` if needed; no separate e2e secret.

### Behaviour tests and per-repo mint enrollment

Behaviour tests install fullsend in **per-repo** mode (`fullsend github setup`). Triage workflows on the pool org's `test-repo` mint same-org `triage` tokens from vendored reusable workflows; that requires per-repo mint enrollment (`PER_REPO_WIF_REPOS`). The install driver does **not** run `mint enroll` — pool org `test-repo` repos must be enrolled once by a GCP admin on the hosted mint project.

Inference (`E2E_GCP_PROJECT_ID`) and mint (`it-gcp-konflux-dev-fullsend` for the hosted mint) may be different GCP projects. The behaviour install driver runs `fullsend inference provision <org>/test-repo` using CI credentials on the inference project (same access model as admin e2e), then passes the repo-scoped WIF provider to `github setup`. `E2E_GCP_WIF_PROVIDER` authenticates the CI job itself; it is not written to pool org repos.

The CI service account needs inference-provision IAM on `E2E_GCP_PROJECT_ID`:

| IAM role | Purpose |
|----------|---------|
| `roles/iam.workloadIdentityPoolAdmin` | Create/update repo-scoped inference WIF providers |
| `roles/resourcemanager.projectIamAdmin` | Grant `roles/aiplatform.user` to repo WIF principals |

One-time enrollment for all pool orgs (idempotent):

```bash
export GCP_PROJECT=it-gcp-konflux-dev-fullsend
for i in $(seq -w 1 12); do
  fullsend mint enroll "halfsend-${i}/test-repo" --project="$GCP_PROJECT" --region=us-central1
done
```

Re-run enrollment when adding a new pool org or after mint infrastructure changes that drop `PER_REPO_WIF_REPOS` entries. See [mint-administration.md](../infrastructure/mint-administration.md) for required operator IAM.

## Pool org provisioning

Each pool org must be provisioned before e2e can use it:

1. Org exists with `botsend` as owner
2. `test-repo` and `e2e-lock` repos (lock created at runtime)
3. All role apps installed, including `fullsend-ai-e2e` with **Repository → Variables: Read and write** (`actions_variables`) and **Organization → Variables: Read and write** (`organization_actions_variables`)
4. `FULLSEND_FOREIGN_E2E_REPOS` includes `fullsend-ai/fullsend` with org-wide visibility (`visibility: all`)
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

Existing pool orgs (`halfsend-01` … `halfsend-12`) need a one-time operator pass: install the e2e app (if missing) and run:

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
membership for members with private visibility. (Note: agent dispatch paths use
the collaborator permission API instead, which does not have this limitation —
see [ADR 0054](../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md).)

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
