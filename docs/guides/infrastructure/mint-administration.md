# Mint service administration

This guide covers deploying and managing the fullsend token mint Cloud Function. The mint is the OIDC token exchange service that lets GitHub Actions workflows authenticate as GitHub Apps — it is infrastructure that serves all enrolled organizations and repositories.

> **This guide is for platform operators** who deploy, manage, or troubleshoot the token mint Cloud Function. If you are an end user setting up fullsend for your organization, see [Installing fullsend](../getting-started/installation.md) instead — the mint is typically deployed once by a platform operator, and organizations are enrolled as needed. Work is in progress to offer a hosted public mint service, which will further reduce the need for per-org mint administration.

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

  | IAM Role | `mint deploy` | `mint enroll` | `mint unenroll` | `mint status` |
  |----------|:---:|:---:|:---:|:---:|
  | `roles/iam.serviceAccountAdmin` | x | | | |
  | `roles/iam.workloadIdentityPoolAdmin` | x | x | x | |
  | `roles/resourcemanager.projectIamAdmin` | \* | \*\* | | |
  | `roles/secretmanager.admin` | \* | x | x† | |
  | `roles/cloudfunctions.developer` | x | | | |
  | `roles/cloudfunctions.viewer` | | x | x | x |
  | `roles/run.admin` | x | x | x | |
  | `roles/secretmanager.viewer` | | | | x |

  \* `roles/resourcemanager.projectIamAdmin` and `roles/secretmanager.admin` are required for `mint deploy` only when using `--pem-dir` (first-time bootstrap). Standard deploys without `--pem-dir` do not need these roles.

  \*\* `roles/resourcemanager.projectIamAdmin` is required for `mint enroll` only in per-repo mode (`mint enroll owner/repo`). Org-scoped enrollment does not grant IAM bindings — use `inference provision` separately.

  † `roles/secretmanager.admin` is required for `mint unenroll` only in org-scoped mode. Repo-scoped unenroll does not touch PEM secrets.

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

The binary includes an embedded copy of the mint Cloud Function source, so it works standalone without needing the repository checked out. If you are developing or testing changes to the mint source, run the CLI from a local clone — the `--source-dir` flag (default `internal/mint/`) uses your local copy when the path exists, falling back to the embedded source when it does not.

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

The `--pem-dir` directory must contain one `{role}.pem` file per agent role (e.g., `fullsend.pem`, `triage.pem`, `code.pem`, `review.pem`, `retro.pem`, `prioritize.pem`). The CLI auto-discovers each app's numeric ID from the GitHub API by looking up the public app slug (`fullsend-ai-{role}`).

> **Note:** PEM bootstrapping requires `roles/resourcemanager.projectIamAdmin` and `roles/secretmanager.admin` in addition to the base roles. It also requires the GitHub Apps to already exist as public apps.

### Mint URL stability

The mint URL is stable across redeploys within the same project and region — updating the Cloud Function does not change its URL. Adding a new org to an existing mint only updates env vars (`ROLE_APP_IDS`, `ALLOWED_ORGS`) without redeploying the function. Existing enrolled repos continue working with no changes.

Deploying to a **different region** (e.g., changing `--region` from `us-central1` to `us-east5`) creates a new Cloud Run service with a different URL. All enrolled repos store the mint URL in a repo or org variable (`FULLSEND_MINT_URL`), so changing the region requires updating every enrolled repo's variable. Avoid changing `--region` after initial deployment unless you plan to update all consumers.

## Enrolling organizations and repositories

`fullsend mint enroll` registers an organization or repository in the mint, copies PEM secrets, and configures WIF to accept OIDC tokens from the target.

```bash
# Enroll an organization
fullsend mint enroll acme-corp --project="$GCP_PROJECT"

# Enroll a specific repository
fullsend mint enroll acme-corp/my-repo --project="$GCP_PROJECT"
```

Enrollment does **not** grant Agent Platform (inference) access — use `fullsend inference provision` separately after enrollment. See [Installing fullsend](../getting-started/installation.md) for the end-user inference setup path.

### What enrollment does

1. Copies GitHub App PEM secrets to the new org's scoped key (`fullsend-{org}--{role}-app-pem`)
2. Updates the mint Cloud Function environment variables (`ALLOWED_ORGS`, `ROLE_APP_IDS`)
3. Configures the mint-side WIF provider to accept OIDC tokens from the organization's repositories

## Unenrolling organizations and repositories

`fullsend mint unenroll` removes an organization or repository from the mint.

```bash
# Unenroll an organization
fullsend mint unenroll acme-corp --project="$GCP_PROJECT"

# Unenroll a specific repository
fullsend mint unenroll acme-corp/my-repo --project="$GCP_PROJECT"
```

Org-scoped unenroll removes PEM secrets and updates WIF providers. Repo-scoped unenroll only removes or disables the repo-specific WIF provider — it does not touch PEM secrets.

## Checking mint status

`fullsend mint status` inspects the deployed mint function and PEM health. This is a read-only operation requiring only viewer-level access.

```bash
fullsend mint status --project="$GCP_PROJECT"
```

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

This creates public apps named `{first-org}-fullsend`, `{first-org}-code`, etc.

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

The installer auto-detects shared public apps by matching installed app IDs against the mint's `ROLE_APP_IDS`. It copies PEM secrets from the app set to the new org's scoped key and records the actual app slug in `config.yaml`, so subsequent operations find the correct app regardless of naming convention.

If the public apps were created with a custom `--app-set`, pass the same value so the CLI uses the correct slug prefix for convention-based lookups:

```bash
fullsend admin install "$ADDITIONAL_ORG" \
  --inference-project "$GCP_PROJECT" \
  --mint-project "$GCP_PROJECT" \
  --app-set "$FIRST_ORG"
```

You can also pass `--mint-url "$MINT_URL"` explicitly to skip the auto-discovery step. PEMs use org-scoped naming (`fullsend-{org}--{role}-app-pem`), so each org's secrets are stored independently. For public apps (shared across orgs), the provisioner copies the same PEM under each org's scoped key.

> **Note:** Multi-org with `--public` requires all orgs to share the same GitHub Apps. Private apps (the default) are single-org only.

## See Also

- [Installing fullsend](../getting-started/installation.md) — End-user setup (inference + GitHub)
- [Setting up with pre-provisioned infrastructure](../getting-started/github-setup.md) — GitHub-only setup when GCP is already provisioned
- [Infrastructure Reference](infrastructure-reference.md) — Token mint, WIF, and secrets deployment details
- [CLI Internals](../dev/cli-internals.md) — Command structure and implementation details
