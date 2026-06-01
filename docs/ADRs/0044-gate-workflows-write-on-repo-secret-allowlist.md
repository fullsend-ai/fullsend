---
title: "44. Gate coder and fix workflows:write on target-repo secret allowlist"
status: Accepted
relates_to:
  - security-threat-model
  - agent-infrastructure
topics:
  - security
  - github-actions
  - secrets
  - coder-agent
  - fix-agent
  - triage-agent
---

# 44. Gate coder and fix workflows:write on target-repo secret allowlist

Date: 2026-06-01

## Status

Accepted

## Context

Granting the coder GitHub App `workflows: write` unblocks legitimate workflow edits ([#470](https://github.com/fullsend-ai/fullsend/issues/470)) but enables a prompt-injection path: push a new workflow with `on: push` on an agent branch, run immediately, and read secrets via `secrets.*` (and abuse broad `GITHUB_TOKEN` defaults) — without merge or review ([#788](https://github.com/fullsend-ai/fullsend/issues/788)).

`contents: write` alone does **not** allow creating `.github/workflows/` files; GitHub rejects those pushes without `workflows` permission. The new risk is tied specifically to granting `workflows: write`.

Minimal privilege: the **code** workflow runs **assessment** and requests `workflows: write` only when the issue has `fullsend-needs-workflow-write`. The **fix** workflow never assesses; it requests elevation only when the PR has `fullsend-workflow-write` from the coder (after assessment) or from an authorized human.

**Prerequisite:** Implementation is blocked until [ADR 41](0041-synchronous-workflow-call-event-dispatch.md) is accepted and landed.

## Options

### Option A: Full repo-readiness assessment (issue #788)

Comprehensive checks before any elevated permission. Correct but large.

### Option B: Issue intent label + target-repo workflow exposure assess (recommended)

Triage or human marks the issue; code inventories workflow-visible secrets on the target repo; fix trusts code’s outcome on the PR.

### Option C: Permanent denial of workflows:write

Safest but leaves #470 unresolved.

## Decision

Adopt **Option B**.

### Labels (two roles)

| Label | On | Meaning | Who may set (provenance) |
|-------|-----|---------|---------------------------|
| `fullsend-needs-workflow-write` | **Issue** | Implementation will need `.github/workflows/` (or related) edits | `{org}-triage[bot]` (via triage `label_actions`) or human with **OWNER \| MEMBER \| COLLABORATOR** (same `is_authorized()` rule as `/fs-fix` in per-org `dispatch.yml`; see [ADR 0034](0034-centralized-shim-routing-via-dispatch.md)) |
| `fullsend-workflow-write` | **PR** | Fix (and subsequent code pushes on this PR) may mint with `workflows: write` | **`{org}-coder[bot]`** after code passes assessment, **or** human with **OWNER \| MEMBER \| COLLABORATOR** (e.g. workflow edits were not foreseen by triage or the first code run) |

### What assessment means

Assessment answers: *if the coder agent adds a workflow on `source_repo` that runs on push to an agent branch, what credentials could that workflow obtain?*

The assess step runs in the **code** workflow (not fix, not mint). It queries the **target repo** (`source_repo`) using APIs that list **names only** (never values). The inventory must match what **workflows executing in that repository’s Actions context** can reach — the same surfaces available via `${{ secrets.* }}` and default `GITHUB_TOKEN` policy for repo-hosted workflows, not secrets injected only into the `.fullsend` caller via `workflow_call`.

**In scope — enumerate and allowlist-check:**

1. **Repository Actions secrets** — `GET /repos/{owner}/{repo}/actions/secrets`
2. **Organization Actions secrets visible to the repository** — `GET /repos/{owner}/{repo}/actions/organization-secrets`
3. **Environment Actions secrets** — for each environment on the repository, `GET .../environments/{name}/secrets`. Include an environment if a push-triggered workflow on a non-default branch could reference it (respect deployment-branch policies; **fail closed** when policies are permissive and the environment holds secrets not on the allowlist).
4. **Default `GITHUB_TOKEN` permissions for workflows** — `GET /repos/{owner}/{repo}/actions/permissions/workflow`. Assessment fails unless defaults are read-only for contents and packages (and do not grant write-all). This blocks elevation when a rogue workflow could exfiltrate via the token even with no named secrets.

**Allowlist:** Every secret name from (1)–(3) must be in builtins ∪ configurable allowlist. (4) is a separate boolean gate, not a name list.

**Builtin secret names (hardcoded):** `FULLSEND_GCP_WIF_PROVIDER`, `FULLSEND_GCP_PROJECT_ID`, `FULLSEND_DISPATCH_TOKEN`.

**Configurable allowlist:** explicit names in `config.yaml` — per-org `repos.<repo>.workflow_secret_allowlist` ([ADR 0011](0011-admin-install-org-config-yaml-v1.md)) or per-repo `workflow_secret_allowlist` ([ADR 0033](0033-per-repo-installation-mode.md)). No prefix wildcards.

**Gate:** `grant_workflows = true` only when (1)–(3) pass the allowlist **and** (4) passes. On success, mint with `workflows: write` and add `fullsend-workflow-write` on the PR. On failure, mint without it; workflow output names unexpected secrets and/or which non-read-only `GITHUB_TOKEN` default blocked the grant.

### When assessment runs

- **Code (`reusable-code.yml`):** Only if the issue has `fullsend-needs-workflow-write` with trusted provenance (issue `labeled` timeline). Otherwise skip assessment and mint without `workflows: write`.
- **Triage:** Add `fullsend-needs-workflow-write` in `label_actions` when workflow file changes are required; update triage agent guidance. Do not add it for application-code-only issues.
- **Human override (issue):** Authorized user adds `fullsend-needs-workflow-write` on the issue, then `/fs-code` (runs assessment before elevation).
- **Human override (PR):** Authorized user adds `fullsend-workflow-write` on the PR so a **fix** run can push workflow changes when triage or code did not anticipate them. This does **not** re-run assessment; the human accepts repo secret exposure risk for that PR.
- **Fix (`reusable-fix.yml`):** Never assess. `grant_workflows = true` only if the PR has `fullsend-workflow-write` with trusted provenance (`{org}-coder[bot]` or authorized human via PR `labeled` timeline).

### Mint and timing

Assessment policy lives in workflows, **not** in mint. Mint accepts `grant_workflows` from the caller.

**Do not implement until ADR 41 is merged.** Touch triage post-script/agent, assess script/composite, `reusable-code.yml`, `reusable-fix.yml`, mint/coder API permissions as needed, and `config.yaml` v1.

## Consequences

- Most code runs never request `workflows: write`; only labeled issues enter assessment.
- Assessment covers repo, org, and environment secrets plus default `GITHUB_TOKEN` posture — omitting any surface would under-estimate exposure.
- Repos with broad CI or environment secrets fail even when triage requested workflows; output should list blockers.
- Fix inherits elevation from a prior successful code assessment on the PR, or from an authorized human who applied `fullsend-workflow-write` on the PR (no assess on that path).
- Depends on ADR 41 before implementation.
