---
title: "147. Evaluate entity CEL over selectively acquired data"
status: Accepted
relates_to:
  - agent-architecture
  - operational-observability
topics:
  - authorization
  - cel
  - entities
  - polling
---

# 147. Evaluate entity CEL over selectively acquired data

Date: 2026-10-22

## Status

Accepted

Partially supersedes [ADR 0098](0098-entity-first-harness-evaluation.md)'s
pre-CEL enumeration, per-element permission refresh, and full audit roster. It
also replaces [ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md)'s
full-entity due checks and full-history audit roster. Their trusted-origin,
process-lifecycle, and model-bound staging rules remain. Actor-role reuse is
defined by [ADR 0148](0148-reuse-verified-actor-roles-for-entity-cel.md), and
actor admission by [ADR 0151](0151-harness-human-role-minimums.md).

## Context

[ADR 0098](0098-entity-first-harness-evaluation.md) and
[ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md) require complete
entity history before entity-source CEL evaluation. That can cause unrelated
provider reads for state-only or short-circuiting predicates. Progressive
evaluation can also repeat role lookups for the same actor. Entity-source CEL
remains disabled pending the [normalized-entity contract tracked by issue
#8420](https://github.com/fullsend-ai/fullsend/issues/8420), which must define
partial-data and coverage semantics. Existing event-only routes are unchanged.

## Options

### Fetch all history before CEL

This makes missing history easy to detect but requires reads and audit records
for categories the predicate never uses.

### Fetch decision-relevant data progressively

This avoids unrelated reads, but requires explicit partial-data semantics and
proof that a result cannot change when more data is fetched.

## Decision

1. Keep the platform's action-indicating categories closed. Acquire only fields
   and history that could affect the compiled predicate, using a conservative
   plan and only sound provider filters.
2. Return `true` or `false` only when fetched facts and proven coverage establish
   that result for every complete entity consistent with the evidence. Otherwise
   return `UNKNOWN` and fetch more relevant data or leave the candidate
   unresolved. Missing, unavailable, inconsistent, or exhausted evidence is
   never treated as false or empty.
3. Fetch, authorize, and audit only decision-relevant facts. Unrelated history is
   neither fetched nor listed, and provider aggregates affected by excluded
   records cannot enter the authorized view unchanged.
4. Record bounded decision evidence—query scopes, coverage, and witness
   provenance—as specified in [Authorization Contract v2](../normative/authorization/v2/README.md).

[ADR 0151](0151-harness-human-role-minimums.md) defines actor admission and
human-minimum rules. [ADR 0148](0148-reuse-verified-actor-roles-for-entity-cel.md)
defines verified-role reuse. [ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md)
continues to define the bound-run lifecycle. Authorization Contract v2
specifies the evaluation, audit, and incomplete-candidate retry rules under
[ADR 0015](0015-normative-specifications-directory.md); it does not change
legacy non-CEL, non-entity workflows.

## Forge scope

This applies to GitHub, GitLab, Forgejo, and Jira entity-source harnesses.
Each adapter must establish sound coverage and provider-backed actor roles
for the facts it supplies; unsupported provider queries or pagination leave
the decision unresolved. Existing event-only dispatch on every forge is
unchanged.

## Consequences

- State-only and short-circuiting predicates can avoid unrelated provider
  reads, permission lookups, and audit volume.
- Negative and aggregate predicates can still require all relevant history;
  provider limits may leave a check unresolved and prevent sandbox launch.
- Adapters and the normalized-entity contract must represent partial coverage
  and source consistency, including raw state versus actor-filtered action
  facts, before entity-source CEL can ship.
- Audit consumers must use bounded decision evidence; omitted actors' roles
  cannot be reconstructed from that record without separate provider logs.
- An unauthorized forge update may defer a staleness check based on raw
  `updated_at`; a harness needing last-authorized-update semantics must acquire
  and authorize the relevant history.
