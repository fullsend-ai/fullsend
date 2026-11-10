---
title: "167. Execute authorized multi-target plans"
status: Accepted
relates_to:
  - durable-work-recovery
  - agent-architecture
  - agent-infrastructure
topics:
  - dispatch
  - coordination
  - authorization
---

# 167. Execute authorized multi-target plans

Date: 2026-11-10

## Status

Accepted

## Context

[ADR 0061](0061-harness-cel-dispatch.md) dispatches matching harnesses and
supports matrix output. These mechanisms can launch several jobs, but do not
define the complete authorized target set for one request, independent target
contexts or collection of every required result. Per-subject serialization
([ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md)) also
needs to distinguish target work that shares one originating event.

[ADR 0165](0165-recover-work-through-repository-owned-operation-journals.md)
defines recovery across attempts and external effects. Target expansion and
collection need their own boundary; see
[durable work recovery](../problems/durable-work-recovery.md). This ADR aims to
execute a bounded, authorized target set without losing isolation or mistaking
partial results for complete coverage.

## Options

A handwritten CI matrix can launch jobs but leaves target identity and
collection rules to each workflow. A model-led coordinator can choose branches
and retries but cannot supply execution authority. A host-admitted execution
plan makes these boundaries explicit while reusing existing schedulers.

## Decision

Fullsend executes host-admitted, immutable multi-target plans to enforce
independent target execution and complete result accounting.

A trusted adapter proposes the target set from reviewed domain data. Before
dispatch, the host freezes a versioned plan containing stable target/action
identities, required targets, repository and branch/component scope, immutable
source revisions, input and policy revisions, pinned worker definitions and
finite concurrency, target-count, attempt and execution-time bounds. Source
selection is separate from the trusted revision supplying harnesses and
plugins. Discovery and model output cannot expand execution permissions.

The host checks the admitted actor's current access and each target action
under existing authorization and disclosure rules. A blocked target stays
visible and undispatched; it is not silently removed from required coverage.
Before launch, the host verifies the checkout against the admitted repository
and source revision. Every child receives a separate checkout, scoped context,
case store and
artifact namespace, bound to its plan, target, action and attempt. Scheduling
and concurrency keys include the target operation identity, so distinct targets
sharing an event or tracker do not cancel or coalesce one another. Retries of
the same operation use the recovery boundary in ADR 0165.

Existing dispatch and CI execution mechanisms launch the children under those
bounds. The host validates each result against its exact attempt and frozen
inputs before collecting it. Missing, invalid, failed, blocked and uncertain
results remain explicit. Collection is accounted for only when every required
target has a retained result or explicit terminal disposition; accounting is
distinct from successful assessment, publication and domain completion. A
failed or uncertain check may request investigation through an authorized
adapter without becoming an affected verdict or remediation permission.

Changed source, mapping, policy or required targets produce a superseding plan
revision. The host stops new work under the old revision and rejects its stale
results and new publication admissions. Adding a required target reopens
collection. Reuse of prior evidence requires explicit revalidation under the
new plan; supersession never silently resets consumed work bounds. Domain
adapters define discovery, result meaning and completion policy. This decision
does not prescribe a new CLI, queue service or agent coordinator.

## Forge scope

The plan contract applies to GitHub, GitLab and Forgejo targets. Each enabled
dispatch backend must preserve target identity, authorization, isolation and
result binding; unsupported combinations are blocked explicitly. Cross-forge
plans require conformance from every participating backend. Jira may identify
the originating request without becoming execution authorization.

## Consequences

- One request can assess multiple branches independently while retaining the
  exact source and scope used for each result.
- Existing matrix execution remains reusable, but rows and concurrency groups
  must preserve admitted target identities.
- Partial collection stays visible and cannot imply complete coverage or
  successful remediation.
- Domain adapters retain branch-discovery and assessment semantics while
  Fullsend enforces execution boundaries.
- Implementation must define the versioned plan/result interface and prove
  isolation, bounds and supersession before enabling multi-target execution.
