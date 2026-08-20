# Infrastructure Reference

This guide provides implementation details for fullsend's infrastructure components: the OIDC token mint, Workload Identity Federation (WIF), and secrets deployment. For basic installation instructions, see the [Getting Started guides](../getting-started/).

## Token Mint (OIDC)

> Managed by: `fullsend mint deploy`, `fullsend mint delete`, `fullsend mint enroll`, `fullsend mint unenroll`, `fullsend mint status`, `fullsend mint add-role`, `fullsend mint remove-role`, `fullsend mint workflow-host`, `fullsend mint token`

The mint exchanges GitHub OIDC tokens for scoped GitHub App installation tokens. This eliminates long-lived PATs from the system. The mint can be deployed on GCP (Cloud Function) or Cloudflare (Worker) — see `fullsend mint deploy --platform`.

### Mint Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     Token Mint Flow                              │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  GitHub Actions Workflow                                        │
│  ┌─────────────────────┐                                        │
│  │ id-token: write      │                                       │
│  │ ┌─────────────────┐  │                                       │
│  │ │ Request OIDC JWT │  │                                       │
│  │ └────────┬────────┘  │                                       │
│  └──────────┼───────────┘                                       │
│             │                                                   │
│             ▼                                                   │
│  ┌──────────────────────────────────────────────────┐           │
│  │ POST /v1/token                                   │           │
│  │ Authorization: Bearer <OIDC JWT>                 │           │
│  │ Body: { "role": "coder", "repos": ["my-repo"] }  │           │
│  └──────────┬───────────────────────────────────────┘           │
│             │                                                   │
│             ▼                                                   │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │              GCF: Token Mint                             │   │
│  │                                                          │   │
│  │  1. Prevalidate OIDC JWT                                 │   │
│  │     ├─ Check iss == token.actions.githubusercontent.com  │   │
│  │     ├─ Extract repository_owner → ALLOWED_ORGS check     │   │
│  │     │   (explicit org list, or * for public mint mode)   │   │
│  │     └─ Validate job_workflow_ref provenance              │   │
│  │        (per-org: .fullsend / upstream;                   │   │
│  │         per-repo/public: WORKFLOW_HOST_REPOS)            │   │
│  │                                                          │   │
│  │  2. STS Token Exchange                                   │   │
│  │     ├─ POST securitytoken.googleapis.com                 │   │
│  │     │   grant_type=urn:ietf:params:oauth:                │   │
│  │     │   grant-type:token-exchange                        │   │
│  │     ├─ WIF pool validates OIDC token                     │   │
│  │     └─ Returns GCP federated access token                │   │
│  │                                                          │   │
│  │  3. Lookup PEM from Secret Manager                       │   │
│  │     ├─ Secret name: fullsend-{role}-app-pem              │   │
│  │     └─ Returns PEM private key bytes                     │   │
│  │                                                          │   │
│  │  4. Generate GitHub App JWT                              │   │
│  │     ├─ Sign with PEM key (RS256)                         │   │
│  │     ├─ App ID from ROLE_APP_IDS env                      │   │
│  │     └─ 10-minute expiry                                  │   │
│  │                                                          │   │
│  │  5. Find Installation                                    │   │
│  │     ├─ GET /app/installations                            │   │
│  │     └─ Match by org login                                │   │
│  │                                                          │   │
│  │  6. Create Scoped Installation Token                     │   │
│  │     ├─ POST /installations/{id}/access_tokens            │   │
│  │     ├─ Scope to requested repos[]                        │   │
│  │     └─ Apply RolePermissions() minimum set               │   │
│  │                                                          │   │
│  └──────────┬───────────────────────────────────────────────┘   │
│             │                                                   │
│             ▼                                                   │
│  Response: { "token": "ghs_...", "expires_at": "..." }          │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Role Permissions Matrix

The mint enforces minimum permission sets per role. Tokens cannot exceed these scopes.
Custom roles can be registered via the standalone mint's `CUSTOM_ROLE_PERMISSIONS` env var — see the [standalone mint guide](standalone-mint.md#custom-role-permissions) for details.

| Role | contents | pull_requests | issues | actions | checks | workflows | actions_variables | organization_projects | metadata |
|------|----------|---------------|--------|---------|--------|-----------|-------------------|-----------------------|----------|
| **fullsend** | write | write | — | write | — | write | read | — | read |
| **triage** | read | — | write | — | — | — | — | — | read |
| **scribe** | read | — | write | — | — | — | — | — | read |
| **coder** | write | write | write | — | read | — | — | — | read |
| **review** | read | write | write | — | read | — | — | — | read |
| **fix** | write | write | write | — | — | — | — | — | read |
| **retro** | read | write | write | read | — | — | — | — | read |
| **prioritize** | read | — | write | — | — | — | — | write | read |

