---
name: mint-enroll
description: >
  SRE runbook for enrolling new GitHub orgs or repos into the fullsend token
  mint service using the fullsend CLI. Use when onboarding a new org, adding a
  per-repo WIF provider, or re-enrolling after infrastructure changes.
allowed-tools: Bash
triggers:
  - mint enroll
  - onboard org
  - enroll organization
  - enroll repo
  - mint onboarding
---

# Mint Service Enrollment

Enroll a new GitHub org or per-repo into the fullsend token mint using the
`fullsend mint` CLI. The production mint is a stateless GCP Cloud Function that
exchanges GitHub OIDC JWTs for scoped GitHub App installation tokens. For
development without GCP, see the [dev mint guide](../../docs/guides/infrastructure/dev-mint.md).

Follow these steps in order. Do not skip steps.

**Always ask the operator for the GCP project ID.** Do not infer it from
`gcloud config get-value project` — the local config may point at the
wrong project.

## Setup

**STOP — ask the operator for these values before proceeding:**

- `GCP_PROJECT` — the GCP project ID where the mint is deployed. Do not
  infer from `gcloud config get-value project`.
- `MINT_REGION` — the Cloud region (default: `us-central1`). Confirm with
  the operator if unsure.
- `TARGET` — the GitHub org (`acme`) or repo (`acme/widget`) to enroll.

```bash
GCP_PROJECT="<your-gcp-project-id>"
MINT_REGION="us-central1"   # default; change if deployed elsewhere
```

Verify the operator has the required IAM roles: Secret Manager Admin,
Workload Identity Pool Admin, Cloud Functions Viewer, Cloud Run Admin.
Per-repo enrollment additionally requires Project IAM Admin (to grant
`roles/aiplatform.user` to the repo WIF principal).

Verify credentials and that the fullsend CLI is available:

```bash
gcloud auth list --filter=status:ACTIVE --format="value(account)"
fullsend --version
```

## Constraints

- **No concurrent enrollment** — two operators enrolling simultaneously will
  race on env var reads/writes. Coordinate enrollment operations serially.
- **Always verify app installation** — the mint cannot produce tokens for
  GitHub Apps that are not installed on the target org. Confirm installation
  before the repo admin triggers a workflow.
- **Use `--dry-run` first** — especially for new operators or unfamiliar
  environments. Dry run previews all changes without applying them.
- **Do not enroll `.fullsend` repos** — `.fullsend` repos use the shared
  per-org WIF provider. Enroll the org instead.

## Shared App Model

The fullsend-ai org maintains public GitHub Apps shared across orgs.

| Role | App Slug | Notes |
|------|----------|-------|
| fullsend | fullsend-ai-fullsend | Dispatch/admin. Per-org only — excluded from per-repo installs. |
| triage | fullsend-ai-triage | |
| coder | fullsend-ai-coder | `fix` role shares this app and PEM but has distinct token permissions. |
| review | fullsend-ai-review | |
| retro | fullsend-ai-retro | |
| prioritize | fullsend-ai-prioritize | |

PEM keys are tied to the app, not the org. Enrolling a new org copies PEMs
from the app set (default: `fullsend-ai`).

Apps must be installed on the target org before the mint can produce tokens.
An org admin installs via `https://github.com/apps/{slug}/installations/new`
or by running `fullsend admin install`.

## Enrollment Steps

### 1. Triage

Determine enrollment type and target. Choose one:

```bash
# Per-org enrollment
TARGET="<github-org>"

# Per-repo enrollment
TARGET="<github-org>/<repo-name>"
```

Validate the target is a valid GitHub org or owner/repo name before
proceeding.

### 2. Pre-check current state

Run `mint status` to see the current mint state, enrolled orgs, Cloud Run
revision info, and PEM health:

```bash
fullsend mint status --project="$GCP_PROJECT" --region="$MINT_REGION"
```

If the mint is not deployed yet, deploy it first:

```bash
fullsend mint deploy --project="$GCP_PROJECT" --region="$MINT_REGION"
```

Check the status output for:

- **Health**: should be "healthy" or "degraded" (not "not-installed")
- **Template divergence**: if the service template diverges from the
  traffic-serving revision, enrollment will fix this (the CLI uses
  REVISION-pinned traffic routing)
