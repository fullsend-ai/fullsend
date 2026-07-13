---
title: "28. GitLab Support Architecture"
status: Deprecated
relates_to:
  - agent-infrastructure
  - agent-architecture
topics:
  - gitlab
  - forge
  - ci-cd
  - multi-platform
---

# 28. GitLab Support Architecture

Date: 2026-04-29

## Status

Deprecated — the harness-level forge-specific vs. forge-neutral split
is now addressed by [ADR 0045](0045-forge-portable-harness-schema.md)
(forge-portable harness schema). The webhook bridge approach described
here is superseded by [ADR 0067](0067-gitlab-cron-polling-event-dispatch.md)
(cron-polling event dispatch), which eliminates webhooks entirely. The
broader GitLab support architecture (CI/CD pipeline mapping, PAT-based
auth) documented here remains reference material.

## Context

Fullsend currently supports GitHub exclusively, using GitHub-specific primitives throughout the agent pipeline:
- GitHub Actions workflows for CI/CD orchestration
- GitHub Apps with fine-grained per-role permissions for authentication
- `pull_request_target` trigger for secure event handling
- `workflow_dispatch` API for cross-repository workflow triggers
- GitHub labels as state machine
- Org-level Actions secrets with repository visibility controls

Organizations using GitLab (self-hosted or GitLab.com) cannot adopt fullsend. Adding GitLab support requires:
1. Mapping GitHub primitives to GitLab equivalents
2. Maintaining security properties (untrusted MR code cannot access secrets)
3. Preserving the same agent workflow (triage → code → review → fix)
4. Keeping the architecture parallel where possible to minimize divergence

The `forge.Client` abstraction (ADR-0005) was designed for this: all forge-specific operations are isolated, making GitLab support an implementation of the interface rather than a rewrite of core logic.

## Options

### Alternative 1: GitLab CI/CD Templates at Root

Instead of `.gitlab/ci/*.yml`, use single `.gitlab-ci.yml` with includes.

**Rejected**: Less organized than GitHub's `.github/workflows/` pattern, harder to scan for stage markers.

### Alternative 2: Group Access Tokens Instead of Project Access Tokens

Use group-level tokens for all roles instead of project-level.

**Rejected**: Less secure (group-wide permissions), harder to scope per-repo. Project Access Tokens better match GitHub Apps model.

### Alternative 3: Service Accounts with Personal Access Tokens

Create GitLab user accounts for each role (fullsend-triage, fullsend-code, etc.) and use their PATs.

**Rejected**: Requires managing user accounts, consumes user licenses, PATs are user-scoped not project-scoped. Project Access Tokens are purpose-built for automation.

### Alternative 4: Unified `.fullsend-ci.yml` Format

Define a forge-neutral CI/CD format that compiles to GitHub Actions or GitLab CI.

**Rejected**: Adds complexity, requires custom compiler, loses ability to use forge-native features. Better to maintain parallel templates that map proven GitHub patterns to GitLab.

## Decision

### High-Level Architecture

GitLab support mirrors the GitHub architecture where primitives map cleanly, and adapts where GitLab's model differs. The config repo convention remains `<group>/.fullsend` (GitLab groups are equivalent to GitHub orgs).

### 1. Directory Structure

**GitHub**: `.github/workflows/*.yml`
**GitLab**: `.gitlab/ci/*.yml`

GitLab allows organizing CI/CD files in subdirectories via `include:`. The `.fullsend` config repo uses:

```
.fullsend/
  .gitlab/
    ci/
      dispatch.yml          # Main dispatcher
      triage.yml           # fullsend-stage: triage
      code.yml             # fullsend-stage: code
      review.yml           # fullsend-stage: review
      fix.yml              # fullsend-stage: fix
  templates/
    shim-pipeline.yml      # Template for enrolled repos
```

**Rationale**: GitLab supports both `.gitlab-ci.yml` at root and `.gitlab/ci/*.yml` via includes. The subdirectory approach keeps the config repo organized and parallel to GitHub's `.github/workflows/` structure.

### 2. CI/CD Pipeline Architecture

**GitHub**: Workflows triggered by events (issues, pull_request_target, issue_comment, pull_request_review)
**GitLab**: Pipelines triggered by events (issues, merge_requests, notes) with `workflow:rules`

Each stage workflow (triage, code, review, fix) is a separate `.gitlab/ci/*.yml` file with a `# fullsend-stage: <name>` comment marker (same pattern as GitHub).

