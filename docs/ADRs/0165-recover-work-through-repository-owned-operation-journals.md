---
title: "165. Recover work through repository-owned operation journals"
status: Accepted
relates_to:
  - durable-work-recovery
  - agent-architecture
  - agent-infrastructure
topics:
  - coordination
  - recovery
---

# 165. Recover work through repository-owned operation journals

Date: 2026-11-10

## Status

Accepted

## Context

Per-subject serialization ([ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md))
and occurrence-aware poll state
([ADR 0132](0132-occurrence-aware-shared-poller-state.md)) reduce duplicate
dispatch. They do not reserve an operation across scheduling, worker failure
and external publication. Runtime workflow packaging in
[#8225](https://github.com/fullsend-ai/fullsend/pull/8225) addresses another
boundary: delivering a sequence to an agent runtime.

[Durable work recovery](../problems/durable-work-recovery.md) describes the
failure cases. This ADR aims to keep admitted work recoverable across hosts
without making a model or a central daemon the coordination authority.

## Options

Integration-owned databases offer flexibility but repeat the same recovery
controls. A mandatory workflow service adds an operated dependency. A
repository-owned journal reuses forge state and existing schedulers, at the
cost of conditional writes and explicit reconciliation.

## Decision

Fullsend adopts a durable saga journal stored as versioned JSON files in the
control repository. A saga records progress across separate external actions
and recovers individual steps without claiming an atomic transaction across
services. It covers admission, target resolution, dispatch, execution,
publication and accounting for every required target, including multiple
branches. Domain dispatch and assessment semantics remain in adapters.

The journal retains stable operation identities, input bindings, work bounds,
effect intents and separate scheduling, execution and publication receipts.
Conditional updates, integrity checks and ownership fencing prevent competing
or superseded workers from accepting results or admitting new effects. An
external request admitted before ownership loss remains in-flight until its
outcome is reconciled.

Trusted host adapters persist intent before external actions and confirm
receipts after verified acceptance. They reconcile ambiguous outcomes before
retrying; an empty eventually consistent search does not prove a write failed.
Retained validated evidence permits publication recovery without repeating
inference, subject to current source, authorization and policy checks. Missing,
exhausted or unresolved targets remain visible and cannot imply completion.
Domain adapters define completion and any compensating actions.

Shared trusted journal operations are consumable from pre/post scripts and
reconcilers. Incremental checkpoints and periodic reconciliation provide
recovery when execution stops. CI cleanup supplies a best-effort checkpoint;
it is not the recovery guarantee. This is a deterministic host capability,
not a coordinator agent.

Only authorized trusted host code writes the journal. Assessment workers
receive admitted context without journal-write credentials. Journal state
contains bounded identities, digests and receipts; artifact publication policy
still applies. Journal contents grant no authority, and integrity failure
stops scheduling and publication rather than resetting state and repeating work.

## Entity state and journal state

[ADR 0098](0098-entity-first-harness-evaluation.md) and
[ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md) retain business and
handled state on the entity. Trackers hold applicability, human workflow status
and assessment handoffs. Entity-first discovery must still find eligible work
whose trigger was lost before admission; a journal cannot recover unrecorded work.

The journal records mechanical progress of admitted work. It adds no separate
business handled-state or revision-deduplication gate. Existing actor, entity,
disclosure and repository checks remain authoritative. Journal storage does
not require one tracker per branch; tracker-first dispatch is a domain decision
tracked separately in [#9260](https://github.com/fullsend-ai/fullsend/issues/9260).

## Forge scope

Execution host, journal storage, source forge and tracker are independent
bindings. The initial journal backend is the GitLab control repository, which
can coordinate GitHub, GitLab or Forgejo source repositories and Jira trackers.
Source repositories need no journal or journal-write credential.

The same journal contract applies to GitHub and Forgejo control repositories;
their storage adapters require conformance before enablement. Source and
publication adapters retain their own authorization and enablement requirements.
Jira issue properties do not satisfy the journal's conditional-update contract.

## Consequences

- The full saga can recover individual branches and external effects without
  keeping a host or model session alive.
- Control-repository history makes state inspectable and avoids a mandatory
  central service, at the cost of commit volume, retention and compaction.
- Conditional updates, ownership fencing and ambiguous-effect reconciliation
  require backend conformance; delivery is not claimed to be exactly once.
- Shared script operations and periodic reconciliation are both required.
  Assessment workers retain their existing source-access boundaries.
- Tracker-state reconciliation and publish-only retry can ship independently
  without narrowing the saga's scope.

Implementation layout, script interfaces, credentials, reconciliation schedules
and detailed acceptance tests belong to
[#9253](https://github.com/fullsend-ai/fullsend/issues/9253). This ADR records
the decision; the journal capability is not yet implemented.
