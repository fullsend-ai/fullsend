# E2E Testing

Guide for running and debugging fullsend admin e2e tests locally and in CI.

Related ADRs: [0010](../../ADRs/0010-stored-session-for-e2e-browser-auth.md) (browser
session), [0039](../../ADRs/0039-totp-automation-for-e2e-2fa.md) (2FA),
[0040](../../ADRs/0040-org-pool-for-parallel-e2e-tests.md) (org pool),
[0043](../../ADRs/0043-e2e-wif-shim-and-pr-authorization-gate.md) (CI gate + WIF).

Operators: see [E2E GCP setup](../infrastructure/e2e-gcp-setup.md).

## Goal: toward secretless e2e

Issue [#1604](https://github.com/fullsend-ai/fullsend/issues/1604) aims to let
authorized e2e runs proceed **without** defining repository secrets or
variables. That work is **not complete**. This change is one increment:

- **Done here:** WIF for GCP auth (no SA JSON keys); PR authorization gate;
  workflow defaults for GCP project, WIF provider, and service account (see
  [E2E GCP setup](../infrastructure/e2e-gcp-setup.md#canonical-resource-names)).
- **Still required:** `E2E_GITHUB_SESSION`, `E2E_MINT_URL`, password/TOTP
  secrets.

See [ADR 0043](../../ADRs/0043-e2e-wif-shim-and-pr-authorization-gate.md) for the
full delivered vs. outstanding breakdown.

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

## CI architecture

Pull requests trigger **E2E Shim** (`.github/workflows/e2e_shim.yml`) via
`pull_request_target` (base-branch workflow; no PR code checkout in the shim),
which calls the trusted **E2E Tests** workflow on `main` via `workflow_call`:

1. **Gate** — authorize the PR author or a fresh `ok-to-test` label
2. **GCP WIF auth** — short-lived credentials via `google-github-actions/auth`
3. **Tests** — checkout PR head, `make e2e-test` (GCP project, WIF provider,
   and service account default to the shared konflux e2e resources documented
   in [E2E GCP setup](../infrastructure/e2e-gcp-setup.md#canonical-resource-names)
   unless override secrets are set)

Pushes to `main` and `workflow_dispatch` run **E2E Tests** directly (gate
skipped).

## ok-to-test (same-repo PRs)

E2e runs automatically when the PR author is an org/repo **member or
collaborator**.

For other same-repo contributors, a maintainer must add the **`ok-to-test`**
label **after** reviewing the latest push. If new commits land after the label
was applied, CI removes the stale label and skips tests until it is re-applied.

Create the label if missing — see
[E2E GCP setup](../infrastructure/e2e-gcp-setup.md#4-github-repository-secrets-and-labels).

## Fork pull requests

Fork PRs use the same shim and gate as same-repo PRs. The shim runs from
`main` (`pull_request_target`) and inherits repository secrets, so authorized
fork PRs can run e2e when path filters match. First-time contributors may still
need maintainer approval before GitHub Actions runs on the PR.

## CI readiness checks

### Contributors

- Read the sticky **E2E tests did not run** comment (marker `<!-- e2e-gate -->`)
  on unauthorized PRs.
- **Secrets unavailable** warning — investigate repo secret configuration; not
  expected for authorized fork PRs once the shim is on `main`.
- Member/collaborator PRs should show **E2E Shim** → **E2E Tests** in the
  Actions tab when path filters match.

### Maintainers

- Run the [quick health check](../infrastructure/e2e-gcp-setup.md#quick-health-check)
  for GCP and GitHub secrets.
- `gh secret list --repo fullsend-ai/fullsend | grep E2E_` — session and mint
  secrets are still required; GCP project/WIF/SA secrets are optional (workflow
  defaults documented in [E2E GCP setup](../infrastructure/e2e-gcp-setup.md#canonical-resource-names)).
- Failed **Authenticate to GCP** — compare workflow log with WIF provider
  attribute condition in the setup guide.
- Test workflow changes on a branch via **workflow_dispatch** on **E2E Tests**
  before merging `e2e.yml` changes (PR shim always calls `@refs/heads/main`).

## Path filters

E2e CI runs when PRs touch Go code, e2e tests, scaffold, mint sources, Makefile,
or the e2e workflow/action files listed in `e2e_shim.yml`.
