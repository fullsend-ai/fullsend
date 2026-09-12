---
title: "101. Steer the running agent on work-item updates instead of cancelling the run"
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

# 101. Steer the running agent on work-item updates instead of cancelling the run

Date: 2026-09-03

## Status

Accepted

## Context

[ADR 0113](0113-preserve-the-agent-run-in-flight-on-work-item-updates.md) stops the
cancellation: the stage job already working on a work item is left to finish, and the newer
event waits as the single pending run the platform keeps per concurrency group. This ADR builds
on that decision and addresses what preserving alone leaves standing.

Three costs survive it. The run in flight finishes on the state it started with and posts output
that is already stale — a review of a commit that no longer exists, which is
[#1207](https://github.com/fullsend-ai/fullsend/issues/1207). The run queued behind it then does
the full job over the same diff, so tokens are saved only when the run in flight absorbs the
update before the queued run starts; that is the waste behind
[#1014](https://github.com/fullsend-ai/fullsend/issues/1014),
[#4960](https://github.com/fullsend-ai/fullsend/issues/4960),
[#1422](https://github.com/fullsend-ai/fullsend/issues/1422) and
[#6573](https://github.com/fullsend-ai/fullsend/issues/6573). And a person on the work item has
no way to add direction to a run that is already working: their only lever is a `/fs-` comment,
which starts another run rather than reaching this one.

The runner cannot simply be handed the update. It runs inside a CI job that can only make
outbound calls, and GitHub Actions has no API for delivering input to a running job.
[ADR 0041](0041-synchronous-workflow-call-event-dispatch.md) fixes the shape of the dispatch
chain this has to work within, and
[#1637](https://github.com/fullsend-ai/fullsend/issues/1637) asked for the concurrency semantics
to be written down.

## Decision

Steering is an **opt-in** extension of preserving, enabled per harness by the `steer:` block.
While the run in flight holds the work item it absorbs updates to that item itself — the queued
follow-up run becomes the *notification*, not the worker — so that when the queued run finally
starts it finds a receipt saying the work is done and exits. With the block absent, which is the
default everywhere, the behaviour is ADR 0113 alone and nothing more. Steering requires
preserving; the reverse is not true.

The runner learns about an update by listing the execution platform's own run records for its
shim, verifies each candidate's **provenance** against server-side fields the sender cannot
write, and delivers what passes into the running session as a message. It authorizes nothing
itself: a follow-up run is accepted only because its own `Route` job already ran
[ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)'s authorization path. A
steer is content, never capability — it cannot widen tools, role, model, scope or network policy.

After the run, its terminal status comment carries a processing receipt naming the follow-up runs
it consumed, and a queued run that finds its own id listed exits without starting the agent. That
receipt is what makes the queued run short rather than a full re-run, and it is load-bearing
rather than an optimization: without one, steering costs *more* than cancelling does today, since
the run in flight absorbs the push and reviews the new head, and the queued run then reviews that
same head again. **Steering may not be enabled anywhere until receipts are authenticated by a channel that
agents and post-scripts cannot mint** — a forged receipt makes the queued run exit without doing
its work, so the failure is a silently dropped update rather than a wasted one.

That the source is the platform's run records rather than forge events is the part of this
decision to check when reviewing it:

- It reads the **platform's own run records**, never forge events — no cursor, no normalization,
  no ordering guarantee, and no input driver.
- It **invokes nothing**: every follow-up event still creates its pending run, and the run in
  flight only reads what the platform already decided.
- It is **bounded** — `max_steers` per run, and a remaining-time floor below which it settles
  rather than starting a turn it cannot finish.
- It **never extends the run's timeout**: the budget is `min(stage timeout, forge token life −
  margin)` and the run settles inside it.
- Each run still **reconciles the item's current state**; a steer is a prompt to reconcile
  sooner, not a substitute for reconciling.

The mechanics — provenance checks, the steer contract, transport, settle, ceilings, the skip
check, the backstop, configuration, per-stage behaviour, known limits and rollout order — are in
[steering.md](../contributing/steering.md). The envelope the agent receives is a byte-level
contract with the fleet agent definitions, versioned in
[normative/steer-envelope/v1](../normative/steer-envelope/v1/README.md).

## Consequences

- A burst of events on one work item produces one agent run that absorbs them plus at most one
  short follow-up, instead of a full re-run per event — but only once the receipt is
  authenticated, since the follow-up is only short if it can trust a receipt to skip on.
- Agents stop posting output computed from state the subject has already moved past, which is the
  complaint in [#1207](https://github.com/fullsend-ai/fullsend/issues/1207).
- The runner gains a dependency on the execution platform's run records and its per-stage
  `actions: write` grant, and steering is unavailable on any platform that exposes neither.
- A run now holds its sandbox until it settles rather than ending at its first result, so a
  steered run occupies a VM longer and can cost as much again per absorbed update.
- Nothing changes for a repository that does not opt in, and the fallback in every failure path —
  no ack, no time left, cap reached, runtime cannot steer — is ADR 0113 on its own.

Related: [#5445](https://github.com/fullsend-ai/fullsend/issues/5445) and
[#2388](https://github.com/fullsend-ai/fullsend/issues/2388) — `/fs-cancel` gains a second
implementation as a "stop" verb on this arm; [#2399](https://github.com/fullsend-ai/fullsend/issues/2399) —
the watcher replaces stale-head re-dispatch;
[#459](https://github.com/fullsend-ai/fullsend/issues/459) — the session-id capture this needs is
the same one a local resume needs.
