---
title: "126. Fullsend-owned mechanism for Codex sub-agents"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - runtime
  - sub-agents
  - security
---

# 126. Fullsend-owned mechanism for Codex sub-agents

Date: 2026-09-29

## Status

Accepted

## Context

Review and retro dispatch sub-agents with fresh contexts and selected personas
([agent architecture](../problems/agent-architecture.md)). On Codex they run in one
context today only because nothing asks for a child: fullsend registers no roles, while
Codex offers a native spawn tool to every parent, `spawn_agent` under collaboration V1
and `collaborationspawn_agent` under V2, neither policed by the runner (#7829 turns both
off until this mechanism ships)
([#6970](https://github.com/fullsend-ai/fullsend/issues/6970)).

[ADR 0104](0104-per-persona-model-resolution.md) makes child models a runner decision
and planned Codex support for `agents[].subagents`. Its persona frontmatter does not
carry over: `model:` values are Claude aliases, and Codex has no per-child tool
allowlist, so ADR 0104's rule that an unservable `tools:` list fails Bootstrap would
reject every review persona. Codex also rereads a role's file and `$CODEX_HOME/hooks.json`
whenever a child starts, so the launch-time checks of
[ADR 0100](0100-codex-sandbox-hooks.md) do not cover children. The model chooses the
spawn arguments (context inheritance, model, effort) and can reopen a closed child
through a separate resume tool. The spawn and resume tools carry different names under
each collaboration version (`spawn_agent` and `multi_agent_v1resume_agent` under V1,
`collaborationspawn_agent` under V2 on `rust-v0.159.3`; every multi-agent tool but the V1
spawn is its namespace and its name joined), so a hook keyed on one exact name misses the
others.

## Options

1. **Single context.** Turn Codex's multi-agent tools off for every agent (#7829 does
   this until a decision) and keep no independent child contexts for review and retro.
2. **Native children governed by instructions only** (the state on `main` with the tools
   on). Nothing enforces fresh context, depth or model choice; a parent can fork its
   conversation into the challenger or pick any OpenAI model.
3. **Native children under a runner-owned role and dispatch policy** (chosen).
4. **Option 3 plus write protection of the runtime's files.** Closes the
   time-of-check window noted below. Each way to do it adds a platform requirement:
   Landlock needs kernel 6.2+ and stops new top-level `$HOME` entries; a read-only
   OpenShell path is fixed at sandbox
   creation, before the per-run files exist; roles baked into the image tie them to
   image releases. A separate decision if the residual below is not acceptable.

## Decision

Codex children run under a policy the runner provisions and enforces.

- An agent delegates when its tools include `Agent` (an absent `tools:` includes it, as
  on Claude Code and pi), harness security is enabled, and Codex resolves the parent
  model to collaboration V1: its catalog entry selects V1, or carries no version and
  Codex's `multi_agent` feature, on by default at the pin and untouched by the runner's
  configuration, supplies V1. Bootstrap then registers its skill personas plus a
  generic `default` and an instruction-only `explore` role. Every other agent runs with
  Codex's multi-agent tools off (`[agents] enabled = false`), a parent whose entry
  selects V2 or that is not in the bundled catalog included.
- Child models resolve in the order the maintainer decided on #6970 (the decision of
  2026-09-03 and the implementation request of 2026-09-25, which replaced the issue body's
  parent-model default): `subagents.<persona>`, then `FULLSEND_CODEX_SUBAGENT_MODEL`, then
  `subagents.default`, then `gpt-5.6-luna`.
  The environment variable sits above the repository default because it is the
  per-run operator override (a validation pass or a cost cap) and below a persona's
  own entry because that entry is a deliberate per-persona choice. `gpt-5.6-luna` is
  the floor for two reasons: #6970 chose the cheap tier of the current family, and it
  is the only listed model whose catalog entry selects V1 on `rust-v0.159.3`, so a
  catalog that can delegate at all serves it. Bootstrap generates each role file. A
  persona with its own entry carries that model in its role file; every other child
  runs the run-level default, which Bootstrap sets as `agents.default_subagent_model`.
  Only OpenAI model IDs are accepted, through the same translation as the parent's model
  (an `openai/` prefix is stripped, a Claude alias rejected). Bootstrap reads the pinned
  CLI's bundled catalog (`codex debug models --bundled`, offline) and fails when a
  resolved child model is not in it, because Codex validates the run-level default
  against its catalog only when a spawn uses it, and a role-file model never. Persona
  `model:` and `tools:` frontmatter is reported, not applied.