**Dispatch pattern**: The `dispatch.yml` pipeline:
1. Receives trigger API call from enrolled repos
2. Scans `.gitlab/ci/*.yml` files for `# fullsend-stage:` markers
3. Uses GitLab's downstream pipeline API to trigger matching stage pipelines
4. Passes event payload and context via pipeline variables

**Key difference from GitHub**: GitLab uses parent/child pipeline relationships and pipeline trigger tokens instead of `workflow_dispatch`. The dispatch pipeline triggers child pipelines via `trigger:` keyword or API calls.

### 3. Authentication Model

**GitHub**: Per-role GitHub Apps with fine-grained repository permissions
**GitLab**: Per-role Project Access Tokens with role-based permissions

GitLab doesn't have an exact GitHub Apps equivalent, but Project Access Tokens (PATs) provide similar functionality:
- Scoped to specific projects (not user-based)
- Support role-based permissions (Guest, Reporter, Developer, Maintainer)
- Can be created programmatically via GitLab API
- Expire after configurable period (max 1 year, renewable)

**Role mapping**:
| Role    | GitLab Permission | Capabilities |
|---------|------------------|--------------|
| fullsend (orchestrator) | Maintainer | Read/write .fullsend config repo, trigger pipelines, manage project access tokens |
| triage  | Reporter | Read target repos, comment on issues |
| code    | Developer | Read/write target repos, create MRs, push branches |
| review  | Developer | Read repos, create MR reviews/comments |
| fix     | Developer | Read/write target repos, push to MR branches |

**Storage**: Project Access Token values stored as CI/CD variables:
- Project-level **masked and protected** variable in `.fullsend`: `FULLSEND_DISPATCH_TOKEN` (used to trigger child pipelines; never exposed to enrolled repos)
- Project-level **masked and protected** variables in `.fullsend`:
  - `FULLSEND_TRIAGE_TOKEN`
  - `FULLSEND_CODE_TOKEN`
  - `FULLSEND_REVIEW_TOKEN`
  - `FULLSEND_FIX_TOKEN`

**CRITICAL SECURITY REQUIREMENT**: All variables containing secrets MUST be marked as "protected" in GitLab (in addition to "masked"). Protected variables are only exposed to pipelines running on protected branches. This is the primary defense-in-depth control ensuring that if a pipeline is somehow triggered on a non-protected branch (via misconfiguration, intermediary compromise, or insider attack), secrets cannot be exfiltrated. Without "protected" status, an attacker with write access to `.fullsend` could create a malicious branch and exfiltrate all secrets.

