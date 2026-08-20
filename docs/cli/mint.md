---
sidebar_label: fullsend mint
---

# fullsend mint

Deploy and manage the OIDC token mint service. The mint exchanges GitHub Actions OIDC tokens for short-lived GitHub App installation tokens, enabling agents to authenticate without long-lived credentials. The mint can be deployed on GCP (Cloud Function) or Cloudflare (Worker).

## Commands

| Command | Description |
|---------|-------------|
| `fullsend mint deploy` | Deploy or update the token mint (GCP or Cloudflare) |
| `fullsend mint delete` | Tear down mint infrastructure (GCP or Cloudflare) |
| `fullsend mint add-role <role>` | Register a role PEM and app ID on the mint |
| `fullsend mint remove-role <role>` | Remove a role from the mint |
| `fullsend mint enroll <org\|owner/repo>` | Register an org or repo in the mint |
| `fullsend mint unenroll <org\|owner/repo>` | Remove an org or repo from the mint |
| `fullsend mint workflow-host add <owner/repo>` | Add a repo to the workflow-host allow-list |
| `fullsend mint workflow-host remove <owner/repo>` | Remove a repo from the workflow-host allow-list |
| `fullsend mint workflow-host list` | List the workflow-host allow-list |
| `fullsend mint status [org]` | Inspect mint state and PEM health |
| `fullsend mint token` | Mint a short-lived token via OIDC (for testing) |

## `mint deploy`

Deploys or updates the token mint. Use `--platform` to select the target platform (default: `gcp`).

### GCP mode (`--platform=gcp`)

Deploys the mint as a GCP Cloud Function, creating the service account, WIF pool, and Secret Manager secrets as needed.

```bash
fullsend mint deploy \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

The CLI automatically detects when the deployed function source is up-to-date (same source hash) and skips code redeployment, only updating WIF infrastructure and org registration.

Use `--public` to deploy a **public mint** (`PER_REPO_WIF_REPOS=*` with permissive WIF). Public mints accept any org that calls upstream reusable workflows in `fullsend-ai/fullsend`; org enrollment is not required. Unlike standalone JWKS mints, GCF-hosted public mints still need permissive WIF for the STS exchange path.

Redeploying an existing mint must match its mode: pass `--public` for public mints, omit it for tight mints. Mode conversion (tight ↔ public) is rejected at deploy time.

```bash
# Public mint (first-time bootstrap still needs --pem-dir):
fullsend mint deploy \
  --project "<GCP_PROJECT>" \
  --region "us-central1" \
  --pem-dir "/path/to/pems" \
  --public
```

### Cloudflare mode (`--platform=cloudflare`)

Deploys the mint as a Cloudflare Worker running the mintcore WASM module with a thin TypeScript adapter. The WASM binary and `wasm_exec.js` are auto-built at deploy time if not already present (requires Go toolchain + wrangler).

```bash
fullsend mint deploy \
  --platform cloudflare
```

Use `--preview=<alias>` for ephemeral preview deploys. This runs `wrangler versions upload --preview-alias=<alias>` instead of `wrangler deploy`, so the durable Worker script is not affected. The preview mint URL includes the account's workers.dev subdomain: `https://<alias>-<worker-name>.<subdomain>.workers.dev` (e.g., `https://bt-abc123-bt-mint.fullsend-ai.workers.dev`). The subdomain is resolved at deploy time from the Wrangler output or the Cloudflare API. Preview teardown via `mint delete --platform=cloudflare --preview=<alias>` abandons the alias without deleting the Worker script.

If the target Worker script does not yet exist (first-time preview on a new `--worker-name`), the CLI automatically creates it with a one-time durable deploy before proceeding with the preview upload. Subsequent preview deploys skip this bootstrap step. When `--pem-dir` is set, the bootstrap deploy includes PEM secrets so the Worker is immediately usable.

Use `--worker-name` to target a specific Worker script name.

#### Custom domain

