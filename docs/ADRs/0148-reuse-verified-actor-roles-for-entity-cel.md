---
title: "148. Reuse verified actor roles for entity CEL within a bounded lifetime"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - authorization
  - caching
  - cel
  - entities
---

# 148. Reuse verified actor roles for entity CEL within a bounded lifetime

Date: 2026-10-22

## Status

Accepted

Partially supersedes [ADR 0098](0098-entity-first-harness-evaluation.md)'s
permission-at-evaluation-time rule and
[ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md)'s mandatory
actor-role refresh at every due check and prohibition on a cached
authorization result substituting for that refresh. This exception applies
only to entity-source CEL. Event admission and model-bound content admission
retain their separate fresh authorization rules.
[ADR 0151](0151-common-actor-admission-floor.md) sets the platform human floor
and effective harness minimum without extending this cache to either gate.

## Context

[ADR 0147](0147-evaluate-entity-cel-over-selectively-acquired-data.md) lets an
entity CEL check load only the facts needed to decide its predicate, fetching
more when the partial result is `UNKNOWN`. Iterations within one check, later
due checks, and separate `poll` and bound `run` processes can encounter the
same actors. [ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md)
requires every due check to refresh actor authorization even when a recent,
provider-verified result is available. That multiplies provider calls as
entity polling becomes more frequent.

Reusing a role result across checks reduces those calls but delays recognition
of a grant or revocation that the platform has not observed. The bounded
period and the gate to which it applies therefore belong in the authorization
contract, rather than being left as a cache implementation detail.

## Options

### Refresh every due check

This notices provider-side permission changes on the next check but repeats
lookups for actors shared by related checks.

### Reuse roles without expiry

This saves lookups but can permanently preserve a revoked grant or stale denial.

### Reuse roles for a bounded period

This bounds permission-change latency while allowing related checks to share
provider results.

## Decision

For entity-source CEL only, Fullsend may reuse a completed provider-verified
human permission result or exact registered-bot-role result across progressive
iterations, separate checks and harnesses, and `poll` and bound `run`
processes. A trusted host-owned cache may be local or shared between
processes. Versioned platform policy must set an explicit maximum age of no
more than five minutes, measured absolutely from the authoritative lookup's
start; absent that policy, cross-check reuse is disabled. A deployment may
choose a shorter age, but a cache hit never renews expiry and a harness or
repository cannot widen it. Verified denials may be cached under the same
bound. Failed, incomplete, or unverifiable lookups are not cache entries and
cannot fall back to an older entry.
The cache stores the verified effective role or verified absence, not a
per-harness threshold result or CEL decision. Harnesses with different
`human_min_role` values may reuse the same role verdict but must each compare
it with their own effective minimum. An exact label-transition grant is not
an actor-role verdict and cannot be stored in this cache.

Cache keys and invalidation must isolate the source provider and tenant,
target repository or project, immutable actor identity, effective permission
source and policy revision, and the selected source ref plus relevant OWNERS,
OWNERS_ALIASES, or bot-registration source revision. A known change to a grant,
OWNERS, OWNERS_ALIASES, bot registry, or policy invalidates affected entries.
Every check separately validates current entity facts, their
source-revision actor attribution, and query coverage. It applies the platform
`triage` floor and effective harness minimum through the applicable ordinary
human role or exact human-label-grant path, checks registered-bot requirements,
and evaluates its CEL predicate. A wakeup invalidates affected entity facts;
it does not by itself turn an unexpired actor-role result into a fresh one.
If cached authorization contributes to a false CEL result, discovery must
schedule a recheck or guarantee rediscovery after expiry or known invalidation
within a bounded reconciliation interval, even when the entity itself has not
changed.

A bound `run` may use an unexpired shared result for its final CEL check and
sandbox admission, but it re-resolves the entity and reevaluates CEL before
each sandbox cycle rather than adopting `poll`'s verdict. If a deciding role
expires or is invalidated before admission, `run` must refresh it and
re-evaluate, or leave the candidate inconclusive without launching a sandbox.
The cache does not authorize a prompting event or model-bound content. The
[ADR 0133](0133-actor-attributed-data-security-boundary.md) gate retains its
fresh checks. The audit records the effective maximum age, lookup time
and age of role evidence used for a CEL decision, and whether it came from a
shared cache, using the bounded evidence format in
[Authorization Contract v2](../normative/authorization/v2/README.md).

## Forge scope

This applies to GitHub, GitLab, Forgejo, and Jira entity-source CEL. An
adapter can share a role result only if it can verify a stable actor identity,
role, and authorization scope and bind any available source revision;
otherwise the check must refresh authorization or remain inconclusive.
Existing event-only routing is unchanged on every source.

## Consequences

- Progressive checks and resident processes can share role results without
  repeating provider calls for every CEL iteration or due check.
- An unobserved revocation can let a newly observed action select and launch a
  sandbox until the cached grant expires; an unobserved grant can delay work
  until expiry and the next bounded recheck.
- Event admission and model-bound content admission still perform fresh checks,
  so each uses authorization evidence independent of the CEL cache.
- Cache implementations must enforce scoped keys, absolute expiry,
  invalidation, and auditable evidence age even when processes share storage.
