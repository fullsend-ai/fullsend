---
title: "119. Learn of later events by listing the platform's own run records"
status: Accepted
relates_to:
  - platform-nativeness
  - agent-infrastructure
topics:
  - dispatch
  - concurrency
  - runtime
---

# 119. Learn of later events by listing the platform's own run records

Date: 2026-09-18

## Status

Accepted

## Context

[ADR 0113](0113-steer-the-running-agent-on-work-item-updates.md) decided that a run in flight
absorbs updates to its own work item, and
[ADR 0118](0118-take-steer-authority-from-the-route-job.md) decided where an update's authority
comes from. Neither says how a run already executing inside a CI job finds out there is one, and
the job has no inbound path: GitHub Actions cannot deliver input to a running job.

[ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md) bounds the answer. Its
Decision states that "`fullsend run` does not poll for later events or invoke another run itself",
and it rejected polling for later events inside `fullsend run` because that needs input drivers
with race-safe cursors and moves scheduling into the execution command. ADR 0113 revisits that
clause and delegates the mechanism here, so this ADR does not reopen it: it decides how a run
learns the item moved, inside ADR 0106's limits.

## Options

### The platform pushes input to a running job

Unavailable: GitHub Actions exposes no API that writes to a job already running.

### The runner polls forge events with input drivers and cursors

The option ADR 0106 rejected. Reading the event stream needs a driver per forge, a race-safe
cursor per driver, normalization, and its own authorization pass.

### A courier job, or a relay through the mint

Both add a component to deploy, credential and keep available, and both re-implement the routing
predicate the shim's own `Route` job already evaluated.

### The agent polls the work item at the end of its run

The shape gh-aw uses. Advisory rather than deterministic: it lands only at the end, and only if
the model follows the instruction. Kept as the fleet-agent backstop.

### The runner lists its own shim's workflow runs

Every legitimate update already fires the shim, so its run records are a server-side log of what
happened since this run started. Reading them needs no driver, cursor, normalization or new
component.

## Decision

The runner learns of later events by listing the execution platform's own run records for its own
shim. Discovery is a read of records the platform already wrote, so ADR 0106's limits hold: no
input driver and no normalization; no cursor, because the list is re-derived from the run's own
start on every poll; every event still creates its pending run through the normal dispatch path;
and `fullsend run` invokes nothing and never extends its own timeout.

The listing uses the job token the stage already holds, filtered server-side to one workflow file
and to runs created at or after this run's start. No new credential or permission enters the stage.

Three things fail closed rather than degrade. A run resolves its own stage job from `GITHUB_JOB`,
then the harness slug, and declines when neither names exactly one in-progress job — so a
multi-agent fan-out steers only while the matrix job's name carries the agent's slug. It declines
when it cannot resolve
the forge logins it posts under, which are what stop it steering on its own output — not every
stage's output carries a marker to recognise it by. And it exports `FULLSEND_STEER_ACTIVE` only
once its watcher is running, because the agent definitions key the envelope's opening line on that
variable; absence is the default.

Polling is periodic and repeats immediately at every turn end, which is the moment a steer is
free. The run settles when a poll finds nothing conclusively new, not merely when a poll delivers
nothing: a poll that reached no verdict leaves the watch open for the next one. A failure that can
never succeed counts as a verdict — as does a candidate whose own run has finished, which is
pending only while that run is queued or in progress — and a bounded number of verdict-less polls
settles the run anyway, so nothing can hold a finished agent's sandbox to the deadline. Two bounds apply:
the platform keeps one pending run per concurrency group, and the runner spends at most
`steer.max_steers` per run.

Delivery is at-least-once and state-based. An update the run never sees falls to the pending run
behind it. What reaches the agent is the item's current state — the head now, and the activity
since the run's baseline — never a per-run head, because a run record says that something
happened, not what the item looked like.

The human path needs no command of its own. Every legitimate update already produces a shim run,
so the existing stage commands already reach a run in flight, and `fullsend steer <url> "<text>"`
posts one from a workstation. There is no `/fs-steer`.

Cadence defaults, stage-job resolution, settle and the run-budget ceilings are in
[steering.md](../contributing/steering.md).

**Direction.** Today's delta is built host-side by reading the item back from the forge. Under the
entity-context staging proposal (ADR 0107, unmerged at the time of writing) an accepted follow-up
run would trigger a re-stage instead, and the envelope would carry record keys and the new head
rather than bodies. Recorded as an option not taken: accepting third-party App review runs as
context-only notifications, which rests on ADR 0118's context-versus-amendment rule.

## Consequences

- Discovery costs one listing request per poll, a jobs read per unseen candidate and an item read
  per accepted one, all against the job token's own budget.
- Steering is unavailable on a platform that exposes no run-records API, so GitLab is not wired.
- Every Actions run in the repository is retitled, because the shim must declare a `run-name` for
  the `issue_comment` and `issues` runs that carry no `pull_requests[]`; a comment-triggered run
  also carries its comment id, so a command pairs to its run exactly rather than by timestamp.
- A dispatch change that renames or fans out stage jobs degrades to ADR 0106 behaviour rather than
  misbinding.
- Until a run records what it absorbed, the pending run behind it redoes the work, so absorbing an
  update costs more than cancelling would.
