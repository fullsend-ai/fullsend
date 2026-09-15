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

Date: 2026-09-12

## Status

Accepted

## Context

Every agent stage job in `reusable-dispatch.yml` runs in a concurrency group keyed on the
work item with `cancel-in-progress: true`. A second push, comment, label or title edit on
the same item cancels the job already running on it, and a fresh job starts over: sandbox
provisioning and bootstrap before the model reads a line, then the whole item again from
cold. On a review minutes from posting that is the entire spend lost; on a spaced burst of
pushes every intermediate review completes and is superseded, the waste behind
[#1014](https://github.com/fullsend-ai/fullsend/issues/1014),
[#4960](https://github.com/fullsend-ai/fullsend/issues/4960),
[#1422](https://github.com/fullsend-ai/fullsend/issues/1422) and
[#6573](https://github.com/fullsend-ai/fullsend/issues/6573). Review shows it most, but
triage, code, fix, retro and prioritize all cancel the same way.

Cancellation was chosen deliberately once: [ADR 0063](0063-polling-based-work-discovery.md)
counts per-stage `cancel-in-progress` among its mitigations against duplicate dispatch from
polling, alongside the source-native lock and agent idempotency. Any change here has to say
what it gives up there.

## Decision

Make the cancellation a repository's choice rather than the workflow's. Every stage job in
`reusable-dispatch.yml` carries

```yaml
cancel-in-progress: ${{ vars.FULLSEND_PRESERVE_RUNS != 'true' }}
```

Unset — the default everywhere — keeps today's behaviour. Set to `true` (GitHub compares
strings case-insensitively, so `TRUE` also preserves; any other value cancels), the run in
flight is left to finish and the newer event waits as the single pending run GitHub keeps
per concurrency group, then works from the item's current state. The expression is
identical on all seven stage jobs on purpose: one role cancelling while another queues on
the same item is harder to reason about than either choice alone. Operators set it with
`fullsend github set`, and the change ships with a release; consumer repositories need no
scaffold sync.

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
recorded as ADR 0101 with the steering change that lands after this one.

## Consequences

- A repository that opts in stops paying a fresh provision and a cold re-read for every
  intermediate event; the run in flight finishes and the queued run works from current state.
- The run in flight may finish on input that is already stale and post output the item has
  moved past; the pending run corrects it, at the cost of doing the work again.
- Opting in removes the per-stage cancellation that ADR 0063 relies on against duplicate
  dispatch, leaving the source-native lock and agent idempotency; the duplicate-poll case is
  the least favourable, since two duplicate dispatches are the same work rather than a newer
  state superseding an older one.
- Agents gain a stable baseline to compare against, but the exported start is later than the
  run's true start; making it exact would cost an Actions API call on every run.
- Nothing changes for a repository that leaves the variable unset.
