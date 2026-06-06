---
title: "43. GitLab support via webhook bridge"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
topics:
  - gitlab
  - forge
  - ci-cd
  - multi-platform
  - webhook
  - security
---

# 43. GitLab support via webhook bridge

Date: 2026-06-01

## Status

Accepted

Supersedes [ADR 0028](0028-gitlab-support.md).

## Context

fullsend supports GitHub exclusively. Organizations on GitLab cannot adopt it. The forge abstraction (`forge.Client`, [ADR 0005](0005-forge-abstraction-layer.md)) was designed for multi-forge support, but the surrounding infrastructure — token mint, dispatch workflows, shim security model — is GitHub-specific.

[ADR 0028](0028-gitlab-support.md) proposed GitLab support in April 2026 but was deprecated because the architecture changed significantly:

- **[ADR 0029](0029-central-token-mint-secretless-fullsend.md)** introduced the central token mint (GCP Cloud Function with OIDC validation), replacing PEM-in-repo-secrets.
- **[ADR 0041](0041-synchronous-workflow-call-event-dispatch.md)** moved dispatch from asynchronous `workflow_dispatch` to synchronous `workflow_call`.
- **[ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md)** introduced reusable workflows so `.fullsend` agent workflows are thin callers to upstream `fullsend-ai/fullsend`.
- **[ADR 0033](0033-per-repo-installation-mode.md)** added per-repo installation mode alongside per-org.

[ADR 0028](0028-gitlab-support.md)'s authentication model (per-role Project Access Tokens stored as CI/CD variables), dispatch model (async pipeline trigger API), and unresolved webhook translation question no longer align with the current architecture. This ADR redesigns GitLab support from the current baseline.

### The core problem: no `pull_request_target`

On GitHub, `pull_request_target` runs the shim workflow from the **base branch**, preventing MR authors from modifying the workflow to exfiltrate secrets ([ADR 0009](0009-pull-request-target-in-shim-workflows.md)). GitLab has no equivalent mechanism. A `.gitlab-ci.yml` in an enrolled repo runs the MR branch version — an attacker could modify it to dump secrets.

This is the fundamental security gap that any GitLab support design must close.

### Webhook-to-pipeline wire incompatibility

GitLab CI/CD pipelines cannot be natively triggered by issue events, note (comment) events, or MR review events. The `CI_PIPELINE_SOURCE` variable has no values for these event types.

GitLab webhooks _can_ fire on these events, but webhooks deliver JSON payloads while the pipeline trigger API (`/api/v4/projects/:id/trigger/pipeline`) expects form-encoded parameters. Pointing a webhook URL at the trigger API produces a malformed request. An intermediary is required.

## Options

