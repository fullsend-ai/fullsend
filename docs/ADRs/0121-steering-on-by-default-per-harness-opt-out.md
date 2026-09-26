---
title: "121. Steering on by default, per-harness opt-out"
status: Accepted
relates_to:
  - flapping-convergence
  - operational-observability
topics:
  - concurrency
  - dispatch
  - configuration
---

# 121. Steering on by default, per-harness opt-out

Date: 2026-09-18

## Status

Accepted

## Context

[ADR 0113](0113-steer-the-running-agent-on-work-item-updates.md) introduces steering opt-in,
because no repository may enable it until receipts are authenticated by a channel agents and
post-scripts cannot mint
([steering.md](../contributing/steering.md#rollout-order)).
[ADR 0120](0120-receipt-the-absorbed-update-under-the-job-token.md) makes the GitHub Actions job
token that channel, so the precondition is met and how a repository turns steering on is open
again.

The meeting recorded on [#6957](https://github.com/fullsend-ai/fullsend/issues/6957) on
2026-09-14 settled the policy for the behaviour steering implements: runs continue rather than
cancel, that is the default, and there is no feature flag for it. It speaks to event coalescing
rather than to steering's own switch, so this ADR is where that choice is made — applying the same
policy, since a steering default behind a flag would reintroduce exactly the toggle that decision
removed. What remains is which switch expresses it, and what a run that cannot steer should say.

## Options

### One `FULLSEND_STEER` switch

A single repository-level variable turns steering on for everything in the repository, which is
one place to look and one place to flip. It cannot express a per-agent exception, so a definition
that does not understand the envelope forces the whole repository off, and it is the feature flag
the decision above rejected.

### A repository variable plus per-harness opt-in

The variable gates the repository and each harness names itself in, which gives a graduated
rollout. It leaves steering off for every harness that says nothing — the state the precondition
existed to protect, kept long after the precondition was satisfied — and it makes the common case
the one that requires two edits in two files.

### On by default, with `steer: {enabled: false}` as the opt-out

The harness `steer:` block is the only switch, and steering arrives with the release that carries
it rather than one harness at a time. A harness that cannot take the envelope has to be found and
opted out before that release, which is work the rollout order already asks a human to do.

## Decision

Steering is on by default. The harness `steer:` block is the only switch, and
`steer: {enabled: false}` is the only way off; such a harness gets run continuation alone and
nothing more.

`enabled` is a pointer internally, because absent and `false` have to mean different things: a
block that sets only `max_steers` or `poll_interval_seconds` is tuning steering, not silently
opting out of it. Composition is unchanged and covers both directions — a child that says nothing
inherits its base's whole block, so a base opt-out is inherited rather than overwritten by the
default.

With most runs now steering by default, most declined watches are ordinary conditions rather than
misconfiguration: a local run is not in GitHub Actions, a GitLab run queues instead, a runtime
that cannot take a message never could. The runner therefore announces a decline only when the
harness asked for steering by name. A missing job token or run id is a defect in the environment
rather than an ordinary condition, and is always announced.

The rollout gates — the fleet definitions teaching agents the envelope, an end-to-end steer
observed inside the sandbox, a live identity match on the receipt, and every stage whose
definition ignores the envelope opting out before the default reaches it — stay guidance that the
people cutting the release judge. Nothing in the binary enforces them, and an eligible run on a
definition that ignores the envelope still acks and still receipts, so the update is dropped
silently. A run that started no watcher is not that case: with no watcher there are no consumed
runs, and a receipt requires one, so such a run claims nothing. Agents scaffolded by
`fullsend agent new` from here on carry the envelope contract in their generated body; ones
scaffolded earlier must be regenerated to gain it.

## Consequences

- Every repository steers on the release that carries this — and a consumer pinned to
  `reusable-dispatch.yml@main` adopts it on its next dispatched run rather than at a version it
  chose — so a burst on one work item produces one run that absorbs it plus at most one queued
  follow-up, which skips when that run absorbed it and otherwise does the work in full.
- A run holds its sandbox until it settles rather than ending at its first result, so the cost of
  the default is VM time on every steered run, not only on harnesses that asked.
- A harness that opts out sits indefinitely in the state
  [ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md) describes, where the
  run in flight finishes and the queued run does the work; nothing is half-enabled.
- Stage definitions that do not act on the envelope must be found and opted out before the
  release, since the failure there is a dropped update rather than a wasted run — though nothing
  reaches the prompt at all until a release carries both the runner's `FULLSEND_STEER_ACTIVE`
  export, set only while a watcher runs for that iteration, and definitions that read it.
- Console output on runs that never asked for steering stays quiet, at the cost of an ordinary
  decline leaving no record at all on such a run.