- A mandatory PreToolUse hook, installed whenever harness security is enabled, even with
  every individual sandbox hook disabled, admits only a V1 spawn of a registered role with
  `fork_context` present and false and no `model` or `reasoning_effort` key, whatever its
  value. It rejects resume and any spawn from a child, which it recognises by the
  `agent_id` Codex puts only in a child's hook payload. Its matcher is the union of three
  clauses: names starting `multi_agent_v1`, names starting `collaboration` (the two
  namespaces Codex gives its multi-agent tools) and bare names ending in `spawn_agent` or
  `resume_agent`; inside that set the handler is deny-by-default: it admits the exact
  hook name `spawn_agent` under the policy above, passes the V1 `wait_agent`,
  `close_agent` and `send_input` through (as hook names, `multi_agent_v1` joined with
  each) when the caller is the parent, and denies every other name and every call from a
  child: the resume, the V2 tools (`collaborationspawn_agent` included, whatever clause
  it matches) and any tool a later CLI adds to either namespace. A tool matching none of
  the three clauses never reaches the hook; the per-bump revalidation diffs the
  multi-agent tool set at the source for one, and Codex's own depth and open-children
  limits still apply to it. Native configuration limits depth to one and open children
  to four; at depth one Codex offers a child no multi-agent tool at all. A spawn past the
  limit is rejected, not queued (`agent thread limit reached`), and a finished child holds
  its slot until closed, so the paired instructions run up to four children at once and
  close a finished child before a spawn that would exceed four open; the runner adds no
  queue. For the ADR 0100 sandbox hooks, the adapter reports the V1 wait, close and
  send_input calls under the Claude name it already gives the spawn, `Agent`, so an org's
  tool allowlist that admits `Agent` admits an admitted delegation's control calls and
  one that does not blocks it at the first spawn; resume stays unmapped, so that
  allowlist still blocks it when the dispatch hook cannot run. The map is revalidated
  with the hook names on each bump.
- Before admitting a spawn, the hook checks `hooks.json` and the role files against
  digests the runner recorded at Bootstrap, as the ADR 0100 adapter does for each
  hook script before invoking it. `config.toml`, which carries the run-level default,
  is not in that set: Codex reads it once at launch and never again, so a rewrite after
  launch cannot reach a child, and the launch-time guard covers it between iterations.
- The hook denies by exiting 2 with a reason on stderr, the outcome Codex honours as a
  block. Every path the handler controls ends that way: a policy violation, a digest
  mismatch, a missing file, unreadable input or any exception, and the reason is never
  empty. It sets its own deadline inside Codex's handler timeout and denies when the
  deadline passes, and it reads a file for hashing only through the ADR 0100 adapter's
  reader, which refuses anything but a regular file under a size cap, checked on the
  open descriptor, so a path swapped for a FIFO or an oversized file is denied rather
  than read. What the handler does not control fails open, for every call the matcher
  reaches and whatever its arguments: Codex records a handler it cannot complete, one
  whose interpreter fails to start or one starved of CPU or I/O until the timeout kills
  it, as failed and lets the call proceed, as under ADR 0100, so on that path a resume, a
  V2 spawn, a spawn with a forbidden argument or of an unregistered role, or a spawn from
  a child goes through unpoliced, and the opt-in tool allowlist, off by default, is no
  backstop for it. Bootstrap fails if it cannot install the hook.

