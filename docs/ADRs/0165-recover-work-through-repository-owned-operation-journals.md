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
and recovers each step without claiming one atomic transaction across services.
It covers admission, target resolution, dispatch, execution, publication and
accounting for every required target. Multi-branch assessment remains in scope.

The initial deployment stores the journal in the GitLab control repository
that hosts Fullsend execution. That repository may assess and fix code hosted
on GitHub, GitLab or Forgejo, and publish findings to Jira. Source repositories
need no journal branch or journal-write credential.

The trusted host records work before dispatch in the control repository,
with integrity checks and compare-and-swap updates. The journal
retains stable operation identities and persistent work bounds. Multi-target
dispatch and collection are tracked separately in
[#9260](https://github.com/fullsend-ai/fullsend/issues/9260). Attempts retain
their own identity, source/input revisions and
scheduler/run identifiers. Scheduling acceptance, worker completion and each
publication effect have separate receipts. Loss of an acknowledgement leaves
recoverable pending work rather than advancing a completion checkpoint.

Short-lived reconcilers use leases and monotonically increasing ownership
generations. Every result acceptance, state transition and publication
admission checks the current generation and plan. A timed-out or superseded
worker cannot authorize a new effect. A publication admitted before ownership
loss remains an in-flight effect to reconcile; expiry cannot revoke a request
already accepted by an external service. Remote writes remain in trusted host
adapters, outside worker sandboxes.

Publication adapters persist intent before writing, use a stable external
operation marker, and reconcile ambiguous outcomes before retrying. An
eventually consistent search returning no match is not proof a create failed.
If the adapter cannot establish the outcome, it holds publication for operator
resolution. It can retry publication from retained validated evidence without
rerunning inference, subject to current authorization, source and policy.
Missing, exhausted or unresolved required children remain visible and cannot
imply completion. A configured domain adapter may request investigation after
failure, but cannot turn that request into a successful worker assessment.

This is a deterministic host capability. It adds neither a coordinator agent
nor authority from journal contents. Existing actor, entity, disclosure and
repository gates still apply. Domain mapping, assessment semantics and ticket
rendering remain adapters. Journal records contain bounded identities, digests
and receipts, not raw private prompts or credentials. Artifact publication
policy still applies. Journal integrity failure stops scheduling and
publication; it does not silently reset state and repeat work.

## Storage and writer boundary

Use a dedicated protected journal branch in the control repository, separate
from workflow configuration and source branches. Bounded, schema-versioned
JSON records identify the saga, required targets, plan and input digests,
attempts, ownership generations, effect intents and receipts. Large validated
outputs remain retained artifacts referenced by immutable identity and digest.
Journal commits must not recursively trigger ordinary agent pipelines.

Only an explicitly authorized trusted host identity writes the journal.
Read-only assessment workers receive admitted context without journal-write
credentials. Source-read, code-publication and journal-write permissions are
separate; a source checkout does not grant control-repository access. The
implementation must define credential routing and protected-branch permissions
before enablement. GitLab writer selection must use the trusted registered-role
contract; an administrator may register a dedicated responsibility identity.
This decision does not expand the built-in Analyst role's repository-write
permissions. Editable tracker properties are not ownership authority.

A state change commits against the exact journal revision read by the writer.
The backend must reject a competing update. On conflict, reload state and
re-evaluate ownership, inputs and the proposed transition. Automatically
merging JSON, force-pushing, or recreating a missing journal branch cannot
resolve an ownership conflict. The GitLab implementation must demonstrate
conditional updates of the complete transition and integrity under concurrent
writers. A per-file check alone is insufficient for a transition that changes
several records. GitHub and Forgejo storage adapters follow the same contract.

## Script consumption and recovery

Expose shared trusted journal operations to pre-scripts, post-scripts and
short-lived scheduled reconcilers. These operations claim work, record intent,
accept input-bound evidence and confirm an effect through the same ownership
and transition checks. Consumers can condition actions on durable saga state
without implementing separate lease or receipt rules in each script. These
are capability requirements, not existing CLI commands or harness fields.

Persist an intent before each external action and its receipt after verified
acceptance. Retain validated worker evidence before publication. A later
invocation can retry publication without inference, subject to current inputs,
authorization and unresolved-effect checks. Independent publish-only recovery
can be delivered without waiting for the complete saga implementation.

A CI finalizer, such as GitLab's `after_script`, makes a best-effort final
checkpoint from verified local evidence. It cannot promise a write after runner
loss, hard termination, timeout or journal outage. It must not mark an operation
complete merely because the job exited. Scheduled reconciliation reads pending
intents and receipts even when the finalizer never ran. Missing acknowledgements
remain unresolved until external acceptance can be established.

Each required branch has independent attempts and receipts. A missing result,
exhausted retry or unresolved publication keeps that target visible. Domain
adapters define cancellation or compensating actions where appropriate, and
forward recovery where an external action cannot safely be undone. Recording a
compensation does not erase its original effect or authorize automatic issue
closure, deletion or code rollback.

## Entity state and journal state

[ADR 0098](0098-entity-first-harness-evaluation.md) and
[ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md) keep business and
handled state on the entity. Trackers retain applicability, human workflow
status and validated assessment handoffs. Entity-first discovery remains
responsible for finding eligible work, including a lost trigger that never
admitted an operation. A journal cannot recover work it never recorded.

The journal records mechanical progress of admitted work: ownership, attempts,
step intents, input bindings and scheduling or publication receipts. It adds
no independent revision-deduplication or business handled-state gate. A bound
receipt must still pass the current entity and policy checks before use.
Tracker-first branch investigations and their assessment semantics remain the
multi-target adapter's decision; journal storage does not require one Jira
issue per journal record.

## Forge scope

Execution host, journal storage, source forge and tracker are independent
bindings. The initial journal backend is the GitLab control repository; a
GitHub source repository or Jira tracker does not need a matching journal
backend. GitLab storage must pass the recovery acceptance tests before use.
GitHub and Forgejo control-repository storage remain disabled until their
adapters pass the same tests. Source and publication adapters retain their own
authorization and enablement requirements.

The capability contract covers GitHub, GitLab and Forgejo without requiring
every deployment to use the source forge for storage. Jira may be a source or
publication destination. Its unconditional issue-property updates do not
satisfy this journal's compare-and-swap requirement.

## Consequences

- The full saga can recover individual branches and external effects without
  keeping a host or model session alive.
- JSON history in the control repository supplies inspectable durable state;
  it adds commit volume, retention and compaction responsibilities.
- A GitLab control repository can coordinate GitHub source and Jira trackers
  without a journal in each source repository.
- Shared script operations and periodic reconciliation are both required;
  best-effort finalization alone cannot provide crash recovery.
- Conditional updates, integrity, ownership and ambiguous-write tests are
  enablement gates; delivery is not claimed to be exactly once.
- Domain adapters retain target identity, completion and compensation rules.
  Tracker-state reconciliation and publish-only retry can ship independently.
- JSON layout, credential provisioning, retention and adapter implementation
  require follow-up work before this capability is available.
