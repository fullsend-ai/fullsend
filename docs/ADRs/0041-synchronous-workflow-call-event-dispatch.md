---
title: "41. Synchronous workflow_call for event-driven agent dispatch"
status: Proposed
relates_to:
  - agent-infrastructure
  - operational-observability
topics:
  - dispatch
  - workflows
  - observability
  - workflow-call
---

# 41. Synchronous workflow_call for event-driven agent dispatch

Date: 2026-05-20

## Status

Proposed

## Context

The per-org event path still fans out from `dispatch.yml` to stage workflows
(`code.yml`, `review.yml`, …) via `gh workflow run` and `on: workflow_dispatch`
([ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md),
[ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md)).
That hop is **asynchronous**: the dispatch job exits when GitHub accepts the
API call, not when the agent finishes. GitHub Actions does not link the
dispatched run to the caller in the UI — operators see unrelated top-level runs
in `.fullsend` with no parent workflow, which makes it hard to answer “is the
agent still running?” from a PR or issue and hard to debug failures.

Operational pain is documented across multiple issues:

- [#504](https://github.com/fullsend-ai/fullsend/issues/504) — enrolled-repo shim
  can show green before review finishes or posts PR feedback.
- [#896](https://github.com/fullsend-ai/fullsend/issues/896) — agent runs lack
  structured source/destination metadata; correlation requires log parsing.
- [#1048](https://github.com/fullsend-ai/fullsend/issues/1048) — `fullsend run`
  and trigger resources are not linked; revives fragile `post-run-link` ideas.
- [#863](https://github.com/fullsend-ai/fullsend/issues/863) / [#529](https://github.com/fullsend-ai/fullsend/issues/529) —
  `post-run-link` was dropped during `workflow_call` shim migration; run links
  no longer posted on triggers.
- [#272](https://github.com/fullsend-ai/fullsend/issues/272) — no lifecycle
  indicators (emoji reactions) on trigger comments/issues/PRs.
- [#957](https://github.com/fullsend-ai/fullsend/issues/957) / [#988](https://github.com/fullsend-ai/fullsend/issues/988) —
  “working on this” comments without reliable failure follow-up when dispatch
  and agent runs are decoupled.
- [#837](https://github.com/fullsend-ai/fullsend/issues/837) — failed review runs
  should update PR status comments.
- [#763](https://github.com/fullsend-ai/fullsend/issues/763) — post-run feedback
  should come from the agent App, not GitHub Actions bot identity.
- [#872](https://github.com/fullsend-ai/fullsend/issues/872) / [#934](https://github.com/fullsend-ai/fullsend/issues/934) —
  silent failures when users cannot find the right `.fullsend` run.

Band-aids (extra comments, annotations, emoji, separate `post-run-link` jobs)
fight the async model. **Per-repo mode** already uses a synchronous
`workflow_call` chain (`reusable-dispatch.yml` → `reusable-{stage}.yml`;
[ADR 0033](0033-per-repo-installation-mode.md), PR #799). Per-org should
converge on the same property for the **event-driven** path.

Cross-repo shim → `.fullsend` is already `workflow_call` ([ADR 0029](0029-central-token-mint-secretless-fullsend.md),
[ADR 0034](0034-centralized-shim-routing-via-dispatch.md)). This ADR targets
only **in-config-repo** dispatch from `dispatch.yml` to agent stages.

[ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md) Option C
introduced runtime `# fullsend-stage:` scanning so org-specific agent workflows
could be wired without editing `dispatch.yml`. Static `workflow_call` jobs in
`dispatch.yml` are incompatible with that model. This ADR drops dynamic agent
discovery rather than replacing it with compile-time tooling. After agent
architecture is revised to support [ADR 0038](0038-universal-harness-access.md),
we may revisit this decision and re-evaluate whether a discovery mechanism is
needed.

## Options

### Option A: Keep `workflow_dispatch` + marker scan (status quo)

Preserves dynamic `# fullsend-stage:` discovery and nesting reset
([ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md)).
Does not fix UI correlation or “dispatch succeeded ≠ agent done” ([#504](https://github.com/fullsend-ai/fullsend/issues/504)).

### Option B: Static `workflow_call` jobs in `dispatch.yml` (recommended)

Replace the scan/`gh workflow run` loop with conditional `workflow_call` jobs
in `dispatch.yml`, aligned with `reusable-dispatch.yml`
([ADR 0033](0033-per-repo-installation-mode.md)). Each stage workflow is wired
explicitly; adding or removing an agent workflow requires editing
`dispatch.yml`.

### Option C: Mitigations only (annotations, comments, reactions)

Implement [#896](https://github.com/fullsend-ai/fullsend/issues/896), [#1048](https://github.com/fullsend-ai/fullsend/issues/1048),
[#272](https://github.com/fullsend-ai/fullsend/issues/272) without changing dispatch
mechanism. Reduces pain but leaves orphan runs and early-success dispatch jobs.

## Decision

For **event-driven** agent dispatch (webhook → shim → routing → agent), eliminate
`workflow_dispatch` and `gh workflow run` between `dispatch.yml` and stage
execution. Use **Option B**: static `workflow_call` jobs in `dispatch.yml`.

Drop the dynamic agent discovery introduced in [ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md)
Option C — remove the runtime `# fullsend-stage:` scanner and the marker
convention. Do not add compile-time sync tooling as a substitute.

`workflow_dispatch` remains allowed for **non-event** entry points only (e.g.
`repo-maintenance.yml`, manual prioritize, admin/CLI triggers).

Prefer `dispatch.yml` → `reusable-*.yml@*` where possible to stay within four
`workflow_call` nesting levels.

After agent architecture is revised to support [ADR 0038](0038-universal-harness-access.md),
re-evaluate whether a discovery mechanism is needed.

## Consequences

- PR/issue observers can use one Actions run to see agent completion without
  hunting orphan `.fullsend` runs; mitigates [#504](https://github.com/fullsend-ai/fullsend/issues/504),
  [#896](https://github.com/fullsend-ai/fullsend/issues/896), [#1048](https://github.com/fullsend-ai/fullsend/issues/1048),
  and the silent-failure cluster ([#957](https://github.com/fullsend-ai/fullsend/issues/957),
  [#988](https://github.com/fullsend-ai/fullsend/issues/988)); notification UX
  may still need follow-up issues.
- **Supersedes** [ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md)
  Option C for the event path, including dynamic `# fullsend-stage:` discovery;
  centralized routing in `dispatch.yml`
  ([ADR 0034](0034-centralized-shim-routing-via-dispatch.md)) remains.
- Adding or removing org-specific agent workflows requires editing
  `dispatch.yml` directly; the single-file marker pattern from ADR 0026 is gone.
- Per-org and per-repo dispatch shapes converge; enrolled-repo shims may need
  `needs:` / concurrency review ([#504](https://github.com/fullsend-ai/fullsend/issues/504)).
- Discovery may be revisited after [ADR 0038](0038-universal-harness-access.md)
  agent architecture changes land.