### Mint Security Controls

Mode is inferred from `ALLOWED_ORGS` — there is no separate trust-mode flag.

**Tight mint** (default): explicit comma-separated org list (no `*`).

- **ALLOWED_ORGS**: Only listed orgs may mint tokens
- **ALLOWED_WORKFLOW_FILES**: Fail-closed allowlist of workflow filenames (use `*` to allow any basename)
- **job_workflow_ref validation (per-org callers)**: `{org}/.fullsend` config repo or `fullsend-ai/fullsend` upstream reusables
- **job_workflow_ref validation (per-repo callers)**: Only repos listed in `WORKFLOW_HOST_REPOS` (defaults to `fullsend-ai/fullsend`)
- **job_workflow_ref validation (dual-enrolled callers)**: Callers matching both `PER_REPO_WIF_REPOS` and `ALLOWED_ORGS` accept workflows from **either** per-org sources (`{org}/.fullsend`, upstream) or per-repo sources (`WORKFLOW_HOST_REPOS`, upstream)
- **WORKFLOW_HOST_REPOS**: Comma-separated repos whose workflows are trusted to call the mint for per-repo callers. Managed via `fullsend mint workflow-host add|remove|list`. Defaults to `fullsend-ai/fullsend` when unset.
- **PER_REPO_WIF_REPOS**: Repos using dedicated WIF providers (repo-scoped isolation)

**Public mint**: `ALLOWED_ORGS` is `*`.

- **ALLOWED_ORGS**: Any org may mint (cross-org isolation still enforced at installation lookup)
- **job_workflow_ref validation**: Same as per-repo callers — only repos listed in `WORKFLOW_HOST_REPOS` (defaults to `fullsend-ai/fullsend`). `ALLOWED_WORKFLOW_FILES` basename gate applies ([ADR 0082](../../ADRs/0082-workflow-host-allow-list.md) §2, revised 2026-08-05)
- **PER_REPO_WIF_REPOS**: Set to `*` for public mode (GCF mint: all repos use `WIF_PROVIDER_NAME`)
- **WORKFLOW_HOST_REPOS**: Same semantics as tight mode — controls which repos may host workflows. Defaults to `fullsend-ai/fullsend` when unset
- **mint enroll**: Succeeds without changing mint configuration (org registration is unnecessary); **mint unenroll** for individual orgs is rejected

**GCF mint (STS verification) only:** The hosted Cloud Function uses `STSVerifier`, which exchanges each OIDC JWT with GCP STS against `WIF_PROVIDER_NAME`. A permissive WIF provider (CEL that does not enumerate orgs/repos) must back that env var, or STS will reject tokens from orgs outside the provider's `attributeCondition` even when `mintcore` prevalidation passes. Use `mint deploy --public` to provision `PER_REPO_WIF_REPOS=*` and permissive WIF together; tight-mode `mint deploy` (default) and `mint enroll` continue to use org-scoped WIF. Redeploys must match the mint mode (`--public` for public, omit for tight).

**Standalone mint (JWKS verification):** `cmd/mint` uses `JWKSVerifier` — direct GitHub JWKS signature checks with no STS or WIF. Public mode is fully determined by `ALLOWED_ORGS` and workflow provenance in `mintcore`; WIF provisioning is not applicable.

- **Minimum permissions**: Tokens are scoped to the role's minimum permission set, not the App's full permissions (both modes)

### Multi-Org Support

A single mint instance can serve multiple orgs:

- **Tight mode:** `EnsureOrgInMint()` additively appends orgs to `ALLOWED_ORGS`
- **Public mode:** `PER_REPO_WIF_REPOS=*` — no per-org registration required; rollback to tight mode is config-only (clear `PER_REPO_WIF_REPOS=*` and set an explicit org list)
- `ROLE_APP_IDS` maps `{role}` to GitHub App IDs (shared across all enrolled orgs)
- Org isolation at token issuance uses the OIDC `repository_owner` claim and GitHub App installation lookup — not per-org app ID entries

### Status Endpoint

`GET /v1/status` returns the configured roles available for the authenticated caller's org.

- **Authentication:** Bearer OIDC JWT (same as `/v1/token`)
- **Authorization:** Any valid OIDC token from an allowed org — no role restriction
- **Response:**
  ```json
  {"org": "my-org", "roles": ["coder", "review", "triage"]}
  ```
