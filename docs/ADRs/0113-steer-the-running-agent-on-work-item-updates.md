---
title: "113. Steer the running agent instead of only preserving it"
status: Accepted
relates_to:
  - security-threat-model
  - operational-observability
  - flapping-convergence
topics:
  - concurrency
  - dispatch
  - runtime
---

# 113. Steer the running agent instead of only preserving it

Date: 2026-09-18

## Status

Accepted

## Context

[ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md) stops the cancellation: the
stage job already working on a work item finishes, and one run waits behind it as the pending run,
working from the item's current state. This ADR addresses what preserving alone leaves standing.

Three costs survive it. The run in flight finishes on the state it started with and posts output
that is already stale, such as a review of a commit that no longer exists
([#1207](https://github.com/fullsend-ai/fullsend/issues/1207)). The run queued behind it then redoes
the full job over the same diff ([#1014](https://github.com/fullsend-ai/fullsend/issues/1014),
[#6573](https://github.com/fullsend-ai/fullsend/issues/6573)). And a person on the work item cannot
add direction to a run already working: their only lever is a `/fs-` comment, which starts another
run rather than reaching this one.

## Options

### Preserve only

ADR 0106 unchanged. Cheapest, and already correct for harnesses that reconcile durable entity state
on their next run. It leaves all three costs in place for event-only harnesses, and leaves a person
with no way to reach a run in flight.

### An end-of-run re-check

The run finishes, then checks whether the item moved and re-stages itself if it did. No new
transport and no runtime capability — it is the shape of the fleet-agent backstop and of gh-aw's
steer issue. But it cannot change what the agent is doing while it is doing it: the stale turn is
still paid for, and the correction costs a second full run.

### Steer the run in flight

Deliver the update into the running session as a message, so the agent's next turn works from the
item's current state. It buys the absorbed turn and the human's mid-run direction, at the price of a
runtime capability, a way to learn the item moved, and a way to tell the queued run the work is
already done.

## Decision

Steer the run in flight. While the run in flight holds the work item it absorbs updates to that item
itself, and the queued follow-up run becomes the **notification** rather than the worker.

This revisits one clause of ADR 0106, which decided that `fullsend run` does not poll for later
events or invoke another run itself. A steered run polls, and only polls: it still invokes nothing,
every follow-up event still creates its own pending run, and a steer never extends the run's timeout
window — the rest of that clause stands. How a run in flight learns the item moved is the
queue-monitoring decision that follows this one.

Steering requires preserving, not the reverse: every failure path — no delivery ack, no time left,
cap reached, runtime cannot steer, provenance refused — is ADR 0106 on its own, and the queued run
does the work exactly as it does today.

A steer is **content, never capability**: it cannot widen tools, role, model, scope or the L7 network
policy, and runtimes render it as a user message. The runner authorizes nothing itself; who may steer
a run, and on whose authority, is the provenance decision that follows — the question ADR 0106
explicitly deferred to an ADR of its own.

This ADR decides only that steering happens. Five decisions follow it, each recorded separately: the
steer interface and its envelope contract; provenance and actor authority; how a run in flight learns
the item moved; the receipt that lets the queued run skip its work; and whether steering is on by
default.

## Consequences

- A burst of events on one work item produces one agent run that absorbs them plus at most one short
  follow-up, instead of a full re-run per event.
- Agents stop posting output computed from state the subject has already moved past.
- A run holds its sandbox until it settles, so a steered run occupies a VM longer and can cost as
  much again per absorbed update.
- Absorbing an update saves nothing unless the queued run can be told the work is done, so the
  receipt decision is load-bearing rather than an optimization.
- Steering is unavailable wherever the run in flight cannot learn that the item moved, so the
  capability is per-platform rather than universal.
