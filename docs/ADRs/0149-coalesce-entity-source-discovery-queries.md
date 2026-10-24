---
title: "149. Coalesce entity-source discovery queries and route candidates locally"
status: Accepted
relates_to:
  - agent-architecture
topics:
  - polling
  - entities
  - dispatch
---

# 149. Coalesce entity-source discovery queries and route candidates locally

Date: 2026-10-22

## Status

Accepted

Partially supersedes [ADR 0098](0098-entity-first-harness-evaluation.md):
combining compatible entity-source queries becomes required, and source-query
membership becomes a harness eligibility condition as well as a way to find
candidates. [ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md)
continues to govern poll and bound-run lifecycles; this decision adds a local
source-query check before each source-backed sandbox cycle.

## Context

ADR 0098 lets harnesses declare entity sources but only permits sharing
compatible provider queries. ADR 0144 relies on recurring repository-scoped
discovery to recover work after a bound run exits. A separate provider search
for every harness increases reads and still leaves `poll` to determine which
harnesses should receive each result. A provider search result also cannot
prove that an entity remains in a harness's source when `run` starts.

Entity-source discovery is not yet implemented.
[#8418](https://github.com/fullsend-ai/fullsend/issues/8418)
tracks entity-first execution, while
[#8421](https://github.com/fullsend-ai/fullsend/issues/8421)
tracks discovery requirements. The current
[`fullsend poll` command](https://github.com/fullsend-ai/fullsend/blob/0777c7f37b9cdcf0d293635be1bd40f25c1806d8/internal/cli/poll.go#L41-L64)
uses event-reconstructing input drivers.

## Options

### Search separately for each harness

This allows arbitrary provider-native queries, but provider search cost grows
with the number of harnesses and duplicate hits still need routing.

### Combine admissible queries and route locally

This shares discovery work, but limits sources to queries that an adapter can
combine without missing candidates and re-evaluate locally.

## Decision

For each discovery cycle and source scope, `fullsend poll` MUST issue one
logical candidate search covering the union of all enabled harness
entity-source queries in that scope. A scope is the provider, tenant,
repository or project, entity kind, and effective credential scope. A trusted
scope guard stays outside the harness-query union; neither a harness query
nor composition may widen it. `poll` verifies every hit's canonical scope
before routing. Pagination and coverage partitions may implement one logical
search, but separate full searches per harness do not.

An entity-source query is admissible only if its adapter can plan a search
that includes every possible local match and can reproduce query membership
locally from a defined routing projection and, if needed, follow-up reads.
A broader search is allowed within the trusted scope and provider bounds.
Opaque provider-only operators and ranking or top-N membership are unsupported
when their result cannot be reproduced locally. The adapter rejects an
unsupported source rather than silently omitting it or searching separately.

`poll` deduplicates hits by canonical entity identity and evaluates each
relevant harness query locally. It routes a matching candidate to every
matching harness for CEL evaluation. If local query membership is unresolved,
`poll` may fetch more evidence or hand the candidate to bound `run` for
resolution; missing data never means false. Candidate search and routing need
not fetch all entity fields or history required for final CEL evaluation.

Before every sandbox cycle for a source-backed harness, including a cycle
prompted directly by an event or wakeup, bound `run` resolves current entity
facts and locally checks both source-query membership and harness CEL. It may
settle either check first, but both MUST be definitively true before starting
a sandbox. A verified event supplies a candidate and retains its separate
actor-authorization gate; it cannot bypass the declared source query. A stale
search hit is not evidence of current membership. Unresolved membership
cannot launch a sandbox and must retain a retry or rediscovery path.

Actor-originated facts used by a query receive the same harness-stage
authorization as action-indicating facts used by CEL. A raw provider filter
must not omit an entity whose query could match after unauthorized facts are
removed locally, including for a negated condition. This decision does not
relax [ADR 0098](0098-entity-first-harness-evaluation.md) or the
[Authorization Contract v1](../normative/authorization/v1/README.md): before
CEL, the full action-indicating superset is still enumerated, every actor's
current permission is resolved, and the required audit is retained. A local
query check that does not evaluate CEL may stop on a proven non-match, but
cannot derive that result from unverified actor evidence.

The combined search must preserve candidate coverage across pagination,
provider caps, delayed indexing, and concurrent changes. Finishing the pages
alone does not prove completeness; an adapter needs a stable covered range or
sound overlap and reconciliation for possibly missed entities. An unproven
range cannot advance a completion checkpoint without durable retry or proven
rediscovery. A plan change requires a catch-up scan for newly eligible
entities. Time-dependent queries require periodic rediscovery or durable due
records even when an entity does not change.

Before entity-source discovery is enabled, a versioned normative query
protocol must define source declarations, allowed operators, routing facts,
provider mappings, bounds, pagination, coverage evidence, and conformance
cases. Before source-backed execution is enabled, a new major authorization
contract under [ADR 0015](0015-normative-specifications-directory.md) must
define the source-query admission gate and actor evidence. This ADR does not
choose query syntax or require full entity hydration during discovery.

## Forge scope

This applies to GitHub, GitLab, Forgejo, and Jira entity-source adapters.
Each adapter must prove the combined-search and local-recheck semantics for
the source queries it accepts. Existing event-reconstructing poll drivers,
including Jira's `--jql` override, remain under
[ADR 0063](0063-polling-based-work-discovery.md). Event-only harnesses
without an entity source are unaffected.

## Consequences

- Logical searches scale with source scopes rather than harness count;
  broader searches can increase result volume and local routing work.
- Source queries cannot use provider features that cannot be combined and
  rechecked locally; harness CEL remains a separate predicate.
- Partial scans and uncertain query results stay visible and retryable rather
  than silently omitting candidates or admitting a sandbox.
- Event-backed runs for source-backed harnesses also reject current query
  non-matches while preserving the existing event-actor gate.