## Consequences

- Review and retro can delegate on Codex once the paired instructions ship, with up to
  four children open at a time. At
  `rust-v0.159.3` the parents that qualify are `gpt-5.6-luna`, the one listed entry that
  selects V1, and `gpt-5.5`, whose entry carries no version;
  every `gpt-6` model and the other `gpt-5.6` tiers select V2 and run in one context,
  with the multi-agent tools off, a new restriction for them rather than a continuation,
  since a V2 parent has its spawn tool today. At the pin that is the main outcome, not an
  edge case: #6970 was probed on 0.152.1, where V1 was the default, and the per-bump
  revalidation is where it changes.
- Departures from ADR 0104 on Codex: persona `model:` is not applied, the chain adds
  `FULLSEND_CODEX_SUBAGENT_MODEL` and ends at `gpt-5.6-luna` instead of the parent's
  live model, persona `tools:` are instructions only (every child has the parent's
  shell and `apply_patch`), and an unregistered role is always rejected, because a
  delegating agent always has `default` and `explore` registered. The unrestricted
  child is an accepted limit of Option 3, not a control left out: Codex has no per-child
  tool allowlist, so a review or retro child can do what its parent can, under the same
  hooks.
- `code` and `fix` declare no `tools:`, so they also get roles, as on pi.
- The residual is a rewritten checker, not only a race: the hook adapter lives under
  the agent-writable `$CODEX_HOME` and is integrity-checked before each iteration, so
  within an iteration the parent can replace it with one that admits everything,
  digest check included. The spawn-time digest check therefore adds coverage only
  against a mid-iteration role or `hooks.json` edit made before the check and leaving the
  adapter untouched; an edit between the check and Codex's reread at child start is the
  time-of-check window Option 4 names; tampering between iterations is caught by the
  launch-time guard either way. Of the
  Decision's fail-open outcomes, an interpreter that fails to start is the same residual,
  reached within an iteration only through the same tampering, and Option 4 closes both.
  Starvation is not: a parent's background processes can hold CPU or I/O until Codex's
  timeout kills the handler without touching a protected file, so it is the fail-open
  path that remains under Option 4, and on it the whole admission policy is unenforced
  for one call: a resume, a V2 spawn, a spawn with a forbidden argument, of an
  unregistered role or from a child.
- Token totals include children, which is runner work: `codex exec` forwards token
  updates for the primary thread and turn only and its JSONL total is that thread's, so
  the runner reads each child's rollout after the run and folds its last cumulative
  `token_count` into the run totals, per model. Codex still reports no dollar cost.
- Role loading, child model binding, hook reload, the multi-agent tool set and the rule
  that forms hook names from it, the hook outcomes Codex honours as a block, the child
  marker in hook payloads (`agent_id` in a child's, absent from the parent's), depth one
  with no multi-agent tool for a child, four open children with overflow rejected, the
  catalog's V1 entries, the version Codex supplies to an entry that carries none and the
  floor model are revalidated on each Codex CLI bump; a bump that changes the payload
  shape ships with the hook updated, or the child-spawn rule fails open.

Verified against `rust-v0.159.3` (the sandbox image pin): role-file and `hooks.json`
reload at child start, `config.toml` read once at launch, the spawn arguments, the child
marker in hook payloads, depth one with no multi-agent tool for a child, four open
children with overflow rejected, the V1 tool set and the hook-name rule, the
exact-or-regex matcher rule, the hook outcomes
(exit 2 with a reason blocks; exit 2 without one, another exit, an `async` handler or a
timeout does not), `codex debug models --bundled`, the catalog's V1 entries and the
version Codex supplies to an entry that carries none (`multi_agent`, on by default), and
the `exec` usage stream carrying the primary thread only. The checks ran on the 0.157.0,
0.159.0 and 0.159.3 binaries.
