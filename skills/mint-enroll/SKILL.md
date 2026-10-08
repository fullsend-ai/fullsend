---
name: mint-enroll
description: >
  SRE runbook for enrolling new GitHub repos into the fullsend token
  mint service using `go run ./cmd/fullsend` from this checkout. Use when
  onboarding a new repo, adding a per-repo WIF provider, or re-enrolling after
  infrastructure changes.
allowed-tools: Bash
triggers:
  - mint enroll
  - onboard org
  - enroll organization
  - enroll repo
  - mint onboarding
---

# Mint Service Enrollment

Enroll a new GitHub repository into the fullsend token mint using
`go run ./cmd/fullsend mint` from this checkout. The mint is a stateless
service (deployed on GCP Cloud Function or Cloudflare Worker) that exchanges
GitHub OIDC JWTs for scoped GitHub App installation tokens.

Follow these steps in order. Do not skip steps.

**Always ask the operator for the GCP project ID.** Do not infer it from
`gcloud config get-value project` — the local config may point at the
wrong project.

**Always run the CLI from this checkout via `go run`.** From the repo root:

```bash
go run ./cmd/fullsend <subcommand> …
```

Do **not** use a `fullsend` binary from mise, `$PATH`, `go install`, or
another checkout. A stale CLI can rewrite hosted-mint env vars with obsolete
merge logic (this previously dropped `e2e`/`fix` from `ALLOWED_ROLES` and
broke e2e). See [Running the fullsend CLI](../../docs/contributing/go-code.md#running-the-fullsend-cli).

## Setup

**STOP — ask the operator for these values before proceeding:**

- `GCP_PROJECT` — the GCP project ID where the mint is deployed. Do not
  infer from `gcloud config get-value project`.
- `MINT_REGION` — the Cloud region (default: `us-central1`). Confirm with
  the operator if unsure.
- `TARGET` — the GitHub repo (`acme/widget`) to enroll. `mint enroll`
  accepts only `owner/repo`; a bare org is rejected with an error.

```bash
GCP_PROJECT="<your-gcp-project-id>"
MINT_REGION="us-central1"   # default; change if deployed elsewhere
```

Verify the operator has the required IAM roles: Workload Identity Pool Admin,
Cloud Functions Viewer, Cloud Run Admin. Secret Manager Admin is only needed
for initial PEM bootstrap (`mint deploy --pem-dir`), not for enrollment.
Enrollment does not grant any IAM roles; Vertex AI access for a repo is
provisioned separately via `fullsend inference provision`.

Verify credentials and that this checkout's CLI builds:

```bash
gcloud auth list --filter=status:ACTIVE --format="value(account)"
go run ./cmd/fullsend --version
```

## Constraints

- **No concurrent enrollment** — two operators enrolling simultaneously will
  race on env var reads/writes. Coordinate enrollment operations serially.
- **Always verify app installation** — the mint cannot produce tokens for
  GitHub Apps that are not installed on the target org. Confirm installation
  before the repo admin triggers a workflow.
- **Use `--dry-run` first** — especially for new operators or unfamiliar
  environments. Dry run previews all changes without applying them.
- **Do not enroll `.fullsend` repos** — `<org>/.fullsend` config repos
  belonged to the removed per-org installation mode and do not call the
  mint. Enroll each repository that runs fullsend workflows instead.

## Shared App Model

The fullsend-ai org maintains public GitHub Apps shared across orgs.

| Role | App Slug | Notes |
|------|----------|-------|
| fullsend | fullsend-ai-fullsend | Dispatch/admin. Not used by per-repo installs. |
| triage | fullsend-ai-triage | |
| coder | fullsend-ai-coder | `fix` role shares this app and PEM but has distinct token permissions. |
| review | fullsend-ai-review | |
| retro | fullsend-ai-retro | |
| prioritize | fullsend-ai-prioritize | |

PEM keys and app IDs are tied to the role, not the org. Secrets use role-only naming
(`fullsend-{role}-app-pem`) — one secret per role, shared across orgs on the
mint. `ROLE_APP_IDS` uses the same model: one GitHub App ID per role (e.g.,
`coder` → `123456`), shared by all enrolled repos. PEMs and app IDs must already
exist (from `mint deploy --pem-dir` or `go run ./cmd/fullsend admin install <owner/repo>`); enrollment
does not create, copy, or modify PEM secrets or app ID mappings.

Apps must be installed on the target org before the mint can produce tokens.
An org admin installs via `https://github.com/apps/{slug}/installations/new`
or by running `go run ./cmd/fullsend admin install <owner/repo>`.

## Enrollment Steps

### 1. Triage

Determine the target repository:

```bash
TARGET="<github-org>/<repo-name>"
```

Validate the target is a valid `owner/repo` name before proceeding.

### 2. Pre-check current state

Run `mint status --project` to see the current mint state, enrolled repos,
Cloud Run revision info, and PEM health — this is the enrollment/admin
pre-check step and must be run with `--project`, since only GCP-based mode
reports PEM health, Cloud Run revision info, and template divergence. If
`FULLSEND_MINT_URL` is set in the environment, pass `--mint-url=` (empty)
to force GCP-based mode — otherwise the CLI errors with "ambiguous mode":

```bash
go run ./cmd/fullsend mint status --mint-url= --project="$GCP_PROJECT" --region="$MINT_REGION"
```

When `FULLSEND_MINT_URL` is already configured, `mint status --mint-url`
is available as a lighter diagnostics command (no GCP IAM roles required),
but it only reports version, commit, org/allowed-orgs, roles, and
workflow-host repos — it does **not** show PEM health, Cloud Run revision
info, or the health summary, so it does not replace the `--project`
pre-check above:

```bash
go run ./cmd/fullsend mint status --mint-url="$FULLSEND_MINT_URL"
```

If the mint is not deployed yet, deploy it first:

```bash
go run ./cmd/fullsend mint deploy --project="$GCP_PROJECT" --region="$MINT_REGION"
```

Check the status output for:

- **Health**: should be "healthy" or "degraded" (not "not-installed")
- **Template divergence**: if the service template diverges from the
  traffic-serving revision, enrollment will fix this (the CLI uses
  REVISION-pinned traffic routing)
- **Existing enrollment**: if the target repo is already listed in the
  per-repo WIF repos, re-enrollment is safe — the CLI merges entries
  idempotently

For an org-level drill-down into PEM status (accepts org name only, not
`owner/repo` — use just the owner portion of the target):

```bash
go run ./cmd/fullsend mint status "<github-org>" --mint-url= --project="$GCP_PROJECT" --region="$MINT_REGION"
```

**STOP — show the status output to the operator.** Confirm the mint is
healthy and the enrollment target is correct before proceeding.

### 3. Enroll

Preview the enrollment first with `--dry-run`:

```bash
go run ./cmd/fullsend mint enroll "$TARGET" \
  --project="$GCP_PROJECT" \
  --region="$MINT_REGION" \
  --dry-run
```

**STOP — show the dry-run output to the operator and wait for explicit
confirmation before running the actual enrollment.**

If the preview looks correct and the operator confirms, run the actual
enrollment:

```bash
go run ./cmd/fullsend mint enroll "$TARGET" \
  --project="$GCP_PROJECT" \
  --region="$MINT_REGION"
```

The CLI performs the following automatically:

1. Discovers the existing mint infrastructure and verifies shared role→app-id mappings exist
2. Adds the repo to Cloud Run service env var `PER_REPO_WIF_REPOS` using REVISION-pinned traffic routing
3. Creates a dedicated WIF provider for the repo

On a public mint (`PER_REPO_WIF_REPOS=*`) the command reports public mode
and exits successfully without changing configuration.

### 4. Verify

Run `mint status` after enrollment (pass `--mint-url=` to force GCP-based
mode if `FULLSEND_MINT_URL` is set in the environment):

```bash
go run ./cmd/fullsend mint status --mint-url= --project="$GCP_PROJECT" --region="$MINT_REGION"
```

Check its output for:

- **Revision state**: confirms which Cloud Run revision is serving traffic
  and whether it matches the latest template
- **Per-Repo WIF Repos**: confirms the enrolled repo is listed. This list is
  read from the traffic-serving Cloud Run revision (falling back to Cloud
  Functions metadata only when revision env vars are unavailable); the
  traffic-serving revision is the authoritative enrollment check
- **ROLE_APP_IDS**: confirms shared role keys (e.g., `coder`, `review`) are configured on the mint

Common causes of verification failure:

- **Template/traffic divergence** — traffic routing step didn't complete.
  Re-run enrollment to trigger a new revision cycle.
- **Missing shared app IDs** — the mint has no role-keyed `ROLE_APP_IDS` entries.
  Run `mint deploy --pem-dir` or `go run ./cmd/fullsend admin install <owner/repo>` on the mint project first.

### 5. Handoff to repo admin

The mint SRE does not configure target repos. Inform the repo admin that
mint-side enrollment is complete and provide:

- **Mint URL**: shown in `mint status` output
- GitHub Actions repository variable `FULLSEND_MINT_URL` (set per repo by `go run ./cmd/fullsend admin install <owner/repo>` or manually)
- `.github/workflows/fullsend.yaml` shim workflow in the target repo

Also provide:

- **WIF Provider ID**: shown in the enrollment output (needed for the
  `google-github-actions/auth` step)

The admin runs `go run ./cmd/fullsend admin install <owner/repo>` for each
repository (the CLI no longer supports org-targeted installs) or configures
it manually.

Verify that all required GitHub Apps are installed on the target org
before the admin triggers a workflow.

## Rollback

**STOP — unenroll is a destructive operation that removes an org or repo
from the mint. Always run `--dry-run` first and confirm with the operator
before proceeding.**

Use the CLI to unenroll:

```bash
# Dry-run first
go run ./cmd/fullsend mint unenroll "$TARGET" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" --dry-run

# Actual unenroll (after operator confirms dry-run output)
go run ./cmd/fullsend mint unenroll "$TARGET" \
  --project="$GCP_PROJECT" --region="$MINT_REGION"
```

Unenroll is interactive — it requires typing the target name to confirm.
Use `--yolo` to skip confirmation in automated contexts.

Org-scoped unenroll removes the org from mint env vars and the shared WIF
provider's attribute condition. Role PEM secrets are shared across orgs and
are not modified. Repo-scoped unenroll disables the repo-specific WIF
provider — it does not touch PEM secrets.

To permanently delete a repo-scoped WIF provider instead of disabling it,
add `--delete-provider` to the unenroll command:

```bash
# Preview permanent WIF provider deletion first (repo-scoped only)
go run ./cmd/fullsend mint unenroll "$TARGET" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" --delete-provider --dry-run

# Permanently delete WIF provider (repo-scoped only, after dry-run confirms)
go run ./cmd/fullsend mint unenroll "$TARGET" \
  --project="$GCP_PROJECT" --region="$MINT_REGION" --delete-provider
```

Only use `--delete-provider` after confirming no workflows depend on the
provider.

## Troubleshooting

When the CLI output is insufficient, use `gcloud` to inspect the Cloud Run
service directly. The commands below are **read-only** — they do not
modify the mint.

**DANGER — never use `--set-env-vars` to modify the mint service.** The
`--set-env-vars` flag **replaces all** env vars, wiping out every other
variable (PER_REPO_WIF_REPOS, ROLE_APP_IDS, PEM secret references, etc.). If
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

Prefer `go run ./cmd/fullsend mint enroll` over manual `gcloud` env var edits — the
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
# List role PEM secrets (shared across orgs on the mint)
gcloud secrets list --project="$GCP_PROJECT" \
  --filter="name:fullsend- AND name:-app-pem" \
  --format="table(name,createTime)"

# Check if a specific role secret has an enabled version
gcloud secrets versions describe latest \
  --secret="fullsend-coder-app-pem" \
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
[mint administration guide](../../docs/guides/infrastructure/mint-administration.md)
in the mint administration guide.
