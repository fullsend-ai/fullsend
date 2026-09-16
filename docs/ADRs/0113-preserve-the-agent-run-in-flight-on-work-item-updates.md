---
title: "113. Preserve the agent run in flight on work-item updates"
status: Accepted
relates_to:
  - flapping-convergence
  - operational-observability
  - agent-architecture
topics:
  - concurrency
  - dispatch
---

# 113. Preserve the agent run in flight on work-item updates

Date: 2026-09-14

## Status

Accepted

## Context

Every agent stage job in `reusable-dispatch.yml` runs in a concurrency group keyed on the
work item with `cancel-in-progress: true`. A second push, comment, label or title edit on
the same item cancels the job already running on it, and a fresh job starts over: sandbox
provisioning and bootstrap before the model reads a line, then the whole item again from
cold. On a review minutes from posting that is the entire spend lost; on a spaced burst of
pushes every intermediate review completes and is superseded, the waste behind
[#1014](https://github.com/fullsend-ai/fullsend/issues/1014), [#4960](https://github.com/fullsend-ai/fullsend/issues/4960),
[#1422](https://github.com/fullsend-ai/fullsend/issues/1422) and [#6573](https://github.com/fullsend-ai/fullsend/issues/6573).
Review shows it most, but triage, code, fix, retro and prioritize all cancel the same way.

Cancellation was chosen deliberately once: [ADR 0063](0063-polling-based-work-discovery.md) counts
per-stage `cancel-in-progress` among its mitigations against duplicate dispatch from polling,
alongside the source-native lock and agent idempotency, so any change here has to say what it
gives up there. [ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md) has since
decided for preserve-and-coalesce and states that the reusable workflows must move to
`cancel-in-progress: false`; this ADR is its GitHub side and records the two run facts it needs.

## Options

**Keep cancelling.** The newest event always wins and nothing is worked from stale input,
the property ADR 0063 leans on — at the price of discarding a run's whole spend for an event
that usually does not invalidate the work in progress.

**Preserve behind a repository variable.** Backward compatible, and a compatibility toggle:
two behaviours for one workflow, selected per repository. The team decided against it on
2026-09-14 — the record on [#6957](https://github.com/fullsend-ai/fullsend/issues/6957) is
run continuation as the default, with no feature flag. It was built first on this branch and
is reversed here, before it reached a release.

**Preserve unconditionally.** Chosen. One behaviour to reason about, in the dispatch workflow
rather than each repository's configuration, at the cost of changing every repository on release.

## Decision

No stage job cancels the run in flight. All seven in `reusable-dispatch.yml` carry
`cancel-in-progress: false`, so the run already working on an item finishes and the last run to
queue waits as the single pending run GitHub keeps per group, normally but not necessarily the
newest event, then works from the item's current state. The value is identical on every one on
purpose: one role cancelling while another queues on the same item is harder to reason about than
either behaviour alone. The change ships with a release, needing no consumer scaffold sync;
cancelling a specific run by hand remains available, and is outside this decision.

So that an agent can tell whether the item moved underneath it, the runner exports two run
facts into the sandbox: `FULLSEND_RUN_HEAD_SHA`, the pull request's head at run start and
empty for an issue, and `FULLSEND_RUN_STARTED_AT`, RFC 3339 UTC. Both are written after the
harness's `.env.d` files and `env.sandbox` are applied, so a harness cannot shadow them —
position is what protects them, since `reservedSandboxKeys` covers `env.sandbox` only. The
start instant is the runner's own clock at the top of `fullsend run`, not the workflow's
`created_at`; the gap is the setup that precedes it, and it is one-directional: an agent
re-checking against this value looks at a slightly narrower window than the run spans,
never a wider one, so it can miss an update that landed during setup but cannot invent one.

This decision needs no receipt or skip check. When nothing tells the run in flight what
changed, the pending run simply does the work, so there is nothing to skip. Delivering the
update *into* the running agent is a separate, opt-in decision that builds on this one,
recorded as ADR 0101, the steering decision stacked on this change.

## Consequences

- Every repository changes behaviour on the release that carries this, with no opt-out: it
  stops paying a fresh provision and a cold re-read for every intermediate event, and the
  queued run works from current state.
- The run in flight may finish on input that is already stale and post output the item has
  moved past; the pending run corrects it, at the cost of doing the work again.
- The per-stage cancellation ADR 0063 relies on against duplicate dispatch is gone
  everywhere, leaving the source-native lock and agent idempotency; the duplicate-poll case
  is the least favourable, two duplicate dispatches being the same work rather than a newer
  state superseding an older one.
