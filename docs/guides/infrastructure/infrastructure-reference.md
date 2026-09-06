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
│  │     ├─ GET /repos/{org}/{repo}/installation              │   │
│  │     │  or GET /orgs/{org}/installation                   │   │
│  │     ├─ Verify the installation account matches the org   │   │
│  │     └─ Read the granted permissions map                  │   │
│  │                                                          │   │
│  │  6. Create Scoped Installation Token                     │   │
│  │     ├─ POST /app/installations/{id}/access_tokens        │   │
│  │     ├─ Scope to requested repos[]                        │   │
│  │     ├─ Intersect with granted permissions                │   │
│  │     └─ Drop only explicitly optional rollout scopes      │   │
│  │                                                          │   │
│  └──────────┬───────────────────────────────────────────────┘   │
│             │                                                   │
│             ▼                                                   │
│  Response: { "token": "ghs_...", "expires_at": "..." }          │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Agent → Mint Role Mapping

Each dispatch stage mints a token for a specific **mint role**. The `code` and
`fix` agents both mint the `coder` role and share the same GitHub App and PEM
(the **coder** App). `fix` is not a separate dispatch-time role. All other
built-in agents use the role matching their name. `scribe` is a mint role
without a built-in dispatch stage.

To change App-level permissions for the `code` or `fix` agent, update the coder
App registration. To change token-level permissions for dispatch, update the
`coder` entry in `canonicalRolePermissions`.

| Agent | Mint Role |
|-------|-----------|
| triage | triage |
| code | coder |
| fix | coder |
| review | review |
| retro | retro |
| prioritize | prioritize |

### Role Permissions Matrix

The mint enforces minimum permission sets per role. Tokens cannot exceed these scopes.
Custom roles can be registered via the standalone mint's `CUSTOM_ROLE_PERMISSIONS` env var — see the [standalone mint guide](standalone-mint.md#custom-role-permissions) for details.

| Role | contents | packages | pull_requests | issues | actions | checks | workflows | actions_variables | organization_projects | metadata |
|------|----------|----------|---------------|--------|---------|--------|-----------|-------------------|-----------------------|----------|
| **fullsend** | write | — | write | — | write | — | write | read | — | read |
| **triage** | read | — | — | write | — | — | — | — | — | read |
| **scribe** | read | — | — | write | — | — | — | — | — | read |
| **coder** | write | read | write | write | — | read | — | — | — | read |
| **fix** *(direct callers only)* | write | read | write | write | — | — | — | — | — | read |
| **review** | read | — | write | write | — | read | — | — | — | read |
| **retro** | read | — | write | write | read | — | — | — | — | read |
| **prioritize** | read | — | — | write | — | — | — | — | write | read |
| **e2e** | write | — | write | write | write | — | write | write | — | read |

The **e2e** role also grants: `administration` (write), `members` (write), `secrets` (write), `organization_actions_variables` (write), `organization_administration` (write). These permissions are omitted from the table above because no other role uses them.

The `fix` row is retained for direct callers that request the canonical `fix`
role. The built-in fix dispatch stage uses `coder`, so the `coder` row and coder
App registration control the code/fix rollout for normal dispatches.

### Roll Out a GitHub App Permission

Use this sequence for any new role permission; the current example is
`packages:read` for the `coder` role (which covers both code and fix stages).
Changing the mint's role map does not update existing GitHub App installations.
GitHub rejects the entire installation-token request (`422`) when mint asks for a
permission the installation has not approved yet — there is no partial downscope.

