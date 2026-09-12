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

### Serialize every triggering event

No event is discarded, but a burst produces redundant runs whose inputs and
effects may substantially overlap.

### Poll for later events within `fullsend run`

An execution loop can retrieve, authorize, and coalesce later events after each
run, with portable ordering and a deterministic follow-up limit. It requires
every input driver, including GitHub, to support polling and race-safe cursors,
and moves scheduling and repeated invocation into the execution command.

### Preserve the active run and coalesce pending runs

The first event starts work immediately. The execution platform retains one
pending run for the newest matching event while the agent is active, and the
next agent run reconciles all current concerns on the subject.

## Decision

Adopt preserve-and-coalesce scheduling for automatic agent triggers. Every event
still follows the normal input-driver normalization, authorization, harness
selection, and CEL trigger path. A matching run enters a concurrency group keyed
by harness and stable normalized-event subject. An event that fails
authorization or does not match the harness trigger creates no pending run.

The execution platform MUST allow the active run to finish and coalesce later
matching events into one pending run representing the newest retained event.
GitHub Actions provides these semantics with a subject-scoped concurrency group,
`cancel-in-progress: false`, and its default single-pending queue
([GitHub concurrency](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency)).
All workflow layers that share responsibility for agent concurrency MUST use
compatible groups and cancellation settings. Integrations for platforms without
equivalent semantics MUST emulate them outside the agent execution process.

Each agent run MUST reconcile the subject's current state rather than assume the
triggering event describes all outstanding work. The retained event may still
select harness overlays and provide immediate context, but `fullsend run` does
not poll for later events or invoke another run itself. A pending follow-up is a
separate platform execution and does not extend the active run's timeout window.
Explicit user or operator cancellation remains available and is outside this
policy.

Dispatch authorization covers the event that creates a run, not every comment
or other piece of subject state the agent may read while reconciling. This
decision does not authorize agents to treat arbitrary subject content as
commands. How content provenance and actor authority constrain agent behavior is
deferred to a separate ADR; existing deterministic authorization and input
security controls remain in force.

## Consequences

- Agent work already in progress completes, and bursts produce at most one
  follow-up run at a time, reducing token and sandbox waste.
- GitHub Actions can implement the policy without a poll driver or a new
  `fullsend run` event protocol; other platforms may require extra coordination.
- Agents must inspect current subject state, while transient intermediate events
  that leave no durable state may be lost.
- Trigger authorization remains deterministic, but authority over other content
  discovered during reconciliation requires a future decision.
- Per-run infrastructure timeouts remain effective, but a single pending slot
  does not bound consecutive runs; sustained triggering still requires rate,
  cost, or loop circuit breakers.
