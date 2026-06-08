---
title: "43. GitLab per-repo support via OIDC/WIF and webhook bridge"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
  - security-threat-model
topics:
  - gitlab
  - forge
  - ci-cd
  - per-repo
  - credentials
  - webhook
---

# 43. GitLab per-repo support via OIDC/WIF and webhook bridge

Date: 2026-06-08

## Status

Accepted

Supersedes [ADR 0028](0028-gitlab-support.md).

## Context

ADR 0028 designed GitLab support around a per-org model mirroring GitHub: a central `.fullsend` project per group, per-role Project Access Tokens stored in GCP Secret Manager, and the token mint extended for GitLab OIDC. A design review revealed this was shoehorning GitHub-native architecture into GitLab:

- **No GitHub Apps equivalent**: GitLab has no asymmetric-key token exchange. OAuth Apps are user-interactive only (no client credentials flow). Service accounts use personal access tokens. There is no project-scoped OAuth token.
- **No sub-day token TTLs**: GitLab access tokens have a minimum ~1 day expiry vs GitHub's 1-hour installation tokens.
- **`CI_JOB_TOKEN` is insufficient for agent operations**: GitLab's native ephemeral credential cannot authenticate GraphQL requests ([documented limitation](https://docs.gitlab.com/ci/jobs/ci_job_token/)), cannot create merge requests, and provides no bot identity — agent actions appear as the pipeline runner, not a recognizable service account. GitLab is migrating planning features to the GraphQL-only Work Items API (Epic API removal planned for 19.0), making this limitation increasingly acute for triage agents.
- **Cross-project credential complexity**: Per-org mode requires the agent pipeline to run in a central `.fullsend` project and act on _other_ enrolled projects — creating the cross-project credential problem that drove the mint/token/Secret Manager complexity.

The key insight: **per-repo mode combined with GitLab's native OIDC `id_tokens` enables secretless credential retrieval via GCP Workload Identity Federation (WIF).** The pipeline runs inside the enrolled project on the protected default branch. A GitLab-issued OIDC token exchanges via WIF for GCP credentials, which are used to retrieve a bot project access token from Secret Manager. No credentials are stored as CI/CD variables — the project is secretless from GitLab's perspective.

ADR 0033 established per-repo installation for GitHub. This ADR adapts per-repo for GitLab, making it the _only_ supported mode. GitLab's CI/CD model — each project runs its own pipelines, shared logic via CI/CD Components — is designed for per-repo operation. The OIDC/WIF credential flow reuses infrastructure fullsend already provisions for Vertex AI inference (WIF pool/provider + Secret Manager).

Two architectural gaps remain:

1. **No `pull_request_target` equivalent**: GitLab has no mechanism to run a pipeline from the base branch when an MR event occurs on an untrusted source branch. A webhook bridge Cloud Function translates GitLab webhook events into Pipeline Trigger API calls, hardcoding `ref=main` to ensure the pipeline always runs trusted code.

2. **Agent operations require a bot project access token**: `CI_JOB_TOKEN` cannot create merge requests, cannot authenticate GraphQL requests, and provides no bot identity. A Developer-role project access token stored in GCP Secret Manager (not as a CI/CD variable) fills these gaps. It is retrieved at runtime via OIDC/WIF — no long-lived credentials in the project.

## Options

### Alternative 1: Per-org with central `.fullsend` project

Mirror the GitHub per-org model: a `.fullsend` config project holds dispatch pipelines, per-role project access tokens in Secret Manager, and the token mint extended for GitLab OIDC.

**Rejected**: Requires cross-project tokens (one per role per enrolled project), mint extension for GitLab OIDC claim normalization, and multiple WIF attribute conditions per project. Per-repo mode with OIDC/WIF achieves the same secretless credential retrieval without cross-project configuration. GitLab has no org-level App concept that would make per-org simpler (unlike GitHub, where one PEM per org covers all repos via installation tokens).

### Alternative 2: Both per-org and per-repo

Support both modes for GitLab, letting users choose.

**Rejected**: Per-org adds cross-project credential complexity with no clear benefit. The OIDC/WIF credential flow works per-project — each enrolled project exchanges its own OIDC token for its own bot PAT from Secret Manager. Supporting per-org means building cross-project WIF attribute conditions and shared credential stores for a mode that adds no capability over per-repo. If a user needs centralized config across projects, they can use GitLab CI/CD Components and group-level CI/CD variables without the full per-org machinery.

### Alternative 3: Downstream pipelines instead of webhook bridge

Use GitLab's multi-project or parent-child pipeline features to dispatch agent work without an external bridge.

**Rejected**: Parent-child pipelines (same project) could replace the dispatch-to-stage mechanism — a dispatch pipeline triggers stage-specific child pipelines — but they don't eliminate the bridge. We still need something to convert GitLab webhook events into the initial pipeline trigger, since GitLab has no `pull_request_target` equivalent that runs base-branch code on MR events. Multi-project pipelines (cross-project) would require the enrolled project to have a trigger relationship with another project, reintroducing cross-project configuration that per-repo mode eliminates. Parent-child pipelines remain a viable implementation optimization within Phase 3 (CI/CD templates).

### Alternative 4: Service accounts with personal access tokens

Create GitLab user accounts for each role (fullsend-triage, fullsend-code, etc.) and use their personal access tokens.

**Rejected**: Requires managing user accounts, consumes user licenses, personal access tokens are user-scoped not project-scoped. A project access token stored in Secret Manager and retrieved via OIDC/WIF provides project-scoped credentials without user account overhead.

