# Infrastructure Reference

This guide provides implementation details for fullsend's infrastructure components: the OIDC token mint, Workload Identity Federation (WIF), and secrets deployment. For basic installation instructions, see the [Installation Guide](installation.md).

## Token Mint (OIDC) — GCF Cloud Function

The mint is a GCP Cloud Function that exchanges GitHub OIDC tokens for scoped GitHub App installation tokens. This eliminates long-lived PATs from the system.

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
│  ┌──────────────────────────────────────────────────┐           │
│  │              GCF: Token Mint                      │           │
│  │                                                   │           │
│  │  1. Prevalidate OIDC JWT                          │           │
│  │     ├─ Check iss == token.actions.githubusercontent.com      │
│  │     ├─ Extract repository_owner → must be in ALLOWED_ORGS   │
│  │     └─ Validate job_workflow_ref against                     │
│  │        ALLOWED_WORKFLOW_FILES (fail-closed)                  │
│  │                                                   │           │
│  │  2. STS Token Exchange                            │           │
│  │     ├─ POST securitytoken.googleapis.com          │           │
│  │     │   grant_type=urn:ietf:params:oauth:         │           │
│  │     │   grant-type:token-exchange                 │           │
│  │     ├─ WIF pool validates OIDC token              │           │
│  │     └─ Returns GCP federated access token         │           │
│  │                                                   │           │
│  │  3. Lookup PEM from Secret Manager                │           │
│  │     ├─ Secret name: fullsend-{org}--{role}-app-pem│           │
│  │     └─ Returns PEM private key bytes              │           │
│  │                                                   │           │
│  │  4. Generate GitHub App JWT                       │           │
│  │     ├─ Sign with PEM key (RS256)                  │           │
│  │     ├─ App ID from ROLE_APP_IDS env               │           │
│  │     └─ 10-minute expiry                           │           │
│  │                                                   │           │
│  │  5. Find Installation                             │           │
│  │     ├─ GET /app/installations                     │           │
│  │     └─ Match by org login                         │           │
│  │                                                   │           │
│  │  6. Create Scoped Installation Token              │           │
│  │     ├─ POST /installations/{id}/access_tokens     │           │
│  │     ├─ Scope to requested repos[]                 │           │
│  │     └─ Apply rolePermissions minimum set          │           │
│  │                                                   │           │
│  └──────────┬───────────────────────────────────────┘           │
│             │                                                   │
│             ▼                                                   │
│  Response: { "token": "ghs_...", "expires_at": "..." }          │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Role Permissions Matrix

The mint enforces minimum permission sets per role. Tokens cannot exceed these scopes:

| Role | contents | pull_requests | issues | actions | checks | workflows | actions_variables | organization_projects | metadata |
|------|----------|---------------|--------|---------|--------|-----------|-------------------|-----------------------|----------|
| **fullsend** | write | write | — | write | — | write | read | — | read |
| **triage** | read | — | write | — | — | — | — | — | read |
| **coder** | write | write | write | — | read | — | — | — | read |
| **review** | read | write | write | — | read | — | — | — | read |
| **fix** | write | write | write | — | — | — | — | — | read |
| **retro** | read | read | write | read | — | — | — | — | read |
| **prioritize** | read | — | write | — | — | — | — | write | read |

### Mint Security Controls

- **ALLOWED_ORGS**: Allowlist of GitHub orgs that can mint tokens
- **ALLOWED_WORKFLOW_FILES**: Fail-closed allowlist of workflow filenames permitted to call mint
- **job_workflow_ref validation**: Only `.fullsend` or `fullsend-ai/fullsend` workflow refs accepted
- **PER_REPO_WIF_REPOS**: Repos using dedicated WIF providers (repo-scoped isolation)
- **Minimum permissions**: Tokens are scoped to the role's minimum permission set, not the App's full permissions

### Multi-Org Support

A single mint instance can serve multiple orgs:
- `EnsureOrgInMint()` additively appends orgs to `ALLOWED_ORGS` env var
- `ROLE_APP_IDS` maps `{org}/{role}` to GitHub App IDs
- Updates are applied atomically by redeploying the function with updated env vars

---

## Inference — Vertex AI with Workload Identity Federation

Inference authentication uses GCP Workload Identity Federation (WIF) to allow GitHub Actions to authenticate to Vertex AI without service account keys.

