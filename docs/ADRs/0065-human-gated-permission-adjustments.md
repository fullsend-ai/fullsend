---
title: "65. Human-gated permission adjustments for agent tokens"
status: Accepted
relates_to:
  - security-threat-model
  - agent-architecture
topics:
  - authorization
  - permissions
  - mint
  - workflows
---

# 65. Human-gated permission adjustments for agent tokens

Date: 2026-07-01

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

Supersedes the automated secret-inventory gating approach proposed in
[#1739](https://github.com/fullsend-ai/fullsend/issues/1739).

## Context

The code agent cannot push `.github/workflows/` changes because
`workflows: write` is intentionally withheld from default coder tokens
([ADR 0007](0007-per-role-github-apps.md),
[ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)). This blocks
[#470](https://github.com/fullsend-ai/fullsend/issues/470). The problem
applies to any permission that is too dangerous for routine agent runs but
sometimes required for legitimate work.

An earlier approach
([#1739](https://github.com/fullsend-ai/fullsend/issues/1739)) proposed
automated secret-inventory validation as the gate for `workflows: write` —
the agent would scan the repository and only receive elevated permissions if
no secrets were detected. This was abandoned because automated readiness
checks cannot substitute for human judgment about when elevated access is
appropriate, and because the threat model
([security-threat-model.md](../problems/security-threat-model.md)) prioritizes
external injection over automated heuristic bypass.

[PR #2548](https://github.com/fullsend-ai/fullsend/pull/2548) prototyped a
label-based human-authorization flow with a layered design. It was closed
without merging after
[#2389](https://github.com/fullsend-ai/fullsend/pull/2389) moved token
minting into the binary, changing the integration surface. This ADR records
the design decisions from that work so implementation can proceed.

## Decision

Permission adjustments use a three-layer design where each layer has a
single responsibility:

### Layer 1: Policy (CLI)

The `fullsend` CLI owns all policy decisions about when a permission
adjustment is allowed. Policy inputs are deterministic forge state — labels,
collaborator status, comment timestamps — not LLM output.

For the initial `workflow-change` gate:

- **`workflow-change-needed`** — triage signals anticipated workflow edits.
  When set without authorization, `ready-to-code` is withheld.
- **`workflow-change-allowed`** — a repository collaborator explicitly
  authorizes the elevation.

The CLI verifies labels, checks that the authorizing user is a collaborator,
and invalidates stale authorization (e.g., when an agent-influencing comment
appears after the authorization label).

`fullsend auth check` runs at three phases:

| Phase | When | Outcome if not authorized |
|-------|------|----|
| `mint` | Before token mint | Token minted without elevation |
| `pre-run` | Before sandbox creation | Agent run skipped |
| `pre-push` | Before git push | Push blocked; `workflow-change-needed` applied |

### Layer 2: Mint (mechanical)

The mint service applies caller-requested permission adjustments
mechanically on top of a role baseline. When the workflow passes
`elevations: ["workflow-change"]` after a successful CLI auth check,
mint merges the gate's permissions (e.g., `workflows: write`) into the
token. The role's GitHub App manifest includes elevated permissions as
a **ceiling**; mint grants them only when explicitly requested.

Mint does not read issues, labels, or comments. It does not make policy
decisions. It trusts that the caller (an enrolled workflow verified by
OIDC audience and `job_workflow_ref`) has already performed authorization.

### Layer 3: Automation (privileged operations)

Privileged git and GitHub operations — pushing branches, creating PRs,
applying labels — remain in the deterministic post-script on the runner,
never in the LLM sandbox. The elevated token is available only to the
post-script; the sandbox agent never holds write-capable credentials
([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)).

### Threat model

Three vectors are addressed, ordered by the project's threat priority
([security-threat-model.md](../problems/security-threat-model.md)):

1. **TOCTOU between authorization and token mint.** A gap between when
   a human authorizes elevation and when the token is minted could allow
   conditions to change (e.g., a malicious comment injected between
   label application and the mint call). Mitigated by performing the
   CLI auth check and mint request in a single workflow invocation —
   the check and the mint are adjacent steps in the same job, not
   separate events.

2. **Post-grant prompt injection expanding scope.** After authorization,
   the agent might be influenced (via injected content in issues,
   comments, or code) to believe it needs broader access than authorized.
   Mitigated by using deterministic gate inputs (labels, collaborator
   API) rather than agent tool discovery or LLM reasoning to decide
   what permissions are needed. The agent does not choose its own
   permissions.

3. **Post-grant injection via privileged tools.** Even with an elevated
   token, a compromised agent could misuse `workflows: write` to deploy
   arbitrary workflow files. Mitigated by keeping write-capable tokens
   out of the LLM sandbox entirely
   ([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md),
   [ADR 0032](0032-safe-push-wrapper-for-sandboxed-agents.md)). The
   elevated token is used only by the post-script, which applies its
   own protected-path checks and review gates before pushing.

### Design decisions

- **Human authorization over automated gating.** Automated repo-readiness
  checks ([#788](https://github.com/fullsend-ai/fullsend/issues/788))
  may remain as optional advisory signals but are not a blocker for
  elevation. The authorization decision is a human collaborator applying
  a label.
- **Reusable gate pattern.** The `workflow-change` gate is the first
  instance of a general `authorization.Gate` registry. Future elevated
  permissions (e.g., `admin` scope for CI config changes) reuse the
  same label + CLI + elevations pattern without mint changes.
- **The automated secret-inventory approach from
  [#1739](https://github.com/fullsend-ai/fullsend/issues/1739) is
  superseded.** Human judgment replaces automated heuristics for
  elevation decisions.

## Consequences

- Unblocks [#470](https://github.com/fullsend-ai/fullsend/issues/470):
  agents can push workflow changes when a collaborator authorizes it.
- Default agent tokens remain unchanged — no org-wide `workflows: write`
  on every code run.
- Orgs must approve updated coder app permissions (adding `workflows: write`
  as a ceiling) in GitHub org settings; `fullsend admin` surfaces
  stale-permission warnings.
- Future elevated permissions reuse the same gate + CLI + elevations
  pattern without modifying mint logic.
- Defense in depth: label gate (CLI) + mint downscoping + credential
  isolation (sandbox never holds write token) + post-script
  protected-path checks + review agent protected-path rules.