For shared hosted Apps (for example `fullsend-ai-coder`), the App owner adds the
permission once on the App registration; each installing org's owners must then
[Accept the update](https://docs.github.com/en/apps/using-github-apps/approving-updated-permissions-for-a-github-app).
New installations of an already-updated App receive the new permission at install
time. Self-managed App owners update their own App registration, then Accept on
their installation.

The implementation sequence is: update `canonicalRolePermissions`, the GCF
embedded mint source, and `AgentAppConfig` together; have mint intersect the
requested role map with the installation's granted `permissions`; and have CLI
`checkPermissions` warn with the installing org's Accept URL instead of failing
**for optional permissions only**. Only permissions explicitly listed in `optionalRolePermissions`
(currently `packages` for `coder` and direct `fix`-role callers) may be omitted when ungranted — all
other permissions remain required and fail before the token POST, preserving
the pre-existing behavior where GitHub's `422` surfaced immediately. Dropped
optional permissions are logged with `org=` and `installation_id=`. The
preflight avoids the two token-creation POSTs that the earlier
packages-specific retry would incur for each lagging installation.

When an installation lookup omits the `permissions` field, mint preserves the
requested map for compatibility with older or incomplete GitHub responses and
lets GitHub validate it at token creation time; the granted-set preflight
applies only when that map is present.

This opt-in degradation means a caller that needs the omitted optional
permission may receive a later GitHub `403`; it does not silently drop any
other permission. Missing non-optional permissions fail once with a `422`, the
missing scopes, and guidance covering both App registration and installation
approval.

Recommended operator order for adding **`packages:read`** to `coder` (code / fix):

1. Add **Packages: Read-only** on the GitHub App's **Permissions & events** page
   (hosted: `https://github.com/organizations/fullsend-ai/settings/apps/<app-slug>/permissions`).
   Optionally include a short note to users explaining why.
2. Update the App used by the pool installations as well, and have each
   `halfsend-01` … `halfsend-12` installation owner Accept its pending update.
   For the test-app setup, that is `fullsend-test-coder`; for pools using the
   shared hosted App, update `fullsend-ai-coder`. Neither app set should be
   left permanently on permission-drop warnings.
3. Deploy mint. Lagging installations keep authenticating; they simply omit
   `packages:read` until they Accept. The preflight avoids the two-POST retry
   volume that the old rollout path incurred.
4. Release the CLI after the App registration and mint change. `fullsend github setup` reports
   pending **optional** permissions — those listed in `optionalRolePermissions`,
   currently `packages:read` — as warnings with the installing org's Accept URL
   and does not block, so a CLI release is not blocked on every installation
   accepting at once. Any other missing permission is still a setup error,
   exactly as before the rollout mechanism existed.
5. Tell installation owners to Accept the pending permission update (GitHub also
   emails org owners), and use the mint permission logs to find lagging installs.

Do **not** block mint or CLI deploy on every installation reporting
`packages:read` — inactive or unreachable installs would stall the platform.
Permission-drop logs and setup warnings are outreach signals during rollout,
not deploy gates. To add another permission in the future, add it to
`canonicalRolePermissions`, the matching App config and GCF embed; add it to
`optionalRolePermissions` only when it is explicitly safe to omit during
rollout. Remove that optional entry once all installations have accepted.

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

`GET /v1/status` returns the configured roles and version information.

- **Authentication:** Bearer token. OIDC is always tried first. When optional status validators are compiled in (e.g. GitHub user token via the `github` build tag), they are tried if OIDC fails. First successful auth wins.
- **Authorization:** Any valid credential from the auth pipeline — no role restriction.
- **OIDC response:** Scoped to the authenticating workflow's org.
  ```json
  {"org": "my-org", "roles": ["coder", "review", "triage"]}
  ```
- **Non-OIDC response** (e.g. GitHub user token): Reports all configured allowed orgs.
  ```json
  {"allowed_orgs": ["org-a", "org-b"], "roles": ["coder", "review", "triage"]}
  ```
- **Use case:** Workflow diagnostics — discover which roles are available before requesting a token. Non-OIDC auth enables status checks from outside GitHub Actions (e.g. `gh` CLI, OAuth login).
- **Security:** OIDC returns only the requesting org. Non-OIDC returns allowed orgs (not individual role app IDs).
- **Enabling optional validators:** Pass `--status-auth=github` to `mint deploy` along with `--status-github-group=ORG/TEAM`. This compiles the GitHub validator via the `github` build tag. Without these flags, OIDC is the only auth path.

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
- `FULLSEND_GCP_REGION` — GCP region for inference (value drift is detected and repaired by convergence)

**.fullsend repo variable (dot-repo fix):**
- `FULLSEND_MINT_URL` — Duplicate of org variable (dot-prefixed repos can't read org-level variables)

### Per-Repo Mode Secrets/Variables

#### GitHub

**Target repo secrets:**
- `FULLSEND_GCP_PROJECT_ID`
- `FULLSEND_GCP_WIF_PROVIDER`

**Target repo variables:**
- `FULLSEND_MINT_URL`
- `FULLSEND_GCP_REGION` (value drift is detected and repaired by convergence)
- `FULLSEND_REVIEW_CLIENT_ID` — OAuth client ID of the review agent's GitHub App (best-effort, conditional on successful lookup)

#### GitLab

**Target repo CI/CD variables (protected):**
- `FULLSEND_FORGE_TOKEN` — Project access token for bot identity (stored as protected CI/CD variable)
- `FULLSEND_LAST_POLL_AT_FAST` — Timestamp of last slash poll run (name predates the slash/events terminology split; used by the slash-command schedule)
- `FULLSEND_LAST_POLL_AT_FULL` — Timestamp of last event poll run (name predates the slash/events terminology split; used by the event-discovery schedule)
- `FULLSEND_POLL_MODE` — Pipeline schedule variable (`"slash"` or `"events"`); set automatically per schedule during install, not a project-level CI/CD variable
- `FULLSEND_LABEL_STATE` — JSON object tracking label sync state
- `FULLSEND_DISPATCHED_KEYS_FAST` — JSON map of recently dispatched event keys (slash-command schedule)
- `FULLSEND_DISPATCHED_KEYS_FULL` — JSON map of recently dispatched event keys (event-discovery schedule)
- `FULLSEND_FAILED_KEYS_FAST` — JSON map of event keys to failure counts (slash-command schedule)
- `FULLSEND_FAILED_KEYS_FULL` — JSON map of event keys to failure counts (event-discovery schedule)

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
