---
title: "74. Human-gated permission adjustments"
status: Accepted
relates_to:
  - security-threat-model
  - agent-architecture
topics:
  - authorization
  - mint
  - least-privilege
  - workflows
---

# 74. Human-gated permission adjustments

Date: 2026-07-26

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

Supersedes the draft approach from [#1739](https://github.com/fullsend-ai/fullsend/issues/1739) (automated secret-inventory gating for `workflows: write`).

## Context

The code agent cannot push `.github/workflows/` changes because `workflows: write` is intentionally withheld from default agent tokens ([ADR 0007](0007-per-role-github-apps.md), [#470](https://github.com/fullsend-ai/fullsend/issues/470)). [ADR 0073](0073-named-mint-privilege-levels.md) provides the general mechanism for minting tokens at differentiated privilege levels within a role. This ADR decides the authorization path for when and how an agent run receives elevated permissions.

An earlier approach ([#1739](https://github.com/fullsend-ai/fullsend/issues/1739)) proposed gating `workflows: write` on automated secret-inventory validation — the system would scan the repo for readiness before granting elevated tokens. That path was abandoned: the threat model prioritizes external injection over insider abuse, and an automated readiness check introduces a machine-evaluable gate that an attacker could satisfy without human awareness.

## Decision

### Layer boundaries

Three layers participate in permission adjustment, each with a single responsibility:

1. **CLI policy layer** (`fullsend auth check`). Determines *whether* an elevation is authorized by inspecting forge-native signals: labels on the issue or PR, collaborator status of the authorizing user, and staleness (whether a non-collaborator agent-influencing comment has occurred since authorization). The CLI posts sticky comments on denial and applies labels on detection. This layer runs in enrolled workflows and post-scripts — deterministic automation on the runner, outside the LLM sandbox.

2. **Mint mechanics layer**. Applies caller-requested permission adjustments mechanically on top of the role's baseline privilege level ([ADR 0073](0073-named-mint-privilege-levels.md)). The mint accepts an optional elevation request in the token mint API and merges the gate's additional permissions into the level's permission set. The mint does not read issues, labels, or comments — it trusts the caller (the enrolled workflow) to have verified authorization.

3. **Automation layer**. Privileged git and GitHub operations — pushing branches that include workflow file changes — execute in the deterministic post-script on the runner, never in the LLM sandbox. The sandbox receives a token scoped to the harness `privilege_levels` configuration; the post-script receives the elevated token only after the CLI policy layer confirms authorization at the `pre-push` phase.

### Threat model

Three vectors specific to permission elevation:

| Threat | Mitigation |
|--------|------------|
| **TOCTOU between authorization and token mint.** A label is applied, then removed or invalidated before the mint call executes. | Authorization check and mint request occur in a single CLI invocation within the enrolled workflow. The window is bounded by a single workflow step, not by human reaction time. |
| **Post-grant prompt injection expanding scope.** After a human authorizes `workflow-change-allowed`, a malicious comment could instruct the agent to believe it needs broader changes than authorized. | The gate inputs are deterministic (label presence, collaborator status, changed-file list). The agent's beliefs about scope are irrelevant — the post-script's `pre-push` check validates that changed files match the gate's scope (`.github/workflows/`), and the agent never selects or requests its own elevation. |
| **Post-grant prompt injection via privileged tools.** If an elevated token entered the sandbox, a compromised agent could use `workflows: write` to deploy arbitrary workflow files. | Write-capable tokens never enter the LLM sandbox. The sandbox receives the `runtime` privilege level (typically `read` per [ADR 0073](0073-named-mint-privilege-levels.md)). Elevated tokens exist only in the post-script's process environment, applied by deterministic code that the agent cannot influence. |

### Authorization flow

The first gate is `workflow-change`:

- **`workflow-change-needed`** — applied by triage when it identifies work requiring `.github/workflows/` edits. Triage's post-script withholds `ready-to-code` until authorization is granted.
- **`workflow-change-allowed`** — applied manually by a repository collaborator, authorizing the code agent to receive an elevated token for that run.

After `workflow-change-allowed` is applied, a subsequent non-collaborator agent-influencing comment (e.g., `/fs-code` from a non-collaborator) invalidates the authorization (stale check). A collaborator must re-authorize.

### Design choice: human authorization over automated gating

The earlier approach ([#1739](https://github.com/fullsend-ai/fullsend/issues/1739)) used automated repo-readiness checks (secret inventory validation) as the gate. This ADR chooses explicit human authorization instead:

- Automated gates can be satisfied by an attacker who understands the check criteria. Human authorization requires social engineering of a specific collaborator, which is a higher bar.
- [#788](https://github.com/fullsend-ai/fullsend/issues/788) (automated repo-readiness checks) may remain as optional advisory tooling — surfacing warnings to the authorizing human — but is not a blocking gate.

## Consequences

- Agents can push `.github/workflows/` changes when a collaborator explicitly authorizes the run, unblocking [#470](https://github.com/fullsend-ai/fullsend/issues/470).
- Existing agent runs are unaffected — without `workflow-change-allowed`, tokens are minted at the default privilege level with no `workflows: write`.
- The gate pattern (`auth check` CLI + label signals + mint elevations) is reusable for future permission adjustments beyond `workflows: write`.
- Implementation requires changes in the mint ([#2823](https://github.com/fullsend-ai/fullsend/issues/2823)), CLI (`fullsend auth check` command), enrolled workflows, and post-scripts. These should not begin until this ADR is accepted.
- `docs/architecture.md` is updated to reflect the authorization gate pattern.
