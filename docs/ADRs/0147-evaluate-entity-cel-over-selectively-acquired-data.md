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
pre-CEL enumeration and actor-permission resolution for every
action-indicating element, and its complete audit roster. The trusted-origin
gate and authorization of decision-relevant action facts remain. Also
partially supersedes [ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md)'s
complete-entity due checks and full-history audit roster. Its process
lifecycle and model-bound staging rules remain. This ADR does not change
actor-role freshness; [ADR 0148](0148-reuse-verified-actor-roles-for-entity-cel.md)
addresses that separately.
[ADR 0151](0151-common-actor-admission-floor.md) separately sets the platform
human actor floor and each harness's human minimum for CEL and model admission.

## Context

[ADR 0098](0098-entity-first-harness-evaluation.md) and
[ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md) require a complete
entity and current actor checks across all action-indicating history before an
entity-source harness evaluates CEL. The corresponding
[Authorization Contract v1](../normative/authorization/v1/README.md) also
requires an audit roster of every retained element and actor. A state-only or
short-circuiting predicate can often be decided without those unrelated API
reads or audit entries. A progressive check may inspect multiple pages;
repeating a role lookup for the same actor on each iteration compounds provider
load. The [observability problem](../problems/operational-observability.md#structured-traces)
already favors references to durable inputs over duplicating source data.
For Git-forge work, [ADR 0141](0141-deterministic-filtered-entity-context-staging.md)
separately assembles selected model inputs before the sandbox starts.
[ADR 0063](0063-polling-based-work-discovery.md)'s `lastCheck` rule advances
after scheduling a reconstructed-event run; a new entity-source candidate
handoff needs its own retry or rediscovery guarantee.

Entity routing remains a target design: at
[`a2be1678`](https://github.com/fullsend-ai/fullsend/blob/a2be1678c5dbd9361a77f04de5ec260320e4fb9f/internal/harness/trigger.go#L12-L16),
the trigger CEL environment still declares only `event`.

## Options

### Fetch all history before CEL

This makes missing history easy to detect but requires reads and audit records
for categories the predicate never uses.

### Fetch decision-relevant data progressively

This avoids unrelated reads, but requires explicit partial-data semantics and
proof that a result cannot change when more data is fetched.

## Decision

Adopt selective, progressive acquisition for entity-source CEL evaluation.
The platform retains a closed classification of action-indicating categories,
but fetches only fields and history that could affect the evaluated harness's
predicate. It derives a conservative acquisition plan from the compiled CEL
expression and may push sound filters into provider queries. Projections of
classified action-indicating elements inherit their source records' actor
dependencies. Provider aggregates affected by unauthorized records cannot
enter the authorized CEL view unchanged.
Whether a state field needs an actor check depends on how CEL uses it. An
ordinary human-driven change that can make the check actionable requires its
actor's verified effective role to meet the platform floor and the evaluated
harness's `human_min_role`. An exact verified label transition may instead use
the narrow grant described below. CEL may narrow selection further.
When `updated_at` advances on each forge update, elapsed
time can make a staleness check on that raw field actionable without checking
every historical updater; any update can defer it. A query plan must not hide a
potentially authorized match.

The evaluator retains fetched entity data and verified actor roles while it
progressively enriches one check. An in-memory or trusted shared cache may
reuse entity data across checks only when its revision and coverage are
validated as current; an accepted event or wake invalidates affected entity
facts. Within one check, an iteration may reuse a verified role for an actor
it has already resolved while that verdict remains eligible under
[ADR 0148](0148-reuse-verified-actor-roles-for-entity-cel.md)'s age bound; an
expired verdict must be refreshed before it contributes to a definitive
decision. Each iteration applies the platform floor and the evaluated harness's
effective `human_min_role` to ordinary human actor-originated facts that could
make the predicate actionable. Exact verified label transitions and registered
bots follow their separate admission paths below; verified effective roles,
when present, remain available to CEL.
Raw state used only for the conservative deferral described above is not actor
authority. ADR 0148 separately permits reuse across checks.

Partial entity data must distinguish not-yet-loaded data from absent or empty
data. A CEL result is definitive `true` or `false` only when every complete
entity consistent with acquired facts, proven source coverage, and the
applicable actor-authorization results yields that result.
Otherwise the platform records `UNKNOWN` and either fetches more relevant data
for re-evaluation or passes an unresolved candidate from `poll` to a bound
`run` for further reading. Exhausted budgets or unavailable, inconsistent, or
unverifiable evidence end as inconclusive, never as empty or false. `poll` may
stop on definitive false, subject to [ADR 0148](0148-reuse-verified-actor-roles-for-entity-cel.md)'s
cache-dependent recheck rule. Its definitive true selects a candidate for bound
`run`, which must recheck current evidence before launching a sandbox. A
handoff is provisional: scheduling or delivering the run does not resolve the
candidate. For any incomplete `poll` check or provisional handoff, discovery
retains a durable retry path or proves its source query will rediscover the
candidate after checkpoint advancement. A failed or inconclusive `run` leaves
the candidate retryable. A definitive false closes the current handoff only
after any cache-dependent recheck is scheduled under ADR 0148; completed
actionable work or safe deferral under ADR 0144 also closes it. `run` launches
a sandbox only after its own definitive true. Absence, negation, counts, and
universal conditions may still require complete coverage of relevant scopes.

Before a classified action-indicating entity fact can influence CEL, Fullsend
verifies its source-revision actor. For an ordinary human fact, that actor's
verified effective role must meet the platform `triage` floor and the
evaluated harness's effective `human_min_role`. A verified human label
addition instead grants `triage`-equivalent authority only for its exact
accepted transition and proven entity projection; a higher harness minimum
also requires the labeler's verified effective role to meet it. This narrow
grant neither sets `actor.role` nor makes `actor.role_verified` true, and it
is not a cacheable actor-role verdict. A bot needs its exact registered role
under ADR 0107, except that a provider-verified bot label handoff separately
grants authority for its exact transition and projection. Independent history
needs its own actor gate. Verified effective roles, when present, remain
available to CEL for further harness conditions.

Unauthorized records are excluded. Unrelated history need not be fetched,
authorized, or listed in the audit. Each check records its verified initiator,
entity, repository, effective policy, harness revision and `human_min_role`,
decision time, dependency and query scopes, and bounded evidence of source
coverage and actor-check outcomes. Identifiable witnesses for a positive
decision retain actor provenance; collection-wide decisions may bind counts,
digests, and source references to the checked
coverage and authorization source instead of listing every record and actor.
Event-actor and model-bound gates remain separate; ADR 0151 applies the same
effective harness minimum to their ordinary human actors.

The breaking change to enumeration and audit guarantees is specified by
[Authorization Contract v2](../normative/authorization/v2/README.md), as
required by [ADR 0015](0015-normative-specifications-directory.md).
Entity-source CEL remains disabled until a versioned normalized-entity
contract defines partial data, coverage, and concurrency semantics.

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
