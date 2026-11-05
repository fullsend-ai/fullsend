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
| `fullsend mint enroll <owner/repo>` | Register a repo in the mint |
| `fullsend mint unenroll <owner/repo>` | Remove a repo from the mint |
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

The CLI automatically detects when the deployed function source is up-to-date (same source hash) and skips code redeployment, only updating WIF infrastructure.

`mint deploy` does not enroll any organization or repository. Per-org enrollment (bare-org `mint enroll <org>` / `mint unenroll <org>`) and the Cloudflare `--allowed-orgs` flag have been removed; enroll each repository with `fullsend mint enroll <owner/repo>`. See [Cleaning up legacy per-org mint state](../guides/infrastructure/mint-administration.md#cleaning-up-legacy-per-org-mint-state) for removing leftover org entries from existing mints.

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

#### Tracing (GCP)

Pass an OTLP/HTTP traces destination to have the Cloud Function export mint spans (see [Mint tracing](../guides/infrastructure/distributed-tracing.md#mint-tracing)). The flags and the endpoint precedence are the same as for [Cloudflare](#tracing):

```bash
chmod 600 otlp-headers   # one key=value per line, OTEL_EXPORTER_OTLP_HEADERS syntax
fullsend mint deploy \
  --project "<GCP_PROJECT>" \
  --otlp-traces-endpoint https://collector.example/v1/traces \
  --otlp-headers-file otlp-headers
```

- The endpoint and the deployment identity (`fullsend-mint/<region>`) are stamped into the generated function source (`trace_consts.go`). Changing the endpoint changes the source hash, so the function is redeployed. Tracing flags cannot be combined with `--skip-deploy`.
- The endpoint must use `https`, and it requires `--otlp-headers-file`, or `--otlp-no-headers` for a collector that needs no authentication.
- Header values are never passed on the command line or written into the function source. The CLI stores the header file content in a new Secret Manager secret with a per-deploy name (`fullsend-mint-otlp-headers-<random>`) and grants the `fullsend-mint` service account `roles/secretmanager.secretAccessor` on it, as for the role PEM secrets. The secret ID is stamped into the function source next to the endpoint, so each deployed revision reads exactly the headers stored for its own endpoint. The function reads the secret once, at cold start, with a bounded timeout. If it cannot read the secret, tracing stays off and token minting is unaffected.
- **Collector rotation:** re-run `mint deploy` with the new endpoint and its `--otlp-headers-file` (or `--otlp-no-headers`). The CLI stores the new headers under a fresh secret name, then deploys the function stamped with the new endpoint and that name. Previous secrets are never modified, so a revision that cold-starts before the deploy finishes (or after it fails) still reads the headers of its own endpoint, and a deploy to another region cannot change them. Every deploy with headers redeploys the function, so re-running after a partial failure is safe. Earlier deploys' secrets are not deleted: remove stale `fullsend-mint-otlp-headers-*` secrets with `gcloud secrets delete <name>` (list them with `gcloud secrets list --filter="name~fullsend-mint-otlp-headers-"`) once no live revision uses them.
- **Disable tracing:** redeploy without an endpoint flag. The function is redeployed without an endpoint and makes no export attempts. You can delete the leftover `fullsend-mint-otlp-headers-*` secrets with `gcloud secrets delete <name>`.
- Only `mint deploy` stamps tracing settings. A later `mint deploy` without the tracing flags redeploys the function with tracing disabled, so pass the flags on every deploy of a traced mint.
- Collector headers are safe against overlapping deploys (each deploy has its own secret), but the function deployment itself is not synchronized: avoid running concurrent deploys against the same function, because the last one to finish decides which endpoint is live.

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

#### Tracing

Pass an OTLP/HTTP traces destination to have the Worker export mint spans (see [Mint tracing](../guides/infrastructure/distributed-tracing.md#mint-tracing)):

```bash
chmod 600 otlp-headers   # one key=value per line, OTEL_EXPORTER_OTLP_HEADERS syntax
fullsend mint deploy \
  --platform cloudflare \
  --otlp-traces-endpoint https://collector.example/v1/traces \
  --otlp-headers-file otlp-headers
```

- `--otlp-traces-endpoint` is used verbatim. `--otlp-endpoint` is a base URL with `/v1/traces` appended, used only when `--otlp-traces-endpoint` is not set. This matches the `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` / `OTEL_EXPORTER_OTLP_ENDPOINT` precedence.
- The endpoint and the deployment identity (`<worker-name>`, or `<worker-name>@<alias>` for previews) are compiled into the WASM binary. Changing the endpoint rebuilds and redeploys the Worker; a pre-staged `mintcore.wasm` is always rebuilt when an endpoint is set. Durable and preview mints can target different collectors.
- Header values are never passed on the command line or compiled into the binary. `--otlp-headers-file` must be a regular file that is not readable by group or others (`chmod 600`). The CLI stores its content as a Worker secret: through `wrangler secret put` under a per-deploy name (`MINT_OTLP_HEADERS_<random>`) for durable deploys, and as the version-scoped `MINT_OTLP_HEADERS` secret (`--secrets-file`) for `--preview` deploys.
- **The collector endpoint must use `https`.** `http://` endpoints are rejected, because the Worker sends the collector headers and the trace payloads to this URL.
- **Endpoint and headers travel together:** a durable deploy that sets an endpoint must also pass `--otlp-headers-file`, or `--otlp-no-headers` for a collector that needs no authentication, so the authentication choice is always explicit.
- **Per-deploy header secret:** every durable deploy stamps a unique secret name (`MINT_OTLP_HEADERS_<random>`) into the WASM build next to the endpoint. The build reads collector headers only from that secret, and the CLI stores the headers under that name after the Worker deploy. Because the endpoint and the secret name live in the same immutable build, the Worker that is live can never read headers that belong to a different endpoint, even if two deploys overlap.
- **Collector rotation:** re-run `mint deploy` with the new endpoint and its `--otlp-headers-file` (or `--otlp-no-headers`). The CLI deploys the Worker stamped with the new endpoint and a fresh secret name, then stores the new headers under that name. Until that last step completes (or if it fails), the mint exports to the new endpoint without authentication; exports fail open, and the previous collector's credentials are never sent to the new endpoint. Re-running the deploy is safe. Secrets of earlier deploys are not deleted: remove stale `MINT_OTLP_HEADERS_*` secrets with `wrangler secret delete <name>` (list them with `wrangler secret list --name <worker-name>`). Preview versions do not inherit the durable Worker's secrets. A preview with an endpoint always binds `MINT_OTLP_HEADERS` explicitly, to the headers file content or, without `--otlp-headers-file`, to an empty value, so pass the file on every preview deploy that needs collector authentication.
- **Concurrent durable deploys:** collector headers are safe against overlapping deploys (see above), but the role PEM secrets are still replaced by separate, unsynchronized steps, so avoid running durable deploys against the same Worker at the same time.
- **Disable tracing:** redeploy without an endpoint flag. The mint then makes no export attempts. You can remove leftover `MINT_OTLP_HEADERS_*` secrets with `wrangler secret delete <name>`.
- Export failures never affect token minting. For GCP Cloud Function deploys, see [Tracing (GCP)](#tracing-gcp). For the standalone mint, see [Standalone mint tracing](../guides/infrastructure/standalone-mint.md#tracing).

Authentication (one of):
- `CLOUDFLARE_API_TOKEN` env var (+ `CLOUDFLARE_ACCOUNT_ID`) — API token with Workers write permission
- Wrangler OAuth session (`wrangler login`, then `wrangler whoami`) — when `CLOUDFLARE_API_TOKEN` is unset, the CLI falls back to the Wrangler login session. If `CLOUDFLARE_ACCOUNT_ID` is also unset, the CLI discovers the account from `wrangler whoami`.

#### Omit-vs-empty semantics for config flags on redeploy

**Durable deploys** use `--keep-vars` so existing Worker bindings are preserved when a flag is omitted:

- **Flag omitted:** existing Worker value is preserved.
- **Flag non-empty:** Worker binding set to the given value.
- **Flag set to `""`:** Worker binding cleared (set to empty string).

Example: `--per-repo-wif-repos=` clears `PER_REPO_WIF_REPOS` without requiring `wrangler delete` first.

**Preview deploys** do **not** use `--keep-vars`. Each preview version is self-contained — only the `--var` env vars and `--secrets-file` PEMs passed in the deploy command are applied. This prevents cross-preview contamination when deploying multiple preview aliases in sequence (e.g. `bt-run-41` → `bt-run-42`). `ALLOWED_WORKFLOW_FILES` defaults to `*` on preview when `--allowed-workflow-files` is omitted, so previews are usable out of the box (mintcore deny-alls workflow refs when the env var is unset). Pass an explicit value to restrict.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--platform` | `gcp` | Target platform: `gcp` or `cloudflare` |
| `--project` | | GCP project ID (GCP only) |
| `--region` | `us-central1` | Cloud region for the function (GCP only) |
| `--pem-dir` | | Directory containing `{role}.pem` files for PEM bootstrap |
| `--app-set` | `fullsend-ai` | App set name for PEM bootstrap |
| `--roles` | _(default roles)_ | Comma-separated role names to bootstrap with `--pem-dir`. Overrides the default set. Example: `--roles=fullsend,triage,coder,review,retro,prioritize`. The `e2e` role is internal-only and not intended for user configuration |
| `--public` | `false` | Deploy public mint (`PER_REPO_WIF_REPOS=*`). Mutually exclusive with `--per-repo-wif-repos` on Cloudflare |
| `--status-auth` | `oidc` | Comma-separated status auth modes. Each non-oidc mode selects a Go build tag. Modes: `oidc`, `github`. `oidc` is always compiled in; `github` requires `--status-github-group` |
| `--status-github-group` | | `ORG/TEAM` slug for GitHub status auth (required when `github` mode enabled). Example: `--status-github-group=acme/platform-team` |
| `--source-dir` | | Path to local mint source (default: checkout path when present, embedded otherwise) |
| `--dry-run` | `false` | Preview changes without making them |
| `--skip-deploy` | `false` | Skip code upload, reuse existing function (GCP only) |
| `--worker-name` | `fullsend-mint` | Cloudflare Worker script name (Cloudflare only) |
| `--preview` | `""` | Preview alias for `wrangler versions upload` (Cloudflare only). Example: `--preview=bt-run-42` |
| `--per-repo-wif-repos` | | Comma-separated per-repo WIF repos (Cloudflare only, sets `PER_REPO_WIF_REPOS`). Mutually exclusive with `--public` |
| `--workflow-host-repos` | | Comma-separated workflow host repos (Cloudflare only, sets `WORKFLOW_HOST_REPOS`). Omit to preserve existing; set to `""` to clear |
| `--allowed-workflow-files` | | Comma-separated workflow file basenames (Cloudflare only, sets `ALLOWED_WORKFLOW_FILES`). Durable: omit to preserve existing binding; set to `""` to clear. Preview: defaults to `*` when omitted (all basenames allowed) |
| `--custom-domain` | | Hostname to attach as a Workers Custom Domain (Cloudflare only, durable deploys only). Zone ID is resolved automatically. Example: `--custom-domain=mint.fullsend.sh` |
| `--otlp-traces-endpoint` | | OTLP/HTTP traces URL stamped into the function source (GCP) or compiled into the Worker (Cloudflare), used verbatim. See [Tracing (GCP)](#tracing-gcp) and [Tracing](#tracing). GCP: not allowed with `--skip-deploy` |
| `--otlp-endpoint` | | OTLP/HTTP base URL; `/v1/traces` is appended. Ignored when `--otlp-traces-endpoint` is set |
| `--otlp-headers-file` | | `chmod 600` file with collector auth headers in `OTEL_EXPORTER_OTLP_HEADERS` syntax. GCP: stored in a per-deploy Secret Manager secret (`fullsend-mint-otlp-headers-<random>`). Cloudflare: stored as a Worker secret (`MINT_OTLP_HEADERS` for previews, `MINT_OTLP_HEADERS_<random>` for durable deploys). Requires an endpoint flag. Required with an endpoint on GCP and Cloudflare durable deploys unless `--otlp-no-headers` is set |
| `--otlp-no-headers` | `false` | The collector needs no auth headers; binds no collector headers to the new endpoint. Requires an endpoint flag; mutually exclusive with `--otlp-headers-file` |

### Required IAM roles (GCP)

| Role | Description |
|------|-------------|
| `roles/iam.serviceAccountAdmin` | Create `fullsend-mint` service account |
| `roles/iam.workloadIdentityPoolAdmin` | Create WIF pool and provider |
| `roles/cloudfunctions.developer` | Deploy the Cloud Function |
| `roles/run.admin` | Set Cloud Run IAM policy |
| `roles/secretmanager.admin` | Create secrets (only with `--pem-dir` or `--otlp-headers-file`) |
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

Registers a GitHub repository in the mint's `PER_REPO_WIF_REPOS` allow-list, enabling it to request tokens. Only `owner/repo` targets are accepted; a bare org argument is rejected with an error because org enrollment was removed with per-org installation.

```bash
fullsend mint enroll <owner/repo> \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

Enrollment creates the WIF provider needed for OIDC verification only — it does not grant any IAM roles. Vertex AI access is provisioned separately via `fullsend inference provision`.

## `mint unenroll`

Removes a repository from the mint's `PER_REPO_WIF_REPOS` list. By default the repository's dedicated WIF provider is disabled; pass `--delete-provider` to delete it permanently.

```bash
fullsend mint unenroll <owner/repo> \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

Like `mint enroll`, `mint unenroll` accepts only `owner/repo` targets. Bare-org unenrollment has been removed and a bare org argument is rejected with an error. To remove leftover org entries from `ALLOWED_ORGS` or the shared WIF provider condition on an existing mint, follow [Cleaning up legacy per-org mint state](../guides/infrastructure/mint-administration.md#cleaning-up-legacy-per-org-mint-state).

## `mint workflow-host`

Manages the `WORKFLOW_HOST_REPOS` allow-list that controls which repositories may host workflows calling the mint. The check applies to every admitted caller; the upstream `fullsend-ai/fullsend` is always accepted, and there is no implicit `{org}/.fullsend` host.

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

Inspects the mint's current state. Two modes of operation:

### API-based mode (`--mint-url`)

Queries GET `/v1/status` on the mint service using auto-discovered GitHub
credentials. Tries GitHub Actions OIDC first, then falls back to
`GH_TOKEN` / `GITHUB_TOKEN` / `gh auth token`. No cloud IAM required.

The `GH_TOKEN` / `GITHUB_TOKEN` / `gh auth token` fallback only works
against a mint that was deployed with `--status-auth=github
--status-github-group=ORG/TEAM` (see [Enabling optional
validators](../guides/infrastructure/infrastructure-reference.md#status-endpoint)).
A default mint (OIDC-only) always rejects it with HTTP 401 — only GitHub
Actions OIDC succeeds against a default deployment.

```bash
fullsend mint status --mint-url "https://mint.example.com"
```

When `FULLSEND_MINT_URL` is set and `--mint-url` is not provided,
the API-based mode is used automatically.

### GCP-based mode (`--project`)

Reads mint state directly from GCP infrastructure (Cloud Function metadata,
Secret Manager). Requires GCP viewer IAM roles.

```bash
fullsend mint status \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

Optionally filter to a specific org (GCP-based mode only). The command
warns when no repository under that org is in `PER_REPO_WIF_REPOS`:

```bash
fullsend mint status <org> \
  --project "<GCP_PROJECT>" \
  --region "us-central1"
```

When `--mint-url` is provided, `--project` is ignored and the API-based path
is used. When `FULLSEND_MINT_URL` is set and `--project` is also provided,
the command returns an error to prevent silent mode ambiguity — either unset
the env var or pass `--mint-url=` (empty) to force GCP-based mode. Omitting
`--project` does not select GCP-based mode; it leaves API-based mode as the
active path whenever `FULLSEND_MINT_URL` is set. The `[org]` argument is not
supported in API-based mode.

Read-only — makes no changes.

| Flag | Default | Description |
|------|---------|-------------|
| `--mint-url` | `$FULLSEND_MINT_URL` | Mint service URL for API-based status |
| `--project` | | GCP project ID (for direct infrastructure queries) |
| `--region` | `us-central1` | GCP region (GCP-based mode only) |

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
| `--level` | `write` | Privilege level name (e.g. `read`, `write`). Both the CLI and server default to `write` when omitted (temporary compatibility default). The value is passed through to the mint — if the role does not define the requested level, the mint returns an error |

## See also

- [Mint service administration](../guides/infrastructure/mint-administration.md) — deployment and management guide
- [Infrastructure reference](../guides/infrastructure/infrastructure-reference.md) — architecture details
- [Operations](../guides/getting-started/operations.md) — standalone commands and IAM role breakdown
- [CLI internals](../guides/dev/cli-internals.md) — command tree and implementation details
