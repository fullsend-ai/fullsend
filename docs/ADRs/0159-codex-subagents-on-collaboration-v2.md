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

[ADR 0126](0126-fullsend-owned-codex-subagents.md) runs review and retro
sub-agents as native Codex children under a runner-owned policy, for a parent
whose catalog entry Codex resolves to collaboration V1 only: the hook policy was
written against the V1 spawn tool, and at that pin V1 was what the cheap tier of
the current family selected. In the catalog bundled with Codex 0.166.0 only
`gpt-5.6-luna`, `gpt-5.5` and one hidden entry still resolve to V1; every
`gpt-6` model, the other `gpt-5.6` tiers and the hidden `daybreak` entries
select V2, so a V2 parent, the
common choice, runs review and retro in one context, without the isolated
challenger and personas that
[#6970](https://github.com/fullsend-ai/fullsend/issues/6970) is about
([#9085](https://github.com/fullsend-ai/fullsend/issues/9085)). Codex is to
leave experimental, and a runtime that delegates only on an ageing tier cannot.

V2 differs from V1 in what the hook sees and in what a parent can do. Its tools
reach the hook as `collaboration` joined with the tool name
(`collaborationspawn_agent`, `collaborationwait_agent`,
`collaborationsend_message`, `collaborationfollowup_task`,
`collaborationinterrupt_agent`, `collaborationlist_agents`), names ADR 0126's
matcher already reaches and its handler denies. The V2 spawn takes `message`
and `task_name`, an optional `agent_type`, `fork_turns` in place of
`fork_context` (absent or `all` is the parent's whole history, `none` a fresh
context, and `0` is refused outright), and `model` and `reasoning_effort`. There
is no close tool: a finished child stays resident until a later spawn needs its
slot, when Codex unloads it with its state kept on disk; the two message tools
share one path to an existing child, finished or not: `send_message` queues mail
for it, and `followup_task` starts a turn on it, reloading one Codex unloaded. A
child inherits its parent's version, and under V2 Codex's depth limit does not
remove the collaboration tools from a child whose own model entry selects V2.

This ADR aims to let every parent in the bundled catalog delegate, on either
collaboration version, under the same runner-owned policy.

## Options

1. **Stay on V1.** Pin Codex and the parent tier until V1 disappears. Rejected:
   the catalog has already moved, and the runtime cannot leave experimental
   frozen on one model.
2. **Force V2 for every delegating parent** (`[features] multi_agent_v2 = true`
   for all). One tool set to police, but it moves `gpt-5.6-luna` and `gpt-5.5`
   parents off the version Codex resolves for them.
3. **Admit both versions, each as Codex resolves it** (chosen).

## Decision

We extend the ADR 0126 policy to collaboration V2 so that any parent in the
pinned CLI's bundled catalog delegates on the direct route.

- Bootstrap delegates when the agent's tools include `Agent`, harness security
  is enabled and the parent model's catalog entry is listed, whichever version
  it selects (an entry without a version resolves to V1 through Codex's
  `multi_agent` feature, as before). An unlisted or `gateway/` parent still runs
  in one context, with one line saying why. Bootstrap records the admitted
  version: `[agents]` carries the roster, depth one and four open children for
  both, and `[features] multi_agent_v2` is `true` for a V2 parent and `false`
  otherwise, so the configuration states the version the runner admitted. The
  key ranks above `[agents] enabled`, so Bootstrap writes `true` only with a
  roster.
- The spawn guard keeps its matcher and admits, from the root thread only, the
  V2 spawn (`collaborationspawn_agent`) of a registered role under the V1 rules
  mapped across: `fork_turns` present and `none`, read as Codex reads it (an
  absent value is a full-history fork, so it is not admitted), no
  `fork_context`, `model` or `reasoning_effort` key whatever its value, and
  `agent_type` absent (Codex applies the `default` role to a fresh-context
  spawn) or a registered role. `task_name` and `message` are Codex's to
  validate. The V1 rules are
  unchanged, and the guard needs no per-run version: a parent holds one tool
  set.
- From the root thread the guard passes `collaborationwait_agent`,
  `collaborationlist_agents` and `collaborationinterrupt_agent` through, as it
  passes the V1 wait, close and send-input, and reports them as `Agent` to the
  ADR 0100 hooks. It denies `collaborationsend_message` and
  `collaborationfollowup_task`: `followup_task` starts a turn on a finished
  child, reloading one Codex unloaded (the V2 counterpart of the resume ADR
  0126 denies), `send_message` queues mail that only such a turn would read,
  the hook cannot tell a running child from a finished one, and the dispatch
  pattern needs neither, because a child's final answer reaches the parent on
  its own. Every other name in the matched set, and every call from a child,
  stays denied.
- The child rule is the depth limit under V2. A V2 child whose own model entry
  selects V2 keeps the collaboration tools despite `[agents] max_depth = 1`; its
  spawn reaches the guard with the child marker and is denied, and the role
  file's note still tells it that it dispatches nothing.
- The runtime note the agents repository keys on is written per version. Under
  V2 it names `task_name` and `fork_turns: "none"`, `wait_agent` without a
  target, `interrupt_agent` for a child past its budget, that a child's answer
  arrives as a message, and that a finished child frees its slot when a later
  spawn needs it, with no close call and no follow-up.

## Forge scope

The decision lives in the runner and the sandbox and applies the same way on
GitHub, GitLab and Forgejo; no forge API is involved. The run-level child model
keeps its per-forge configuration path (a runner variable, or
`subagents.default` where that path is unavailable).

## Consequences

- Review and retro delegate on every catalog model of the pinned Codex,
  `gpt-6.1-sol` and `gpt-6.1-terra` included; the paired instructions in the
  agents repository gain a V2 dispatch shape (`fork_turns`, `wait_agent`
  without a target, no close call).
- Four open children means four resident: a fifth spawn unloads a finished child
  and fails while four are still running; Codex derives the count from
  `max_concurrent_threads_per_session` plus one minus one.
- Under V2 the only control on a child's own spawn is the hook, which fails open
  on the paths ADR 0126 names; a child on the floor model, a V1 entry, has no
  collaboration tools at all.
- A V2 spawn leaves no item in the `exec --json` stream, so for a V2 parent the
  runner's check of spawns against the hook's admissions rests on the children's
  rollouts on disk, each child once; a spawn that a child got past the hook
  appears there as a spawn without an admission and fails the run.
- Token totals still come from the usage record in each child's rollout, which
  Codex writes for every thread on both versions; the per-bump revalidation
  gains the V2 tool names and the namespace default they rest on, the
  `fork_turns` semantics, the message tools' paths, residency and the slot
  arithmetic, and the collaboration-tool rule for children. A V2 parent that
  ran in one context spawns children once this lands, with their tokens, which
  the implementation announces as breaking.

Verified against `rust-v0.166.0` in source: the hook-name rule and the V2
namespace default, the V2 tool set and arguments, `fork_turns` handling, the
child marker, the version resolution order and the `multi_agent_v2` feature
(stable, off by default), residency and eviction, the message tools' shared
path, the collaboration-tool rule for children, the slot arithmetic, the role
applied to a fresh-context spawn, the usage record written for every thread,
and the stream's silence on a V2 spawn. The catalog was dumped from the 0.166.0
binary. A live run on a V2 parent still has to show the hook names as read, a
child's rollout with its usage record, and the spawn's `message`, which the V2
schema marks encrypted, reaching the hook and the child in clear on fullsend's
provider.
