---
title: "149. Combine entity-source searches and route matches locally"
status: Accepted
relates_to:
  - agent-architecture
topics:
  - polling
  - entities
  - dispatch
---

# 149. Combine entity-source searches and route matches locally

Date: 2026-10-22

## Status

Accepted

This ADR partially supersedes
[ADR 0098](0098-entity-first-harness-evaluation.md): compatible entity-source
queries must be combined, and matching a source is required both to find a
candidate and to let a harness act on it.
[ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md) still governs
poll and bound-run lifecycles. This ADR adds a local source check before each
sandbox cycle for an entity-driven harness.

## Context

ADR 0098 lets harnesses declare entity sources, but allows sharing only when
their provider queries are compatible. ADR 0144 relies on repeated
repository-wide discovery to recover work after a bound run exits. Searching
separately for each harness increases provider reads and still leaves `poll` to
decide which harnesses receive each result. A search result also cannot prove
that an entity still matches a harness's source when `run` starts.

Entity-source discovery is not yet implemented.
[#8418](https://github.com/fullsend-ai/fullsend/issues/8418)
tracks entity-first execution, while
[#8421](https://github.com/fullsend-ai/fullsend/issues/8421)
tracks discovery requirements. The current
[`fullsend poll` command](https://github.com/fullsend-ai/fullsend/blob/0777c7f37b9cdcf0d293635be1bd40f25c1806d8/internal/cli/poll.go#L41-L64)
uses event-reconstructing input drivers.

## Options

### Search separately for each harness

Each harness can use any query its provider supports. But provider reads grow
with the number of harnesses, and duplicate results still need routing.

### Combine admissible queries and route locally

One search uses less provider work. But adapters can accept only queries they
can combine without missing matches and check locally.

## Decision

For each discovery cycle and source scope, `fullsend poll` MUST run one
combined logical search for candidates matching any enabled harness
entity-source query in that scope. A scope includes the provider, tenant,
repository or project, entity kind, and effective credential scope. This keeps
the trusted scope guard separate from the harness queries; neither a query nor
their combination may widen it. Before routing each result, `poll` MUST verify
its canonical scope. `poll` MUST NOT run a separate full search for each
harness.

An adapter may accept a query only if its provider search includes every entity
that might match locally. It must then be able to check whether each result
belongs to the harness source, using defined routing fields and follow-up reads
when needed. It may search more broadly if it stays within the trusted scope
and provider limits. It cannot accept provider-only operators or ranking and
top-N rules when local checks cannot reproduce their results. Reject a source
the adapter cannot support; do not silently omit it or search for it separately.

`poll` removes duplicate results by canonical entity identity and checks each
relevant harness query locally. It sends each candidate to every harness whose
query matches for CEL evaluation. If provider pagination guarantees that a page
contains all results the query can return for a given entity, `poll` MAY route
that entity once it has processed the page while it fetches later pages. This
does not establish complete coverage for the combined search; the coverage
rules below still apply. If it cannot tell whether a query matches, `poll` may
fetch more data or pass the candidate to bound `run`; missing data never counts
as a non-match. Discovery and routing need not fetch every entity field or
history needed for the final CEL check.

Before each sandbox cycle for an entity-driven harness, bound `run` reads the
current entity facts and checks both source membership and harness CEL locally.
This also applies to cycles started by an event or wakeup. The checks may run
in either order, but both MUST be true before a sandbox starts. A verified
event supplies a candidate and still goes through its separate
actor-authorization check; it cannot skip the source query. A stale search result
does not prove current membership. If membership is unknown, do not start a
sandbox; keep a retry or rediscovery path.

Before a query uses actor-originated facts, it MUST apply the actor-admission
rules for entity-source CEL in
[ADR 0147](0147-evaluate-entity-cel-over-selectively-acquired-data.md) and
[Authorization Contract v2](../normative/authorization/v2/README.md). This
includes the platform floor and harness minimum for decision-relevant facts,
current source-revision attribution, and eligible verified role evidence under
[ADR 0148](0148-reuse-verified-actor-roles-for-entity-cel.md). A provider
filter MUST NOT exclude an entity that could match after facts excluded from
that harness's authorized view are removed locally, including when the query
negates a condition. Discovery may acquire only the facts needed to establish
source membership, with enough coverage to decide a match; unresolved data
cannot prove a non-match. ADR 0147 and Authorization Contract v2 govern
selective acquisition, coverage, decision evidence, and unresolved-candidate
retry. A query check that does not run CEL may stop when it proves there is no
match, but it cannot use unverified actor facts to prove that.

The combined search must not miss candidates because of pagination, provider
result caps, delayed indexing, or concurrent changes. Reaching the last page
does not prove the search is complete. An adapter must establish a stable
covered range or use overlap and reconciliation to find possible misses. If
coverage is uncertain, it cannot mark the range complete unless it records a
durable retry or can prove the entities will be found later. When the search
plan changes, scan for newly eligible entities. Queries that depend on time
need periodic rediscovery or durable due records, even when an entity does not
change.

Entity-source discovery cannot be enabled until there is a versioned normative
query protocol. It must define source declarations, allowed operators, routing
facts, provider mappings and limits, pagination, coverage evidence, and
conformance cases. Entity-driven execution cannot be enabled until a new major
authorization contract under
[ADR 0015](0015-normative-specifications-directory.md) defines the
source-query admission gate and actor evidence. This ADR leaves query syntax
open and does not require fetching every entity field during discovery.

## Forge scope

This applies to entity-source adapters for GitHub, GitLab, Forgejo, and Jira.
Each adapter must show that it can combine and locally check the queries it
accepts. Existing poll drivers that reconstruct events, including Jira's
`--jql` override, remain under
[ADR 0063](0063-polling-based-work-discovery.md). Harnesses without an
entity source are unaffected.

## Consequences

- Search work grows with the number of source scopes, not harnesses. Broader
  searches may return more results and require more local routing.
- Source queries cannot use provider features that cannot be combined and
  checked locally. Harness CEL remains a separate condition.
- Partial scans and uncertain results stay visible and retryable; they cannot
  silently drop candidates or start a sandbox.
- For entity-driven harnesses, event-backed runs also reject entities that no
  longer match the source query and still check the event actor's authorization.
