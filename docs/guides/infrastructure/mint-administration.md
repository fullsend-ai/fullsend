# Mint service administration

> Background: [How fullsend authenticates](../user/how-fullsend-authenticates.md).

This guide covers deploying and managing the fullsend token mint. The mint is the OIDC token exchange service that lets GitHub Actions workflows authenticate as GitHub Apps — it is infrastructure that serves all enrolled organizations and repositories. The mint can be deployed on GCP (Cloud Function) or Cloudflare (Worker).

| Command | Description |
|---------|-------------|
| `mint deploy` | Deploy or update the token mint (GCP Cloud Function or Cloudflare Worker) |
| `mint delete` | Tear down mint infrastructure (Cloud Function, secrets, SA, WIF pool or Worker) |
| `mint add-role` | Add an agent role (PEM secret + `ROLE_APP_IDS` entry) |
| `mint remove-role` | Remove an agent role from the mint (deletes PEM secret by default) |
| `mint enroll` | Register a repo in `PER_REPO_WIF_REPOS` and create its WIF provider |
| `mint unenroll` | Remove a repo from the mint |
| `mint workflow-host add` | Add a repo to the workflow-host allow-list |
| `mint workflow-host remove` | Remove a repo from the workflow-host allow-list |
| `mint workflow-host list` | List the workflow-host allow-list |
| `mint status` | Inspect mint health, enrolled repos, and PEM secrets |
| `mint token` | Exchange a GitHub Actions OIDC token for an installation token |

> **This guide is for platform operators** who deploy, manage, or troubleshoot the token mint. If you are an end user setting up fullsend for your organization, see [Getting Started](../getting-started/) instead — the mint is typically deployed once by a platform operator, and repositories are enrolled as needed.

## Hosted mint

The fullsend team operates a public hosted community mint service. Install the shared public GitHub Apps and use the CLI defaults — no separate enrollment step is required.

**Platform GCP project:** The hosted mint currently runs in GCP project `it-gcp-konflux-dev-fullsend` (region `us-central1`).

**Mint URL:**

```
https://mint.fullsend.sh
```

The CLI defaults to this URL. You can also set the `FULLSEND_MINT_URL` repository/org variable in GitHub explicitly. If you are using the hosted community mint, the rest of this guide (deploying, enrolling, troubleshooting) is for platform operators managing self-hosted mints — you do not need to manage mint infrastructure yourself.

## Prerequisites

- **GCP project** with the following APIs enabled:

  ```bash
  gcloud services enable \
    iam.googleapis.com \
    cloudresourcemanager.googleapis.com \
    cloudfunctions.googleapis.com \
    run.googleapis.com \
    secretmanager.googleapis.com \
    iamcredentials.googleapis.com \
    --project="$GCP_PROJECT"
  ```

  > **Note:** `iamcredentials.googleapis.com` is a runtime dependency — the deployed mint Cloud Function uses it for WIF token exchange, not the CLI itself. It must be enabled before `mint deploy`.

