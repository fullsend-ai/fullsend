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
[ADR 0151](0151-harness-human-role-minimums.md) sets the platform human floor
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

For entity-source CEL, Fullsend may reuse completed, provider-verified human
role results and exact registered-bot-role results across progressive checks,
harnesses, and `poll`/bound `run` processes. A trusted host-owned cache may be
local or shared. The initial maximum age is five minutes from lookup start.
This is an arbitrary starting cap, not a measured optimum. After entity polling
is in production, revisit the cap using observed authorization-freshness needs
and SCM API rate-limit pressure. A platform policy may choose a shorter age;
without that policy, cross-check reuse is disabled. Cache hits never renew
expiry. Completed positive and negative verdicts may be cached; failed,
incomplete, or unverifiable results may not.

The cache stores a verified role or verified absence, not a harness threshold
result or CEL decision. Each harness applies its own `human_min_role`, and an
exact label-transition grant is never cached as a role. Cache identity and
invalidation isolate each verified permission source and its revision; the
exact key dimensions and audit evidence are specified by
[Authorization Contract v2](../normative/authorization/v2/README.md). Every
check still validates current entity facts, actor attribution, and coverage.
A bound `run` re-resolves the entity and evaluates its own CEL result before a
sandbox cycle. Event admission and model-bound content retain their fresh gates.

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