**Selected**: Webhook bridge Cloud Function (see [Decision](#decision) below).

### Alternative 1: In-repo CI job as shim

A `.gitlab-ci.yml` job in enrolled repos receives pipeline events and calls the `.fullsend` trigger API.

**Rejected**: Cannot enforce protected-branch-only execution. MR authors can modify the CI job to exfiltrate the trigger token or any secrets available in the pipeline context. This reintroduces the exact vulnerability that `pull_request_target` prevents on GitHub.

### Alternative 2: GitLab serverless functions

Deploy a GitLab-hosted serverless function that translates webhooks to trigger API calls.

**Rejected**: Requires GitLab Premium or Ultimate tier. Excluding Free tier users is an unacceptable constraint for an open-source project.

### Alternative 3: Per-role Project Access Tokens stored as CI/CD variables

Store PATs directly in `.fullsend` CI/CD variables ([ADR 0028](0028-gitlab-support.md)'s original approach) instead of using the central token mint.

**Rejected (storage mechanism, not PATs themselves)**: The issue is storing PATs in CI/CD variables rather than Secret Manager — not the use of PATs. CI/CD variable storage diverges from the token mint model ([ADR 0029](0029-central-token-mint-secretless-fullsend.md)) by splitting credential management across two systems (Secret Manager for GitHub, CI/CD variables for GitLab), increasing operational complexity and the attack surface. The chosen design uses PATs but stores them in Secret Manager and distributes them through the mint, preserving centralized credential storage and OIDC-gated access.

### Alternative 4: Pull-based model with GitLab CI scheduled pipelines

Use GitLab CI jobs for events that CI can natively trigger (MR events via `merge_request_event` pipeline source). For everything else (issue events, note events, label changes), use a scheduled GitLab CI job that polls the GitLab API for recent activity and dispatches agent work.

**Advantages**: No external infrastructure needed — everything runs in GitLab CI. Scheduled jobs run in the context of the target repo and have access to repo secrets. No webhook bridge to deploy, monitor, or make network-adjacent to self-hosted instances.

**Deferred as a fallback for specific deployment profiles**: The pull model trades latency for simplicity — scheduled pipelines have a minimum 5-minute interval, so issue triage could be delayed up to 5 minutes. It also requires idempotent event processing (tracking which events have already been handled across polling intervals). Recommended for: multiple self-hosted GitLab instances behind VPNs where deploying a network-adjacent bridge per instance is prohibitive. Not recommended for: GitLab.com or single-instance deployments where the webhook bridge provides real-time dispatch. **Decision criteria for revisiting**: will be prototyped if two or more organizations report that webhook bridge deployment is blocked by network constraints (e.g., VPN-isolated instances with no public ingress path). If no such reports arise during initial adopter rollout, this alternative will be dropped.

## Decision

### Webhook bridge Cloud Function

Deploy a lightweight GCP Cloud Function — separate from the token mint — that translates GitLab webhook payloads into pipeline trigger API calls.

```
GitLab webhook (JSON)
  → Bridge Cloud Function
    → validates webhook secret (constant-time)
    → extracts event type and payload
    → base64-encodes payload (prevents YAML injection)
    → calls Pipeline Trigger API with ref=main (hardcoded)
  → .fullsend dispatch pipeline (protected main branch)
    → determines stage from event payload
    → triggers child pipeline via trigger: include: artifact:
    → child pipeline authenticates to token mint via GitLab OIDC
    → agent executes in sandbox
```

**Why a separate function, not co-deployed with the mint**: The bridge handles untrusted webhook payloads from the internet. If it has a vulnerability, compromise of a separate function gives the attacker only a pipeline trigger token. Co-deployed, a bridge exploit could reach the mint's Secret Manager access and all stored PEM keys.

**Infrastructure cost**: Unlike the token mint (which can be centralized), the bridge must be network-reachable from the GitLab instance that sends webhooks. For GitLab.com this is straightforward (public endpoint). For self-hosted GitLab instances behind VPNs, the bridge must be deployed adjacent to each instance — potentially requiring a separate bridge deployment per GitLab instance with distinct network configurations. This is a meaningful operational cost beyond "one more Cloud Function." Each bridge deployment needs its own monitoring, trigger tokens, and webhook token cache. Organizations with multiple self-hosted GitLab instances should weigh this against the pull-based alternative (Alternative 4).

### Tension with [ADR 0009](0009-pull-request-target-in-shim-workflows.md)

[ADR 0009](0009-pull-request-target-in-shim-workflows.md) rejected hosted webhook receivers for GitHub, citing "breaking compute-platform agnosticism." This ADR introduces a hosted webhook bridge for GitLab — a direct departure from that position. The key differences that make this acceptable:

1. **Precedent**: [ADR 0029](0029-central-token-mint-secretless-fullsend.md) subsequently introduced a hosted GCP Cloud Function (the token mint), establishing that fullsend already depends on hosted infrastructure. The bridge follows the same deployment pattern.
2. **Necessity**: GitLab lacks `pull_request_target`, so there is no in-platform equivalent to the GitHub shim workflow. A webhook intermediary is required to close the security gap, not merely convenient.
3. **Scope**: The bridge is scoped to GitLab only. GitHub continues to use the in-repo shim model from [ADR 0009](0009-pull-request-target-in-shim-workflows.md) — compute-platform agnosticism is preserved for the primary platform.

The per-instance cost is real but bounded — most organizations have one or two GitLab instances, not dozens.

### Defense in depth

Three independent layers prevent unauthorized pipeline execution:

1. **Bridge hardcodes `ref=main`**: The target ref is never derived from the webhook payload. Even if an attacker crafts a malicious webhook, the dispatch pipeline always runs on the protected default branch. This requires the `.fullsend` project to use `main` as its default branch — validated during `fullsend admin install`.

2. **Protected CI/CD variables**: All secrets in the `.fullsend` project (trigger tokens, role credentials) are marked as "protected." (Webhook tokens and PATs live in Secret Manager, not CI/CD variables.) GitLab only exposes protected variables to pipelines running on protected branches. If the bridge is compromised to call the trigger API with `ref=attacker-branch`, secrets are not exposed.

3. **Per-project webhook secret validation**: Each enrolled project has a unique webhook secret token, stored in Secret Manager (key: `fullsend-webhook-{escaped-group}--{project}`). The bridge Cloud Function retrieves the expected token from Secret Manager (TTL-cached) and validates the received token via constant-time comparison (`crypto/subtle.ConstantTimeCompare`) before triggering the dispatch pipeline. The pipeline itself only checks the `WEBHOOK_VALIDATED=true` flag set by the bridge — the raw secret never passes through the pipeline. (Note: earlier drafts proposed storing webhook secrets as CI/CD variables in `.fullsend`; the implementation plan uses Secret Manager instead, consistent with centralized credential management.)

### Token mint extension

The existing token mint ([ADR 0029](0029-central-token-mint-secretless-fullsend.md)) is extended with a credential backend abstraction:

- **`GitHubCredentialBackend`** (existing): OIDC → validate claims → retrieve PEM from Secret Manager → generate JWT → exchange for scoped GitHub App installation token.
- **`GitLabCredentialBackend`** (new): OIDC → validate claims → retrieve stored Project Access Token from Secret Manager → return token.

GitLab OIDC integration:

- GitLab CI jobs declare `id_tokens:` with audience `fullsend-mint`.
- A WIF provider for the GitLab OIDC issuer is added to the existing WIF pool.
- GitLab claim names are normalized to the mint's internal representation: `project_path` → `repository`, `namespace_path` → `repository_owner`.
- The mint validates the pipeline originated from the `.fullsend` project (or a registered per-repo project) on a protected ref.

### Credential model

GitLab Project Access Tokens (PATs) replace GitHub Apps:

- Created per-role per-project during `fullsend admin install` via GitLab API.
- Stored in Secret Manager with project-scoped naming: `fullsend-{group}--{project}--{role}-pat`. Including the project name is necessary because PATs are per-project (unlike GitHub App PEMs which are per-org).
- Stage-to-role mapping (matching GitHub's `dispatch.yml` canonical role names): triage → `triage` (Reporter), code|fix → `coder` (Developer), review → `review` (Developer), retro|prioritize → `fullsend` (Maintainer), fullsend orchestrator → `fullsend` (Maintainer). Note: the GitLab dispatch pipeline must translate stage names to canonical role names before minting — the mint uses `coder` not `code`, and retro/prioritize share the `fullsend` Maintainer role as on GitHub.
- **120-day creation expiry** with a **90-day rotation cadence** — the 30-day buffer prevents expiry if a scheduled rotation runs slightly late or fails once. `fullsend admin analyze` warns when any PAT is within 30 days of expiry. `fullsend admin rotate-tokens` performs rotation. Note: self-hosted GitLab administrators can configure `max_personal_access_token_lifetime` to a value shorter than 120 days; `fullsend admin install` must check this setting and either adjust the creation expiry accordingly or fail with a clear message if the instance constraint prevents the intended rotation buffer.

**Mint statefulness trade-off**: Unlike GitHub PEMs (one per org per role), GitLab PATs are per-project per role. This means the mint must store credentials that scale with the number of enrolled projects, making it less stateless than the GitHub model. This scaling cost is accepted because the mint provides centralized credential management, OIDC validation, and audit logging — benefits that outweigh the per-project storage overhead.

**PAT limitation**: GitLab PATs cannot be used to mint further scoped-down tokens — they are the final credential. The mint returns the PAT directly rather than exchanging it for a short-lived token. This means PAT scope must be carefully set at creation time (per-role mapping above). This is a security regression compared to GitHub's model where the mint generates short-lived, repo-scoped installation tokens from long-lived PEMs.

### GitLab dispatch pipeline

The `.fullsend` project's dispatch pipeline mirrors GitHub's `dispatch.yml`:

- Triggered by the bridge function via Pipeline Trigger API.
- Validates the source project is enrolled (config.yaml lookup).
- Determines the stage from the base64-decoded event payload (same routing logic as GitHub: issue events, note events, MR events, label events).
- Generates a child pipeline config that includes the matching stage file.
- Triggers the child pipeline via `trigger: include: artifact:` with `strategy: depend` (synchronous, preserving the run-correlation property from [ADR 0041](0041-synchronous-workflow-call-event-dispatch.md)).

### Forge abstraction evolution

New methods added to `forge.Client`:

```go
CreateWebhook(ctx, owner, repo, targetURL, secretToken string, events []string) (webhookID string, err error)
DeleteWebhook(ctx, owner, repo, webhookID string) error
CreateRoleCredential(ctx, owner, repo, roleName string, scopes []string, expiresAt time.Time) (credential string, credentialID string, err error)
RevokeRoleCredential(ctx, owner, repo, credentialID string) error
IsProtectedBranch(ctx, owner, repo, branch string) (bool, error)
```

`CreateRoleCredential`/`RevokeRoleCredential` abstract over both GitHub Apps and GitLab PATs. The GitHub implementation returns `forge.ErrNotSupported` since GitHub credentials are managed through the token mint's PEM-based flow, not through the forge interface.

GitHub-specific methods (`ListOrgInstallations`, `GetAppClientID`) move to an extension interface (`GitHubExtensions`). Callers that need them type-assert.

### Installation modes

Both per-org and per-repo modes are supported for GitLab:

- **Per-org**: `.fullsend` is a project within the GitLab group. Enrolled projects configure webhooks pointing to the bridge. The dispatch pipeline and agent pipelines run in `.fullsend`.
- **Per-repo**: `.fullsend/` directory lives within the target project. The webhook points to the bridge, which triggers the project's own pipeline on the protected default branch. The same `ref=main` constraint applies — per-repo target projects must use `main` as their default branch, validated during `fullsend admin install`.

### Platform requirements

- **Minimum GitLab version**: 17.0+ (CI/CD components maturity, stable `trigger:include:artifact:` support, and project access token / webhook API stability; note: OIDC `id_tokens:` was introduced in 15.7 but CI/CD components and other fullsend dependencies reached stable maturity in 17.0).
- **Self-hosted support**: Configurable instance URLs via `--gitlab-url` flag and `gitlab_instance_url` in config.yaml. The WIF pool needs per-instance OIDC providers.

## Consequences

### Positive

- Organizations on GitLab (both GitLab.com and self-hosted 17.0+) can adopt fullsend.
- Reuses the central token mint model ([ADR 0029](0029-central-token-mint-secretless-fullsend.md)) rather than introducing a parallel credential system.
- The same agent workflow (triage → code → review → fix → retro) works identically from the user's perspective.
- Webhook-based dispatch is architecturally cleaner than an in-repo shim for GitLab's security model.
- Forge abstraction ([ADR 0005](0005-forge-abstraction-layer.md)) is validated and strengthened by a second implementation.

### Negative

- Two CI/CD template sets to maintain (`.github/workflows/` and `.gitlab/ci/`).
- The bridge hardcodes `ref=main` — GitLab projects (`.fullsend` and per-repo targets) must use `main` as their default branch. Organizations using `master`, `develop`, or other branch names must rename before adopting fullsend.
- Project Access Tokens are less granular than GitHub Apps (role-level, not per-permission).
- PATs require rotation (120-day creation expiry, 90-day cadence; max 1-year allowed by GitLab); GitHub App private keys do not expire.
- The webhook bridge adds a Cloud Function to deploy and monitor.
- Post-scripts may need minor forge-specific branches where GitHub and GitLab APIs diverge (e.g., review semantics).
- **The token mint is no longer stateless for GitLab deployments**: GitLab PATs scale O(projects × roles) vs GitHub's O(orgs × roles). While the mint remains stateless for the GitHub model (org-scoped PEMs), GitLab support requires per-project credential storage in Secret Manager. This increases operational cost and complexity proportional to the number of enrolled GitLab projects.

### Risks

- **Protected branch misconfiguration**: If `.fullsend` project's default branch is not protected, MR authors could modify pipeline code. Mitigated by validation during install and protected CI/CD variables as defense-in-depth.
- **Bridge function availability**: If the bridge is down, no GitLab events are processed. Mitigated by Cloud Function auto-scaling and health monitoring.
- **GitLab API rate limits**: GitLab.com has lower rate limits than GitHub. Mitigated by exponential backoff and retry in the GitLab forge client.
- **Self-hosted GitLab version drift**: Wide version range among self-hosted instances. Mitigated by requiring 17.0+ and detecting version during install.
- **PAT compromise blast radius**: Unlike GitHub installation tokens (1-hour TTL, repo-scoped), a compromised GitLab PAT grants persistent role-level project access until expiry (up to 90 days with the enforced creation expiry). The exposure window is larger than GitHub's model.
- **WEBHOOK_VALIDATED bypass**: The `WEBHOOK_VALIDATED=true` flag is a convention, not a cryptographic guarantee. Anyone who obtains `FULLSEND_DISPATCH_TOKEN` (a protected CI/CD variable accessible to `.fullsend` Maintainers) can call the Pipeline Trigger API directly and set this flag, bypassing per-project webhook secret validation. This is an accepted risk: Maintainer trust already allows direct pipeline code modification. A stronger alternative — HMAC-signed payloads verified by the pipeline using a Secret Manager secret — would eliminate this bypass at the cost of implementation complexity.

### Mitigations

- **Protected branch validation**: `fullsend admin install` validates that `.fullsend` project's default branch is protected before enrollment; `fullsend admin analyze` re-checks on each run.
- **Protected CI/CD variables**: All secrets marked protected — only exposed to pipelines on protected branches, providing defense-in-depth if the bridge is compromised to trigger a non-`main` ref.
- **Per-project webhook secrets**: Unique per enrolled project, validated via constant-time comparison, preventing cross-project replay.
- **Cloud Function auto-scaling and health monitoring**: Reduces bridge unavailability risk.
- **Exponential backoff and retry**: GitLab forge client handles rate limits gracefully.
- **Version detection during install**: Enforces GitLab 17.0+ minimum, preventing version-drift issues.
- **PAT rotation automation**: 120-day expiry enforced at creation with 90-day rotation cadence (30-day buffer prevents expiry on late/failed rotation); `fullsend admin rotate-tokens` provides manual rotation. The default GitLab scaffold includes a scheduled pipeline in `.fullsend` that runs `fullsend admin rotate-tokens` on a 90-day cadence, ensuring organizations get automated rotation out of the box without manual setup. GitLab audit log alerting on unusual PAT usage patterns is recommended as an additional detection layer.
- **Per-role PAT scoping**: Each PAT is scoped to the minimum role required (Reporter for triage, Developer for code/review/fix, Maintainer for orchestrator), limiting blast radius of any single compromised token.
- **Secret Manager storage with IAM access controls**: PATs stored in Secret Manager rather than CI/CD variables, centralizing credential management and providing audit trails.

## Open Questions

### Mint role for GitLab PAT storage

**Concern raised during review**: The mint was designed to *generate* short-lived, scope-limited tokens (GitHub App installation tokens). For GitLab, it stores and returns pre-created PATs — it does not actually mint anything. This raises two issues: (1) the mint no longer provides scope-limiting, which was a core security guarantee; (2) each project enrollment requires writing a PAT into the shared mint's Secret Manager, which complicates the "public mint" goal of zero-touch onboarding.

**Current rationale**: The mint still provides OIDC claim validation (credentials only reach pipelines running `.fullsend` on a protected ref) and centralized audit logging. Without it, PATs would need to be stored as protected CI/CD variables in `.fullsend`, visible to all Maintainers. The mint-via-OIDC path avoids persisting credentials in CI/CD variables.

**Alternative worth exploring**: Use a privileged service account or group-level bot account to generate project-scoped, time-limited GitLab PATs *on demand* at mint time (analogous to how GitHub App private keys are used to generate repo-scoped installation tokens). If GitLab gains broader group-level access token support, this could restore the mint's stateless, token-generating model. **This alternative must be prototyped before the GitLab implementation reaches production.** If a group-level token approach is viable, it should replace the current PAT storage model — restoring on-demand, short-lived token generation and reducing the 90-day exposure window to hours (matching GitHub's model). If not viable (e.g., GitLab Free tier limitations), the current PAT storage model is accepted with the mitigations documented above.

**Decision criteria for revisiting**: If a privileged-account-based on-demand PAT generation approach is validated as feasible (group token API available on the target GitLab tier, adequate permission scoping), this ADR should be amended to adopt it.

## References

- [ADR 0005](0005-forge-abstraction-layer.md): Forge abstraction layer
- [ADR 0007](0007-per-role-github-apps.md): Per-role GitHub Apps (authentication model to replicate)
- [ADR 0009](0009-pull-request-target-in-shim-workflows.md): `pull_request_target` security model (problem to solve)
- [ADR 0028](0028-gitlab-support.md): GitLab Support Architecture (superseded by this ADR)
- [ADR 0029](0029-central-token-mint-secretless-fullsend.md): Central token mint
- [ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md): Reusable workflows
- [ADR 0033](0033-per-repo-installation-mode.md): Per-repo installation mode
- [ADR 0041](0041-synchronous-workflow-call-event-dispatch.md): Synchronous dispatch
- [GitLab support implementation details](../plans/gitlab-support.md): Companion implementation document
