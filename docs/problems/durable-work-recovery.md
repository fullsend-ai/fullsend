---
title: Durable work recovery
---

# Durable work recovery

A job can disappear after discovery, finish after its caller stops, or publish
an external change before its acknowledgement is saved. A successful model
invocation does not answer whether the requested work was scheduled, which
attempt owns it, or whether publication must be retried.

The [repository coordination model](agent-architecture.md#interaction-model-the-repo-as-coordinator)
keeps authority in repository-visible controls. Recovery must preserve that
boundary. It needs deterministic bookkeeping, rather than an agent deciding
which other agents should run. The
[infrastructure discussion](agent-infrastructure.md) also separates execution
capacity from waiting for people or external systems.

## Failure boundaries

- A dispatcher stops after a remote scheduler accepts a request but before it
  records the scheduler's run identifier.
- Two hosts discover the same operation while neither has reserved it.
- A worker's lease expires; its replacement starts while the old worker is
  still running.
- A worker saves valid evidence, but its publication request times out after
  the external system creates the issue or comment.
- One required child never returns while other children succeed.
- Source, authorization or a target plan changes while work is queued.

Serialization for a harness and subject reduces overlap. It does not make
dispatch, execution and external writes one transaction. A source checkpoint
or the absence of a running CI job cannot prove completion. Operators need to
distinguish these conditions from ordinary agent failures; see
[operational observability](operational-observability.md#what-just-happened).

Persistent attempt bounds supply recovery mechanics, while
[cross-run escalation](cross-run-memory.md#error-recovery-and-retry-mechanics)
also needs failure interpretation and policy. A journal does not decide whether
a different model strategy is likely to succeed.

## Options and trade-offs

### Let each integration own its recovery

An adapter can maintain a database and reconcile its own jobs and external
effects. This is flexible and avoids a new platform contract. Each adapter
must also implement authorization rechecks, duplicate prevention, restart
handling and publication recovery. Guarantees can differ across workflows.

### Extend repository-owned state

A repository-scoped journal can use conditional forge writes and short-lived
reconcilers. Existing schedulers still run individual workers. This fits the
repository coordination model and avoids a required central service. It needs
explicit trust, retention, conflict, lease and external-effect protocols;
eventual-consistency searches cannot establish that an ambiguous write failed.

### Delegate to a durable workflow service

A service can supply timers, retries and ownership. It introduces another
operated component and trust boundary. Its scheduler guarantees still do not
make a remote issue creation atomic or authorize a retry after scope changes.

## Dispatching a complete target set

Multiple CI jobs are an execution mechanism, not a definition of coverage.
When one request maps to several maintained branches, each branch needs a
separate source binding and work identity. A concurrency key based only on
the originating issue can incorrectly serialize or coalesce distinct targets.
A successful child does not account for another child that never started.

A handwritten matrix keeps these rules local to its workflow. A generic
host-admitted plan can reuse existing matrix execution while enforcing target
scope, finite bounds and result correlation. A model choosing the target set
cannot grant authority to execute it.

[ADR 0167](../ADRs/0167-execute-authorized-multi-target-plans.md) chooses the
plan boundary. ADR 0165 supplies its recovery mechanics. Domain adapters
still discover maintained targets and decide what their results mean.
Acceptance must include distinct branches sharing one event, a wrong-target
result, an exhausted target, a missing child, and a new plan that adds a
required branch while an old worker is still running. Accounted failure is
visible failure, not successful assessment or publication.

## Decision and acceptance

[ADR 0165](../ADRs/0165-recover-work-through-repository-owned-operation-journals.md)
chooses a repository-owned journal. Its implementation remains separate from
the decision. A local database test is not distributed acceptance.

Fault tests must stop both sides of each handoff, overlap reconcilers, expire
ownership while a worker is still alive, and supersede a plan before
publication. A remote write that succeeded before its acknowledgement was
lost must be reconciled without rerunning inference or blindly repeating the
write. Every required target remains accounted for, including exhausted or
missing attempts. A domain adapter may publish an investigation request when
analysis fails; that request is not an assessment or permission to remediate.

## Open questions

- Which journal layout, signing rules, retention and compaction bounds meet
  the conditional-write and integrity requirements on each forge?
- Which existing scheduler APIs can reconcile accepted-but-unacknowledged
  requests by stable operation identity?
- Which publication adapters can safely prove a write absent, and which must
  retain an ambiguous operation for human reconciliation?
- How should an operator repair corrupted or inaccessible state without
  discarding accepted work or permitting stale workers to publish?
