---
title: "143. Pre-read fixed per-kind entity profiles for scheduled discovery"
status: Accepted
relates_to:
  - entity-source-discovery
  - agent-architecture
topics:
  - cel
  - entities
  - polling
  - dispatch
---

# 143. Pre-read fixed per-kind entity profiles for scheduled discovery

Date: 2026-10-19

## Status

Accepted

## Context

[ADR 0098](0098-entity-first-harness-evaluation.md) evaluates one harness CEL
predicate over a required `entity` and a nullable `event`. During scheduled
discovery `event` is null, so everything the predicate reads comes from
provider reads. ADR 0098 requires a query-planning protocol before
entity-first execution is enabled, and
[#8421](https://github.com/fullsend-ai/fullsend/issues/8421) requires an
acquisition strategy to be decided before field acquisition is implemented.

The [entity-source discovery](../problems/entity-source-discovery.md)
requirements record compares four strategies and lists safety invariants that
hold for all of them. Under ADR 0098, Fullsend must enumerate the complete
action-indicating superset, with each actor's current permission, before any
predicate runs. That read floor does not depend on which fields the predicate
uses, and it dominates per-candidate cost. A strategy can therefore save reads
only on fields outside the floor. Today `EvaluateTrigger` in
`internal/harness/trigger.go` evaluates CEL over a fully materialized value,
with no provider I/O during evaluation.

## Options

**A. On-demand loading during evaluation.** This option reads only what the
predicate touches. However, it does network I/O inside evaluation, loses
batching across candidates, and makes evaluation impure. CEL's
error-absorbing `&&` and `||` can also return a match after a failed load.

**B. Static dependency analysis, then planned reads.** This option keeps
evaluation pure and reads batchable, but needs a read planner and
over-approximates dynamic access. Its savings apply only outside the
mandatory floor.

**C. Hybrid staged partial evaluation.** This option can decide cheap
candidates early. However, it adds a second evaluation path whose soundness
must be proved and audited.

**D. Fixed per-kind profile.** Fullsend reads a bounded, platform-defined
profile per entity kind and provider before evaluation, and harness
validation rejects predicates that read outside the profile. It is the
simplest option to prove complete, but it over-reads for predicates that use
little beyond entity state.

## Decision

Adopt option D for scheduled entity discovery.

For each supported entity kind and provider, Fullsend defines a fixed, bounded
read profile. A profile contains the identity and state fields, the mandatory
action-indicating superset with current-revision actor provenance and current
permission, and any further fields the platform includes. Fullsend acquires
the whole profile for a candidate before evaluating any predicate. It then
evaluates CEL over a fully materialized, read-only `entity`. No provider read
happens during CEL evaluation.

When harness entity sources are validated, a static check confirms that every
`entity` access in the predicate stays within the profile of each declared
entity kind. If an access falls outside the profile, or the checker cannot
show that it stays inside, the entity sources are malformed. Scheduled
evaluation for that harness is then denied, as ADR 0098 requires for
malformed sources.

Fullsend records acquisition coverage outside the CEL value, and checks it
before evaluation. A candidate is evaluated only when the mandatory
action-indicating enumeration floor was acquired completely, whatever fields the
predicate uses (ADR 0098), and every profile component the predicate can access
was also acquired completely. Otherwise the candidate is denied, so
error-absorbing operators cannot mask a failed or incomplete read.
Whether partial states are visible to CEL remains with normalized entity v1
([#8420](https://github.com/fullsend-ai/fullsend/issues/8420)).

The profiles, their bounds, and the coverage codes are specified in the
versioned normative discovery/query protocol. This ADR does not decide the
entity-source declaration shape, the first-slice scenarios or providers, or
the sharing scope for reads. Planned reads (option B) or staged evaluation
(option C) may supersede this decision if measurements show that profile reads
outside the mandatory floor are a material cost.

## Consequences

- Scheduled evaluation stays a pure function of pre-read inputs, matching how
  `EvaluateTrigger` works today, and can be replayed from the audit record.
- Per-candidate read cost is bounded, predictable, and batchable across
  candidates and harnesses, at the price of over-reading for state-only
  predicates.
- Scheduled-discovery predicates can read only profile fields. Exposing a new
  field requires a profile change in the normative protocol.
- Harness validation needs a new CEL field-access check against profiles.
  That check is the same dependency analysis option B would extend later.
- The normative discovery/query protocol must define per-kind, per-provider
  profiles and their bounds before entity-first execution is enabled.