- **fullsend CLI** — download the latest binary from [GitHub Releases](https://github.com/fullsend-ai/fullsend/releases),
  or run [`fullsend self-update`](../../cli/self-update.md) on v0.55.0 or later

- **GCP IAM roles** — the user running mint commands authenticates via ADC (`gcloud auth application-default login`). The required roles depend on the command:

  | IAM Role | `mint deploy` | `mint delete` | `mint add-role` | `mint remove-role` | `mint enroll` | `mint unenroll` | `mint status` |
  |----------|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
  | `roles/iam.serviceAccountAdmin` | x | x | | | | | |
  | `roles/iam.workloadIdentityPoolAdmin` | x | x | | | x | x | |
  | `roles/resourcemanager.projectIamAdmin` | \* | | | | | | |
  | `roles/secretmanager.admin` | \* | x | \*\* | \*\*\* | | | |
  | `roles/cloudfunctions.developer` | x | x | | | | | |
  | `roles/cloudfunctions.viewer` | | | x | x | x | x | x‡ |
  | `roles/run.admin` | x | | x | x | x | x | |
  | `roles/secretmanager.viewer` | | | § | | | | x‡ |

  \* `roles/resourcemanager.projectIamAdmin` is required for `mint deploy` only when using `--pem-dir` (first-time bootstrap). `roles/secretmanager.admin` is required for `mint deploy` only when using `--pem-dir` or `--otlp-headers-file` (which creates a collector headers secret and grants access to it). Standard deploys without these flags do not need these roles.

  \*\* `roles/secretmanager.admin` is required for `mint add-role` when uploading a new PEM (`--pem` or browser mode). When using `--use-existing-pem-secret`, only `roles/secretmanager.viewer` is required (see §).

  \*\*\* `roles/secretmanager.admin` is required for `mint remove-role` unless `--keep-pem` is passed (default deletes the PEM secret).

  § `roles/secretmanager.viewer` is required for `mint add-role` when using `--use-existing-pem-secret` (checks that the PEM secret exists).

  ‡ `roles/cloudfunctions.viewer` and `roles/secretmanager.viewer` are required for `mint status` only when using `--project` (GCP-based) mode. The API-based mode (`--mint-url` / `FULLSEND_MINT_URL`) requires only valid GitHub credentials and no GCP IAM roles.

  Enrollment does not grant IAM bindings — Vertex AI access is provisioned separately via `inference provision`.

  `roles/owner` covers all of the above for users with broad access.

  **Behaviour / e2e pool orgs:** Enroll `halfsend-NN/test-repo-01` … `test-repo-12` (created by each slot claim and installed on demand by the shared slot driver — see [behaviour-testing.md](../dev/behaviour-testing.md#repo-allocation-via-the-shared-slot-driver)) on the hosted mint (`PER_REPO_WIF_REPOS`); use `e2e/org-pool/enroll.sh` to do it. For the STAGE environment, also enroll `halfsend/test-repo-01` … `test-repo-12` (see the STAGE enrollment block in [e2e-testing.md](../dev/e2e-testing.md#behaviour-tests-and-per-repo-mint-enrollment)). Do not enroll the singular `test-repo`, `*-fork` names, or the `*-mint-host` / `*-mint-unapproved` probe hosts. Repos need not exist at enrollment time — enroll is a mint allowlist / WIF-provider update only; the unified driver creates the repos when a behaviour scenario first leases them. See [e2e-testing.md](../dev/e2e-testing.md#behaviour-tests-and-per-repo-mint-enrollment).

  An administrator can grant all required roles with a single script:

  ```bash
  export GCP_PROJECT="my-project-id"    # target GCP project
  export USER_EMAIL="alice@example.com" # email of the user who will run mint commands

  for ROLE in \
    roles/iam.workloadIdentityPoolAdmin \
    roles/iam.serviceAccountAdmin \
    roles/resourcemanager.projectIamAdmin \
    roles/secretmanager.admin \
    roles/cloudfunctions.developer \
    roles/run.admin; do
    gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
      --member="user:$USER_EMAIL" \
      --role="$ROLE"
  done
  ```

## Deploying the mint

`fullsend mint deploy` creates or updates the token mint Cloud Function and its supporting GCP infrastructure (service account, WIF pool/provider, Secret Manager secrets).

```bash
export GCP_PROJECT="<your-gcp-project>"

fullsend mint deploy --project="$GCP_PROJECT"
```

The binary includes an embedded copy of the mint Cloud Function source, so it works standalone without needing the repository checked out. If you are developing or testing changes to the mint source, run the CLI from a local clone — the `--source-dir` flag (default `internal/mint/`) uses your local copy when the path exists, falling back to the embedded source when it does not. The mint consists of two modules: `internal/mint/` (the entry point) and `internal/mintcore/` (shared verification and token exchange logic). The provisioner bundles `mintcore` automatically from the sibling directory.

The deploy command automatically detects when the deployed function is up-to-date (same source hash) and skips code redeployment, only updating WIF infrastructure and configuration.

### Public mint deployment

Use `--public` to bootstrap a public mint: `PER_REPO_WIF_REPOS=*` on the Cloud Function and a permissive WIF provider CEL for the STS authentication path. Orgs call the mint via upstream reusable workflows in `fullsend-ai/fullsend` after installing the shared public GitHub Apps — `mint enroll` is not required.

```bash
fullsend mint deploy --project="$GCP_PROJECT" --pem-dir=/path/to/pems --public
```

Redeploying or upgrading an existing mint must use the same mode: `--public` for public mints, omit `--public` for tight mints. Deploy rejects mode conversion in either direction.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | | GCP project ID for the mint (required) |
| `--region` | `us-central1` | Cloud region for the mint function |
| `--pem-dir` | | Path to directory containing `{role}.pem` files for PEM bootstrap (GCP and Cloudflare) |
| `--app-set` | `fullsend-ai` | App set name for PEM bootstrap (used with `--pem-dir`) |
| `--roles` | _(default roles)_ | Comma-separated role names to bootstrap with `--pem-dir` |
| `--public` | `false` | Deploy public mint (`PER_REPO_WIF_REPOS=*`); required to redeploy an existing public mint |
| `--source-dir` | | Path to local mint source directory (default: checkout path when present, embedded otherwise) |
| `--skip-deploy` | `false` | Skip code upload, reuse existing function (only update WIF/config). Cannot be combined with an OTLP endpoint on GCP |
| `--otlp-traces-endpoint` | | OTLP/HTTP traces URL stamped into the function source. See [Tracing (GCP)](../../cli/mint.md#tracing-gcp) |
| `--otlp-endpoint` | | OTLP/HTTP base URL; `/v1/traces` is appended. Ignored when `--otlp-traces-endpoint` is set |
| `--otlp-headers-file` | | `chmod 600` file with collector auth headers, stored in a per-deploy Secret Manager secret. Requires an endpoint flag |
| `--otlp-no-headers` | `false` | The collector needs no auth headers. Requires an endpoint flag; mutually exclusive with `--otlp-headers-file` |
| `--status-auth` | `oidc` | Comma-separated status auth modes. Each non-oidc mode selects a Go build tag. Modes: `oidc`, `github`. `oidc` is always compiled in; `github` requires `--status-github-group` |
| `--status-github-group` | | `ORG/TEAM` slug for GitHub status auth (required when `github` mode enabled). Example: `--status-github-group=acme/platform-team` |
| `--dry-run` | `false` | Preview changes without making them |

### Bootstrapping PEMs

The optional `--pem-dir` flag seeds role PEM secrets during deployment on both GCP and Cloudflare. This allows `mint enroll` (GCP) or immediate token minting (Cloudflare) to work without a separate `mint add-role` step.

```bash
# GCP bootstrap:
fullsend mint deploy --project="$GCP_PROJECT" --pem-dir=/path/to/pems

# Cloudflare bootstrap:
fullsend mint deploy --platform=cloudflare --pem-dir=/path/to/pems --per-repo-wif-repos=acme/widget
```

The `--pem-dir` directory must contain one `{role}.pem` file per agent role (e.g., `fullsend.pem`, `triage.pem`, `coder.pem`, `review.pem`, `retro.pem`, `prioritize.pem`). The CLI auto-discovers each app's numeric ID from the GitHub API by looking up the public app slug (`fullsend-ai-{role}`).

Use `--app-set` to target a non-default app set (default: `fullsend-ai`). Use `--roles` to override the default role list — for example, to include the `e2e` role:

```bash
fullsend mint deploy --project="$GCP_PROJECT" --pem-dir=/path/to/pems \
  --roles=fullsend,triage,coder,review,retro,prioritize,e2e
```

> **Note:** On GCP, PEM bootstrapping requires `roles/resourcemanager.projectIamAdmin` and `roles/secretmanager.admin` in addition to the base roles. On Cloudflare, PEM secrets are stored via `wrangler secret put` (durable) or `--secrets-file` (preview). The GitHub Apps must already exist as public apps.

### Mint URL stability

The mint URL is stable across redeploys within the same project and region — updating the Cloud Function does not change its URL. Enrolling a new repository in an existing mint only updates `PER_REPO_WIF_REPOS` (and WIF configuration) without redeploying the function. Shared `ROLE_APP_IDS` are managed at deploy/bootstrap time (`mint deploy --pem-dir`) or per-role via `mint add-role` / `remove-role` — not during enrollment. Existing enrolled repos continue working with no changes when new repos are enrolled.

Deploying to a **different region** (e.g., changing `--region` from `us-central1` to `us-east5`) creates a new Cloud Run service with a different URL. All enrolled repos store the mint URL in a repo or org variable (`FULLSEND_MINT_URL`), so changing the region requires updating every enrolled repo's variable. Avoid changing `--region` after initial deployment unless you plan to update all consumers.

### HTTP Strict Transport Security

Mint responses from the Cloud Function and the Cloudflare Worker carry
this header, success and error responses alike:

```
Strict-Transport-Security: max-age=31536000
```

The shared mint handler sets it (`hstsHeaderValue` in
`internal/mintcore/handler.go`) on every response it writes. The
Cloudflare Worker also sets it on the error responses it writes before
the handler runs, such as `429 rate_limited`.

- **`max-age=31536000` (one year):** after one HTTPS response, a browser
  or HSTS-aware client upgrades `http://` requests for the mint hostname to
  HTTPS for a year. Clients that ignore HSTS still get the HTTP-to-HTTPS
  redirect at the edge.
- **No `includeSubDomains`:** the policy applies only to the mint's own
  hostname (for example `mint.fullsend.sh`). It never extends to sibling
  or child hostnames.
- **No `preload`:** the mint does not ask to be added to browser preload
  lists. Preloading covers the whole registrable domain, including the
  public website, and is hard to undo.

Clients ignore the header on plain-HTTP responses, so a self-hosted mint
served without TLS is unaffected. The
[GitHub mint scope behaviour scenarios](../dev/behaviour-testing.md#github-mint-scope-scenarios)
check the header on `/v1/token` and `/health` against each deployed
behaviour-test mint.

## Managing roles

Agent roles on the mint are **global** — each role maps to a GitHub App PEM secret (`fullsend-{role}-app-pem`) and an entry in the shared `ROLE_APP_IDS` environment variable. Use `fullsend mint add-role` and `fullsend mint remove-role` to manage individual roles after the mint is deployed.

| Command | When to use |
|---------|-------------|
| `mint deploy --pem-dir` | First-time bootstrap of the default app set (`fullsend-ai`) — seeds all default roles at once |
| `mint add-role` | Add a single role later, or register a custom app set one role at a time |
| `mint remove-role` | Remove a role from the mint (updates env vars; deletes PEM secret by default) |

`mint enroll` does **not** create or modify roles — it only authorizes repos to use roles that already exist on the mint.

### Adding a role

`fullsend mint add-role` requires the mint to already be deployed. Choose one of three mutually exclusive input modes:

**1. Existing app + PEM file** (`--slug` and `--pem`):

```bash
fullsend mint add-role coder \
  --project="$GCP_PROJECT" \
  --slug=fullsend-ai-coder \
  --pem=/path/to/coder.pem
```

The CLI looks up the app's numeric ID from the GitHub API, verifies the PEM matches the app, stores the PEM in Secret Manager, and updates `ROLE_APP_IDS` / `ALLOWED_ROLES`.

**2. Existing PEM secret** (`--slug` and `--use-existing-pem-secret`):

```bash
fullsend mint add-role review \
  --project="$GCP_PROJECT" \
  --slug=fullsend-ai-review \
  --use-existing-pem-secret
```

Use this when the PEM secret `fullsend-{role}-app-pem` already exists in Secret Manager (for example, copied from another project) and you only need to register the app ID on the mint. `--pem` and `--use-existing-pem-secret` cannot be combined.

**3. Create GitHub App via browser** (`--org`):

```bash
fullsend mint add-role prioritize \
  --project="$GCP_PROJECT" \
  --org=acme-corp \
  --app-set=acme
```

Opens the GitHub App manifest flow in your browser, stores the PEM in Secret Manager, and updates the mint. Requires a GitHub token (`GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth login`).

#### add-role flags

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | | GCP project ID (required) |
| `--region` | `us-central1` | Cloud region for the mint service |
| `--slug` | | GitHub App slug (with `--pem` or `--use-existing-pem-secret`) |
| `--pem` | | Path to PEM file (with `--slug`; mutually exclusive with `--use-existing-pem-secret`) |
| `--use-existing-pem-secret` | `false` | Skip PEM upload; require existing Secret Manager secret (with `--slug`) |
| `--org` | | GitHub org for browser-based app creation |
| `--app-set` | `fullsend-ai` | App set prefix for browser mode (`{app-set}-{role}`) |
| `--public` | `false` | Install existing public app without confirm prompt (browser mode) |
| `--force` | `false` | Overwrite existing `ROLE_APP_IDS` entry for this role |
| `--dry-run` | `false` | Preview changes without making them |

The `fix` and `code` dispatch stages both mint the `coder` role and reuse the `coder` app — add role `coder` instead; there is no separate `fix` app or mint role to add.

### Removing a role

`fullsend mint remove-role` removes a role from `ROLE_APP_IDS` and `ALLOWED_ROLES`. By default it also deletes the PEM secret from Secret Manager. Use `--keep-pem` to retain the secret for later re-registration.

```bash
# Remove role and delete PEM secret (default)
fullsend mint remove-role retro --project="$GCP_PROJECT"

# Remove role but keep PEM secret
fullsend mint remove-role retro --project="$GCP_PROJECT" --keep-pem
```

Requires typing the role name to confirm (unless `--dry-run` or `--yolo`). Removing `coder` also prevents `fix`/`code` token minting.

#### remove-role flags

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | | GCP project ID (required) |
| `--region` | `us-central1` | Cloud region for the mint service |
| `--keep-pem` | `false` | Retain PEM secret in Secret Manager (default: delete) |
| `--dry-run` | `false` | Preview changes without making them |
| `--yolo` | `false` | Skip interactive confirmation |

This command does not uninstall GitHub Apps from organizations or update any repository's `.fullsend` configuration — edit repository configuration separately (for example with `fullsend github set <owner/repo> <key> <value>`).

## Enrolling repositories

`fullsend mint enroll` registers a repository in the mint and configures WIF
to accept OIDC tokens from it. Only `owner/repo` targets are accepted; a bare
org argument is rejected because org enrollment was removed with per-org
installation.

```bash
fullsend mint enroll acme-corp/my-repo --project="$GCP_PROJECT"
```

Enrollment does **not** grant Agent Platform (inference) access — use `fullsend inference provision` separately after enrollment. See [Getting Started](../getting-started/) for the end-user inference setup path.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | | GCP project ID (required) |
| `--region` | `us-central1` | Cloud region for the mint service |
| `--dry-run` | `false` | Preview changes without making them |

### Migration from per-org app ID flags

Prior versions of `mint enroll` accepted `--app-set`, `--role-app-ids`, `--roles`, and `--source-org` to copy per-org app ID mappings into `ROLE_APP_IDS`. App IDs are now **shared per role** on the mint (like PEM secrets) and are set at deploy time via `mint deploy --pem-dir` or per-role via `mint add-role`. Enrollment only adds the repo to `PER_REPO_WIF_REPOS` and creates its WIF provider — remove those flags from scripts and ensure the mint already has role-keyed `ROLE_APP_IDS` before enrolling.

### What enrollment does

1. Discovers the existing mint infrastructure and verifies shared role→app-id mappings exist
2. Adds the repository to the mint Cloud Run service environment variable `PER_REPO_WIF_REPOS` using REVISION-pinned traffic routing
3. Creates a dedicated mint-side WIF provider that accepts OIDC tokens from the repository

Role PEM secrets and `ROLE_APP_IDS` must already exist on the mint, created during `mint deploy --pem-dir` or `mint add-role`. Enrollment does not create, copy, or modify PEM secrets or app ID mappings.

### Public mint mode

When the mint is configured with `PER_REPO_WIF_REPOS=*` (public mode), `mint enroll` exits successfully (exit code 0) in both public and tight modes, but only tight mode updates `PER_REPO_WIF_REPOS` and WIF. In public mode, repository registration is unnecessary because all repositories are already allowed — the command discovers the mint and reports public mode without changing configuration. Scripts can call enroll in both modes without branching; per-repo installs on a public mint use the default WIF provider and upstream reusable workflows.

`mint unenroll` cannot remove individual repositories from a public mint. To restrict access, clear `PER_REPO_WIF_REPOS=*` and set an explicit repo list. PEM rotation is not required, but the transition is not config-only on every mint:

- **JWKS-backed mints** verify tokens without per-repo WIF providers, so replacing `*` with an explicit repo list is a config-only change.
- **GCF mints** select a dedicated per-repo WIF provider for every repo in the explicit list. Public-mode enrollment skips creating those providers, so a public-only GCF mint may not have them. Provision a dedicated repo WIF provider for each repo before (or as part of) switching to the explicit list; otherwise token minting for those repos fails.

### REVISION-pinned traffic routing

The CLI updates the Cloud Run service using a two-step process: first it patches the service template (env vars), then it explicitly routes 100% of traffic to the newly created revision. This is called REVISION-pinned routing.

This prevents a class of bugs where the service template is updated but traffic continues serving from an older revision with stale env vars. Without REVISION-pinned routing, a newly enrolled repo might not be recognized by the mint because the traffic-serving revision still has the old `PER_REPO_WIF_REPOS` value.

### Enrollment ordering

Enroll repositories serially — do not run concurrent enrollment commands against the same mint. The CLI reads the current env vars, merges the new repo's entry, and writes the result back. Two concurrent enrollments will race, and one repo's entry may be lost.

## Unenrolling repositories

`fullsend mint unenroll` removes a repository from the mint. Only `owner/repo`
targets are accepted. A bare org argument is rejected because per-org
unenrollment was removed with per-org installation. To clean up org entries
that an older CLI left behind, see
[Cleaning up legacy per-org mint state](#cleaning-up-legacy-per-org-mint-state).

```bash
fullsend mint unenroll acme-corp/my-repo --project="$GCP_PROJECT"
```

Unenroll removes the repository from `PER_REPO_WIF_REPOS` and disables its repo-specific WIF provider. Pass `--delete-provider` to delete the provider permanently instead. Role PEM secrets are shared across repositories and are not modified.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | | GCP project ID (required) |
| `--region` | `us-central1` | Cloud region for the mint service |
| `--delete-provider` | `false` | Permanently delete the repo's WIF provider |
| `--dry-run` | `false` | Preview changes without making them |
| `--yolo` | `false` | Skip interactive confirmation (for automation) |

## Cleaning up legacy per-org mint state

Per-org mint enrollment was removed along with per-org installation. This is a
breaking change for scripts and automation:

- `fullsend mint enroll <org>` and `fullsend mint unenroll <org>` with a bare org argument now fail. Enroll and unenroll each repository with an `owner/repo` target.
- `fullsend mint deploy --allowed-orgs` was removed. Passing it fails with `unknown flag: --allowed-orgs`.
- `mint deploy` no longer writes `ALLOWED_ORGS` on GCP or Cloudflare, and `mint status` no longer reports it.
- The fullsend action no longer runs a vendored CLI from the per-org location `bin/fullsend`. It only uses `.fullsend/bin/fullsend`, which per-repo installs create when the repository resolves `vendor: true` in `repos.yaml` (for example `fullsend repos set defaults.vendor true`, then `fullsend repos install`; `fullsend github setup --vendor` does the same for a repository installed with `github setup`). If `bin/fullsend` exists, the action logs a warning and ignores it.

Mints that older CLI versions deployed or enrolled orgs in can still hold org-level state. The mint authorizes callers only through `PER_REPO_WIF_REPOS` and ignores `ALLOWED_ORGS`, so this cleanup is hygiene: leftover entries do not grant access to the mint. The CLI cannot remove them anymore, so clean them up manually.

> **Do not touch inference WIF.** These steps apply only to the mint's WIF pool (`fullsend-pool`, provider `github-oidc`). Organization-scoped inference WIF set up by `fullsend inference provision <org>` uses the separate `fullsend-inference` pool and is managed by `fullsend inference deprovision <org>`. Do not edit the inference pool's providers as part of this cleanup.

### GCP mints

1. Inspect the attribute condition on the mint's shared WIF provider:

   ```bash
   gcloud iam workload-identity-pools providers describe github-oidc \
     --workload-identity-pool=fullsend-pool \
     --location=global \
     --project="$GCP_PROJECT" \
     --format='value(attributeCondition)'
   ```

   If the condition is `assertion.repository_owner != ''`, the mint is in public mode. Leave it as is. If it lists organizations, such as `assertion.repository_owner in ['x0fullsend0placeholder', 'acme-corp', 'other-org']`, those org entries were added by the removed per-org enrollment.

2. Rewrite the condition without the legacy org entries. Keep the deploy-time placeholder org `x0fullsend0placeholder` so the condition never becomes empty:

   ```bash
   gcloud iam workload-identity-pools providers update-oidc github-oidc \
     --workload-identity-pool=fullsend-pool \
     --location=global \
     --project="$GCP_PROJECT" \
     --attribute-condition="assertion.repository_owner == 'x0fullsend0placeholder'"
   ```

   Per-repo providers created by `mint enroll <owner/repo>` are separate providers in the same pool. Leave them alone.

3. Remove the leftover `ALLOWED_ORGS` environment variable from the mint service:

   ```bash
   gcloud run services update fullsend-mint \
     --project="$GCP_PROJECT" --region="$MINT_REGION" \
     --remove-env-vars=ALLOWED_ORGS
   ```

   This creates a new revision. The mint uses REVISION-pinned traffic, so the new revision does not serve requests until you route traffic to it:

   ```bash
   gcloud run services update-traffic fullsend-mint \
     --project="$GCP_PROJECT" --region="$MINT_REGION" \
     --to-latest
   ```

   Then run `fullsend mint status --mint-url= --project="$GCP_PROJECT"` and confirm that the template and the traffic revision match. See [Template/traffic revision divergence](#templatetraffic-revision-divergence). The next `fullsend mint enroll` for a new repository pins traffic to a specific revision again.

### Cloudflare mints

Durable Worker deploys pass `--keep-vars`, so an `ALLOWED_ORGS` variable from an older deploy remains on the Worker. Delete it in the Cloudflare dashboard: open **Workers & Pages**, select the mint Worker (`fullsend-mint` by default), then go to **Settings** > **Variables and Secrets** and delete `ALLOWED_ORGS`. Later `mint deploy` runs do not add it back.

Cloudflare mints have no GCP WIF provider, so no attribute condition cleanup is needed.

## Managing workflow hosts

`fullsend mint workflow-host` manages the `WORKFLOW_HOST_REPOS` environment variable, which controls which repositories may host workflows that call the mint. The check applies to every admitted caller: the workflow must be hosted by the upstream `fullsend-ai/fullsend` repo (always accepted) or a repository in `WORKFLOW_HOST_REPOS`, and its basename must be in `ALLOWED_WORKFLOW_FILES`.

When `WORKFLOW_HOST_REPOS` is not set, it defaults to `fullsend-ai/fullsend`.

### Adding a workflow host

```bash
fullsend mint workflow-host add acme-corp/my-workflows --project="$GCP_PROJECT"
```

Idempotent — skips repos already listed.

### Removing a workflow host

```bash
fullsend mint workflow-host remove acme-corp/my-workflows --project="$GCP_PROJECT"
```

### Listing workflow hosts

```bash
fullsend mint workflow-host list --project="$GCP_PROJECT"
```

Read-only — makes no changes.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | | GCP project ID (required) |
| `--region` | `us-central1` | Cloud region for the mint service |
| `--dry-run` | `false` | Preview changes without making them (`add` and `remove` only) |

## Checking mint status

`fullsend mint status` inspects the mint's state and PEM health. Two modes are available:

### API-based mode (recommended)

When `--mint-url` (or `FULLSEND_MINT_URL`) is provided, the command queries
the mint's `/v1/status` endpoint using auto-discovered GitHub credentials.
No GCP IAM roles are required.

Authentication is attempted in order: GitHub Actions OIDC first, then
`GH_TOKEN` / `GITHUB_TOKEN` / `gh auth token`.

> The `GH_TOKEN` / `GITHUB_TOKEN` / `gh auth token` fallback only succeeds
> against a mint deployed with `--status-auth=github
> --status-github-group=ORG/TEAM` (see
> [infrastructure-reference.md](infrastructure-reference.md#status-endpoint)).
> A default (OIDC-only) mint rejects it with HTTP 401 — only GitHub Actions
> OIDC works there.

```bash
# Query via the mint API
fullsend mint status --mint-url="$FULLSEND_MINT_URL"

# Or set the env var and omit the flag
export FULLSEND_MINT_URL="https://mint.example.com"
fullsend mint status
```

API-based mode returns the following fields:

- **version** — the mint's build version
- **commit** — the mint's build commit hash
- **org** — the calling workflow's organization (OIDC auth only)
- **roles** — configured role names
- **workflow_host_repos** — repositories allowed as workflow hosts

### GCP-based mode

When `--project` is provided (and `--mint-url` is not), the command reads
mint state directly from GCP infrastructure. This requires GCP viewer IAM
roles (see the [IAM table above](#prerequisites)).

```bash
# Overview of all enrolled repos
fullsend mint status --mint-url= --project="$GCP_PROJECT"

# Drill into a specific org's PEM status
fullsend mint status acme-corp --mint-url= --project="$GCP_PROJECT"
```

When `FULLSEND_MINT_URL` is set and `--project` is also provided, the
command returns an error to prevent silent mode ambiguity — either unset
the env var or pass `--mint-url=` to force GCP-based mode, as in the
examples above.

> **Note:** The IAM roles listed in the [table above](#prerequisites) apply
> only to `--project` (GCP-based) mode. API-based mode requires only valid
> GitHub credentials.

### What status reports

> The fields below are reported by `--project` (GCP-based) mode.
> API-based mode (`--mint-url`) returns a different payload — see the
> field listing in the [API-based mode](#api-based-mode-recommended) section above.

**Cloud Run revision section:**

- **Traffic revision** — which Cloud Run revision is currently serving requests (e.g., `fullsend-mint-00114-fm9`)
- **Allocation type** — `TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION` (pinned to a specific revision) or `TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST` (auto-routes to newest)
- **Template divergence** — a warning when the service template's latest revision does not match the traffic-serving revision, meaning the mint may be serving stale configuration
- **Recent revisions** — the last 5 revisions with their create time and active/inactive status

**Enrollment section:**

- Shared role→app-id mappings (from role-keyed `ROLE_APP_IDS`)
- Per-repo WIF repos list (from `PER_REPO_WIF_REPOS`, which is what authorizes callers)

The legacy `ALLOWED_ORGS` variable is not shown; the mint no longer uses it
to authorize callers.

**Org drill-down** (when an org argument is provided):

- PEM secret status for each role (present/missing)
- A warning when no `<org>/*` repository is in `PER_REPO_WIF_REPOS`

**Health summary:**

The status command reports overall health as one of:

| Health | Condition |
|--------|-----------|
| `healthy` | At least one repo in `PER_REPO_WIF_REPOS` (or public mode), traffic-serving revision readable, template matches traffic revision |
| `degraded` | No enrolled repos, OR the traffic-serving revision is unreadable (enrollment unverified), OR template diverges from traffic-serving revision |
| `not-installed` | Mint function not found in the specified project/region |

## Minting tokens at runtime

`fullsend mint token` exchanges a GitHub Actions OIDC JWT for a short-lived, role-scoped GitHub App installation token. This is a runtime operation intended for use inside GitHub Actions workflows (not infrastructure management).

```bash
# Mint a token for the triage role scoped to a specific repo
fullsend mint token --role triage --repos my-repo --mint-url "$FULLSEND_MINT_URL"

# Mint a token scoped to multiple repos
fullsend mint token --role coder --repos repo-a,repo-b --mint-url "$FULLSEND_MINT_URL"

# Mint a read-only token (explicit level)
fullsend mint token --role triage --repos my-repo --level read --mint-url "$FULLSEND_MINT_URL"
```

The `--level` flag selects the privilege level for the minted token: `read` returns read-only permissions, `write` returns the full permission set. Both the CLI and the server default to `write` when omitted (temporary compatibility default — a future release will change the server default to `read`).

The command prints the token to stdout for shell capture. When running in GitHub Actions (`GITHUB_ACTIONS=true`), it also emits `::add-mask::` to stderr to prevent the token from appearing in logs.

**Required environment:** The calling GitHub Actions job must declare `id-token: write` permission so that `ACTIONS_ID_TOKEN_REQUEST_URL` and `ACTIONS_ID_TOKEN_REQUEST_TOKEN` are available.

## Multi-org setup

A single token mint can serve multiple GitHub organizations. The first org deploys the mint infrastructure and creates **public unlisted** GitHub Apps; additional orgs reuse the existing mint and install the same apps.

**First org (deploys mint + provisions inference + configures GitHub):**

```bash
export FIRST_ORG="<first-github-org>"
export FIRST_REPO="<first-github-repo>"
export GCP_PROJECT="<your-gcp-project>"

# 1. Deploy the token mint
fullsend mint deploy --project="$GCP_PROJECT" --pem-dir=/path/to/pems

# 2. Enroll the first repository in the mint
fullsend mint enroll "$FIRST_ORG/$FIRST_REPO" --project="$GCP_PROJECT"

# 3. Provision inference WIF for the repository. Use the same owner/repo
#    target as the GitHub setup in step 4, so the Vertex AI role is granted to
#    that repository. Repeat this step for each repository you configure.
fullsend inference provision "$FIRST_ORG/$FIRST_REPO" --project="$GCP_PROJECT"

# 4. Configure a repository and create public apps (installable by other orgs).
#    `github setup` does not create GitHub Apps, so use `admin install`, which
#    creates the apps (as public with --public) and configures the repository.
#    It targets a single owner/repo; repeat per repository. `admin install`
#    prints a deprecation warning; it still creates every role's app in one
#    run. To create apps one role at a time on the deployed mint, use
#    `fullsend mint add-role <role> --project "$GCP_PROJECT" --org "$FIRST_ORG"`
#    (add --public / --app-set as needed).
# $MINT_URL: the "Mint deployed at ..." URL printed by step 1.
# $WIF_PROVIDER: the FULLSEND_GCP_WIF_PROVIDER value from step 3 — run
#   `fullsend inference status "$FIRST_ORG/$FIRST_REPO" --project="$GCP_PROJECT" --format=env`
#   and copy it, or parse it from `--format=json`.
fullsend admin install "$FIRST_ORG/$FIRST_REPO" \
  --mint-url "$MINT_URL" \
  --inference-wif-provider "$WIF_PROVIDER" \
  --inference-project "$GCP_PROJECT" \
  --public
```

The `--public` flag creates GitHub Apps as public unlisted — they won't appear in the marketplace but can be installed by other organizations via their installation URL.

When the first org uses a custom app set prefix, pass `--app-set` so the apps are named accordingly:

```bash
fullsend admin install "$FIRST_ORG/$FIRST_REPO" \
  --mint-url "$MINT_URL" \
  --inference-wif-provider "$WIF_PROVIDER" \
  --inference-project "$GCP_PROJECT" \
  --public \
  --app-set "$FIRST_ORG"
```

This creates public apps named `{first-org}-fullsend`, `{first-org}-coder`, etc.

**Additional orgs (enroll in existing mint + install existing public apps):**

```bash
export ADDITIONAL_ORG="<additional-github-org>"
export ADDITIONAL_REPO="<additional-github-repo>"
```

`GCP_PROJECT` carries over from the first-org step above.

```bash
# 1. Enroll the additional repository in the existing mint
fullsend mint enroll "$ADDITIONAL_ORG/$ADDITIONAL_REPO" --project="$GCP_PROJECT"

# 2. Provision inference WIF for the additional repository. Each repository
#    has its own WIF provider, so do not reuse the first repository's value.
fullsend inference provision "$ADDITIONAL_ORG/$ADDITIONAL_REPO" --project="$GCP_PROJECT"

# 3. Get this repository's WIF provider (FULLSEND_GCP_WIF_PROVIDER) and set it
#    as $WIF_PROVIDER for the commands below.
fullsend inference status "$ADDITIONAL_ORG/$ADDITIONAL_REPO" --project="$GCP_PROJECT" --format=env

# 4. Configure a repository (owner/repo) — auto-detects shared public apps
fullsend github setup "$ADDITIONAL_ORG/$ADDITIONAL_REPO" \
  --mint-url "$MINT_URL" \
  --inference-wif-provider "$WIF_PROVIDER" \
  --inference-project "$GCP_PROJECT"
```

The setup command auto-detects shared public apps by matching installed app IDs against the mint's `ROLE_APP_IDS`. It records the actual app slug in `config.yaml`, so subsequent operations find the correct app regardless of naming convention.

If the public apps were created with a custom `--app-set`, pass the same value so the CLI uses the correct slug prefix for convention-based lookups:

```bash
fullsend github setup "$ADDITIONAL_ORG/$ADDITIONAL_REPO" \
  --mint-url "$MINT_URL" \
  --inference-wif-provider "$WIF_PROVIDER" \
  --inference-project "$GCP_PROJECT" \
  --app-set "$FIRST_ORG"
```

PEMs use role-only naming (`fullsend-{role}-app-pem`) — one secret per role, shared across orgs on the mint.

> **Note:** Multi-org with `--public` requires all orgs to share the same GitHub Apps. Private apps (the default) are single-org only.

## Troubleshooting

### Template/traffic revision divergence

**Symptom:** `mint status` reports health as "degraded" with the message "template diverges from traffic-serving revision".

**What it means:** The Cloud Run service template was updated (e.g., env vars changed) but traffic is still routed to an older revision. The mint is serving requests with the old revision's configuration — newly enrolled repos may not be recognized.

**Common causes:**

- A manual edit in the GCP Console that updated the template without routing traffic
- A failed traffic routing step during enrollment (the template PATCH succeeded but the traffic PATCH failed)
- An external tool (Terraform, `gcloud run deploy`) that doesn't use REVISION-pinned routing

**Resolution:**

1. Run `fullsend mint status --mint-url= --project="$GCP_PROJECT"` to confirm which revision is serving and what the template expects
2. Run `fullsend mint enroll` for a repository that is not yet enrolled — this triggers a new revision and routes traffic to it (re-running for an already-enrolled repo makes no change)
3. If no enrollment is needed, manually route traffic with:

   ```bash
   gcloud run services update-traffic fullsend-mint \
     --project="$GCP_PROJECT" --region="$MINT_REGION" \
     --to-latest
   ```

### LATEST allocation type

**Symptom:** `mint status` shows allocation type as `TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST` instead of `REVISION`.

**What it means:** Traffic is auto-routed to the newest revision. This can cause issues if a non-enrollment deployment creates a new revision that doesn't include the latest env vars (e.g., deploying new source code via `gcloud functions deploy` without preserving env vars).

**Resolution:** Run `fullsend mint enroll` for a repository that is not yet enrolled, or route traffic manually as described above. The CLI always uses REVISION-pinned routing, which overrides the LATEST setting.

### Concurrent enrollment race

**Symptom:** After enrolling two repos in parallel, one repo is missing from `PER_REPO_WIF_REPOS`.

**What it means:** Both enrollment commands read the same initial state, merged their repo independently, and wrote back. The second write overwrote the first repo's entry.

**Resolution:**

1. Run `fullsend mint status --mint-url= --project="$GCP_PROJECT"` to confirm which repo is missing
2. Re-run `fullsend mint enroll` for the missing repo
3. Always enroll repos serially — one at a time

### Mint not found

**Symptom:** `mint enroll` or `mint status` reports "mint not found in project X region Y".

**Resolution:**

1. Verify the `--project` and `--region` flags match where the mint was deployed
2. Run `fullsend mint deploy --project="$GCP_PROJECT"` to create the mint if it doesn't exist
3. If the mint was deployed to a different region, use the correct `--region` flag

### PEM secret errors

**Symptom:** `mint status` reports role PEM secrets as missing.

**Common causes:**

- Role PEM secrets were never bootstrapped — run `mint deploy --pem-dir` or `mint add-role` first
- The enrolled roles in `ROLE_APP_IDS` reference apps whose PEM secrets do not exist in Secret Manager

### Debugging with gcloud

When the CLI output is insufficient, inspect the Cloud Run service
directly. The commands below are **read-only** — they do not modify the
mint.

> **DANGER — never use `--set-env-vars` to modify the mint service.**
> `--set-env-vars` **replaces all** env vars, destroying PER_REPO_WIF_REPOS,
> ROLE_APP_IDS, PEM secret references, and every other variable. If you
> need to fix an env var manually, use `--update-env-vars` which
> **merges** the provided values into the existing set. Prefer
> `fullsend mint enroll` over manual `gcloud` env var edits — the CLI
> handles read-merge-write with REVISION-pinned traffic routing.

```bash
export MINT_SERVICE="fullsend-mint"
export MINT_REGION="us-central1"  # change if deployed elsewhere

# Read env vars from the traffic-serving revision (not the template)
TRAFFIC_REV=$(gcloud run services describe "$MINT_SERVICE" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" \
  --format="value(status.traffic[0].revisionName)")
gcloud run revisions describe "$TRAFFIC_REV" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" \
  --format="yaml(spec.containers[0].env)"

# Read env vars from the service template (may differ from traffic)
gcloud run services describe "$MINT_SERVICE" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" \
  --format="yaml(spec.template.spec.containers[0].env)"

# List recent revisions
gcloud run revisions list \
  --service="$MINT_SERVICE" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" \
  --limit=5

# Read mint Cloud Function logs
gcloud functions logs read fullsend-mint \
  --project="$GCP_PROJECT" --region="$MINT_REGION" --gen2 --limit=50
```

## Cloudflare Worker custom domain

Durable Cloudflare Worker deployments can be attached to a [Workers Custom Domain](https://developers.cloudflare.com/workers/configuration/routing/custom-domains/) (e.g. `mint.fullsend.sh`) using the `--custom-domain` flag. The zone ID is resolved automatically from the domain name via the Cloudflare API.

### Deploying with a custom domain

```bash
fullsend mint deploy \
  --platform cloudflare \
  --custom-domain "mint.fullsend.sh" \
  --pem-dir "/path/to/pems"
```

The `FULLSEND_MINT_URL` output uses the custom domain hostname (`https://mint.fullsend.sh`) instead of the default `workers.dev` URL.

### Tearing down with a custom domain

Pass `--custom-domain` to `mint delete` so the CLI removes the custom domain binding before deleting the Worker:

```bash
fullsend mint delete \
  --platform cloudflare \
  --custom-domain "mint.fullsend.sh"
```

### Requirements

- Cloudflare authentication is required (either `CLOUDFLARE_API_TOKEN` env var or `wrangler login` session)
- The zone must already exist in the Cloudflare account — the CLI fails early with a clear error if the zone cannot be found
- Custom domains are only supported for durable deploys — preview deploys use bare `workers.dev` hostnames

## Cloudflare Worker rate limiting

The Cloudflare Worker deployment includes a native `[[ratelimits]]` binding (`MINT_TOKEN_RATE_LIMITER`) that rate-limits `POST /v1/token`, `POST /v1/bot-role` and `POST /v1/webhooks/github` requests, each path with its own counter. The binding is configured in `wrangler.toml` and enforced in the Worker before WASM initialization — no operator action is required.

| Setting | Value |
|---------|-------|
| Binding name | `MINT_TOKEN_RATE_LIMITER` |
| Limit | 60 requests per minute per key |
| Key format | `{hostname}:{path}:{client-IP}` |

The rate limit key incorporates the request hostname, so preview deployments (which produce distinct hostnames like `<alias>-<worker>.<subdomain>.workers.dev`) get isolated counters. Durable deploys use a stable hostname. When the rate limit is exceeded, the Worker returns HTTP 429 with `{"error":"rate_limited"}`.

If the `[[ratelimits]]` section is removed from `wrangler.toml`, the Worker logs a warning and continues without rate limiting (fail-open).

## GitHub webhook for fork PR reviews

**What you get:** a push to a fork PR starts the repository's fork PR review
poll (`.github/workflows/fullsend-poll-prs.yml`) within seconds. Without the
webhook, the poll waits for its `*/5` schedule, which GitHub runs late and
sometimes skips. The schedule stays on as the backstop for any lost delivery.

**What you do:** two manual steps, because `mint deploy` has no flag for
either of them.

1. Set the `FULLSEND_WEBHOOK_SECRET` secret on the mint, using the setup for
   your host below.
2. Turn on the webhook of the orchestrator (`fullsend`) GitHub App and give
   it the same secret.

> **Requires a fullsend release whose `repos install` writes the fork PR
> review poll** (ADR 0171,
> [fullsend#9626](https://github.com/fullsend-ai/fullsend/issues/9626)).
> Until a repository has that workflow, every dispatch for it answers
> `workflow_not_dispatchable` and nothing else happens.

How it works: the App sends its `pull_request` webhook to
`<mint URL>/v1/webhooks/github`. The mint checks the signature, keeps only
pushes to fork PRs of enrolled repositories, reads the repository's current
default branch from GitHub, and sends one `workflow_dispatch` of the poll
workflow with no inputs. No webhook field reaches a job. The endpoint
contract is in the
[Infrastructure Reference](infrastructure-reference.md#github-webhook-endpoint).

### Before you start

- **The `fullsend` role on the mint.** `ROLE_APP_IDS` has a `fullsend` entry,
  `ALLOWED_ROLES` (when set) includes `fullsend`, and the mint holds the
  `fullsend` App's private key. Otherwise the route stays off (`404`), and
  the mint logs `github webhook: FULLSEND_WEBHOOK_SECRET is set but
  ALLOWED_ROLES does not allow role "fullsend"; the route is off` at startup.
- **The App on each repository** with the **Actions: Read and write**
  permission. The mint asks for a token with only `actions: write` and
  `metadata: read`.
- **A new secret, used for this App's webhook only.** Do not reuse it for a
  repository or organization webhook, or for another App. The signature
  covers the body, not the headers that name the App, so any hook that shares
  the secret can send deliveries the mint accepts.

  ```bash
  umask 077
  openssl rand -hex 32 | tr -d '\n' > webhook-secret
  ```

  The mint compares the exact value, so the file must not end with a newline.

- **The mint URL.** Every enrolled repository stores it in the
  `FULLSEND_MINT_URL` variable. Read it from one of your enrolled
  repositories (here `acme-corp/my-repo`):

  ```bash
  export MINT_URL="$(gh variable get FULLSEND_MINT_URL --repo acme-corp/my-repo)"
  ```

  If the variable is set at organization level, use
  `--org acme-corp` instead of `--repo`. For example,
  `gh variable get FULLSEND_MINT_URL --repo fullsend-ai/fullsend` prints
  `https://mint.fullsend.sh`. Each setup below also says where the host
  shows the URL.

### Set up on GCP (Cloud Function)

1. Name the mint's project and region. The region is the `--region` you
   passed to `fullsend mint deploy` (default `us-central1`):

   ```bash
   export GCP_PROJECT="my-project-id"
   export MINT_REGION="us-central1"
   ```

   The mint URL is the function's URI:

   ```bash
   gcloud functions describe fullsend-mint --gen2 \
     --project="$GCP_PROJECT" --region="$MINT_REGION" \
     --format="value(serviceConfig.uri)"
   ```

2. Write every current env var of the mint service, plus the secret, to a
   file. The next step's `--env-vars-file` **replaces all** env vars, so the
   file must hold the existing ones. The `jq` program stops if an env var is
   not a plain value:

   ```bash
   umask 077
   gcloud run services describe fullsend-mint \
     --project="$GCP_PROJECT" --region="$MINT_REGION" --format=json \
     | jq -e --rawfile secret webhook-secret '
         .spec.template.spec.containers[0].env
         | if all(has("value")) then from_entries + {FULLSEND_WEBHOOK_SECRET: $secret}
           else error("an env var is not a plain value; stop") end' > mint-env.yaml
   jq -r 'keys[]' mint-env.yaml
   ```

   Check the printed names: every name from the service (such as
   `ROLE_APP_IDS`, `PER_REPO_WIF_REPOS`, `ALLOWED_ROLES`) plus
   `FULLSEND_WEBHOOK_SECRET`.

3. Apply the file, route traffic to the new revision (the mint uses
   [REVISION-pinned traffic routing](#revision-pinned-traffic-routing)), and
   delete the file:

   ```bash
   gcloud run services update fullsend-mint \
     --project="$GCP_PROJECT" --region="$MINT_REGION" \
     --env-vars-file=mint-env.yaml
   REV=$(gcloud run services describe fullsend-mint \
     --project="$GCP_PROJECT" --region="$MINT_REGION" \
     --format="value(status.latestCreatedRevisionName)")
   gcloud run services update-traffic fullsend-mint \
     --project="$GCP_PROJECT" --region="$MINT_REGION" \
     --to-revisions="$REV=100"
   rm mint-env.yaml
   ```

   Not run against a live mint for this guide. The `jq` step was run on a
   sample `describe` output, and `gcloud` accepted the file it wrote (the run
   stopped at authentication).

4. Check that the route is on (see [Check the route](#check-the-route)).

Why a file, not a flag or Secret Manager:

- `--update-env-vars=FULLSEND_WEBHOOK_SECRET=...` would put the secret in the
  `gcloud` command line, where other local users can see it in the process
  list and your shell history keeps it.
- A Secret Manager binding (`--update-secrets`) does not last:
  `mint enroll`, `mint unenroll` and `mint workflow-host` rewrite the env vars
  as plain values and drop the binding, and the route then answers `404`.
- A plain env var is kept by those commands and by `mint deploy`. Anyone who
  can view the Cloud Run service or the function can read it.

### Set up on Cloudflare (Worker)

1. Name the Worker. `WORKER_NAME` is the `--worker-name` you passed to
   `fullsend mint deploy --platform cloudflare`, or `fullsend-mint` if you
   passed none. The Cloudflare dashboard lists it under
   **Workers & Pages**.

   ```bash
   export WORKER_NAME="fullsend-mint"
   ```

   The mint URL is the `FULLSEND_MINT_URL` that `mint deploy` printed: your
   custom domain when you deployed with `--custom-domain`, otherwise
   `https://<WORKER_NAME>.<your workers.dev subdomain>.workers.dev`.

2. Store the secret as a Worker secret. `wrangler` reads the value from
   standard input, so it never appears in a command line:

   ```bash
   wrangler secret put FULLSEND_WEBHOOK_SECRET --name "$WORKER_NAME" < webhook-secret
   ```

   This deploys a new Worker version, and later durable `mint deploy` runs
   keep the secret. If the newest version is a `mint deploy --preview`
   upload, `wrangler secret put` fails with "the latest version of your
   Worker isn't currently deployed": run a durable `fullsend mint deploy`
   first, then set the secret. Whether a preview carries the secret was not
   checked.

   Not run for this guide (no Cloudflare account was used).

3. Check that the route is on (see [Check the route](#check-the-route)).

The Worker rate-limits `POST /v1/webhooks/github` like `/v1/token` (see
[Cloudflare Worker rate limiting](#cloudflare-worker-rate-limiting)).

### Set up on a standalone mint

1. Check the configuration from
   [Standalone mint](standalone-mint.md#step-4-configure-and-run):
   `ROLE_APP_IDS` has a `fullsend` entry and `PEM_DIR` holds `fullsend.pem`.

2. Start the mint with the secret in its environment:

   ```bash
   export FULLSEND_WEBHOOK_SECRET="$(cat webhook-secret)"
   ./fullsend-mint
   ```

   `export` is a shell builtin, so the secret is not in any process's
   command line. Under a service manager, put the variable in an env file
   with mode `600` that the service reads.

3. The mint URL is the public HTTPS URL you set up in
   [Step 5: Expose the mint to GitHub Actions](standalone-mint.md#step-5-expose-the-mint-to-github-actions).
   Check that the route is on (see [Check the route](#check-the-route)).

### Check the route

An unsigned request shows whether the route is on, without the secret:

```bash
curl -s -w ' %{http_code}\n' -X POST "$MINT_URL/v1/webhooks/github"
```

Output when the route is on (from a local standalone mint):

```text
{"error":"invalid signature"} 401
```

A `404` means the route is off: see the first row of
[Troubleshooting the webhook](#troubleshooting-the-webhook).

### Turn on the orchestrator App's webhook

In the settings of the GitHub App registered as `fullsend` in
`ROLE_APP_IDS`:

1. **General → Webhook:** check **Active**, set **Webhook URL** to
   `$MINT_URL/v1/webhooks/github` and **Webhook secret** to the content of
   `webhook-secret`. Save.
2. **Permissions & events → Subscribe to events:** check **Pull request**.
   Save.

Then delete the local `webhook-secret` file, or move it to your secret store.

The mint acts only on deliveries for that App ID. A repository or
organization webhook pointed at the mint has a different secret, so the mint
answers it `401`.

### What success looks like

In the App's **Advanced** tab, under **Recent Deliveries**:

- the `ping` GitHub sends when the webhook is turned on is answered `200`
  `{"ok":true}`;
- a `pull_request` delivery for a push to a fork PR on an enrolled
  repository is answered `202` `{"dispatched":true}`, and the repository's
  **Actions** tab shows a `workflow_dispatch` run of the poll workflow;
- another push to the same repository within 10 seconds is answered `202`
  `{"dispatched":false,"reason":"debounced"}`. That is expected.

The mint logs one line per delivery, for example:

```text
github webhook: delivery="local-1" event="ping" action="" repo="" outcome=ping
```

### Limits

- **Debounce:** after a dispatch for a repository, the mint answers further
  deliveries for it with `debounced` for 10 seconds, without calling GitHub.
  The poll's concurrency group keeps at most one queued run, and every run
  scans every open PR. A push that lands after the dispatched run has listed
  the PRs waits for the next run or the schedule.
- **Cloudflare rate limit:** the Worker limits this path per source address,
  like `/v1/bot-role`. GitHub sends every delivery from a few addresses, and
  every `pull_request` action of the App counts, including ones the route
  ignores. A busy fleet can get `429`; those wake-ups fall back to the
  schedule.
- **Negative cache:** `app_not_installed` and `workflow_not_dispatchable`
  are remembered per repository for 5 minutes and answered without calling
  GitHub.
- **Per instance:** this state lives in one mint instance (one Cloud Run
  instance, one Worker isolate, or one standalone process). It is not shared
  across instances or isolates and is lost on restart. Each instance
  remembers at most 10,000 repositories.
- **Cloudflare only:** at most 60 requests per minute per source IP (see
  [Cloudflare Worker rate limiting](#cloudflare-worker-rate-limiting)). Over
  that, the Worker answers `429`.

### Troubleshooting the webhook

| What you see | Cause | Fix |
|--------------|-------|-----|
| `404` `{"error":"not found"}` | The route is off: `FULLSEND_WEBHOOK_SECRET` is not set on the revision or Worker that serves traffic, `ROLE_APP_IDS` has no `fullsend` entry, or `ALLOWED_ROLES` leaves out `fullsend`. A preview Worker, or a Webhook URL that does not end in `/v1/webhooks/github`, also gets `404`. | Set the secret for your host. Check the startup log for `the route is off`. On GCP, check the traffic-serving revision's env vars (see [Debugging with gcloud](#debugging-with-gcloud)). |
| `401` `{"error":"invalid signature"}` | The App's webhook secret and `FULLSEND_WEBHOOK_SECRET` differ. A trailing newline in either is enough. A repository or organization webhook pointed at the mint also gets `401`. | Set both from the same `webhook-secret` file, then **Redeliver**. Use the App's webhook, not a repository or organization webhook. |
| `413` `{"error":"request body too large"}` | The payload is larger than 1 MiB. | None. The schedule picks up the PR. |
| `429` `{"error":"rate_limited"}` (Cloudflare only) | More than 60 deliveries per minute from one GitHub source IP. | None. The schedule picks up the PR. |
| `202` `{"dispatched":false,"reason":"not_fork"}` | The PR's head branch is in the base repository. A same-repository PR is not a fork PR, and the route acts only on fork PRs. | None. To test the webhook, push to a PR opened from a fork. |
| `202` `{"dispatched":false,"reason":"debounced"}` | This mint instance dispatched the poll for the repository in the last 10 seconds, or is dispatching it now. | None. See [Limits](#limits). |
| `202` `{"dispatched":false,"reason":"workflow_not_dispatchable"}` | GitHub answered the repository read with `404`, or the dispatch with `404` or `422`. In observed GitHub behaviour these mean: no `.github/workflows/fullsend-poll-prs.yml` on the default branch (also the case before the fullsend release that installs it), no `workflow_dispatch` trigger, or Actions disabled. The answer may come from the 5-minute cache, with no GitHub call. | Install or update the poll workflow. Deliveries within 5 minutes of the first such answer may still get it from the cache. The schedule picks up the PR meanwhile. |
| `202` `{"dispatched":false,"reason":"app_not_installed"}` | The orchestrator App is not installed on the repository. The answer may come from the 5-minute cache. | Install the App on the repository. Deliveries within 5 minutes may still get the cached answer. |
| `202` `{"dispatched":false,"reason":"not_orchestrator_app"}` | The delivery names an App whose ID is not the `fullsend` entry of `ROLE_APP_IDS`. | Configure the webhook on the App registered as `fullsend`. |
| `202` `{"dispatched":false,"reason":"repo_not_enrolled"}` | The base repository is not in `PER_REPO_WIF_REPOS`. | [Enroll the repository](#enrolling-repositories). |
| `502` `{"error":"dispatch failed"}` | A GitHub API call failed: the installation lookup, the token request (for example, the App lacks **Actions: Read and write**), the repository read with an answer other than `200` or `404`, or a dispatch answer other than 2xx, `404` or `422`. The mint's log line for the delivery holds the error. | Fix the cause, then **Redeliver** from the **Advanced** tab. |
| `500` `{"error":"dispatch failed"}` | The mint could not read the `fullsend` private key or sign the App JWT (`outcome=credential_error` in the log). | Check the `fullsend` PEM (see [PEM secret errors](#pem-secret-errors)). |
| GitHub shows the delivery as timed out | GitHub waits 10 seconds for an answer. A cold start plus the four GitHub calls can take longer. The dispatch may still have happened. | Check the repository's **Actions** tab, or the mint log for the delivery ID with `outcome=dispatched`. A **Redeliver** is harmless: at most one more poll run, or a `debounced` answer. |

## Bot-role lookup endpoint

`POST /v1/bot-role` resolves a bot login to a registered role for dispatch
authorization. Callers authenticate with the same OIDC token as `/v1/token`.

- **Canonical role.** The `role` field of a recognized response (`{"outcome": "recognized", "role": "review"}`) is the canonical role of the matching GitHub App; the dispatch adapter surfaces it as `actor.bot_role` in the normalized event. When several allowed roles share one App ID (for example `fix` reuses the `coder` App), the lexicographically first role name is returned, so `fix` resolves to `coder`. Treat this tie-break as a stable contract: renaming a role or registering a new role that sorts earlier for a shared App changes the `role` the endpoint returns.
- **Rate limiting.** The Worker rate limiter described above also covers `POST /v1/bot-role`, with a separate counter per path, because each authenticated call makes outbound GitHub API requests.

## See Also

- [Getting Started](../getting-started/) — End-user setup (inference + GitHub)
- [Advanced setup](./advanced-setup.md) — Alternative installation paths and setup flags
- [Standalone Mint](standalone-mint.md) — Running the mint without GCP, with custom agent roles
- [Infrastructure Reference](infrastructure-reference.md) — Token mint, WIF, and secrets deployment details
- [CLI Internals](../dev/cli-internals.md) — Command structure and implementation details
- [Cross-org authorization (ADR 0060)](../../ADRs/0060-cross-org-mint-authorization-via-org-variables.md) — Org-level FOREIGN authorization via `FULLSEND_FOREIGN_<ROLE>_REPOS` org variables
- [Repo-level foreign grants (ADR 0083)](../../ADRs/0083-repo-level-foreign-allow-list.md) — Per-repo FOREIGN authorization grants; manage with `fullsend admin foreign` commands