## Decision

### Overview

GitLab support uses **per-repo installation mode only**. The selection of GitLab as the forge enforces per-repo installation — `fullsend admin install group/project --forge gitlab` is the only valid form; passing just a group name is an error.

The agent pipeline runs inside the enrolled project on the protected default branch, triggered by a webhook bridge Cloud Function. A bot project access token — retrieved at runtime via GitLab OIDC/GCP WIF from Secret Manager — serves as the single credential for all agent operations (REST, GraphQL, MR creation).

```
GitLab per-repo architecture:

ENROLLED PROJECT                           GCP
────────────────                           ───
.gitlab-ci.yml (root pipeline,             WIF pool/provider (validates GitLab OIDC)
               id_tokens: declared)        Service Account (impersonated by jobs)
.gitlab/ci/dispatch.yml (routing)    ←──── Bridge Cloud Function (webhook → trigger)
.gitlab/ci/triage.yml                      Secret Manager:
.gitlab/ci/code.yml                          - bot PAT per enrolled project
.gitlab/ci/review.yml                        - webhook secrets per project
.gitlab/ci/fix.yml                           - trigger tokens per project
.gitlab/ci/retro.yml
.fullsend/ (config workspace)

Credential flow:
  Pipeline job → OIDC token → GCP STS → WIF → impersonate SA → Secret Manager → bot PAT
```

### 1. Credential model

**Primary credential — bot project access token via OIDC/WIF**: A Developer-role project access token with `api` scope, created during `fullsend admin install` and stored in GCP Secret Manager. Retrieved at runtime via GitLab OIDC → GCP WIF — no credentials are stored as CI/CD variables in the enrolled project.

**OIDC token exchange flow**:
1. Each stage pipeline declares `id_tokens: { FULLSEND_ID_TOKEN: { aud: "fullsend" } }`
2. GitLab issues a signed JWT with claims: `project_id`, `project_path`, `namespace_id`, `ref_protected`, `pipeline_source`
3. The job exchanges the OIDC token at GCP STS (`sts.googleapis.com`)
4. GCP WIF validates the JWT signature against GitLab's JWKS public keys
5. GCP WIF validates attribute conditions: enrolled project ID and `ref_protected == "true"`
6. The job impersonates the fullsend GCP Service Account
7. The job reads the bot project access token from Secret Manager
8. The agent uses the bot PAT for all REST and GraphQL API operations

**Why `api` scope**: No narrower project access token scope covers MR creation. GitLab's fine-grained CI/CD job token permissions (GA in GitLab 18.3+) support only `READ_MERGE_REQUESTS`, not write. Fine-grained personal access tokens (beta) support per-resource MR create permissions but are user-scoped (not project-scoped) and not GA. When GitLab makes fine-grained project access tokens available, the bot PAT should be migrated to the narrowest possible scope.

**Bot identity**: The project access token creates a dedicated bot user in GitLab. Agent comments, label changes, and MR operations are attributable to this bot — providing the same recognizable identity that GitHub Apps give fullsend via `fullsend-ai-review[bot]`.

