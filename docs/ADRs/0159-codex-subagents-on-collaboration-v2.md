---
title: "159. Codex sub-agents on collaboration V2"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - runtime
  - sub-agents
  - security
---

# 159. Codex sub-agents on collaboration V2

Date: 2026-11-03

## Status

Accepted

Supersedes the rule of [ADR 0126](0126-fullsend-owned-codex-subagents.md) that a
parent delegates only when Codex resolves it to collaboration V1. Its role,
child-model, digest, hook-outcome and token-accounting decisions stand.

## Context

[ADR 0126](0126-fullsend-owned-codex-subagents.md) permits native Codex
sub-agents only when the parent model selects collaboration V1. Most models in
the Codex 0.166.0 catalog select V2, so review and retro on those models run in
one context without the isolated challenger and personas requested in
[#6970](https://github.com/fullsend-ai/fullsend/issues/6970) and
[#9085](https://github.com/fullsend-ai/fullsend/issues/9085).

V2 changes context inheritance, child lifecycle and depth enforcement. A spawn
inherits the parent's history unless explicitly given a fresh context, finished
children need no close call, and a V2 child can retain delegation tools despite
the native depth limit. Supporting it therefore requires an explicit extension
of the runner-owned policy.

This ADR aims to let every parent in the bundled catalog delegate, on either
collaboration version, under the same runner-owned policy.

## Options

### Keep V1-only delegation

This keeps the existing policy but leaves most current parent models without
isolated review and retro contexts.

### Force every delegating parent onto V2

One tool set is simpler to enforce, but it overrides the catalog's version
selection for V1 models such as `gpt-5.6-luna` and `gpt-5.5`.

### Support each model's catalog-selected version

This preserves Codex's model-specific selection and enables delegation across
the catalog, at the cost of maintaining both dispatch protocols.

## Decision

We support both catalog-selected collaboration versions so Codex parents can
delegate without giving up the runner-owned policy of ADR 0126.

The existing role, child-model, digest, hook-outcome and token-accounting rules
continue to apply. V1 dispatch is unchanged; V2 adds these rules:

1. **Select the version at Bootstrap.** Delegation requires the `Agent` tool,
   harness security and a direct-route parent in the pinned CLI's catalog; an
   entry without a version resolves to V1. Unlisted and `gateway/` parents run
   in one context with an explanation. Keep ADR 0126's roster, depth one and
   four-child limit. Set `multi_agent_v2` to `true` only for an admitted V2
   parent with a roster, and `false` otherwise, because it overrides
   `[agents] enabled`.
2. **Require a fresh-context spawn.** Admit `collaborationspawn_agent` only
   from the root thread, with `fork_turns: "none"` as Codex interprets it and
   `agent_type` absent (the registered `default` role) or registered. Reject
   any `fork_context`, `model` or `reasoning_effort` key. At 0.166.0, omitting
   `fork_turns` inherits all history and `"0"` is invalid; Codex validates
   `task_name` and `message`.
3. **Keep control calls parent-only.** Pass V2 `wait_agent`, `list_agents`
   and `interrupt_agent` from the root thread and report them as `Agent` to
   the [ADR 0100](0100-codex-sandbox-hooks.md) hooks. Deny `send_message` and
   `followup_task`: the latter can resume a finished child, the hook cannot
   distinguish running from finished children, and review/retro needs neither.
   Keep the existing matcher; deny every other matched name and every call
   carrying the child marker, including a child's attempt to spawn.
4. **Use version-specific dispatch instructions.** The V2 runtime note and
   agents-repository instructions require `task_name`, `fork_turns: "none"`
   and `wait_agent` without a target; results arrive as messages and an
   over-budget child is interrupted. There is no close or follow-up call:
   Codex evicts a finished resident when a new spawn needs its slot, and
   rejects a fifth spawn while four children are running. Child instructions
   continue to prohibit delegation.

## Forge scope

The decision lives in the runner and the sandbox and applies the same way on
GitHub, GitLab and Forgejo; no forge API is involved. The run-level child model
keeps its per-forge configuration path (a runner variable, or
`subagents.default` where that path is unavailable).

## Consequences

- V2 parents gain isolated review and retro contexts and incur child token
  usage, so the implementation is a breaking behavior change.
- For V2-model children, this decision accepts replacing V1's structural
  removal of delegation tools with hook-only depth enforcement, including
  ADR 0126's documented fail-open paths.
- V2 spawns emit no `exec --json` item, so rollout reconciliation is
  after-the-fact, best-effort detection: observed spawn counts exceeding
  recorded admissions fail the run, but unused admissions or missing/altered
  rollouts can conceal an unauthorized spawn.
- Run totals continue to fold the last `token_usage_record` from each child's
  rollout on either collaboration version.
- Every CLI bump must revalidate V2 hook names and namespace, fork and message
  semantics, child tool exposure, residency limits and rollout accounting.
