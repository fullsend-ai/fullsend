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
| `E2E_GCP_PROJECT_ID` | GCP project for inference setup and behaviour per-repo mint enroll (must be the mint project when using the hosted mint) |

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
| `E2E_GCP_MINT_PROJECT_ID` | Optional override for `mint enroll --project` (default: hosted mint project when using public mint URL) |

Mint URL uses the hosted public endpoint by default (same as `fullsend admin --mint-url`). Override with org/repo variable `FULLSEND_MINT_URL` if needed; no separate e2e secret.

### Behaviour tests and per-repo mint enrollment

Behaviour tests install fullsend in **per-repo** mode (`fullsend github setup`) and run `fullsend mint enroll <org>/test-repo` so vendored reusable workflows can mint same-org `triage` tokens (`PER_REPO_WIF_REPOS`). This is the first CI workflow that exercises per-repo mint enrollment; admin e2e only needs org-level cross-org `e2e` mint auth.

The CI service account (`E2E_GCP_SERVICE_ACCOUNT`, impersonated via `E2E_GCP_WIF_PROVIDER`) must have these roles on the **mint GCP project** — not necessarily the same project as `E2E_GCP_PROJECT_ID` (inference). Behaviour install resolves the mint project via `E2E_GCP_MINT_PROJECT_ID`, or defaults to `it-gcp-konflux-dev-fullsend` when using the hosted mint URL.

In practice the e2e workflow often authenticates as a **WIF external account** (not a service-account key). Grant the roles below to the value of `E2E_GCP_SERVICE_ACCOUNT` *and/or* the WIF `principalSet` for `fullsend-ai/fullsend` on the pool named in `E2E_GCP_WIF_PROVIDER` (commonly `fullsend-pool/providers/github-oidc`). Gen2 mint discovery uses the Cloud Run API (`run.services.get`), not Cloud Functions — `roles/run.admin` is required; `roles/cloudfunctions.viewer` alone is insufficient for WIF principals.

| IAM role | Purpose |
|----------|---------|
| `roles/cloudfunctions.viewer` | Discover mint (`fullsend-mint`) during `mint enroll` |
| `roles/run.admin` | Update Cloud Run env vars (`ALLOWED_ORGS`, `PER_REPO_WIF_REPOS`) |
| `roles/iam.workloadIdentityPoolAdmin` | Create/update repo-scoped WIF providers |
| `roles/resourcemanager.projectIamAdmin` | Grant `roles/aiplatform.user` to per-repo WIF principals |

Hosted mint example (project `it-gcp-konflux-dev-fullsend`). Grant roles on the **same** service account stored in the `E2E_GCP_SERVICE_ACCOUNT` repository secret (commonly `github-fullsend-ai-fullsen-966@…` or `github-fullsend-ai-fullsend-ci@…`):

```bash
export GCP_PROJECT=it-gcp-konflux-dev-fullsend
export E2E_SA="<value of E2E_GCP_SERVICE_ACCOUNT secret>"

for ROLE in \
  roles/cloudfunctions.viewer \
  roles/run.admin \
  roles/iam.workloadIdentityPoolAdmin \
  roles/resourcemanager.projectIamAdmin; do
  gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
    --member="serviceAccount:${E2E_SA}" \
    --role="$ROLE"
done
```

`mint enroll` is idempotent — behaviour install can re-run safely when a pool org's `test-repo` is already enrolled.

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
