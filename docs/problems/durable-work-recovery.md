---
title: Durable work recovery
---

# Durable work recovery

In the current fullsend architecture, agents run as independent jobs, with
[the repository acting as coordinator](agent-architecture.md#interaction-model-the-repo-as-coordinator).
Work can span several jobs and external systems. Interruptions and overlapping
jobs create distributed failure scenarios that need to be addressed.

## Failure scenarios

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

Recovery needs to track attempts across jobs and enforce retry limits. Choosing
whether a retry should use a different model or strategy is a separate question
in [cross-run escalation](cross-run-memory.md#error-recovery-and-retry-mechanics).
The [infrastructure discussion](agent-infrastructure.md) considers the execution
capacity needed to resume work.

## Options and trade-offs

### Let each integration own its recovery

An adapter can maintain a database and reconcile its own jobs and external
effects. This is flexible and avoids a new platform contract. Each adapter
must also implement authorization rechecks, duplicate prevention, restart
handling and publication recovery. Guarantees can differ across workflows.

### Extend repository-owned state

A journal in a control repository can use versioned JSON files, conditional
forge writes and short-lived reconcilers. Storage need not share the forge of
the source code or tracker. Existing schedulers still run individual workers. This fits the
repository coordination model and avoids a required central service. It needs
explicit trust, retention, conflict, lease and external-effect protocols;
eventual-consistency searches cannot establish that an ambiguous write failed.

### Use an opt-in local stage engine

A local engine can store accepted outputs and enforce the order of stages in a
workflow. Integrations can opt in without requiring a shared service. Recovery
on another host also needs a way to transfer durable state and reject stale
owners. Stage ordering alone does not reconcile external writes whose outcome
is unknown. A workflow can combine stage gating with external-action recovery.

### Delegate to a durable workflow service

A service can supply timers, retries and ownership. It introduces another
operated component and trust boundary. Its scheduler guarantees still do not
make a remote issue creation atomic or authorize a retry after scope changes.

## Decision and acceptance

[ADR 0165](../ADRs/0165-recover-work-through-repository-owned-operation-journals.md)
chooses JSON journal files in the control repository, initially hosted on
GitLab. The journal covers the full saga and each required branch, while
source repositories and trackers can use other providers. Workflow-supplied
trusted host adapters claim work and use shared journal operations to record
progress and reconcile unfinished actions. Assessment runs and their scripts
do not receive journal-write credentials. CI finalization is a best-effort
checkpoint, not the recovery guarantee. Its implementation remains separate
from the decision. A local database test is not distributed acceptance.

Entity-first discovery must also find eligible work whose trigger was lost
before admission. Trackers retain business and handled state; the journal
records mechanical progress after admission. Publish-only retry from retained
validated evidence can be implemented independently of the complete journal.

Fault tests must stop both sides of each handoff, overlap reconcilers, expire
ownership while a worker is still alive, and supersede a plan before
publication. Kill a runner before its finalizer, interrupt journal writes and
race multi-record updates; only a valid conditional transition may win. A
GitLab control repository must demonstrate recovery with GitHub source and
Jira publication. Journal writes must not trigger recursive agent runs. A remote write that succeeded before its acknowledgement was
lost must be reconciled without rerunning inference or blindly repeating the
write. Every required target remains accounted for, including exhausted or
missing attempts. A domain adapter may publish an investigation request when
analysis fails; that request is not an assessment or permission to remediate.

## Open questions

- Which bounded JSON layout, integrity checks, retention and compaction rules
  preserve history and conditional transitions in the control repository?
- Which trusted host identity writes the protected GitLab journal branch, and
  how are its credentials kept separate from assessment runs?
- How should workflow adapters be implemented and packaged, and should fullsend
  ship reusable adapters? Shared journal integration is tracked in
  [#9253](https://github.com/fullsend-ai/fullsend/issues/9253); the tracker-first
  consumer integration is tracked in
  [#9260](https://github.com/fullsend-ai/fullsend/issues/9260).
- Which shared script interface and reconciliation schedule expose the saga
  without making CI finalizers a prerequisite for recovery?
- Which existing scheduler APIs can reconcile accepted-but-unacknowledged
  requests by stable operation identity?
- Which publication adapters can safely prove a write absent, and which must
  retain an ambiguous operation for human reconciliation?
- How should an operator repair corrupted or inaccessible state without
  discarding accepted work or permitting stale workers to publish?
