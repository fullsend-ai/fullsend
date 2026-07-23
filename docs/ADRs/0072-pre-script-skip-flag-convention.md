---
title: "72. Skip-flag convention for pre-scripts invoked inline and via harness pre_script"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - harness
  - configuration
  - workflows
  - conventions
---

# 72. Skip-flag convention for pre-scripts invoked inline and via harness pre_script

Date: 2026-07-15

## Status

Accepted

## Context

An agent's harness `pre_script` runs once, immediately before sandbox creation, inside `fullsend run`. Some reusable workflows also need a fast pre-check *before* that point, to gate expensive setup (GCP credentials, bot identity, agent-env prep) that would otherwise run unconditionally on invalid input or a redundant retry. `reusable-code.yml` and `reusable-fix.yml` both do this today by calling the same pre-script inline, ahead of `fullsend run` — which means the script runs twice per invocation.

Deleting the inline call is not always an option: for `code`, the inline step's `skipped=` output gates four downstream workflow steps that all run *before* `fullsend run` starts, so the check has to happen there. Running the pre-script's full body twice is wasteful at best (redundant tool installs) and unsafe at worst (repeated GitHub API side effects — label creation, issue comments — from the existing-human-PR check) ([fullsend-ai/fullsend#4718](https://github.com/fullsend-ai/fullsend/issues/4718)).

## Options

**Split into two scripts** (a lightweight inline "gate" script plus a harness-only "prepare" script) avoids a control flag and keeps each script single-purpose, but requires two entry points per agent to keep in sync and a bigger diff to introduce. **Env var skip-flag** reuses the single existing script and existing delivery mechanisms (`runner_env` / workflow step `env:`), at the cost of a conditional block inside the script. Chose the flag: smaller surface area, and it follows precedent — [ADR 0049](0049-agent-configuration-env-var-convention.md) already establishes `{AGENT}_{SETTING_NAME}` env vars as the standard way to signal harness-scoped behavior differences.

## Decision

When a pre-script must run in two contexts that need different subsets of its behavior — an inline reusable-workflow step needing only a fast, gating subset, and the harness `pre_script` invocation needing the full script — gate the part that must run exactly once behind a dedicated env var, rather than deleting either call site.

**Naming:** `{AGENT}_SKIP_{THING}`, following [ADR 0049](0049-agent-configuration-env-var-convention.md)'s `{AGENT}_{SETTING_NAME}` syntax. Examples: `CODE_SKIP_EXISTING_PR_CHECK`, `FIX_SKIP_TOOL_INSTALL`.

**This is a distinct category from ADR 0049's config vars.** ADR 0049 covers user-facing behavioral knobs, documented in `docs/agents/<agent>.md`. Skip flags are internal invocation-context signals — never user-set, always wired automatically by whichever call site should skip the guarded block (`harness/<agent>.yaml`'s `env.runner`, or the legacy `runner_env` on harnesses not yet migrated per [ADR 0055](0055-unified-env-var-delivery.md), for the harness invocation; the reusable workflow step's `env:` for the inline invocation). Document them in the pre-script's header comment and a one-line comment at the entry that sets them — not in the agent's user-facing docs.

**Which side sets the flag** depends on which invocation should skip the guarded block, not a fixed rule — for `code`, the harness invocation skips the expensive existing-PR check (already done inline); for `fix`, the inline invocation skips the tool install (only needed once, by the harness invocation, before the post-script's pre-commit run). Whichever invocation's output or side effects the surrounding pipeline actually depends on keeps the default (unset) behavior; the flag is a positive opt-out set on the other, redundant call site.

**Scope:** this pattern applies when an agent's reusable workflow needs to gate expensive setup on a pre-check *and* the harness `pre_script` also needs to run the full script. It does not retroactively require adding inline gating to agents that don't have this need today (`triage`, `prioritize`, `retro` don't inline-call their pre-scripts at all; `review` inline-calls a different script, `pre-fetch-prior-review.sh`, not its harness `pre_script`) — apply it if and when such an agent grows the same duplication problem.

## Consequences

- New agents (or new expensive/side-effecting logic added to an existing pre-script) that need workflow-level gating have a documented, precedented pattern to follow instead of reinventing one per PR.
- Reviewers can check a single naming rule (`{AGENT}_SKIP_{THING}`, set on exactly one call site) instead of re-litigating the mechanism each time.
- Two invocations of the same pre-script remain — the flag reduces what the redundant invocation *does*, not whether it runs at all; removing the inline call entirely would require reordering which workflow steps run before `fullsend run` is invoked, not just an output-surfacing feature in `fullsend run` itself, since the steps it gates already run in an earlier step position.
- Skip flags must not be confused with [ADR 0049](0049-agent-configuration-env-var-convention.md) config vars despite sharing syntax — code review should flag a skip flag that ends up in `docs/agents/<agent>.md`'s Variables table, or a config var wired only through `runner_env` without user documentation.

## References

- [fullsend-ai/fullsend#4718](https://github.com/fullsend-ai/fullsend/issues/4718) — originating issue
- [fullsend-ai/fullsend#5013](https://github.com/fullsend-ai/fullsend/pull/5013), [fullsend-ai/agents#175](https://github.com/fullsend-ai/agents/pull/175) — first implementation (`code`, `fix`)
- [ADR 0049](0049-agent-configuration-env-var-convention.md) — env var naming precedent
- [ADR 0024](0024-harness-definitions.md), [ADR 0045](0045-forge-portable-harness-schema.md) — `runner_env` / `env.runner` delivery mechanism
- [ADR 0055](0055-unified-env-var-delivery.md) — deprecates `runner_env` in favor of `env.runner`/`env.sandbox`
- [ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md) — reusable workflow structure
