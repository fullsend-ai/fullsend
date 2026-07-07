---
sidebar_label: fullsend mint
---

# fullsend mint

Deploy and manage the OIDC token mint service. The mint is a GCP Cloud Function that exchanges GitHub Actions OIDC tokens for short-lived GitHub App installation tokens, enabling agents to authenticate without long-lived credentials.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend mint deploy` | Deploy or update the mint Cloud Function |
| `fullsend mint add-role <role>` | Register a role PEM and app ID on the mint |
| `fullsend mint remove-role <role>` | Remove a role from the mint |
| `fullsend mint enroll <org\|owner/repo>` | Register an org or repo in the mint |
| `fullsend mint unenroll <org\|owner/repo>` | Remove an org or repo from the mint |
| `fullsend mint status [org]` | Inspect mint state and PEM health |
| `fullsend mint token` | Mint a short-lived token via OIDC (for testing) |

## `mint deploy`

Deploys or updates the token mint Cloud Function, creating the service account, WIF pool, and Secret Manager secrets as needed.

```bash
fullsend mint deploy \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

The CLI automatically detects when the deployed function source is up-to-date (same source hash) and skips code redeployment, only updating WIF infrastructure and org registration.

Use `--public` to deploy a **public mint** (`ALLOWED_ORGS=*` with permissive WIF). Public mints accept any org that calls upstream reusable workflows in `fullsend-ai/fullsend`; org enrollment is not required. Unlike standalone JWKS mints, GCF-hosted public mints still need permissive WIF for the STS exchange path.

Redeploying an existing mint must match its mode: pass `--public` for public mints, omit it for tight mints. Mode conversion (tight ↔ public) is rejected at deploy time.

```bash
# Public mint (first-time bootstrap still needs --pem-dir):
fullsend mint deploy \
  --project "<GCP_PROJECT>" \
  --region "us-central1" \
  --pem-dir "/path/to/pems" \
  --public
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | | GCP project ID |
| `--region` | `us-central1` | Cloud region for the function |
| `--pem-dir` | | Directory containing role PEM files (first-time bootstrap) |
| `--public` | `false` | Deploy public mint (`ALLOWED_ORGS=*`, permissive WIF) |

### Required IAM roles

| Role | Description |
|------|-------------|
| `roles/iam.serviceAccountAdmin` | Create `fullsend-mint` service account |
| `roles/iam.workloadIdentityPoolAdmin` | Create WIF pool and provider |
| `roles/cloudfunctions.developer` | Deploy the Cloud Function |
| `roles/run.admin` | Set Cloud Run IAM policy |
| `roles/secretmanager.admin` | Create secrets (only with `--pem-dir`) |
| `roles/resourcemanager.projectIamAdmin` | Set project IAM policy (only with `--pem-dir`) |

### Required GCP APIs

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

## `mint add-role`

Registers a GitHub App role on the mint by uploading its PEM key and recording the app ID.

```bash
fullsend mint add-role <role> \
  --project "<GCP_PROJECT>" \
  --region "us-central1" \
  --pem "<path-to-pem>" \
  --app-id "<github-app-id>"
```

Pass `--use-existing-pem-secret` to reference a PEM secret that already exists in Secret Manager (only requires `roles/secretmanager.viewer`).

## `mint remove-role`

Removes a role from the mint. Deletes the PEM secret by default.

```bash
fullsend mint remove-role <role> \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

Pass `--keep-pem` to preserve the PEM secret in Secret Manager.

## `mint enroll`

Registers a GitHub organization or repository in the mint's allowed list, enabling it to request tokens.

```bash
fullsend mint enroll <org> \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

Per-repo mode:

```bash
fullsend mint enroll <owner/repo> \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

## `mint unenroll`

Removes an organization or repository from the mint's allowed list.

```bash
fullsend mint unenroll <org|owner/repo> \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

## `mint status`

Inspects the mint's current state: deployed function, registered roles, enrolled orgs, and PEM health.

```bash
fullsend mint status \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

Optionally filter to a specific org:

```bash
fullsend mint status <org> \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

Read-only — makes no changes.

## `mint token`

Mints a short-lived GitHub App installation token via OIDC exchange. Primarily used for testing.

```bash
fullsend mint token \
  --role <name> \
  --repos <repo1,repo2> \
  --mint-url <url>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--role` | | Agent role (triage, coder, review, etc.) |
| `--repos` | | Comma-separated repository names |
| `--mint-url` | `$FULLSEND_MINT_URL` | Mint service URL |
| `--audience` | `fullsend-mint` | OIDC audience |

## See also

- [Mint service administration](../guides/infrastructure/mint-administration.md) — deployment and management guide
- [Infrastructure reference](../guides/infrastructure/infrastructure-reference.md) — architecture details
- [Operations](../guides/getting-started/operations.md) — standalone commands and IAM role breakdown
- [CLI internals](../guides/dev/cli-internals.md) — command tree and implementation details
