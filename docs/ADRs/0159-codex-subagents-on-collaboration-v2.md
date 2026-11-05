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
sub-agents only when the parent model selects collaboration V1, the older of
Codex's two native delegation protocols. Most models in the Codex 0.166.0
catalog (the CLI's bundled model metadata) select V2, so review and retro on
those models run in one context without the isolated challenger and personas
requested in
[#6970](https://github.com/fullsend-ai/fullsend/issues/6970) and
[#9085](https://github.com/fullsend-ai/fullsend/issues/9085). The sandbox still
pins 0.159.3; this decision targets 0.166.0, and its implementation ships with
the independent bump in [#9088](https://github.com/fullsend-ai/fullsend/pull/9088).

V2 changes context inheritance, child lifecycle and depth enforcement. A spawn
inherits the parent's history unless explicitly given a fresh context, finished
children need no close call, and Codex does not apply `agents.max_depth` under
V2. Child results arrive as injected messages rather than `wait_agent` output,
bypassing the current PostToolUse checks. Supporting V2 therefore requires an
explicit extension of the runner-owned policy.

This ADR aims to let parents selecting either supported collaboration version
delegate under the same runner-owned policy.

## Options

### Keep V1-only delegation

This keeps the existing policy but leaves most current parent models without
isolated review and retro contexts.

### Force every delegating parent onto V2

One tool set is simpler to enforce, but it overrides the catalog's version
selection for V1 models such as `gpt-5.6-luna` and `gpt-5.5`.

### Support each parent's catalog-selected version

This preserves the parent model's selection and enables delegation across the
supported catalog versions, at the cost of maintaining both dispatch protocols
and checking V2's separate child-result delivery path.

## Decision

We support both catalog-selected collaboration versions so Codex parents can
delegate without giving up the runner-owned policy of ADR 0126.

The existing role, child-model, digest, hook-outcome and token-accounting rules
continue to apply. V1 dispatch is unchanged; V2 adds these rules:

1. **Select the version at Bootstrap.**
   - Require the `Agent` tool, harness security and a direct-route parent in
     the pinned CLI's catalog selecting V1 or V2; an absent version means V1.
     Unlisted, `gateway/`, `disabled` and unknown-version parents run in one
     context with an explanation.
   - In `[features.multi_agent_v2]`, set `enabled = true` only for an admitted
     V2 parent with a roster, and `false` otherwise: it overrides
     `[agents] enabled` and is inherited by every child, whatever its model.
   - Keep ADR 0126's roster and four-child limit using
     `[agents] max_concurrent_threads_per_session = 4`; the same key under
     `[features.multi_agent_v2]` counts the root too. Under V2 the hook alone
     enforces depth one, with the fail-open limits below.
2. **Require a fresh-context spawn.**
   - Admit `collaborationspawn_agent` only from the root thread, with
     `fork_turns` explicitly set to a string Codex maps to no fork: trimmed,
     case-insensitive `none`. At 0.166.0, omission inherits all history and
     `"0"` is invalid.
   - Require `agent_type` absent (the registered `default` role) or registered;
     reject any `fork_context`, `model` or `reasoning_effort` key. Codex
     validates `task_name` and `message`.
   - Record each hook approval as an admission for later reconciliation.
3. **Restrict tools at configuration and hook boundaries.**
   - In the runner-owned, integrity-checked `[features.multi_agent_v2]` table,
     render `disable_direct_message = true` to remove `send_message` and
     `followup_task`, and `expose_spawn_agent_model_overrides = false` to hide
     `model` and `reasoning_effort` from the spawn schema. Keep hook denials as
     a backstop: follow-up can resume a finished child, the hook cannot tell
     running from finished children, and review/retro needs neither tool.
   - Pass V2 `wait_agent`, `list_agents` and `interrupt_agent` only from the
     root. Report `collaborationspawn_agent` and those permitted control calls
     as `Agent` to the [ADR 0100](0100-codex-sandbox-hooks.md) hooks, including
     `Agent`/`Task` matcher support; leave `send_message` and `followup_task`
     unmapped.
   - Keep the broad spawn-guard matcher; deny every other matched name and
     every call carrying a child marker (a top-level `agent_id` or
     `agent_type` identifying a child's hook payload), including child spawns.
4. **Check child results before the parent receives them.** Every V2 child's
   result must pass the ADR 0100 PostToolUse chain (unicode, canary, suppression
   and redaction), with the same integrity and block/withhold semantics, before
   entering the parent's context. Checking `wait_agent`'s acknowledgement or
   filtering stored transcripts is insufficient. V2 delegation stays
   experimental until this delivery path is implemented and verified live.
5. **Use version-specific dispatch instructions.** The V2 runtime note and
   agents-repository instructions require `task_name`, `fork_turns: "none"`
   and `wait_agent` with only `timeout_ms`, never a target. Results arrive as
   messages through the checked delivery path above; interrupt an over-budget
   child. Dispatch uses no close or follow-up call: Codex evicts a finished
   resident when a new spawn needs its slot and rejects a fifth spawn while
   four children are running. Child instructions continue to prohibit
   delegation.

## Forge scope

The decision lives in the runner and the sandbox and applies the same way on
GitHub, GitLab and Forgejo; no forge API is involved. The run-level child model
keeps its per-forge configuration path (a runner variable, or
`subagents.default` where that path is unavailable).

## Consequences

- V2 parents gain isolated review and retro contexts and incur child token
  usage, so the implementation is a breaking behavior change.
- For every child of a V2 parent, whatever its catalog model, this decision
  accepts replacing V1's structural removal of delegation tools with hook-only
  depth enforcement, including ADR 0126's documented fail-open paths.
- V2 spawns emit no `exec --json` item, so reconciliation uses rollouts
  (per-thread session logs) for after-the-fact, best-effort detection:
  - Observed spawn counts exceeding recorded admissions fail the run.
  - Unused admissions or missing/altered rollouts can conceal an unauthorized
    spawn; matching counts do not prove that every child was admitted.
- Run totals continue to fold the last `token_usage_record` from each child's
  rollout on either collaboration version.
- Every CLI bump must revalidate V2 hook names and namespace, fork and message
  semantics, the child result delivery path, child tool exposure, residency
  limits and rollout accounting.
