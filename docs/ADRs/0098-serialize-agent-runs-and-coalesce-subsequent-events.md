---
title: "98. Serialize agent runs and coalesce subsequent events"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
  - security-threat-model
topics:
  - agents
  - dispatch
  - concurrency
  - events
---

# 98. Serialize agent runs and coalesce subsequent events

Date: 2026-09-02

## Status

Accepted

## Context

Agent workflows currently use concurrency groups that cancel an in-progress run
when a later event triggers the same agent for the same issue, pull request, or
other subject. Cancellation wastes the inference and sandbox work already
performed. It also makes a burst of related events behave as competing
replacements: for example, several user comments may each cancel a run instead
of letting the agent finish and then consider the accumulated concerns.

The dispatch architecture already gives input drivers responsibility for
producing forge-neutral `NormalizedEvent` values and gives each harness a CEL
`trigger` over those values ([ADR 0061](0061-harness-cel-dispatch.md)). The
security threat model also identifies event coalescing as a defense against
resource amplification
([security-threat-model.md](../problems/security-threat-model.md#threat-6-denial-of-service-dos--resource-exhaustion)).

## Options

### Continue cancelling the active run

The newest event takes priority immediately, but completed work is discarded
and bursts can repeatedly consume tokens without producing a result.

### Queue every triggering event

No event is discarded, but a burst produces redundant runs whose inputs and
effects may substantially overlap.

### Finish the active run and coalesce later events

The first event starts work immediately; later events are considered together
after the run, trading immediate reaction to each event for bounded, useful
follow-up work.

## Decision

Adopt finish-and-coalesce scheduling for automatic agent triggers. For a given
harness and normalized event subject, the first matching event starts a run.
Later events do not cancel or queue another run while that run is active.
Explicit user or operator cancellation remains available and is outside this
policy.

`fullsend dispatch` remains responsible for normalizing and authorizing the
initial event, evaluating harness triggers, and selecting runs. It MUST pass the
initial `NormalizedEvent` to `fullsend run` as a first-class input. This becomes
the canonical event input for execution; the existing legacy `event_payload`
protocol MAY remain alongside it during migration for agents that do not yet
use CEL dispatch.

Input drivers MUST provide an operation that accepts the event that started a
run and returns later events for the same subject as `NormalizedEvent` values.
The execution loop within `fullsend run` calls this operation after the sandbox
and runtime reach a terminal state. It applies the same platform authorization
gate used for initial dispatch before evaluating the current harness's CEL
`trigger` against each returned event. If any authorized event matches,
`fullsend run` selects the newest matching event, resolves the harness and its
CEL-guarded overlays against that event, and invokes a fresh sandbox and runtime
run. Events coalesced into that follow-up do not each receive their own run.

`fullsend run` repeats this check after each follow-up run, subject to a
platform-enforced maximum number of consecutive runs. Reaching the maximum
stops automatic continuation and produces an observable limit-exhausted result;
it does not cancel the run that reached the limit. Drivers MUST define stable
subject identity and event ordering so the check cannot move backwards or
silently cross between subjects.

## Consequences

- Agent work already in progress completes, and bursts produce at most one
  follow-up run at a time, reducing token and sandbox waste.
- A follow-up run sees the newest triggering context and may use different
  harness overlays from the preceding run.
- Input drivers must support ordered, race-safe retrieval of later events for a
  subject; supporting current GitHub-event agents therefore requires GitHub
  poll drivers.
- `fullsend run` must receive the initial normalized event through a canonical
  input protocol, while the legacy event payload may coexist during migration.
- Follow-ups retain dispatch authorization guarantees but are delayed and
  bounded, so limit exhaustion can leave work for a later trigger.
