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

Fullsend adopts repository-owned operation journals to recover work across
dispatch, execution and publication.

The trusted host records work before dispatch in repository-scoped forge
storage with integrity checks and compare-and-swap updates. An admitted plan
defines stable operation identities, required children and persistent work
bounds. Attempts retain their own identity, source/input revisions and
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

## Forge scope

The contract applies to GitHub, GitLab and Forgejo target repositories.
Each backend must demonstrate conditional updates and the same recovery
tests before enablement. Initial implementation may enable GitHub alone;
GitLab and Forgejo remain disabled until their adapters conform. Jira may be
a source or publication destination; its unconditional issue-property writes
do not satisfy the journal's compare-and-swap requirement.

## Consequences

- Recovery can resume scheduling or publication without keeping a host or
  model session alive.
- Existing forge storage and CI schedules remain the coordination substrate.
- Backend integrity, ownership and ambiguous-write tests become enablement
  gates; delivery is not claimed to be exactly once.
- Adopters must define target identity and completion rules while Fullsend
  enforces the shared recovery boundary.
- Journal layout, credentials, retention and adapter implementation require
  follow-up work before this capability is available.
