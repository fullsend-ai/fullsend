# Mint service administration

This guide covers deploying and managing the fullsend token mint Cloud Function. The mint is the OIDC token exchange service that lets GitHub Actions workflows authenticate as GitHub Apps — it is infrastructure that serves all enrolled organizations and repositories.

| Command | Description |
|---------|-------------|
| `mint deploy` | Deploy or update the mint Cloud Function and GCP infrastructure |
| `mint add-role` | Add an agent role (PEM secret + `ROLE_APP_IDS` entry) |
| `mint remove-role` | Remove an agent role from the mint (deletes PEM secret by default) |
| `mint enroll` | Register an org or repo in `ALLOWED_ORGS` and configure WIF |
| `mint unenroll` | Remove an org or repo from the mint |
| `mint status` | Inspect mint health, enrolled orgs, and PEM secrets |
| `mint token` | Exchange a GitHub Actions OIDC token for an installation token |

> **This guide is for platform operators** who deploy, manage, or troubleshoot the token mint Cloud Function. If you are an end user setting up fullsend for your organization, see [Installing fullsend](../../reference/installation.md) instead — the mint is typically deployed once by a platform operator, and organizations are enrolled as needed.

## Hosted mint

The fullsend team operates a public hosted mint service. If your organization is enrolled, you can use it directly without deploying your own:

```
https://fullsend-mint-gljhbkcloq-uc.a.run.app
```

Pass this URL as `--mint-url` when running `fullsend admin install`, or set the `FULLSEND_MINT_URL` repository/org variable in GitHub. If you are using the hosted mint, the rest of this guide (deploying, enrolling, troubleshooting) is handled by the fullsend team — you do not need to manage mint infrastructure yourself.

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