- **Existing enrollment**: if the target org is already listed, re-enrollment
  is safe — the CLI merges entries idempotently

For per-org drill-down into PEM status (accepts org name only, not
`owner/repo` — for per-repo enrollment, use just the org portion):

```bash
fullsend mint status "<github-org>" --project="$GCP_PROJECT" --region="$MINT_REGION"
```

**STOP — show the status output to the operator.** Confirm the mint is
healthy and the enrollment target is correct before proceeding.

### 3. Enroll

Preview the enrollment first with `--dry-run`:

```bash
fullsend mint enroll "$TARGET" \
  --project="$GCP_PROJECT" \
  --region="$MINT_REGION" \
  --dry-run
```

**STOP — show the dry-run output to the operator and wait for explicit
confirmation before running the actual enrollment.**

If the preview looks correct and the operator confirms, run the actual
enrollment:

```bash
fullsend mint enroll "$TARGET" \
  --project="$GCP_PROJECT" \
  --region="$MINT_REGION"
```

The CLI performs the following automatically:

1. Discovers the existing mint infrastructure and resolves role→app-id mappings
2. Copies PEM secrets from the app set to the new org's scoped keys
3. Updates Cloud Run service env vars (ALLOWED_ORGS, ROLE_APP_IDS) using
   REVISION-pinned traffic routing
4. Runs post-enrollment verification
5. Configures WIF provider (shared for per-org, dedicated for per-repo)

**Optional flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--app-set` | `fullsend-ai` | App set to copy PEMs and app IDs from |
| `--role-app-ids` | | Explicit JSON map of role→app-id (overrides `--app-set`) |
| `--roles` | `fullsend,triage,coder,review,retro,prioritize` | Comma-separated roles to enroll |

### 4. Verify

The CLI runs post-enrollment verification automatically. Check its output for:

- **Revision state**: confirms which Cloud Run revision is serving traffic
  and whether it matches the latest template
- **ALLOWED_ORGS**: confirms the enrolled org is present in the
  traffic-serving revision's env vars
- **ROLE_APP_IDS**: confirms all expected role keys are present

If the CLI reports "Post-write verification FAILED", run `mint status` to
diagnose:

```bash
fullsend mint status --project="$GCP_PROJECT" --region="$MINT_REGION"
```

Common causes of verification failure:

- **Template/traffic divergence** — traffic routing step didn't complete.
  Re-run enrollment to trigger a new revision cycle.
- **Missing role keys** — the app set doesn't have all roles. Use
  `--role-app-ids` to provide explicitly.

### 5. Handoff to repo admin

The mint SRE does not configure target repos. Inform the repo admin that
mint-side enrollment is complete and provide:

- **Mint URL**: shown in `mint status` output
- GitHub Actions org variable `FULLSEND_MINT_URL` (set by `fullsend admin install` or manually)
- `.github/workflows/fullsend.yaml` shim workflow in the target repo

For per-repo enrollments, also provide:

- **WIF Provider ID**: shown in the enrollment output (needed for the
  `google-github-actions/auth` step)

For per-org, the `repo-maintenance` workflow in `.fullsend` handles shim
deployment. For per-repo, the admin runs
`fullsend admin install <owner/repo>` or configures manually.

Verify that all required GitHub Apps are installed on the target org
before the admin triggers a workflow.

## Rollback

**STOP — unenroll is a destructive operation that removes an org or repo
from the mint. Always run `--dry-run` first and confirm with the operator
before proceeding.**

Use the CLI to unenroll:

```bash
# Dry-run first
fullsend mint unenroll "$TARGET" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" --dry-run

# Actual unenroll (after operator confirms dry-run output)
fullsend mint unenroll "$TARGET" \
  --project="$GCP_PROJECT" --region="$MINT_REGION"
```

Unenroll is interactive — it requires typing the target name to confirm.
Use `--yolo` to skip confirmation in automated contexts.

By default, org-scoped unenroll disables (not deletes) PEM secrets and
removes the org from the shared WIF provider's attribute condition.
Repo-scoped unenroll disables the repo-specific WIF provider — it does
not touch PEM secrets. Both are reversible — re-enrollment will re-enable
disabled resources and re-add the org to the WIF condition.

To permanently delete instead of disable, add `--delete-secrets` (org only)
or `--delete-provider` (repo only) to the same unenroll command. These are
flags on a single invocation, not separate follow-up commands:

```bash
# Preview permanent secret deletion first
fullsend mint unenroll "$TARGET" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" --delete-secrets --dry-run

