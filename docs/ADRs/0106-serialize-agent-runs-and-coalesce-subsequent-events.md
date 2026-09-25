---
title: "106. Serialize agent runs and coalesce subsequent events"
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

# 106. Serialize agent runs and coalesce subsequent events

Date: 2026-09-02

## Status

Accepted

[#6957](https://github.com/fullsend-ai/fullsend/issues/6957) is the origin
issue for the later steering work that builds on this preserve-and-coalesce
decision. Its validation criteria still describe `harness.steer` as off by
default; that criterion is superseded by the default-on decision in
[PR #7461](https://github.com/fullsend-ai/fullsend/pull/7461) (ADR 0121,
not on `main`). This ADR's decision is unchanged.

## Context

Agent execution currently uses platform-specific concurrency controls that may
cancel an in-progress run when a later event triggers the same agent for the
same issue, merge or pull request, or other subject. Cancellation wastes the
inference and sandbox work already performed. It also makes a burst of related
events behave as competing replacements: for example, several user comments
may each cancel a run instead of letting the agent finish and then consider the
accumulated concerns.

[ADR 0098](0098-entity-first-harness-evaluation.md) makes durable entity state
and activity available to harnesses that declare entity sources. Such harnesses
can tolerate skipped intermediate events by reconciling the entity on their next
run instead of depending on every transition being delivered. Event-only
harnesses cannot make the same assumption.

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
selection, and CEL trigger path. A matching run enters a platform serialization
scope keyed by harness identity, normalized target `repo`, and a canonical
subject identity. The subject identity consists of the resolved entity's
`entity.source.system`, canonical kind, and canonical ID. A `work_item` with
`entity.linked_change_proposal` uses `change_proposal` and the linked change
proposal's ID, so a comment on a GitHub pull request shares a scope with that
pull request's review and lifecycle events. Other work items use their own kind
and ID, and conversations remain a distinct kind. Consequently, equal numeric
IDs from GitHub and Jira cannot share a scope. Event-backed and
scheduled-discovery runs for the same resolved entity use the same subject
identity; the key never depends on an event-only field such as
`event.source.system`. An event that fails authorization or does not match the
harness trigger creates no pending run.

The execution platform MUST allow the active run to finish and coalesce later
matching events into one pending run representing the newest retained event.
All execution layers that share responsibility for serialization MUST use a
compatible key and active-run cancellation policy. Integrations that cannot
provide the complete invariant natively MUST emulate the missing behavior
outside the agent execution process.

On GitHub Actions, the serialization scope is a subject-scoped `concurrency`
group. Setting `cancel-in-progress: false` preserves the active run, while the
platform's single-pending behavior replaces an older pending run with the newest
([GitHub concurrency](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency)).
Fullsend's reusable GitHub workflows currently use subject-scoped agent-stage
concurrency groups with `cancel-in-progress: true`; implementing this decision
requires changing that setting to `false`. Layers that serialize the same work
MUST derive compatible harness-and-entity identities, but a synchronous
`workflow_call` caller and callee MUST retain distinct group-name prefixes so
the child does not wait on the parent that is waiting for it.

On GitLab CI/CD, a subject-scoped `resource_group` serializes agent jobs.
`workflow:auto_cancel:on_new_commit: none` and a non-interruptible agent job
preserve active work, while the resource group's `newest_first` process mode
selects the newest waiting pipeline next
([GitLab resource groups](https://docs.gitlab.com/ci/resource_groups/),
[GitLab auto-cancel](https://docs.gitlab.com/ci/yaml/#workflowauto_cancelon_new_commit)).
Fullsend already uses `fullsend-${STAGE}-${RESOURCE_KEY}` resource groups, sets
their process mode to `newest_first`, and configures `on_new_commit: none` when
it can do so without overwriting repository policy. GitLab nevertheless retains
all waiting jobs rather than coalescing them into one slot, so the GitLab
integration must cancel superseded waiters through the API or avoid creating
them in the dispatch path to satisfy this decision.

Each agent run MUST reconcile the subject's current state rather than assume the
triggering event describes all outstanding work. Reading current state does not
make all of its content actionable: a run MUST NOT treat content from an actor
who is unauthorized for the corresponding action as instructions. The retained
event may still select harness overlays and provide immediate context, but
`fullsend run` does not poll for later events or invoke another run itself. A
pending follow-up is a separate platform execution and does not extend the
active run's timeout window. Explicit user or operator cancellation remains
available and is outside this policy.

Dispatch authorization covers the event that creates a run, not every comment
or other piece of subject state the agent may read while reconciling. This
decision does not authorize agents to treat arbitrary subject content as
commands. How content provenance and actor authority constrain agent behavior is
deferred to a separate ADR; existing deterministic authorization and input
security controls remain in force.

## Consequences

- Agent work already in progress completes, and bursts produce at most one
  follow-up run at a time, reducing token and sandbox waste.
- GitHub Actions provides the full invariant natively; GitLab provides active-run
  preservation, serialization, and newest-first ordering but needs integration
  work to discard superseded waiting jobs.
- Agents must inspect current subject state, while transient intermediate events
  that leave no durable state may be lost.
- Trigger authorization remains deterministic, but authority over other content
  discovered during reconciliation requires a future decision.
- Per-run infrastructure timeouts remain effective, but a single pending slot
  does not bound consecutive runs; sustained triggering still requires rate,
  cost, or loop circuit breakers.
