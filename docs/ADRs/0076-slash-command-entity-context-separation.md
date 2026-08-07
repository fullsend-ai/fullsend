---
title: "76. Slash command entity-context separation"
status: Accepted
relates_to:
  - agent-architecture
topics:
  - slash-commands
  - dispatch
  - routing
---

# 76. Slash command entity-context separation

Date: 2026-07-28

## Status

Accepted

Refines [ADR 0002](0002-initial-fullsend-design.md) (initial design) and
reinforces [ADR 0020](0020-composable-single-responsibility-agents-with-individual-sandboxes.md)
(single-responsibility agents).

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

Each agent consumes inputs from a specific entity context. The code agent
reads issue title, body, labels, and triage comments to implement a fix;
the fix agent reads review feedback and the PR diff to iterate on an
existing branch. Running either agent in the wrong context is a category
error — the code agent cannot extract issue fields from a PR comment, and
the fix agent cannot iterate on a branch that does not yet exist
([#533](https://github.com/fullsend-ai/fullsend/issues/533)).

[ADR 0002](0002-initial-fullsend-design.md) defined `/implement` (now
`/fs-code` per [ADR 0042](0042-fs-prefix-for-slash-commands.md)) on
issues and `/review` on PRs, but did not explicitly restrict commands to
their entity context. [ADR 0020](0020-composable-single-responsibility-agents-with-individual-sandboxes.md)
established single-responsibility agents with tailored sandboxes — context
separation is the dispatch-layer corollary.

The restriction was implemented in
[#533](https://github.com/fullsend-ai/fullsend/issues/533) as `if`
guards in the per-repo shim dispatch workflow
(`internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml`) and is
enforced today via `ISSUE_HAS_PR` checks there. The Go dispatch router (`internal/dispatch/router.go`) does not
yet enforce entity-kind separation — it accepts any valid agent name
regardless of entity context. Future CEL trigger expressions on harness
files ([ADR 0061](0061-harness-cel-dispatch.md)) are the intended
long-term enforcement mechanism.

## Decision

Slash commands are restricted to the entity context where their agent's
inputs exist:

| Command | Allowed context | Rationale |
|---------|----------------|-----------|
| `/fs-code` | Issue without an associated PR | Consumes issue title/body/triage output |
| `/fs-fix` | PR (issue with associated PR) | Consumes review feedback and PR diff |
| `/fs-review` | PR (issue with associated PR) | Evaluates the current PR head |
| `/fs-triage` | Either | Operates on issue metadata (available on both) |
| `/fs-retro` | Either | Read-only analysis of completed workflows |
| `/fs-prioritize` | Either | Read-only scoring of issue metadata |

Dispatch layers must enforce these restrictions before agent invocation.
The enforcement mechanism is implementation-specific:

- **GitHub Actions dispatch** (current): `ISSUE_HAS_PR` guards in the
  routing script.
- **Go router** (interim): should add `Entity.Kind` checks to
  `routeSlashCommand` for `code` and `fix` stages.
- **CEL triggers** (target state per [ADR 0061](0061-harness-cel-dispatch.md)):
  harness `trigger` expressions encode the entity-kind constraint
  (e.g., `event.entity.kind == "work_item" && !has(event.entity.linked_change_proposal)`
  for the code agent).

## Consequences

- Commands invoked in the wrong context are silently dropped — no agent
  runs, no error comment. This matches the existing behavior for
  unauthorized or unrecognized commands.
- Users see only the commands relevant to their context in practice,
  reducing confusion from overlapping command names
  ([#461](https://github.com/fullsend-ai/fullsend/issues/461)).
- Adding a new agent requires deciding its entity context and encoding
  the constraint in the dispatch layer alongside the harness definition.
- The Go router needs a follow-up change to enforce entity-kind
  separation, aligning it with the GitHub Actions dispatch behavior.