- **Use case:** Workflow diagnostics — discover which roles are available before requesting a token
- **Security:** Returns only the requesting org and its role names (not app IDs, not other orgs' roles)

---

## Inference — Agent Platform with Workload Identity Federation

> Managed by: `fullsend inference provision`, `fullsend inference deprovision`, `fullsend inference status`

Inference authentication uses GCP Workload Identity Federation (WIF) to allow GitHub Actions to authenticate to Agent Platform without service account keys.

```
┌─────────────────────────────────────────────────────────────┐
│               Inference Authentication Flow                 │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  GitHub Actions Runner                                      │
│  ┌─────────────────────┐                                    │
│  │ OIDC JWT             │                                   │
│  │ (id-token: write)    │                                   │
│  └──────────┬──────────┘                                    │
│             │                                               │
│             ▼                                               │
│  ┌──────────────────────────────────────────┐               │
│  │ GCP Security Token Service (STS)         │               │
│  │                                          │               │
│  │ WIF Pool: fullsend-inference             │               │
│  │ WIF Provider: github-oidc                │               │
│  │                                          │               │
│  │ Validates OIDC issuer:                   │               │
│  │   token.actions.githubusercontent.com    │               │
│  │                                          │               │
│  │ Attribute mapping:                       │               │
│  │   sub → assertion.sub                    │               │
│  │   repo → assertion.repository            │               │
│  └──────────┬───────────────────────────────┘               │
│             │                                               │
│             ▼                                               │
│  ┌─────────────────────────────────┐                        │
│  │ Federated Access Token          │                        │
│  │ (short-lived, auto-rotated)     │                        │
│  └──────────┬──────────────────────┘                        │
│             │                                               │
│             ▼                                               │
│  ┌─────────────────────────────────┐                        │
│  │ Agent Platform API              │                        │
│  │                                 │                        │
│  │ Project: FULLSEND_GCP_PROJECT_ID│                        │
│  │ Region:  FULLSEND_GCP_REGION    │                        │
│  │                                 │                        │
│  │ Models:                         │                        │
│  │  - claude-haiku-4-5             │                        │
│  │  - claude-sonnet-4-6            │                        │
│  │  - claude-opus-4-6              │                        │
│  └─────────────────────────────────┘                        │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### WIF Provisioning

During installation, the GCF provisioner creates:

1. **Service Account** — For the Cloud Function identity
2. **WIF Pool** — `fullsend-inference` for inference, `fullsend-pool` for mint
3. **WIF Provider** — Maps GitHub OIDC claims to GCP attributes
4. **IAM Bindings** — Grants `roles/aiplatform.user` to federated identities
5. **Per-repo providers** (per-repo mode) — Scoped WIF provider per repository via `mintcore.BuildRepoProviderID()` (GitHub only; GitLab uses a shared `gitlab-oidc` provider scoped via attribute conditions on the WIF pool)

---

## GitHub Secrets & Variables Deployment

> Individual values can be updated with `fullsend github set <target> <key> <value>`. See [Operations](../getting-started/operations.md#updating-configuration-values) for the full configuration management guide.

Secrets and variables are deployed at different scopes depending on the installation mode.

### Per-Org Mode Secrets/Variables

**Org-level variable:**
- `FULLSEND_MINT_URL` — URL of the token mint Cloud Function

**.fullsend repo variables (per role):**
- `FULLSEND_{ROLE}_CLIENT_ID` — GitHub App client ID

**.fullsend repo secrets (inference):**
- `FULLSEND_GCP_PROJECT_ID` — GCP project for inference
- `FULLSEND_GCP_WIF_PROVIDER` — WIF provider resource name

**.fullsend repo variables (inference):**
- `FULLSEND_GCP_REGION` — GCP region for inference (install-time only, not managed by sync)

**.fullsend repo variable (dot-repo fix):**
- `FULLSEND_MINT_URL` — Duplicate of org variable (dot-prefixed repos can't read org-level variables)

### Per-Repo Mode Secrets/Variables

#### GitHub

**Target repo secrets:**
- `FULLSEND_GCP_PROJECT_ID`
- `FULLSEND_GCP_WIF_PROVIDER`

**Target repo variables:**
- `FULLSEND_MINT_URL`
- `FULLSEND_GCP_REGION` (install-time only, not managed by sync)
- `FULLSEND_PER_REPO_INSTALL` — Flag indicating per-repo mode (set to "true")

#### GitLab

**Target repo CI/CD variables (protected):**
- `FULLSEND_FORGE_TOKEN` — Project access token for bot identity (stored as protected CI/CD variable)
- `FULLSEND_FORGE` — Set to `"gitlab"`
- `FULLSEND_PER_REPO_INSTALL` — Flag indicating per-repo mode (set to `"true"`)
- `FULLSEND_LAST_POLL_AT_FAST` — Timestamp of last slash poll run (name predates the slash/events terminology split; used by the slash-command schedule)
- `FULLSEND_LAST_POLL_AT_FULL` — Timestamp of last event poll run (name predates the slash/events terminology split; used by the event-discovery schedule)
- `FULLSEND_POLL_MODE` — Pipeline schedule variable (`"slash"` or `"events"`); set automatically per schedule during install, not a project-level CI/CD variable
- `FULLSEND_LABEL_STATE` — JSON object tracking label sync state

**Inference variables (required when inference is configured):**
- `FULLSEND_GCP_PROJECT_ID` — GCP project ID for inference (stored as a CI/CD secret, protected + masked)
- `FULLSEND_GCP_WIF_PROVIDER` — WIF provider resource name for inference (stored as a CI/CD secret, protected + masked)
- `FULLSEND_GCP_REGION` — GCP region for inference (e.g., `us-central1`)

### Secrets Layer Behavior

- **Install**: Writes inference secrets when an inference project is configured.
- **Analyze**: Checks that expected secrets/variables exist. Cannot verify secret values (GitHub Secrets API is write-only for values).
- **Uninstall**: Deletes repo secrets and variables for all managed names.

### Inference Layer Behavior

- **Install**: Unconditionally writes secrets and variables (no way to check if values changed since GitHub doesn't expose secret values).
- **Analyze**: Checks presence of `FULLSEND_GCP_PROJECT_ID`, `FULLSEND_GCP_WIF_PROVIDER`, `FULLSEND_GCP_REGION`.

---

## GCF Provisioner Flow

The GCF provisioner handles full GCP infrastructure deployment:

```
┌─────────────────────────────────────────────────────────────────┐
│               GCF Provisioner: Provision() Flow                 │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌───────────────────┐                                          │
│  │ Get GCP project   │ resourcemanager.projects.get             │
│  │ number            │                                          │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  ┌───────────────────┐                                          │
│  │ Create Service    │ fullsend-mint@{project}.iam              │
│  │ Account           │ (skip if exists)                         │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  ┌───────────────────┐                                          │
│  │ Create WIF Pool   │ fullsend-inference (or fullsend-pool)    │
│  │                   │ (skip if exists)                         │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  ┌───────────────────┐                                          │
│  │ Create WIF        │ github-oidc                              │
│  │ Provider          │ OIDC issuer:                             │
│  │                   │   token.actions.githubusercontent.com    │
│  │                   │ (skip if exists)                         │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  ┌───────────────────┐                                          │
│  │ Grant Agent       │ roles/aiplatform.user                    │
│  │ Platform access   │ on the inference project                 │
│  │ to federated IDs  │                                          │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  ┌───────────────────┐                                          │
│  │ Store PEMs in     │ fullsend-{role}-app-pem                  │
│  │ Secret Manager    │ once per agent role (shared)             │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  ┌───────────────────┐                                          │
│  │ Deploy Cloud      │ Source: embedded mint code               │
│  │ Function          │ SHA256 hash comparison to skip           │
│  │                   │ redundant deploys                        │
│  │                   │ Env vars:                                │
│  │                   │   ALLOWED_ORGS                           │
│  │                   │   GCP_PROJECT_NUMBER                     │
│  │                   │   WIF_POOL_NAME                          │
│  │                   │   WIF_PROVIDER_NAME                      │
│  │                   │   ROLE_APP_IDS                           │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  ┌───────────────────┐                                          │
│  │ Health check      │ Exponential backoff polling              │
│  │                   │ POST /v1/token (expect 401)              │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  Return: FULLSEND_MINT_URL = https://{region}-{project}.        │
│          cloudfunctions.net/fullsend-mint                       │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Source Hash Optimization

The GCF provisioner avoids redundant Cloud Function deployments by computing a SHA256 hash of the source zip and comparing it to metadata stored on the deployed function. Only deploys when the hash changes.

## See Also

- [Getting Started](../getting-started/) — Standard per-repo installation
- [Mint service administration](mint-administration.md) — Deploying and managing the token mint
- [Standalone Mint](standalone-mint.md) — Running the mint without GCP, with custom agent roles
- [Advanced setup](./advanced-setup.md) — Alternative installation paths and setup flags
- [Running agents locally](../user/running-agents-locally.md) — Run agents locally (binary download, GCP credentials, per-agent env vars)