**Limitations vs GitHub Apps**:
- No installation flow (tokens created via API, no OAuth redirect)
- Less granular permissions (e.g., can't grant "issues:write but not code:write")
- Mandatory rotation (GitLab PATs expire after max 1 year; GitHub App private keys never expire, though installation tokens have 1-hour TTL and auto-refresh)
- No per-permission scoping within a role (e.g., Developer can push and approve, can't separate)

**Alternative considered**: OAuth Applications. Rejected because they're user-scoped (not project-scoped) and require user interaction, similar to GitHub App manifest flow but less suitable for automation.

### 4. Event Handling and Webhook Architecture

**GitHub**: GitHub Actions natively supports event-driven workflows triggered by issue events, issue comments, pull request reviews, etc. The `pull_request_target` event provides both secure event handling (runs base branch code) and native event dispatch.

**GitLab**: GitLab CI/CD pipelines do not have native support for issue events, issue comment events, or merge request comment events as pipeline triggers. The `CI_PIPELINE_SOURCE` variable supports 15 values (`push`, `web`, `trigger`, `schedule`, etc.), but **none cover issue events, notes (comments), or MR review events**.

This creates an architectural gap: GitLab webhooks can fire on these events (issues, notes, merge_requests), but there is no native way to trigger a GitLab CI/CD pipeline in response. GitLab webhooks deliver JSON event payloads, while the pipeline trigger API (`/api/v4/projects/:id/trigger/pipeline`) expects form-encoded parameters. **These are not wire-compatible** — pointing a webhook URL directly at the trigger endpoint results in a malformed request.

**Solution**: An intermediary webhook-to-trigger translation layer is required. This intermediary:
1. Receives GitLab webhook payloads (JSON)
2. Validates webhook secret tokens
3. Translates event payloads to trigger API parameters
4. Calls the `.fullsend` trigger API with `ref=main` (hardcoded, never from payload)

**Architectural Options**:
1. **GitLab CI/CD webhook job** (in enrolled repo): Reintroduces security concerns — cannot enforce protected-branch-only execution without blocking MR event reactions entirely
2. **GitLab serverless functions**: Maintains compute-platform agnosticism but requires GitLab Premium/Ultimate tier
3. **Minimal external bridge service**: Works on GitLab Free tier but introduces hosted webhook receiver (conflicts with compute-platform agnosticism goal from ADR-0009)

**Decision**: This architectural constraint is documented in the Open Questions section below. For production deployment, either option 2 (GitLab Premium/Ultimate tier) or option 3 (external bridge with documented trade-offs) must be chosen. The webhook translation requirement is a fundamental difference from GitHub's native event-to-workflow dispatch model.

**Key security property**: The intermediary MUST hardcode `ref=main` when calling the trigger API. It MUST NOT derive the ref from webhook payload fields, as this would allow an attacker to trigger arbitrary branches in `.fullsend`. Protected CI/CD variables (see Authentication Model above) provide defense-in-depth if this control fails.

### 5. Agent Execution Environment (Out of Scope)

**Explicitly scoped out**: This ADR does not specify how fullsend agents (OpenShell, etc.) execute on GitLab runners. Topics including runner executor types (docker, kubernetes, shell), isolation models, runner registration requirements, and OpenShell integration specifics are **orthogonal to the CI/CD dispatch architecture** and will be addressed in a future ADR or agent infrastructure design document.

**Assumption**: Agents will execute in isolated environments (containers or VMs) managed by GitLab runners, similar to the current GitHub Actions model. The dispatch pipelines (covered in this ADR) trigger agent jobs; the agent sandbox and compute architecture are implementation-specific and follow the same principles regardless of forge.

**Rationale for scoping out**: The webhook dispatch architecture, authentication model, and event handling decisions can be made independently of the agent execution environment. Combining both topics would create an overly broad ADR that conflates pipeline orchestration with compute isolation.

### 6. Forge Abstraction Compliance

**ADR-0005 Promise**: "Adding a new forge requires implementing `forge.Client` — no changes to layers, CLI, or app setup code."

**Challenge**: The current `forge.Client` interface contains GitHub-specific methods (`ListOrgInstallations`, `GetAppClientID`, `DispatchWorkflow`) that do not map to GitLab. Implementing GitLab support without extending the interface would violate ADR-0005's abstraction boundary.

**Solution**: Extend `forge.Client` with **forge-neutral** methods that abstract over GitHub and GitLab primitives:

```go
// Credential management (abstracts GitHub Apps and GitLab Project Access Tokens)
CreateRoleCredential(ctx context.Context, role, owner, repo string, permissions []string) (credentialID string, err error)
RevokeRoleCredential(ctx context.Context, owner, repo, credentialID string) error

// Pipeline triggering (abstracts workflow_dispatch and trigger API)
TriggerPipeline(ctx context.Context, owner, repo, stage string, variables map[string]string) error

// Webhook management (GitLab-specific but exposed as optional interface method)
CreateWebhook(ctx context.Context, owner, repo, targetURL, secretToken string, events []string) error
```

**Minimizing Changes to Layers/CLI/Appsetup**:
- `appsetup`: Calls `forge.CreateRoleCredential()` instead of GitHub App-specific code
- `layers/workflows`: Calls `forge.GetTemplateDirectory()` to retrieve `.github/` or `.gitlab/` path (pushes forge-specific logic into Client implementation)
- `layers/enrollment`: Calls `forge.GetEnrollmentSnippet()` for shim workflow syntax (pushes forge-specific logic into Client implementation)
- `CLI`: Calls `forge.DetectForge(repoURL)` (detection logic moved to `internal/forge/detect.go` per ADR-0005 boundary rule)

**Compliance Result**: Changes to layers/CLI/appsetup are limited to **calling new forge-neutral interface methods**. The bulk of GitLab-specific logic lives in `internal/forge/gitlab/gitlab.go`, preserving ADR-0005's abstraction boundary.

See [Forge Interface Evolution](../problems/gitlab-implementation.md#forge-interface-evolution) in the implementation document for detailed method signatures and migration strategy.

## Implementation Details

Detailed implementation guidance has been moved to [docs/problems/gitlab-implementation.md](../problems/gitlab-implementation.md), including:

- Shim pipeline security (webhook-based architecture)
- Cross-repo dispatch mechanism (child pipelines, trigger API)
- Stage markers, event mapping, state machine primitives
- Implementation phases and rollout plan
- Forge interface evolution (`CreateRoleCredential`, `TriggerPipeline`, `CreateWebhook`)
- CLI changes and config schema updates
- Security considerations (protected branches, token scoping, webhook validation)

The implementation document is structured for iterative evolution as GitLab support progresses from design to production.

## Consequences

### Positive

- **Multi-forge support**: Organizations on GitLab can adopt fullsend
- **Forge abstraction strengthened**: Implementing GitLab reveals areas where the interface needs to evolve (credential management, pipeline triggering) and validates that forge-specific operations can be pushed into the Client implementation per ADR-0005
- **ADR-0005 compliance**: Changes to layers/CLI/appsetup are minimized by adding forge-neutral interface methods (`CreateRoleCredential`, `TriggerPipeline`) rather than adding conditional logic
- **Parallel architecture**: GitLab implementation closely mirrors GitHub, reducing cognitive load
- **Same workflow**: Triage → Code → Review → Fix stages work identically from user perspective

### Negative

- **Increased maintenance**: Two CI/CD template sets to maintain (`.github/` and `.gitlab/`)
- **Authentication complexity**: Project Access Tokens less capable than GitHub Apps, require rotation
- **Security model differences**: No `pull_request_target` equivalent requires careful protected branch configuration
- **Feature parity gaps**: Some GitHub features may not map perfectly (e.g., fine-grained permissions)
- **Testing overhead**: Need GitLab instance for E2E tests (self-hosted or GitLab.com)

### Risks

- **Protected branch misconfiguration**: If GitLab project doesn't protect `main`, MR authors could modify shim
- **Token expiration**: Project Access Tokens expire (max 1 year), need renewal automation
- **API rate limits**: GitLab.com has lower rate limits than GitHub, may need request throttling
- **Self-hosted GitLab versions**: Wide version range, feature availability varies

### Mitigations

- **Validation during install**: CLI checks that target branch is protected before enrolling repos
- **Token expiration monitoring**: Warn 30 days before expiration, provide renewal command
- **Rate limit handling**: Exponential backoff + retry in GitLab client
- **Version detection**: CLI detects GitLab version, warns about unsupported versions


## Open Questions

### Webhook-to-Trigger Translation Architecture

**Problem**: GitLab webhooks (JSON payloads) and the pipeline trigger API (form-encoded parameters) are not wire-compatible. An intermediary is required to translate webhook events to trigger API calls.

**Trade-offs**:
- **Option 1 (CI/CD webhook integration)**: Runs in enrolled repo, but cannot enforce protected-branch-only execution without blocking MR reactions entirely. Reintroduces security concern.
- **Option 2 (GitLab serverless functions)**: Keeps compute within GitLab infrastructure, but requires GitLab Premium/Ultimate tier.
- **Option 3 (Minimal bridge service)**: Works on GitLab Free tier, but reintroduces hosted webhook receiver concern from ADR-0009.

**Decision needed**: Choose between infrastructure cost (options 2/3) and security model compromise (option 1). For GitLab Free tier, option 3 appears to be the only viable path. This question should be resolved before production deployment.

### ADR Scope and Structure

**Resolved**: Implementation details have been extracted to [docs/problems/gitlab-implementation.md](../problems/gitlab-implementation.md). The ADR now focuses on the architectural decision (context, options, rationale, consequences) while the implementation document contains evolving details about security mechanisms, pipeline configurations, forge interface evolution, and rollout phases. This aligns with CLAUDE.md's guidance that problem-oriented documents handle evolving design while ADRs record decisions.

## References

- ADR-0005: Forge abstraction layer
- ADR-0007: Per-role GitHub Apps (authentication model to replicate)
- ADR-0008: workflow_dispatch for cross-repo dispatch (pattern to replicate with triggers)
- ADR-0009: pull_request_target security model (challenge to solve)
- GitLab CI/CD documentation: https://docs.gitlab.com/ee/ci/
- GitLab Project Access Tokens: https://docs.gitlab.com/ee/user/project/settings/project_access_tokens.html
- GitLab Pipeline Triggers: https://docs.gitlab.com/ee/ci/triggers/