- **fullsend CLI** — download the latest binary from [GitHub Releases](https://github.com/fullsend-ai/fullsend/releases)

- **GCP IAM roles** — the user running mint commands authenticates via ADC (`gcloud auth application-default login`). The required roles depend on the command:

  | IAM Role | `mint deploy` | `mint add-role` | `mint remove-role` | `mint enroll` | `mint unenroll` | `mint status` |
  |----------|:---:|:---:|:---:|:---:|:---:|:---:|
  | `roles/iam.serviceAccountAdmin` | x | | | | | |
  | `roles/iam.workloadIdentityPoolAdmin` | x | | | x | x | |
  | `roles/resourcemanager.projectIamAdmin` | \* | | | \*\* | | |
  | `roles/secretmanager.admin` | \* | \*\*\* | \*\*\*\* | | | |
  | `roles/cloudfunctions.developer` | x | | | | | |
  | `roles/cloudfunctions.viewer` | | x | x | x | x | x |
  | `roles/run.admin` | x | x | x | x | x | |
  | `roles/secretmanager.viewer` | | § | | | | x |

  \* `roles/resourcemanager.projectIamAdmin` and `roles/secretmanager.admin` are required for `mint deploy` only when using `--pem-dir` (first-time bootstrap). Standard deploys without `--pem-dir` do not need these roles.

  \*\* `roles/resourcemanager.projectIamAdmin` is required for `mint enroll` only in per-repo mode (`mint enroll owner/repo`). Org-scoped enrollment does not grant IAM bindings — use `inference provision` separately.

  \*\*\* `roles/secretmanager.admin` is required for `mint add-role` when uploading a new PEM (`--pem` or browser mode). When using `--use-existing-pem-secret`, only `roles/secretmanager.viewer` is required (see §).

  \*\*\*\* `roles/secretmanager.admin` is required for `mint remove-role` unless `--keep-pem` is passed (default deletes the PEM secret).

  § `roles/secretmanager.viewer` is required for `mint add-role` when using `--use-existing-pem-secret` (checks that the PEM secret exists).

  `roles/owner` covers all of the above for users with broad access.

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

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | | GCP project ID for the mint (required) |
| `--region` | `us-central1` | Cloud region for the mint function |
| `--pem-dir` | | Path to directory containing `{role}.pem` files (first-time bootstrap only); uses the default app set (`fullsend-ai`) |
| `--source-dir` | (embedded) | Path to local mint source directory (for development; default uses the embedded copy) |
| `--skip-deploy` | `false` | Skip code upload, reuse existing function (only update WIF/config) |
| `--dry-run` | `false` | Preview changes without making them |

### Bootstrapping PEMs (first-time only)

For first-time setup, the optional `--pem-dir` flag seeds the default app set's PEM secrets during deployment. This allows `mint enroll` to work immediately without running `admin install` first.

```bash
# First-time bootstrap with PEMs:
fullsend mint deploy --project="$GCP_PROJECT" --pem-dir=/path/to/pems
```

The `--pem-dir` directory must contain one `{role}.pem` file per agent role (e.g., `fullsend.pem`, `triage.pem`, `coder.pem`, `review.pem`, `retro.pem`, `prioritize.pem`). The CLI auto-discovers each app's numeric ID from the GitHub API by looking up the public app slug (`fullsend-ai-{role}`).

> **Note:** PEM bootstrapping requires `roles/resourcemanager.projectIamAdmin` and `roles/secretmanager.admin` in addition to the base roles. It also requires the GitHub Apps to already exist as public apps.

### Mint URL stability

The mint URL is stable across redeploys within the same project and region — updating the Cloud Function does not change its URL. Adding a new org to an existing mint only updates `ALLOWED_ORGS` (and WIF configuration) without redeploying the function. Shared `ROLE_APP_IDS` are managed at deploy/bootstrap time (`mint deploy --pem-dir`) or per-role via `mint add-role` / `remove-role` — not during enrollment. Existing enrolled repos continue working with no changes when orgs are added.

Deploying to a **different region** (e.g., changing `--region` from `us-central1` to `us-east5`) creates a new Cloud Run service with a different URL. All enrolled repos store the mint URL in a repo or org variable (`FULLSEND_MINT_URL`), so changing the region requires updating every enrolled repo's variable. Avoid changing `--region` after initial deployment unless you plan to update all consumers.

## Managing roles

Agent roles on the mint are **global** — each role maps to a GitHub App PEM secret (`fullsend-{role}-app-pem`) and an entry in the shared `ROLE_APP_IDS` environment variable. Use `fullsend mint add-role` and `fullsend mint remove-role` to manage individual roles after the mint is deployed.

| Command | When to use |
|---------|-------------|
| `mint deploy --pem-dir` | First-time bootstrap of the default app set (`fullsend-ai`) — seeds all default roles at once |
| `mint add-role` | Add a single role later, or register a custom app set one role at a time |
| `mint remove-role` | Remove a role from the mint (updates env vars; deletes PEM secret by default) |

`mint enroll` does **not** create or modify roles — it only authorizes orgs/repos to use roles that already exist on the mint.

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

The `fix` and `code` roles reuse the `coder` app — add role `coder` instead.

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

This command does not uninstall GitHub Apps from organizations or update org `.fullsend` configuration — use `fullsend github setup` or edit config repos separately.

## Enrolling organizations and repositories

`fullsend mint enroll` registers an organization or repository in the mint and configures WIF to accept OIDC tokens from the target.

```bash
# Enroll an organization
fullsend mint enroll acme-corp --project="$GCP_PROJECT"

# Enroll a specific repository
fullsend mint enroll acme-corp/my-repo --project="$GCP_PROJECT"
```

Enrollment does **not** grant Agent Platform (inference) access — use `fullsend inference provision` separately after enrollment. See [Installing fullsend](../../reference/installation.md) for the end-user inference setup path.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | | GCP project ID (required) |
| `--region` | `us-central1` | Cloud region for the mint service |
| `--dry-run` | `false` | Preview changes without making them |

### Migration from per-org app ID flags

Prior versions of `mint enroll` accepted `--app-set`, `--role-app-ids`, `--roles`, and `--source-org` to copy per-org app ID mappings into `ROLE_APP_IDS`. App IDs are now **shared per role** on the mint (like PEM secrets) and are set at deploy time via `mint deploy --pem-dir`, `fullsend admin install`, or per-role via `mint add-role`. Enrollment only adds the org to `ALLOWED_ORGS` and updates WIF — remove those flags from scripts and ensure the mint already has role-keyed `ROLE_APP_IDS` before enrolling.

### What enrollment does

1. Discovers the existing mint infrastructure and verifies shared role→app-id mappings exist
2. Updates the mint Cloud Run service environment variable `ALLOWED_ORGS` using REVISION-pinned traffic routing
3. Runs post-enrollment verification (see below)
4. Configures the mint-side WIF provider to accept OIDC tokens from the organization's repositories

Role PEM secrets and `ROLE_APP_IDS` must already exist on the mint, created during `mint deploy --pem-dir`, `fullsend admin install`, or `mint add-role`. Enrollment does not create, copy, or modify PEM secrets or app ID mappings.

### Post-enrollment verification

After updating the mint, the CLI automatically verifies that the enrollment took effect on the traffic-serving revision:

- **Revision state check** — confirms which Cloud Run revision is serving traffic and whether it matches the latest template
- **Env var read-back** — reads `ALLOWED_ORGS` from the traffic-serving revision (not the template) to confirm the enrolled org is present
- **Shared app IDs** — verifies the mint has role-keyed `ROLE_APP_IDS` entries (e.g., `coder`, `review`) for all configured roles

If verification fails, the CLI prints actionable diagnostics and suggests running `mint status` to investigate. See [Troubleshooting](#troubleshooting) for common failure scenarios.

### REVISION-pinned traffic routing

The CLI updates the Cloud Run service using a two-step process: first it patches the service template (env vars), then it explicitly routes 100% of traffic to the newly created revision. This is called REVISION-pinned routing.

This prevents a class of bugs where the service template is updated but traffic continues serving from an older revision with stale env vars. Without REVISION-pinned routing, a newly enrolled org might not be recognized by the mint because the traffic-serving revision still has the old `ALLOWED_ORGS` value.

### Enrollment ordering

Enroll organizations serially — do not run concurrent enrollment commands against the same mint. The CLI reads the current env vars, merges the new org's entries, and writes the result back. Two concurrent enrollments will race, and one org's entries may be lost.

## Unenrolling organizations and repositories

`fullsend mint unenroll` removes an organization or repository from the mint.

```bash
# Unenroll an organization
fullsend mint unenroll acme-corp --project="$GCP_PROJECT"

# Unenroll a specific repository
fullsend mint unenroll acme-corp/my-repo --project="$GCP_PROJECT"
```

Org-scoped unenroll removes the org from mint env vars and the shared WIF provider's attribute condition. Role PEM secrets are shared across orgs and are not modified. Repo-scoped unenroll only disables the repo-specific WIF provider (or permanently deletes it with `--delete-provider`) — it does not touch PEM secrets.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | | GCP project ID (required) |
| `--region` | `us-central1` | Cloud region for the mint service |
| `--delete-provider` | `false` | Permanently delete WIF provider (repo-scoped only) |
| `--dry-run` | `false` | Preview changes without making them |
| `--yolo` | `false` | Skip interactive confirmation (for automation) |

## Checking mint status

`fullsend mint status` inspects the deployed mint function, Cloud Run revision state, enrolled orgs, and PEM health. This is a read-only operation requiring only viewer-level access.

```bash
# Overview of all enrolled orgs
fullsend mint status --project="$GCP_PROJECT"

# Drill into a specific org's PEM status
fullsend mint status acme-corp --project="$GCP_PROJECT"
```

### What status reports

**Cloud Run revision section:**

- **Traffic revision** — which Cloud Run revision is currently serving requests (e.g., `fullsend-mint-00114-fm9`)
- **Allocation type** — `TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION` (pinned to a specific revision) or `TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST` (auto-routes to newest)
- **Template divergence** — a warning when the service template's latest revision does not match the traffic-serving revision, meaning the mint may be serving stale configuration
- **Recent revisions** — the last 5 revisions with their create time and active/inactive status

**Enrollment section:**

- List of enrolled organizations (from `ALLOWED_ORGS`)
- Shared role→app-id mappings (from role-keyed `ROLE_APP_IDS`)
- Per-repo WIF repos list

**Per-org drill-down** (when an org argument is provided):

- PEM secret status for each role (present/missing)

**Health summary:**

The status command reports overall health as one of:

| Health | Condition |
|--------|-----------|
| `healthy` | At least one org enrolled, template matches traffic revision |
| `degraded` | No enrolled orgs, OR template diverges from traffic-serving revision |
| `not-installed` | Mint function not found in the specified project/region |

## Minting tokens at runtime

`fullsend mint token` exchanges a GitHub Actions OIDC JWT for a short-lived, role-scoped GitHub App installation token. This is a runtime operation intended for use inside GitHub Actions workflows (not infrastructure management).

```bash
# Mint a token for the triage role scoped to a specific repo
fullsend mint token --role triage --repos my-repo --mint-url "$FULLSEND_MINT_URL"

# Mint a token scoped to multiple repos
fullsend mint token --role coder --repos repo-a,repo-b --mint-url "$FULLSEND_MINT_URL"
```

The command prints the token to stdout for shell capture. When running in GitHub Actions (`GITHUB_ACTIONS=true`), it also emits `::add-mask::` to stderr to prevent the token from appearing in logs.

**Required environment:** The calling GitHub Actions job must declare `id-token: write` permission so that `ACTIONS_ID_TOKEN_REQUEST_URL` and `ACTIONS_ID_TOKEN_REQUEST_TOKEN` are available.

## Multi-org setup

A single token mint can serve multiple GitHub organizations. The first org deploys the mint infrastructure and creates **public unlisted** GitHub Apps; additional orgs reuse the existing mint and install the same apps.

**First org (deploys mint + creates public apps):**

```bash
export FIRST_ORG="<first-github-org>"
export GCP_PROJECT="<your-gcp-project>"

fullsend admin install "$FIRST_ORG" \
  --inference-project "$GCP_PROJECT" \
  --mint-project "$GCP_PROJECT" \
  --public
```

The `--public` flag creates GitHub Apps as public unlisted — they won't appear in the marketplace but can be installed by other organizations via their installation URL.

When the first org uses a custom app set prefix, pass `--app-set` so the apps are named accordingly:

```bash
fullsend admin install "$FIRST_ORG" \
  --inference-project "$GCP_PROJECT" \
  --mint-project "$GCP_PROJECT" \
  --public \
  --app-set "$FIRST_ORG"
```

This creates public apps named `{first-org}-fullsend`, `{first-org}-coder`, etc.

**Additional orgs (install existing public apps):**

```bash
export ADDITIONAL_ORG="<additional-github-org>"
```

`GCP_PROJECT` and `FIRST_ORG` carry over from the first-org step above.

```bash
fullsend admin install "$ADDITIONAL_ORG" \
  --inference-project "$GCP_PROJECT" \
  --mint-project "$GCP_PROJECT"
```

The installer auto-detects shared public apps by matching installed app IDs against the mint's `ROLE_APP_IDS`. It records the actual app slug in `config.yaml`, so subsequent operations find the correct app regardless of naming convention.

If the public apps were created with a custom `--app-set`, pass the same value so the CLI uses the correct slug prefix for convention-based lookups:

```bash
fullsend admin install "$ADDITIONAL_ORG" \
  --inference-project "$GCP_PROJECT" \
  --mint-project "$GCP_PROJECT" \
  --app-set "$FIRST_ORG"
```

You can also pass `--mint-url "$MINT_URL"` explicitly to skip the auto-discovery step. PEMs use role-only naming (`fullsend-{role}-app-pem`) — one secret per role, shared across orgs on the mint.

> **Note:** Multi-org with `--public` requires all orgs to share the same GitHub Apps. Private apps (the default) are single-org only.

## Troubleshooting

### Template/traffic revision divergence

**Symptom:** `mint status` reports health as "degraded" with the message "template diverges from traffic-serving revision".

**What it means:** The Cloud Run service template was updated (e.g., env vars changed) but traffic is still routed to an older revision. The mint is serving requests with the old revision's configuration — newly enrolled orgs may not be recognized.

**Common causes:**

- A manual edit in the GCP Console that updated the template without routing traffic
- A failed traffic routing step during enrollment (the template PATCH succeeded but the traffic PATCH failed)
- An external tool (Terraform, `gcloud run deploy`) that doesn't use REVISION-pinned routing

**Resolution:**

1. Run `fullsend mint status --project="$GCP_PROJECT"` to confirm which revision is serving and what the template expects
2. Re-run `fullsend mint enroll` for any org — this triggers a new revision and routes traffic to it
3. If no enrollment is needed, manually route traffic with:

   ```bash
   gcloud run services update-traffic fullsend-mint \
     --project="$GCP_PROJECT" --region="$MINT_REGION" \
     --to-latest
   ```

### Post-enrollment verification failure

**Symptom:** After `mint enroll`, the CLI reports "Post-write verification FAILED" — the enrolled org is missing from the traffic-serving revision's `ALLOWED_ORGS`.

**What it means:** The env var update was applied to the service template, but the traffic-serving revision does not reflect the change. This typically means traffic routing did not complete.

**Resolution:**

1. Run `fullsend mint status --project="$GCP_PROJECT"` to check revision state
2. If the template diverges from traffic, re-run the enrollment command — the CLI will detect the org is already in the template and route traffic to the new revision
3. Check the CLI output for partial failure messages — if the traffic PATCH failed, the new revision name is reported for manual recovery

### LATEST allocation type

**Symptom:** `mint status` shows allocation type as `TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST` instead of `REVISION`.

**What it means:** Traffic is auto-routed to the newest revision. This can cause issues if a non-enrollment deployment creates a new revision that doesn't include the latest env vars (e.g., deploying new source code via `gcloud functions deploy` without preserving env vars).

**Resolution:** Re-run `fullsend mint enroll` for any org. The CLI always uses REVISION-pinned routing, which overrides the LATEST setting.

### Concurrent enrollment race

**Symptom:** After enrolling two orgs in parallel, one org is missing from `ALLOWED_ORGS`.

**What it means:** Both enrollment commands read the same initial state, merged their org independently, and wrote back. The second write overwrote the first org's entries.

**Resolution:**

1. Run `fullsend mint status` to confirm which org is missing
2. Re-run `fullsend mint enroll` for the missing org
3. Always enroll orgs serially — one at a time

### Mint not found

**Symptom:** `mint enroll` or `mint status` reports "mint not found in project X region Y".

**Resolution:**

1. Verify the `--project` and `--region` flags match where the mint was deployed
2. Run `fullsend mint deploy --project="$GCP_PROJECT"` to create the mint if it doesn't exist
3. If the mint was deployed to a different region, use the correct `--region` flag

### PEM secret errors

**Symptom:** `mint status` reports role PEM secrets as missing.

**Common causes:**

- Role PEM secrets were never bootstrapped — run `mint deploy --pem-dir` or `fullsend admin install` first
- The enrolled roles in `ROLE_APP_IDS` reference apps whose PEM secrets do not exist in Secret Manager

### Debugging with gcloud

When the CLI output is insufficient, inspect the Cloud Run service
directly. The commands below are **read-only** — they do not modify the
mint.

> **DANGER — never use `--set-env-vars` to modify the mint service.**
> `--set-env-vars` **replaces all** env vars, destroying ALLOWED_ORGS,
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

## See Also

- [Installing fullsend](../../reference/installation.md) — End-user setup (inference + GitHub)
- [Setting up with pre-provisioned infrastructure](../../reference/github-setup.md) — GitHub-only setup when GCP is already provisioned
- [Infrastructure Reference](infrastructure-reference.md) — Token mint, WIF, and secrets deployment details
- [CLI Internals](../dev/cli-internals.md) — Command structure and implementation details