**GraphQL support**: Unlike `CI_JOB_TOKEN` (which [cannot authenticate GraphQL requests](https://docs.gitlab.com/ci/jobs/ci_job_token/)), the bot PAT authenticates both REST and GraphQL APIs. This is required for GitLab's Work Items API (issues, epics, custom fields, health status) which is GraphQL-only.

**Compensating controls for broad scope**:
- Set project access token expiry to 90 days (shorter than the 1-year maximum) to limit the window of exposure
- Bot PAT stored in Secret Manager with IAM access controls — only the fullsend Service Account can read it
- WIF attribute conditions restrict retrieval to pipelines from enrolled projects on protected branches
- Token is never stored as a CI/CD variable — `CI_DEBUG_TRACE` cannot expose it

**What is NOT needed**:
- Token mint (no custom credential exchange service — standard GCP WIF handles it)
- `CI_JOB_TOKEN` for API operations (insufficient for GraphQL and MR creation)
- Per-project CI/CD variables for credentials (the project is secretless from GitLab's perspective)
- Per-role tokens (all stages share the same bot PAT; per-role isolation is a future possibility via WIF attribute conditions)

**What IS needed**:
- GCP WIF pool/provider configured for GitLab OIDC (same one used for inference, or a dedicated one)
- GCP Service Account for the WIF-authenticated job to impersonate
- Secret Manager secret per enrolled project (bot PAT value)

### 2. Webhook bridge Cloud Function

GitLab webhooks deliver JSON event payloads, while the Pipeline Trigger API (`/api/v4/projects/:id/trigger/pipeline`) expects form-encoded parameters. These are not wire-compatible. A bridge Cloud Function translates between them.

**Request flow**:
1. Receive webhook POST from GitLab
2. Extract `X-Gitlab-Token` header (webhook secret)
3. Extract `project.path_with_namespace` from JSON payload
4. Look up project config by `sha256(project_path)` from GCP Secret Manager (with TTL cache)
5. Validate webhook token using `crypto/subtle.ConstantTimeCompare`
6. Determine event type from `X-Gitlab-Event` header
7. Extract minimal event payload, base64-encode it
8. Call Pipeline Trigger API: `POST /api/v4/projects/{projectID}/trigger/pipeline` with:
   - `ref=main` (**hardcoded, never from payload**)
   - `EVENT_TYPE` — mapped event type
   - `EVENT_PAYLOAD_B64` — base64-encoded minimal payload
   - `WEBHOOK_VALIDATED=true`
   - `RESOURCE_KEY` — for concurrency groups (e.g., issue IID or MR IID)

**Per-project config** stored in GCP Secret Manager:
- `expectedWebhookToken` — the webhook secret for this project
- `projectID` — GitLab project ID (numeric)
- `triggerToken` — Pipeline Trigger token for this project

**Deployment**: GCP Cloud Function, deployed alongside but separately from the mint (compromise isolation). The bridge has no access to bot project access tokens or any project credentials — it only holds trigger tokens (which can only trigger pipelines, not access project resources).

**Network connectivity**: The bridge must be able to reach the GitLab instance's Pipeline Trigger API endpoint. For gitlab.com (public API), a standard Cloud Function works. For self-hosted GitLab behind a VPN or firewall, the bridge should be deployed where it can already reach GitLab — not the other way around. Setting up VPN peering from GCP to a corporate network solely to solve a CI trigger limitation is disproportionate infrastructure for the problem.

Deployment options for internal GitLab instances:
- **On-premise deployment (preferred)**: Deploy the bridge as a standalone container on infrastructure inside the corporate network (OpenShift, Kubernetes, or any container host). The bridge is a self-contained Go binary with no GCP runtime dependency — it only needs outbound HTTPS to the GitLab API. This is the expected default for internal instances like gitlab.cee.
- **Cloud Run + VPC Connector**: Deploy as a Cloud Run service with a Serverless VPC Access connector peered to the GitLab network. More complex, but keeps all fullsend infrastructure in GCP.
- **VPN peering**: Peer the GCP VPC hosting the Cloud Function with the corporate network. Highest complexity; only justified if other GCP services also need GitLab connectivity.

**Event type mapping**:

| `X-Gitlab-Event` header | `EVENT_TYPE` variable |
|---|---|
| `Issue Hook` | `issues` |
| `Note Hook` | `note` |
| `Merge Request Hook` | `merge_request` |

### 3. Pipeline architecture

**Root pipeline** (`.gitlab-ci.yml`):
```yaml
include:
  - local: '.gitlab/ci/dispatch.yml'

workflow:
  rules:
    - if: $CI_PIPELINE_SOURCE == "trigger" && $CI_COMMIT_REF_PROTECTED == "true"
```

The `workflow:rules` ensure the pipeline only runs when triggered via the Pipeline Trigger API (not on push, schedule, or web) AND only on a protected branch. This is defense-in-depth on top of the bridge's `ref=main` hardcoding.

**Dispatch pipeline** (`.gitlab/ci/dispatch.yml`):
1. Validates `WEBHOOK_VALIDATED == "true"` (set by bridge after token validation)
2. Validates `CI_COMMIT_REF_PROTECTED == "true"` (redundant with workflow rules, defense-in-depth)
3. Guards against `CI_DEBUG_TRACE` (aborts if enabled — debug tracing exposes all variables)
4. Routes event to stage based on `EVENT_TYPE` and payload content (representative routes shown — see Section 4 for the complete event routing table):
   - `issues` + label `ready-to-code` → code stage
   - `issues` + label `ready-for-review` → review stage
   - `note` + `/fs-{stage}` slash commands → corresponding stage
   - `note` + non-command on issue with `needs-info` label → triage stage
   - `note` + MR comment with changes-requested marker (same-project) → fix stage
   - `merge_request` + `open`/`update`/`reopen` → review stage
   - `merge_request` + `merged` → retro stage
5. Triggers the matched stage job via GitLab CI `needs:` and `rules:if`

**Stage pipelines** (`.gitlab/ci/triage.yml`, `code.yml`, etc.):
```yaml
# fullsend-stage: triage

triage:
  stage: build
  image: ghcr.io/fullsend-ai/fullsend:latest
  id_tokens:
    FULLSEND_ID_TOKEN:
      aud: "fullsend"
  variables:
    FULLSEND_FORGE: "gitlab"
  script:
    - |
      # CI_DEBUG_TRACE guard
      if [[ "${CI_DEBUG_TRACE:-}" == "true" ]]; then
        echo "ERROR: CI_DEBUG_TRACE enabled — aborting to protect secrets"
        exit 1
      fi

      # Exchange OIDC token for GCP credentials via WIF
      gcloud auth login --cred-file=<(cat <<CRED
      {
        "type": "external_account",
        "audience": "//iam.googleapis.com/${FULLSEND_WIF_PROVIDER}",
        "subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
        "token_url": "https://sts.googleapis.com/v1/token",
        "credential_source": { "file": "${FULLSEND_ID_TOKEN_FILE}" },
        "service_account_impersonation_url": "https://iam.googleapis.com/v1/projects/-/serviceAccounts/${FULLSEND_SA}:generateAccessToken"
      }
      CRED
      )

      # Read bot PAT from Secret Manager
      export FULLSEND_FORGE_TOKEN=$(gcloud secrets versions access latest \
        --secret="${FULLSEND_BOT_TOKEN_SECRET}" \
        --project="${FULLSEND_GCP_PROJECT_ID}")

      # Decode event payload to temp file
      EVENT_PAYLOAD_FILE=$(mktemp)
      trap 'rm -f "${EVENT_PAYLOAD_FILE}"' EXIT
      echo "${EVENT_PAYLOAD_B64}" | base64 -d > "${EVENT_PAYLOAD_FILE}"

      # Run the agent
      fullsend run \
        --stage triage \
        --source-project "${CI_PROJECT_PATH}" \
        --event-type "${EVENT_TYPE}" \
        --event-payload-file "${EVENT_PAYLOAD_FILE}" \
        --forge gitlab
  resource_group: "fullsend-triage-${RESOURCE_KEY}"
  rules:
    - if: $STAGE == "triage"
```

All stages use the same credential flow — the bot PAT is the single credential for all operations (REST, GraphQL, MR creation). No stage-specific token handling is needed.

### 4. Event routing

| GitLab Event | Payload Signal | Stage |
|---|---|---|
| `Issue Hook` — label added | Label name `ready-to-code` | code |
| `Issue Hook` — label added | Label name `ready-for-review` | review |
| `Note Hook` — issue comment | Body starts with `/fs-triage` | triage |
| `Note Hook` — issue comment | Body starts with `/fs-code` | code |
| `Note Hook` — issue comment | Body starts with `/fs-review` | review |
| `Note Hook` — issue comment | Body starts with `/fs-fix` | fix |
| `Note Hook` — issue comment | Body starts with `/fs-retro` | retro |
| `Note Hook` — issue comment | Body starts with `/fs-prioritize` | prioritize |
| `Note Hook` — non-command on issue with `needs-info` | — | triage |
| `Merge Request Hook` — open/update/reopen | — | review |
| `Merge Request Hook` — merged | — | retro |
| `Note Hook` — MR comment with changes-requested marker + same-project MR | — | fix |

**Fork MR protection**: The fix stage is skipped when the MR's `source_project_id != target_project_id`. This prevents fork MRs from triggering fix pipelines that would push commits to the target project.

### 5. Config layering

Same `customized/` convention as GitHub per-repo (ADR 0033, ADR 0035):

```
fullsend-ai/fullsend defaults  <  .fullsend/customized/  <  AGENTS.md
(base, fetched at runtime)       (project overrides)       (instructions)
```

The `.fullsend/` directory in the enrolled project serves as the config workspace. At runtime, the pipeline fetches upstream defaults from `fullsend-ai/fullsend` and copies `customized/` overrides on top — identical layering logic to GitHub per-repo, different CI syntax.

**Config is always read from the protected default branch**, not from MR source branches. The pipeline runs on `ref=main`, so the checkout reflects the default branch. This prevents MR authors from injecting modified agent instructions or policies.

### 6. Repo layout

```
enrolled-project/
├── .gitlab-ci.yml                    ← root pipeline
├── .gitlab/ci/
│   ├── dispatch.yml                 ← event routing
│   ├── triage.yml                   ← fullsend-stage: triage
│   ├── code.yml                     ← fullsend-stage: code
│   ├── review.yml                   ← fullsend-stage: review
│   ├── fix.yml                      ← fullsend-stage: fix
│   ├── retro.yml                    ← fullsend-stage: retro
│   └── prioritize.yml               ← fullsend-stage: prioritize
├── .fullsend/                        ← config workspace (optional)
│   ├── config.yaml                  ← project-level config
│   └── customized/                  ← user overrides
│       ├── agents/
│       ├── harness/
│       ├── policies/
│       ├── skills/
│       └── scripts/
├── AGENTS.md
└── ... (source code)
```

### 7. CLI support

```
fullsend admin install group/project --forge gitlab
```

The argument must be `group/project` format. Passing just a group name with `--forge gitlab` is an error: "GitLab installation supports per-repo mode only. Use: fullsend admin install group/project --forge gitlab".

**Flags**:
- `--forge {github|gitlab}` — auto-detected from remote URL, overridable
- `--gitlab-url` — GitLab instance URL (default: `https://gitlab.com`)
- `--inference-project` — GCP project for Vertex AI inference (required)
- `--inference-region` — GCP region for inference (default: `global`)
- `--bridge-project` — GCP project for the bridge Cloud Function (defaults to `--inference-project`)
- `--bridge-region` — GCP region for the bridge (default: `us-central1`)
- `--skip-bridge-deploy` — skip bridge deployment, reuse existing bridge URL
- `--bridge-url` — pre-existing bridge URL (requires `--skip-bridge-deploy`)
- `--dry-run` — preview changes without making them

**Install flow**:
1. Parse `group/project`, resolve GitLab token (`GL_TOKEN` / `GITLAB_TOKEN` / `glab auth token`)
2. Create GitLab forge client, validate project exists and user has Maintainer access
3. Validate default branch is protected (`IsProtectedBranch`)
4. Validate `CI_DEBUG_TRACE` is not enabled project-wide
5. Set up WIF pool/provider for GitLab OIDC (if not already configured)
6. Create Project Access Token (Developer role, `api` scope) → store in Secret Manager
7. Configure WIF attribute condition for this project: `assertion.project_id == "<id>" && assertion.ref_protected == "true"`
8. Deploy bridge Cloud Function (if `--skip-bridge-deploy` is not set)
9. Create pipeline trigger token for the project
10. Register project with bridge (webhook secret + trigger token + project ID → Secret Manager)
11. Create project webhook pointing to bridge URL (events: Issue, Note, Merge Request)
12. Commit CI/CD template files to the project via GitLab API
13. Set protected CI/CD variables: `FULLSEND_WIF_PROVIDER`, `FULLSEND_SA`, `FULLSEND_BOT_TOKEN_SECRET`, `FULLSEND_GCP_PROJECT_ID`, `FULLSEND_FORGE=gitlab`, `FULLSEND_PER_REPO_INSTALL=true`
14. Set up inference WIF if `--inference-project` provided

**Uninstall flow** (`fullsend admin uninstall group/project --forge gitlab`):
1. Delete project webhook
2. Delete pipeline trigger token
3. Remove project registration from bridge Secret Manager
4. Revoke bot project access token, delete Secret Manager secret
5. Remove WIF attribute condition for this project
6. Remove CI/CD template files from project
7. Remove protected CI/CD variables
8. Optionally tear down bridge Cloud Function (if no other projects use it)

### 8. Forge abstraction compliance

ADR 0005 promises: "Adding a new forge requires implementing `forge.Client` — no changes to layers, CLI, or app setup code."

This ADR adds the following forge-neutral methods to `forge.Client`:
- `CreateWebhook(ctx, owner, repo, targetURL, secretToken string, events []string) (webhookID string, err error)`
- `DeleteWebhook(ctx, owner, repo, webhookID string) error`
- `IsProtectedBranch(ctx, owner, repo, branch string) (bool, error)`
- `TriggerPipeline(ctx, owner, repo, ref string, variables map[string]string) error`

GitHub-only methods (`ListOrgInstallations`, `GetAppClientID`) move to a `GitHubExtensions` extension interface. Callers type-assert to access them.

A new `ErrNotSupported` sentinel allows forge implementations to reject inapplicable operations (e.g., GitLab returns `ErrNotSupported` for `DispatchWorkflow`; GitHub returns it for `TriggerPipeline`).

The GitLab forge client constructor accepts a single token:
```go
func New(token, baseURL string) (*LiveClient, error)
```
The bot project access token serves all operations — REST, GraphQL, and MR creation. No dual-token model is needed.

Changes to layers and app setup are limited to calling these new forge-neutral methods and detecting the forge type. The CLI requires forge-specific flags (`--gitlab-url`, `--bridge-project`, etc.) and a GitLab-specific install flow (`runGitLabPerRepoInstall`), but the forge abstraction boundary is preserved — GitLab-specific API logic lives in `internal/forge/gitlab/gitlab.go`, not in shared infrastructure.

## Security Model

### Layer 1: Bridge hardcodes `ref=main`

The bridge Cloud Function always calls the Pipeline Trigger API with the project's protected default branch (typically `main`). This value is read from a per-project config stored in GCP Secret Manager — set at install time and never derived from webhook payload fields. This is the primary control ensuring that MR code cannot modify the pipeline to exfiltrate secrets.

**Threat**: An attacker pushes a malicious branch to the enrolled project with modified `.gitlab-ci.yml` that exfiltrates WIF configuration or attempts OIDC token exchange. If the bridge derived `ref` from the webhook payload, the attacker could trigger a pipeline on the malicious branch. WIF attribute conditions (Layer 4) provide defense-in-depth: even if the pipeline runs on a non-protected branch, `ref_protected == "true"` fails and the token exchange is rejected.

### Layer 2: Protected CI/CD variables

All CI/CD variables in the enrolled project that gate credential retrieval MUST be marked as "protected." GitLab restricts protected variables to pipelines running on protected branches only. No credentials are stored directly as CI/CD variables — the bot PAT lives in Secret Manager — but the WIF configuration variables enable credential retrieval, so protecting them is defense-in-depth on top of WIF attribute conditions (Layer 4).

**Required protected variables**:
- `FULLSEND_WIF_PROVIDER` — WIF provider resource name (enables OIDC token exchange)
- `FULLSEND_SA` — GCP Service Account email (impersonated after WIF exchange)
- `FULLSEND_BOT_TOKEN_SECRET` — Secret Manager secret name for the bot PAT
- `FULLSEND_GCP_PROJECT_ID` — GCP project for Secret Manager and inference

### Layer 3: Webhook secret validation and replay protection

Each enrolled project has a unique webhook secret. The bridge validates the `X-Gitlab-Token` header against the expected secret using `crypto/subtle.ConstantTimeCompare` to prevent timing side-channel attacks. Invalid tokens are rejected before any processing.

The bridge also deduplicates webhooks using the `X-Gitlab-Event-UUID` header (unique per delivery). A short-lived cache (TTL matching GitLab's retry window) tracks seen delivery IDs and rejects replays. This prevents intercepted valid webhook payloads from being replayed to trigger redundant agent pipelines.

### Layer 4: OIDC/WIF attribute conditions

GCP WIF attribute conditions restrict which GitLab pipelines can exchange OIDC tokens for GCP credentials. The condition for each enrolled project enforces:
- `assertion.project_id == "<enrolled_project_id>"` — only the specific enrolled project
- `assertion.ref_protected == "true"` — only pipelines running on protected branches

This provides cryptographic enforcement that the bot PAT can only be retrieved by the correct project on a protected branch. Even if an attacker obtains WIF configuration variables (from `CI_DEBUG_TRACE` or a compromised pipeline on a non-protected branch), the OIDC token exchange fails because the JWT's `ref_protected` claim is `false`.

The OIDC token itself has a short TTL (~5 minutes), limiting the window for replay attacks.

### Layer 5: `CI_DEBUG_TRACE` guard (best-effort)

GitLab's `CI_DEBUG_TRACE` variable, when enabled, prints all CI/CD variables (including protected ones) to job logs. This is a known escape hatch that bypasses variable masking and protection.

Two defenses:
1. **Install-time**: `fullsend admin install` validates that `CI_DEBUG_TRACE` is not enabled on the project before proceeding.
2. **Runtime**: Every stage pipeline includes an early guard that aborts if `CI_DEBUG_TRACE` is detected:
   ```bash
   if [[ "${CI_DEBUG_TRACE:-}" == "true" ]]; then
     echo "ERROR: CI_DEBUG_TRACE enabled — aborting to protect secrets"
     exit 1
   fi
   ```

**Reduced risk with OIDC/WIF**: The bot PAT is not stored as a CI/CD variable, so `CI_DEBUG_TRACE` cannot directly expose it. The variables that ARE exposed (WIF provider, Service Account, secret name) are configuration pointers, not credentials. However, the OIDC token itself may be logged, and an attacker with the WIF config + OIDC token could replay the token exchange within its ~5 minute TTL. The runtime guard limits this window, and the install-time check is the stronger control.

**Limitation**: GitLab Runner begins trace-level logging before the script runs, so variable values may already be logged before the guard fires `exit 1`. A sufficiently privileged insider (project Maintainer) can enable `CI_DEBUG_TRACE` at any time — this is an inherent GitLab limitation with no perfect defense.

### Layer 6: Payload encoding

Event payloads are base64-encoded by the bridge before passing as pipeline variables. This prevents YAML injection attacks where attacker-controlled event content (issue titles, MR descriptions containing YAML metacharacters) could break out of the `variables:` block and inject arbitrary pipeline configuration.

### Layer 7: Fork MR protection

The fix stage is skipped when the MR's `source_project_id != target_project_id`. This prevents fork MRs from triggering fix pipelines that would push commits to the target project.

### Known security limitations

1. **Bot PAT scope**: The `api` scope grants full project API access, not just MR creation or GraphQL. GitLab does not offer a narrower project access token scope for these operations. **Migration path**: When GitLab ships fine-grained project access tokens, the bot PAT should be re-created with the narrowest scope that covers MR creation + GraphQL.

   **Post-compromise abuse scenarios** (if a stage is compromised and the attacker obtains the bot PAT from the `FULLSEND_FORGE_TOKEN` environment variable): the `api` scope permits modifying CI/CD variables (including removing the `protected` flag from other variables), altering project webhooks (redirecting events to attacker-controlled endpoints), changing project settings (disabling branch protection), and accessing project-level API endpoints beyond agent operations. **Compensating controls**: 90-day expiry (reduces window of validity), WIF attribute conditions restrict retrieval to enrolled projects on protected branches (Layer 4), Secret Manager IAM controls limit which service accounts can read the bot PAT, and the OIDC token has a ~5 minute TTL (limits replay window).

2. **Token rotation**: The bot PAT expires (max 1 year for project access tokens). GitHub App private keys do not expire. `fullsend admin install` should record the expiration date and warn 30 days before expiry. A `fullsend admin rotate-token` command updates the Secret Manager secret — centralized, no per-project CI/CD variable changes needed.

3. **No per-role credential isolation (initially)**: All stages share the same bot PAT. If the triage stage is compromised, it has the same project access as the code stage (including MR creation). **Future path**: WIF attribute conditions could be extended to select different Secret Manager secrets per stage (e.g., a read-only PAT for triage/review and a write PAT for code/fix), enabling per-role isolation without per-role Apps.

4. **Bridge as external infrastructure**: The webhook bridge is a GCP Cloud Function that must be continuously available. Bridge downtime means webhook events are lost (GitLab retries failed webhooks, but only for a limited time). Bridge compromise could allow triggering pipelines on arbitrary branches (mitigated by Layer 2 — protected variables — and Layer 4 — WIF attribute conditions).

## Comparison with GitHub

This section documents how the GitLab per-repo model differs from GitHub per-repo (ADR 0033). These comparisons are for reference — the GitLab architecture stands independently and should not be evaluated against GitHub as a baseline.

| Concern | GitHub (ADR 0033) | GitLab (this ADR) |
|---------|-------------------|-------------------|
| Installation modes | Per-org and per-repo | Per-repo only |
| Primary credential | GitHub App installation token via mint OIDC | Bot project access token via OIDC/WIF |
| MR/PR creation | Same App installation token | Same bot project access token |
| GraphQL support | App installation token authenticates GraphQL | Bot PAT authenticates GraphQL |
| Bot identity | `fullsend-ai-review[bot]` (App identity) | Bot user from project access token |
| Credential lifetime | 1 hour (installation token) | Max 1 year (PAT), retrieved per-job via WIF |
| Token mint | Required (custom Cloud Function) | Not needed (standard GCP WIF) |
| GCP Secret Manager | PEM keys for all Apps | Bot PAT + webhook secrets |
| Workload Identity Federation | Required (OIDC → mint) | Required (OIDC → Secret Manager) |
| Central config repo | Optional (per-repo skips it) | N/A (per-repo only) |
| Event dispatch | `pull_request_target` + shim workflow | Webhook → bridge → Pipeline Trigger API |
| Shared logic | Reusable workflows via `workflow_call` | CI/CD Components |
| Per-role isolation | Separate Apps per role | Single bot PAT (per-role possible via WIF in future) |
| External infrastructure | Mint Cloud Function | Bridge Cloud Function |
| Credential rotation | App keys never expire | PAT expires (max 1 year), centralized in Secret Manager |
| Credential storage | Per-project CI/CD variables | Centralized in GCP Secret Manager (project is secretless) |
| Secure event trigger | `pull_request_target` (native) | Bridge hardcodes `ref=main` (external) |
| Debug trace risk | `ACTIONS_STEP_DEBUG` shows step output only | `CI_DEBUG_TRACE` exposes WIF config (not credentials) |

**Where GitLab is simpler**:
- No App creation dance (no browser-based manifest flow)
- No custom mint service (standard GCP WIF replaces the mint Cloud Function for credential exchange)
- No PEM handling (no private key generation, no JWT signing)
- No installation token exchange (no PEM → JWT → installation token → scoped token chain)
- No org-level secrets or variables
- Secretless from the project's perspective (no credentials stored as CI/CD variables)

**Where GitLab is harder**:
- No `pull_request_target` equivalent (requires external bridge)
- Review semantics (no native review object — must synthesize from notes and approvals)
- Project access token rotation (GitHub App keys don't expire; PAT must be rotated in Secret Manager)
- `CI_DEBUG_TRACE` exposure (though reduced risk since credentials are not CI/CD variables)
- Subgroup paths (deeply nested namespaces like `org/sub1/sub2/project`)

## Consequences

### Positive

- **Dramatically simpler than per-org**: Eliminates the custom token mint, cross-project credentials, and central `.fullsend` project. Uses standard GCP WIF instead of a custom credential exchange service.
- **Secretless from the project's perspective**: No credentials stored as CI/CD variables. The bot PAT lives in Secret Manager and is retrieved via OIDC/WIF at runtime. `CI_DEBUG_TRACE` cannot directly expose credentials.
- **Bot identity**: Agent actions are attributable to a dedicated bot user, providing the same recognizable identity that GitHub Apps offer via `fullsend-ai-review[bot]`.
- **GraphQL support**: The bot PAT authenticates both REST and GraphQL APIs, supporting GitLab's Work Items API and future GraphQL-only features.
- **One project access token instead of per-role tokens**: Only one credential to manage and rotate, not four or more.
- **No mint changes**: GitLab support requires zero changes to the existing token mint infrastructure.
- **Reuses inference infrastructure**: The WIF pool/provider and Secret Manager are the same GCP services already provisioned for Vertex AI inference.
- **Pure egress**: The OIDC/WIF credential flow is entirely outbound from GitLab to GCP — no inbound connectivity to the GitLab instance is needed for credential retrieval (the bridge still needs inbound for webhooks).
- **Same agent workflow**: Triage → Code → Review → Fix stages work identically from the user's perspective.

### Negative

- **Per-repo only**: No centralized config, policies, or credential management across projects. Organizations wanting uniform agent behavior must manage CI/CD Components and group-level variables independently. This creates a user-visible asymmetry with GitHub, which supports both per-org and per-repo modes — teams working across both forges should expect different installation and management workflows.
- **Bridge Cloud Function required**: External infrastructure that GitHub avoids entirely. Must be deployed, monitored, and maintained.
- **Token rotation**: The bot PAT expires (max 1 year). Requires renewal automation. GitHub App keys do not expire. Rotation is centralized in Secret Manager (`fullsend admin rotate-token`).
- **GCP dependency for forge credentials**: Secret Manager + WIF are required for credential retrieval, not just inference. The project cannot operate without GCP infrastructure.
- **`api` scope is broad**: The bot project access token has full project API access. A narrower scope is not available in GitLab today.
- **`CI_DEBUG_TRACE` is an escape hatch**: Exposes WIF configuration variables. While the bot PAT itself is not exposed, the OIDC token + WIF config could enable replay within the token's ~5 minute TTL.

### Risks

Ordered by the project's threat priority (external injection > insider > drift > supply chain):

1. **External injection — bridge compromise**: If the bridge is compromised, an attacker could trigger pipelines on arbitrary branches. **Mitigation**: Protected CI/CD variables (Layer 2) prevent secret exposure on non-protected branches. Bridge holds only trigger tokens, not project credentials.

2. **External injection — YAML injection via payloads**: Attacker-controlled event content (issue titles, MR descriptions) could inject YAML if embedded directly in pipeline variables. **Mitigation**: Base64 encoding (Layer 6).

3. **Insider — `CI_DEBUG_TRACE` enablement**: A project maintainer could enable `CI_DEBUG_TRACE` to expose WIF configuration and OIDC tokens. The bot PAT itself is not a CI/CD variable and is not directly exposed, but an attacker could replay the OIDC token exchange within its ~5 minute TTL. **Mitigation**: Runtime guard aborts the pipeline (Layer 5). WIF attribute conditions (Layer 4) provide independent enforcement.

4. **Insider — pipeline modification**: A contributor with write access could modify `.gitlab-ci.yml` or `.gitlab/ci/*.yml` in a PR. Unlike GitHub's `pull_request_target`, there is no mechanism to run the _base branch_ version of the pipeline for MR events — but the pipeline only runs on `ref=main` via the trigger API, so MR branch modifications don't affect the running pipeline until merged. **Mitigation**: CODEOWNERS on `.gitlab-ci.yml`, `.gitlab/ci/`, and `.fullsend/`.

5. **Drift — token expiration**: If the bot PAT expires without renewal, all agent stages fail to authenticate. **Mitigation**: Expiration monitoring and `fullsend admin rotate-token` command (updates Secret Manager secret centrally).

## Implementation Details

Detailed implementation guidance is maintained in a companion document: [docs/plans/gitlab-per-repo-implementation.md](../plans/gitlab-per-repo-implementation.md).

The implementation is organized in six phases:

### Phase 0: Forge interface preparation
Add `CreateWebhook`, `DeleteWebhook`, `IsProtectedBranch`, `TriggerPipeline` to `forge.Client`. Move GitHub-only methods to `GitHubExtensions` extension interface. Add `ErrNotSupported` sentinel. Create `internal/forge/detect.go` for forge auto-detection.

### Phase 1: GitLab forge client
Implement `internal/forge/gitlab/gitlab.go` with full `forge.Client` interface using `gitlab.com/gitlab-org/api/client-go`. Single-token constructor for the bot PAT retrieved via OIDC/WIF. Synthesize review semantics from notes and approvals.

### Phase 2: Webhook bridge Cloud Function
Implement `internal/bridge/main.go` (separate Go module). Webhook → trigger API translation with constant-time token validation, hardcoded `ref=main`, base64-encoded payloads. Per-project configs in Secret Manager.

### Phase 3: GitLab CI/CD templates
Create `internal/scaffold/fullsend-repo-gitlab/` with dispatch routing and per-stage pipeline templates. OIDC/WIF credential flow in each stage (no mint calls, no CI/CD variable credentials).

### Phase 4: CLI changes
Add `--forge` and `--gitlab-url` flags to `fullsend admin install`. Implement `runGitLabPerRepoInstall()` with project access token creation, bridge deployment, webhook setup, and scaffold file commit.

### Phase 5: Integration and testing
Wire forge detection into CLI. Unit tests for GitLab client, bridge, forge detection. Integration tests with mock GitLab API. E2E tests against GitLab.com test project.

### Dependency order

Phases 1 and 2 depend on Phase 0 (forge interface changes). Phase 3 (CI/CD templates) has no code dependency on Phase 0 and can start immediately. Phase 4 depends on Phase 1. Phase 5 depends on all prior phases.

## Open Questions

### Per-role credential isolation via WIF

The current design uses a single bot PAT for all stages. WIF attribute conditions could be extended to select different Secret Manager secrets per stage — for example, a read-only PAT for triage/review and a write PAT for code/fix. This would require per-stage `id_tokens` with different audiences or claims, and corresponding WIF attribute conditions. This is a future optimization that adds complexity (multiple PATs to create and rotate per project) but provides the per-role isolation that GitHub achieves with separate Apps.

### WIF for bridge trigger tokens

The bridge currently stores per-project trigger tokens in Secret Manager. A future optimization could eliminate these by using the bridge's own GCP identity to call the GitLab Pipeline Trigger API via a different mechanism (e.g., a project access token with `api` scope stored centrally). The inbound direction (GitLab → bridge) still requires webhook secrets, as GitLab webhooks authenticate via a shared secret in the `X-Gitlab-Token` header with no OIDC-based alternative.

### Native `merge_request_event` dispatch via `include: project:`

The current design routes all events — including MR events — through the webhook bridge. GitLab's native `merge_request_event` pipeline source could handle MR events directly, using [`include: project: ref: main`](https://docs.gitlab.com/ci/yaml/includes/) to pull trusted dispatch logic from a central templates project. The `ref: main` pin ensures the MR author cannot tamper with dispatch routing, `CI_DEBUG_TRACE` guards, or fork protection — the template is always fetched from the protected branch of the templates project.

This would reduce the bridge's scope to only issue and comment events (which have no native CI trigger), eliminating it from the high-frequency MR path and reducing the blast radius of bridge downtime. Both `merge_request_event` and `include: project:` are available on GitLab Free tier.

**Why deferred**: The bridge is still required for issue/comment events regardless, so it cannot be eliminated — only reduced in scope. Adding a second dispatch path (native for MR events, bridge for issue/comment events) increases operational complexity. The `include: project:` mechanism also introduces a central templates project dependency, and GitLab documents that nested includes execute as a public user — the templates project visibility requirements need investigation. This optimization can be layered on later without changing the credential model or security architecture.

### Parent-child pipelines for stage dispatch

Parent-child pipelines within the enrolled project could replace the current dispatch-to-stage mechanism (dispatch job routes event → triggers stage job). Instead, the dispatch pipeline would trigger child pipelines for each stage. This is an implementation optimization for Phase 3 — it doesn't affect the architecture or credential model.

## References

- [ADR 0005: Forge abstraction layer](0005-forge-abstraction-layer.md) — abstraction boundary preserved
- [ADR 0007: Per-role GitHub Apps](0007-per-role-github-apps.md) — GitHub authentication model (not replicated for GitLab)
- [ADR 0028: GitLab Support Architecture](0028-gitlab-support.md) — deprecated, superseded by this ADR
- [ADR 0029: Central token mint](0029-central-token-mint-secretless-fullsend.md) — not needed for GitLab
- [ADR 0033: Per-repo installation mode](0033-per-repo-installation-mode.md) — GitHub per-repo (adapted for GitLab)
- [ADR 0035: Layered content resolution](0035-layered-content-resolution.md) — same `customized/` convention
- [GitLab OIDC `id_tokens`](https://docs.gitlab.com/ci/secrets/id_token_authentication/) — native OIDC token issuance in CI/CD jobs
- [GCP Workload Identity Federation for GitLab](https://docs.gitlab.com/ci/cloud_services/google_cloud/) — OIDC → GCP credential exchange
- [GitLab CI/CD job tokens](https://docs.gitlab.com/ee/ci/jobs/ci_job_token.html) — `CI_JOB_TOKEN` limitations (cannot authenticate GraphQL, cannot create MRs)
- [GitLab Project Access Tokens](https://docs.gitlab.com/user/project/settings/project_access_tokens/) — scopes and limitations
- [GitLab Pipeline Triggers](https://docs.gitlab.com/ee/ci/triggers/) — trigger API for the bridge