# Permanently delete PEM secrets (org-scoped only, after dry-run confirms)
fullsend mint unenroll "$TARGET" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" --delete-secrets

# Preview permanent WIF provider deletion first (repo-scoped only)
fullsend mint unenroll "$TARGET" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" --delete-provider --dry-run

# Permanently delete WIF provider (repo-scoped only, after dry-run confirms)
fullsend mint unenroll "$TARGET" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" --delete-provider
```

Only use `--delete-secrets` or `--delete-provider` after confirming no
workflows depend on the resources.

## Troubleshooting

When the CLI output is insufficient, use `gcloud` to inspect the Cloud Run
service directly. The commands below are **read-only** — they do not
modify the mint.

**DANGER — never use `--set-env-vars` to modify the mint service.** The
`--set-env-vars` flag **replaces all** env vars, wiping out every other
variable (ALLOWED_ORGS, ROLE_APP_IDS, PEM secret references, etc.). If
you need to fix an env var manually, use `--update-env-vars` which
**merges** the provided values into the existing set:

```bash
# SAFE — merges into existing env vars
gcloud run services update "$MINT_SERVICE" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" \
  --update-env-vars="KEY=value"

# DANGEROUS — replaces ALL env vars, destroying every other variable
# gcloud run services update "$MINT_SERVICE" --set-env-vars="KEY=value"
```

Prefer `fullsend mint enroll` over manual `gcloud` env var edits — the
CLI handles read-merge-write with REVISION-pinned traffic routing.

Set these variables for the commands below:

```bash
MINT_SERVICE="fullsend-mint"
ORG="<github-org>"           # the org you are troubleshooting
WIF_POOL="fullsend-pool"
```

### Read env vars from the traffic-serving revision

The traffic-serving revision may differ from the service template. To see
what the mint is actually serving:

```bash
# Get the traffic-serving revision name
TRAFFIC_REV=$(gcloud run services describe "$MINT_SERVICE" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" \
  --format="value(status.traffic[0].revisionName)")

# Read its env vars
gcloud run revisions describe "$TRAFFIC_REV" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" \
  --format="yaml(spec.containers[0].env)"
```

### Compare template vs traffic revision

```bash
# Template env vars (what new revisions would get)
gcloud run services describe "$MINT_SERVICE" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" \
  --format="yaml(spec.template.spec.containers[0].env)"

# List recent revisions
gcloud run revisions list \
  --service="$MINT_SERVICE" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" \
  --limit=5
```

### Check PEM secrets

```bash
# List all PEM secrets for the org
gcloud secrets list --project="$GCP_PROJECT" \
  --filter="name:fullsend-${ORG}--" \
  --format="table(name,createTime)"

# Check if a specific secret has an enabled version
gcloud secrets versions describe latest \
  --secret="fullsend-${ORG}--coder-app-pem" \
  --project="$GCP_PROJECT" --format="value(state)"
```

### Check WIF provider state

```bash
# List all WIF providers
gcloud iam workload-identity-pools providers list \
  --project="$GCP_PROJECT" \
  --workload-identity-pool="$WIF_POOL" \
  --location=global \
  --format="table(name.basename(),state,disabled)"

# Check the shared provider's attribute condition
gcloud iam workload-identity-pools providers describe github-oidc \
  --project="$GCP_PROJECT" \
  --workload-identity-pool="$WIF_POOL" \
  --location=global \
  --format="value(attributeCondition)"
```

### Read mint logs

```bash
gcloud functions logs read fullsend-mint \
  --project="$GCP_PROJECT" --region="$MINT_REGION" --gen2 --limit=50 \
  --format="table(timecreated,severity,textPayload)" \
  | grep -i "$ORG"
```

For more troubleshooting scenarios, see the
[Troubleshooting section](../../docs/guides/infrastructure/mint-administration.md#troubleshooting)
in the mint administration guide.