Use `--custom-domain` to attach a [Workers Custom Domain](https://developers.cloudflare.com/workers/configuration/routing/custom-domains/) (e.g. `mint.fullsend.sh`) to the durable Worker. The zone ID is resolved automatically from the domain name via the Cloudflare API. Custom domains are only supported for durable deploys — preview deploys use bare `workers.dev` hostnames.

```bash
fullsend mint deploy \
  --platform cloudflare \
  --custom-domain "mint.fullsend.sh"
```

When a custom domain is configured, the mint URL (`FULLSEND_MINT_URL`) uses the custom domain hostname instead of the `workers.dev` URL.

To tear down a durable Worker with a custom domain, pass `--custom-domain` to `mint delete` so the CLI removes the domain binding before deleting the Worker.

Authentication (one of):
- `CLOUDFLARE_API_TOKEN` env var (+ `CLOUDFLARE_ACCOUNT_ID`) — API token with Workers write permission
- Wrangler OAuth session (`wrangler login`, then `wrangler whoami`) — when `CLOUDFLARE_API_TOKEN` is unset, the CLI falls back to the Wrangler login session. If `CLOUDFLARE_ACCOUNT_ID` is also unset, the CLI discovers the account from `wrangler whoami`.

#### Omit-vs-empty semantics for config flags on redeploy

**Durable deploys** use `--keep-vars` so existing Worker bindings are preserved when a flag is omitted:

- **Flag omitted:** existing Worker value is preserved.
- **Flag non-empty:** Worker binding set to the given value.
- **Flag set to `""`:** Worker binding cleared (set to empty string).

Example: `--per-repo-wif-repos=` clears `PER_REPO_WIF_REPOS` without requiring `wrangler delete` first.

**Preview deploys** do **not** use `--keep-vars`. Each preview version is self-contained — only the `--var` env vars and `--secrets-file` PEMs passed in the deploy command are applied. This prevents cross-preview contamination when deploying multiple preview aliases in sequence (e.g. `both` → `per-repo` → `per-org`). `ALLOWED_WORKFLOW_FILES` defaults to `*` on preview when `--allowed-workflow-files` is omitted, so previews are usable out of the box (mintcore deny-alls workflow refs when the env var is unset). Pass an explicit value to restrict.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--platform` | `gcp` | Target platform: `gcp` or `cloudflare` |
| `--project` | | GCP project ID (GCP only) |
| `--region` | `us-central1` | Cloud region for the function (GCP only) |
| `--pem-dir` | | Directory containing `{role}.pem` files for PEM bootstrap |
| `--app-set` | `fullsend-ai` | App set name for PEM bootstrap |
| `--roles` | _(default roles)_ | Comma-separated role names to bootstrap with `--pem-dir`. Overrides the default set. Example: `--roles=fullsend,triage,coder,review,retro,prioritize,e2e` |
| `--public` | `false` | Deploy public mint (`PER_REPO_WIF_REPOS=*`). Mutually exclusive with `--per-repo-wif-repos` on Cloudflare |
| `--source-dir` | | Path to local mint source (default: checkout path when present, embedded otherwise) |
| `--dry-run` | `false` | Preview changes without making them |
| `--skip-deploy` | `false` | Skip code upload, reuse existing function (GCP only) |
| `--worker-name` | `fullsend-mint` | Cloudflare Worker script name (Cloudflare only) |
| `--preview` | `""` | Preview alias for `wrangler versions upload` (Cloudflare only). Example: `--preview=bt-run-42` |
| `--allowed-orgs` | | Comma-separated allowed GitHub orgs (Cloudflare only, sets `ALLOWED_ORGS`). Omit to preserve existing; set to `""` to clear |
| `--per-repo-wif-repos` | | Comma-separated per-repo WIF repos (Cloudflare only, sets `PER_REPO_WIF_REPOS`). Mutually exclusive with `--public` |
| `--workflow-host-repos` | | Comma-separated workflow host repos (Cloudflare only, sets `WORKFLOW_HOST_REPOS`). Omit to preserve existing; set to `""` to clear |
| `--allowed-workflow-files` | | Comma-separated workflow file basenames (Cloudflare only, sets `ALLOWED_WORKFLOW_FILES`). Durable: omit to preserve existing binding; set to `""` to clear. Preview: defaults to `*` when omitted (all basenames allowed) |
| `--custom-domain` | | Hostname to attach as a Workers Custom Domain (Cloudflare only, durable deploys only). Zone ID is resolved automatically. Example: `--custom-domain=mint.fullsend.sh` |

### Required IAM roles (GCP)

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

## `mint delete`

Tears down mint infrastructure. This is the inverse of `mint deploy`. Use `--platform` to select the target platform (default: `gcp`).

### GCP mode (`--platform=gcp`)

Deletes all GCP mint infrastructure in order: Cloud Function, PEM secrets, service account, and WIF pool (with all providers). Non-critical resource failures (service account, WIF pool) are reported as warnings rather than hard errors.

```bash
fullsend mint delete \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

### Cloudflare durable mode (`--platform=cloudflare`)

Deletes the durable Worker script and all associated bindings/secrets via `wrangler delete`. When the Worker was deployed with a custom domain, pass `--custom-domain` to also remove the custom domain binding before deleting the Worker.

```bash
fullsend mint delete --platform cloudflare

# With custom domain teardown:
fullsend mint delete --platform cloudflare \
  --custom-domain "mint.fullsend.sh"
```

### Cloudflare preview mode (`--platform=cloudflare --preview=<alias>`)

Abandons the preview alias without deleting the durable Worker script. This is the explicit teardown for preview mints deployed with `mint deploy --preview=<alias>`.

```bash
fullsend mint delete --platform cloudflare --preview bt-run-42
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--platform` | `gcp` | Target platform: `gcp` or `cloudflare` |
| `--project` | | GCP project ID (GCP only, required) |
| `--region` | `us-central1` | GCP region for the Cloud Function (GCP only) |
| `--worker-name` | `fullsend-mint` | Cloudflare Worker script name (Cloudflare only) |
| `--preview` | | Tear down a preview mint identified by this alias (Cloudflare only) |
| `--custom-domain` | | Custom domain hostname to remove during teardown (Cloudflare only, zone ID resolved automatically) |
| `--dry-run` | `false` | Preview changes without making them |
| `--yolo` | `false` | Skip confirmation prompt |

### Required IAM roles (GCP)

| Role | Description |
|------|-------------|
| `roles/cloudfunctions.developer` | Delete the Cloud Function |
| `roles/secretmanager.admin` | Delete PEM secrets |
| `roles/iam.serviceAccountAdmin` | Delete the mint service account |
| `roles/iam.workloadIdentityPoolAdmin` | Delete the WIF pool and providers |

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

Enrollment creates the WIF provider needed for OIDC verification only — it does not grant any IAM roles. Vertex AI access is provisioned separately via `fullsend inference provision`.

## `mint unenroll`

Removes an organization or repository from the mint's allowed list.

```bash
fullsend mint unenroll <org|owner/repo> \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

## `mint workflow-host`

Manages the `WORKFLOW_HOST_REPOS` allow-list that controls which repositories may host workflows calling the mint for per-repo callers. Per-org callers are not affected.

### `mint workflow-host add`

```bash
fullsend mint workflow-host add <owner/repo> \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

Idempotent — skips repos already listed.

### `mint workflow-host remove`

```bash
fullsend mint workflow-host remove <owner/repo> \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

### `mint workflow-host list`

```bash
fullsend mint workflow-host list \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

Read-only — makes no changes.

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | | GCP project ID (required) |
| `--region` | `us-central1` | Cloud region for the mint service |
| `--dry-run` | `false` | Preview changes without making them (`add` and `remove` only) |

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