```
┌─────────────────────────────────────────────────────────────┐
│               Inference Authentication Flow                  │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  GitHub Actions Runner                                      │
│  ┌─────────────────────┐                                    │
│  │ OIDC JWT             │                                   │
│  │ (id-token: write)    │                                   │
│  └──────────┬──────────┘                                    │
│             │                                               │
│             ▼                                               │
│  ┌─────────────────────────────────┐                        │
│  │ GCP Security Token Service (STS)│                        │
│  │                                 │                        │
│  │ WIF Pool: fullsend-pool         │                        │
│  │ WIF Provider: fullsend-github   │                        │
│  │                                 │                        │
│  │ Validates OIDC issuer:          │                        │
│  │   token.actions.githubusercontent.com                    │
│  │                                 │                        │
│  │ Attribute mapping:              │                        │
│  │   sub → assertion.sub           │                        │
│  │   repo → assertion.repository   │                        │
│  └──────────┬──────────────────────┘                        │
│             │                                               │
│             ▼                                               │
│  ┌─────────────────────────────────┐                        │
│  │ Federated Access Token          │                        │
│  │ (short-lived, auto-rotated)     │                        │
│  └──────────┬──────────────────────┘                        │
│             │                                               │
│             ▼                                               │
│  ┌─────────────────────────────────┐                        │
│  │ Vertex AI API                   │                        │
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
2. **WIF Pool** — `fullsend-pool` in the GCP project
3. **WIF Provider** — Maps GitHub OIDC claims to GCP attributes
4. **IAM Bindings** — Grants `roles/aiplatform.user` to federated identities
5. **Per-repo providers** (per-repo mode) — Scoped WIF provider per repository via `BuildRepoProviderID()`

---

## GitHub Secrets & Variables Deployment

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
- `FULLSEND_GCP_REGION` — GCP region for inference (default: `global`)

**.fullsend repo variable (dot-repo fix):**
- `FULLSEND_MINT_URL` — Duplicate of org variable (dot-prefixed repos can't read org-level variables)

### Per-Repo Mode Secrets/Variables

**Target repo secrets:**
- `FULLSEND_GCP_PROJECT_ID`
- `FULLSEND_GCP_WIF_PROVIDER`

**Target repo variables:**
- `FULLSEND_MINT_URL`
- `FULLSEND_GCP_REGION`
- `FULLSEND_PER_REPO_INSTALL` — Flag indicating per-repo mode (set to "true")

### Secrets Layer Behavior

- **Install (OIDC mode)**: No-op — PEMs are stored in GCP Secret Manager, not as repo secrets. Only client IDs are written as repo variables.
- **Analyze**: Checks that expected secrets/variables exist. Cannot verify secret values (GitHub Secrets API is write-only for values). Flags stale secrets from pre-OIDC deployments.
- **Uninstall**: Deletes repo secrets and variables for all managed names.

### Inference Layer Behavior

- **Install**: Unconditionally writes secrets and variables (no way to check if values changed since GitHub doesn't expose secret values).
- **Analyze**: Checks presence of `FULLSEND_GCP_PROJECT_ID`, `FULLSEND_GCP_WIF_PROVIDER`, `FULLSEND_GCP_REGION`.

---

## GCF Provisioner Flow

The GCF provisioner handles full GCP infrastructure deployment:

```
┌─────────────────────────────────────────────────────────────────┐
│               GCF Provisioner: Provision() Flow                  │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌───────────────────┐                                          │
│  │ Get GCP project   │ resourcemanager.projects.get              │
│  │ number            │                                          │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  ┌───────────────────┐                                          │
│  │ Create Service    │ fullsend-mint@{project}.iam              │
│  │ Account           │ (skip if exists)                         │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  ┌───────────────────┐                                          │
│  │ Create WIF Pool   │ fullsend-pool                            │
│  │                   │ (skip if exists)                         │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  ┌───────────────────┐                                          │
│  │ Create WIF        │ fullsend-github                          │
│  │ Provider          │ OIDC issuer:                             │
│  │                   │   token.actions.githubusercontent.com    │
│  │                   │ (skip if exists)                         │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  ┌───────────────────┐                                          │
│  │ Grant Vertex AI   │ roles/aiplatform.user                    │
│  │ access to         │ on the inference project                 │
│  │ federated IDs     │                                          │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  ┌───────────────────┐                                          │
│  │ Store PEMs in     │ fullsend-{org}--{role}-app-pem           │
│  │ Secret Manager    │ for each agent role                      │
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
│  │                   │   OIDC_AUDIENCE                          │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  ┌───────────────────┐                                          │
│  │ Health check      │ Exponential backoff polling              │
│  │                   │ POST /v1/token (expect 401)              │
│  └─────────┬─────────┘                                          │
│            ▼                                                    │
│  Return: FULLSEND_MINT_URL = https://{region}-{project}.       │
│          cloudfunctions.net/fullsend-mint                        │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Source Hash Optimization

The GCF provisioner avoids redundant Cloud Function deployments by computing a SHA256 hash of the source zip and comparing it to metadata stored on the deployed function. Only deploys when the hash changes.

## See Also

- [Installation Guide](installation.md) - Step-by-step setup instructions
- [Local Development](../dev/local-dev.md) - Developer setup
