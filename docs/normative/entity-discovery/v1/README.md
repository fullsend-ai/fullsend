# Entity discovery v1

This contract defines the runtime guarantees for entity discovery. Entity
discovery is the part of a `fullsend poll` run that enumerates candidate
entities for entity sources that repository configuration already declares.
It answers four questions for each poll pass: what counts as complete, when
progress within a pass is safe, how uncovered work is recovered, and what
invalidates earlier coverage. MUST, SHOULD, and MAY express requirements for
conforming implementations.

> **Target contract, not implemented:** No shipped `fullsend poll` path
> enumerates entity sources. Publishing this contract does not enable
> scheduled execution, entity-driven dispatch, or resident eventless runs.
> Those still require their separate authorization, scope, runtime, and
> implementation gates, as listed in [Activation](#activation).

This contract composes with these decisions and contracts:

- [ADR 0149](../../../ADRs/0149-coalesce-entity-source-discovery-queries.md)
  requires one combined logical search per discovery scope and forbids
  treating uncertain discovery as absence.
- [ADR 0079](../../../ADRs/0079-bind-entity-execution-through-named-sources.md)
  and [entity-source configuration v1](../../entity-source-configuration/v1/README.md)
  own source declarations, bindings, provider query languages, effective
  selections, trusted scopes, and source-membership semantics.
- [ADR 0144](../../../ADRs/0144-reuse-poll-and-entity-bound-run-processes.md)
  owns the poll and bound-run lifecycles and the meaning of recurring
  discovery coverage.
- [ADR 0147](../../../ADRs/0147-evaluate-entity-cel-over-selectively-acquired-data.md)
  and [Authorization Contract v2](../../authorization/v2/README.md) own
  selective field acquisition, actor admission, and unresolved CEL decisions.
- [Normalized entity v1](../../normalized-entity/v1/README.md) owns the entity
  data model and its partial-data states.

## Scope and boundaries

This contract consumes the effective selections that entity-source
configuration v1 produces. It does not define a source model, a configuration
field, or a query language. Where this contract mentions a query, it means the
effective selection of a primary `source` binding: the trusted source scope,
the source query, and the harness filter for that provider, combined as
entity-source configuration v1 describes.

Entity discovery, as used here, means enumerating candidates during a
`fullsend poll` run. It does not mean:

- Discovering which entity sources exist. Configuration declares them.
- Keeping a persistent inventory of every entity in a scope.
- Acquiring the fields that CEL needs for an already-identified candidate.
  ADR 0147 and [#9583](https://github.com/fullsend-ai/fullsend/issues/9583)
  cover that step.
- Keeping cached normalized entities fresh. That cache has its own freshness
  and revision policy.

This contract does not gate a one-cycle `fullsend run` for an entity whose
identity is already known. Such a run needs no discovery pass and no discovery
readiness evidence. Every entity-driven sandbox cycle, whether started by
discovery, an event, or a wakeup, still rechecks current source membership and
the shared CEL eligibility decision before the sandbox starts.

Harnesses without an entity source are unaffected. Existing poll drivers that
reconstruct events, including Jira's `--jql` override, remain under
[ADR 0063](../../../ADRs/0063-polling-based-work-discovery.md) and are not
subject to this contract.

## Terms

| Term | Meaning |
|------|---------|
| Discovery scope | The ADR 0149 tuple of provider, tenant, repository or project, entity kind, and effective credential scope. |
| Search plan | The provider requests that one adapter issues to enumerate the logical union of all effective selections in one discovery scope. A plan includes any partitions and native request splits it needs. |
| Partition | One part of a search plan. A partition narrows the plan with provider-expressible bounds that stay inside the trusted scope, such as a creation-date range, an entity-number range, or one native lifecycle state. |
| Pass | One execution of a search plan by one `fullsend poll` process. |
| Candidate | An entity that a pass returned and that might match at least one effective selection. A candidate is never an execution decision. |
| Unresolved candidate | A candidate whose routing, source membership, or handoff to a bound run is not yet decided. |
| Coverage evidence | The facts an adapter records to show that a partition returned every entity it must return, as defined in [Complete partitions](#complete-partitions). |
| Plan identity | A value that changes whenever any input listed in [Plan identity](#plan-identity) changes. |
| Settled boundary | The latest time for which the adapter treats the provider's search view as up to date. Changes after this time may not yet appear in results. |

## Pass outcomes

Every pass reports exactly one outcome for each discovery scope. Every
partition also reports its own outcome.

| Outcome | Meaning | Candidates |
|---------|---------|------------|
| `complete` | Every partition in the plan has coverage evidence. | Zero or more. A complete pass with zero candidates is **complete-empty**. |
| `incomplete` | The pass was interrupted or stopped before every partition had coverage evidence. Reasons include a result cap, a rate limit, a budget or deadline, a plan change, an unproven page sequence, and a process exit. | Zero or more. Candidates already found MAY still be processed. |
| `failed` | A provider or host error prevented the pass from producing trustworthy results for at least one partition, and the pass did not recover within its budget. | Zero or more. Results from unaffected partitions MAY still be processed. |
| `unsupported` | The adapter cannot build a conforming plan for the scope, provider version, kind, or combined selection. | None from this plan. |

These rules apply to every outcome:

1. A pass is `complete` only when every partition is complete. One
   incomplete or failed partition makes the pass `incomplete` or `failed`.
2. Only a `complete` pass with zero candidates is complete-empty. An
   incomplete, failed, or unsupported pass MUST NOT be reported, logged, or
   consumed as complete-empty, even when it found no candidates.
3. An unsupported plan MUST NOT fall back to an unrestricted search, drop a
   selection, or run a separate full search for each harness. Entity-source
   configuration v1 requires this explicit failure.
4. Absence from discovery results is never evidence that an entity does not
   match a selection. This holds for every outcome, including
   complete-empty. Discovery produces candidates. It never records a
   non-match, a handled state, or a false CEL result.
5. A `complete` pass is not a point-in-time snapshot of the provider. It
   guarantees only what [Complete partitions](#complete-partitions) states.

## Complete partitions

A partition is complete only when the adapter records all of this evidence:

1. **End of results.** The provider's documented end-of-results signal for
   the request sequence, such as Jira's `isLast` or the absence of a GitHub
   next-page link. Every page in the sequence returned without an error that
   could affect results. A partial response, such as a GitHub REST search
   response with `incomplete_results: true` or a GraphQL response with errors
   on result fields, is not complete.
2. **Below every cap.** The number of matching entities in the partition is
   known to be below every provider result cap that applies to the request
   sequence. A match count equal to a cap, above it, or unknown while a cap
   could apply is not evidence. The adapter narrows such a partition until
   each part is below the cap, or reports the partition as incomplete.
3. **No skipped entities.** The page traversal cannot skip an entity that
   matched the partition throughout the traversal, even if other entities
   change during it. [Progress within a pass](#progress-within-a-pass) lists
   the accepted methods.
4. **Exhaustive partitions.** The partitions of the plan together cover the
   full logical union of effective selections in the discovery scope. The
   adapter shows this by construction, for example with ranges over a
   required, immutable key that leave no gaps and end with open-ended bounds.
5. **Settled boundary.** The adapter records the settled boundary that it
   used. [Delayed indexing](#delayed-indexing-and-concurrent-change)
   describes how it is chosen.

These signals alone are never coverage evidence:

- Receiving a last page.
- Receiving an empty page, including an empty page that still carries a
  next-page token.
- Receiving fewer results than the requested page size.
- Reaching a provider result cap or a configured fullsend result limit.
- A total-count field or header that the provider omits, estimates, or
  documents as approximate.

When a partition has all five kinds of evidence, the pass guarantees that it
returned every entity that matched the partition, in the provider's view at
the settled boundary, throughout the partition's traversal. It does not
guarantee entities whose membership, sort position, or index entry changed
during the traversal or after the settled boundary. Later passes recover
those entities, as [Recovery](#recovery-of-uncovered-work) describes.

## Progress within a pass

Version 1 defines no durable checkpoint, watermark, or persistent candidate
inventory. Progress exists only inside one pass in one process. A covered
range is the part of a partition that the pass has traversed with the
evidence above. It advances only within that pass.

An implementation MAY store hints across processes, such as ordering hints or
conditional-request validators. A hint MUST NOT mark any part of a later pass
complete. Coverage claims in a pass use only evidence gathered in that pass.
Defining durable checkpoints requires a later contract version.

A pass MAY route and process candidates as pages arrive. ADR 0149 permits
routing an entity once its page is processed, when provider pagination
guarantees that the page holds every result the query can return for that
entity. Early routing never establishes coverage for the partition.

### Page traversal

Pagination over data that changes can skip entities that did not change. For
example, with offset pagination ordered by update time, an update to an
entity on an earlier page moves it to the front. Every later entity shifts by
one position, so the next offset skips one entity that did not change. An
adapter MUST use at least one of these methods for each partition it reports
as complete:

- **Consistent cursor.** A provider cursor that the provider documents as
  consistent across pages for the request sequence.
- **Keyset bounds over a stable key.** Ordering by a required, immutable key,
  such as creation time or entity number, with a unique tiebreaker. Each
  request after the first is bounded by the last key seen, using the
  provider's filter syntax. The bound is inclusive, and the adapter removes
  duplicates by canonical entity identity. Because the bound is a key value
  and not a position, an entity that is removed or stops matching earlier in
  the order cannot shift a later entity out of the next page. An immutable
  ordering alone, paired with offset pagination, does not meet this method.
  Deleting or un-matching an entity on an earlier page shifts every later
  entity by one position, so the next offset skips an entity that matched
  throughout.
- **Consistent snapshot.** The provider serves every page of the request
  sequence from one snapshot, and documents that guarantee.
- **Overlap and reconciliation.** The adapter re-requests a margin around
  each page boundary or re-traverses the partition. It checks that no
  boundary entity moved, was removed, or changed membership in a way that
  could hide another entity.

Ties on the sort key need care. Providers may order entities with equal keys
differently on each request. At a page boundary where the last key equals the
next key, the adapter MUST re-request every entity with that key value. If one
key value has more matching entities than one page or cap can hold, the
adapter narrows the partition further or reports it as incomplete.

### Partitioning

The adapter splits a partition when it reaches a result cap, a request-length
limit, or a nesting limit. Splitting MUST keep every condition of every
effective selection, as entity-source configuration v1 requires. The adapter
SHOULD bound partitions by immutable keys. Partitions bounded by a key that
changes, such as update time, MAY be used. An entity can then move between
partitions during the pass, so the pass guarantees it only under the
concurrent-change rule below.

Overlapping partitions are allowed within the trusted scope. The adapter
removes duplicates by canonical entity identity before routing. A native
request split that a selection requires belongs to the same plan. For example,
the GitLab change-proposal `closed` preset needs both native `state=closed`
and `state=merged` requests. Both requests MUST complete before the partition
is complete.

### Delayed indexing and concurrent change

Provider search indexes can lag behind writes. The adapter chooses a settled
boundary for each partition. It is the pass start time minus a lag margin
that the adapter documents for that provider. When the provider offers a
read-after-write option or an unindexed list endpoint, the adapter MAY use it
and set the boundary closer to the pass start.

An entity that changed after the settled boundary, or during the traversal,
might be absent from the pass or appear in the wrong partition. The pass can
still be complete. This gap is the reason for rule 4 under
[Pass outcomes](#pass-outcomes): absence is never evidence of non-membership.
The bound run's current membership check, required by ADR 0149, decides
whether a returned candidate still matches.

## Ordering and processing

Discovery optimizes for time to the first useful candidate under provider API
limits. Where provider behavior and the selection's membership semantics
allow, the adapter SHOULD:

- Request the most recently updated candidates first.
- Process candidates as pages arrive instead of waiting for the full pass.
- Respect page sizes, result caps, rate limits, and the provider's backoff
  guidance, such as `Retry-After` headers and secondary rate limits.

Ordering never changes membership. The set of entities a complete partition
returns is the same with or without freshness-first ordering. Jira `ORDER BY`
clauses in source queries are separated before combination, as entity-source
configuration v1 requires. The adapter owns the traversal order.

When the provider cannot order by update time for a selection, the adapter
reports that freshness-first ordering is unsupported for that partition. All
coverage rules still apply.

When a provider rate limit interrupts a pass, the adapter waits as directed
when the remaining budget allows. Otherwise it stops the affected partitions
with the outcome `incomplete` and the reason `rate_limited`. It MUST NOT retry
faster than the provider allows. Candidates already found MAY still be
processed.

## Recovery of uncovered work

### In-memory retry

A poll process keeps retry state in memory. Retry state holds unresolved
candidates and the incomplete partitions of the current plan. The process
retries them with bounded backoff while it runs. Version 1 does not require
a shared durable retry record.

Retries and new passes share the provider budget. The process MUST NOT let
retries starve new passes or let new passes starve retries. It SHOULD retry
the oldest unresolved items first.

When an item exhausts its retry budget, the process stops retrying it in that
process. It reports the item as unresolved. Exhausting retries MUST NOT turn
an incomplete partition into a complete one or an unresolved candidate into a
non-match, a false result, or a handled state.

### Restart and later passes

A process restart loses in-memory retry state. A later scheduled poll starts a
fresh pass of the current plan. Because no checkpoint advances, every later
pass still includes the range of every lost item.

Recovery across processes therefore depends on recurring discovery, as ADR
0144 defines coverage: a configured recurring invocation or resident discovery
cycle that can eventually re-enumerate each eligible entity. Authorization
Contract v2 requires discovery to keep a durable retry record or to prove
rediscovery. Under this contract, recurring discovery of a plan that never
skips a range, together with the backfill rule below, provides that proof.
Where an execution adapter cannot show recurring discovery for a scope, that
scope lacks discovery readiness. Entity-source configuration v1 then keeps the
automatic once behavior and rejects explicit requests for resident eventless
runs.

### Older-candidate backfill

Freshness-first passes that stop early can keep revisiting the newest entities
and never reach older ones. The adapter MUST give older candidates recurring
opportunities through one of these methods:

- **Backfill partitions.** Each pass that cannot complete its plan spends part
  of its budget on at least one partition beyond the freshness frontier.
- **Safe query partitions.** The plan is split so that each pass can complete
  some partitions, and different passes complete different partitions.
- **Provider reconciliation.** A provider-specific method that can find every
  entity whose membership changed since it was last covered, with evidence
  equivalent to coverage evidence.

The choice of which older partition to visit MUST NOT depend only on state
that a restart loses. `fullsend poll` defaults to the once lifecycle, so each
scheduled invocation is often a new process. A rotation derived from a trusted
clock or schedule slot meets this rule. A random choice meets it only when it
also bounds the gap between visits, for example by drawing without
replacement within a clock-derived cycle. A rotation that restarts from the
first partition in every process does not.

There is no global deadline for catching up. Every partition MUST still be
visited again within a bounded number of passes that have a backfill budget.
A pass records which partitions it visited for backfill and their outcomes.

Visiting a partition is not enough. Without persistent progress, an adapter
can rotate across partitions in fresh processes, spend each visit on the
first page, and never reach a candidate deeper in a partition. The adapter
MUST therefore ensure that every backfill range is eventually traversed
exhaustively. For example, it can rotate restart-independent subpartitions
that are small enough for one pass to complete, or it can use provider
reconciliation with equivalent evidence. A visit that traverses only the
first pages of a range does not satisfy the bounded-visit rule.

## Plan identity

The plan identity changes whenever any of these inputs changes:

- An effective selection in the scope, including a source query, a harness
  filter, a primary `source` binding, or the set of enabled harnesses.
- The trusted source scope, such as a repository list or Jira project list.
- The connection endpoint or the effective credential scope.
- Provider query behavior that the adapter depends on, such as the detected
  search mode, the provider version, or a documented change to pagination or
  caps.
- The adapter version, when it changes how the plan is built.

### Changes during and after a pass

When the plan identity changes during an active pass, the process MUST replan:

1. Stop issuing requests under the old plan.
2. Discard coverage claims from the old plan. They do not count toward the
   new plan's completeness. The old pass reports `incomplete` with the
   reason `plan_changed`.
3. Treat candidates already found as candidates only. Routing after the
   change uses the new plan's selections, and every bound run still checks
   current membership.
4. Start the new plan as a fresh pass over its whole search space. It
   inherits no covered range from the old plan, so it can find newly eligible
   entities.

Every later pass uses the new plan.

A plan change does not invalidate cached normalized entities. Their
freshness and revision policy is independent of discovery. The cache's own
rules still apply. For example, entity-source configuration v1 shares
acquired facts only across compatible credential and admission scopes, and it
invalidates cached membership results and discovery readiness when the plan
changes.

### Time-dependent selections

Some selections change membership over time even when no entity changes. For
example, JQL relative date functions or an absolute date that is still in the
future can admit an entity without an update. Freshness-first ordering cannot
find such an entity, because its update time does not change.

The adapter MUST mark a plan as time-dependent when any effective selection
can change membership with time alone. A time-dependent plan needs recurring
rediscovery of its full search space. Freshness-first passes alone do not
satisfy this rule. Backfill partitions or complete passes MAY satisfy it. When
the adapter cannot tell whether a selection depends on time, it treats the
selection as time-dependent.

## Pass reports

Each pass emits a host-side report for each discovery scope. This contract
defines its logical content, not a wire format. The report includes:

- The plan identity and the discovery scope.
- The pass outcome and a reason code for each non-complete outcome.
- Each partition, with its outcome, reason, and settled boundary.
- Whether the plan is time-dependent and whether freshness-first ordering was
  used.
- The partitions visited for backfill.
- Counts of candidates found, routed, and still unresolved.

Reports MUST NOT contain credentials, tokens, or entity content beyond
canonical identities. Execution adapters MAY use reports as discovery
readiness evidence under entity-source configuration v1.

## Provider limits

This section records provider behavior only where it affects the guarantees
above. Each adapter MUST verify these limits for the deployed provider
version. ADR 0079 sets GitHub and GitLab work items and change proposals, plus
Jira work items, as the initial targets.

| Provider | Behavior that affects coverage | Required handling |
|----------|--------------------------------|-------------------|
| GitHub | [Search](https://docs.github.com/en/rest/search/search#search-issues-and-pull-requests) returns at most 1,000 results for one query. REST search can return `incomplete_results: true`. GraphQL search can return [partial errors](https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api). The search index can lag behind writes. Search has its own [rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api), including secondary limits. | Partition below the cap. Treat incomplete results and partial errors as incomplete. Use a lag margin for the settled boundary. A repository issue list endpoint without the search cap MAY enumerate a broader set, with exact local checks under entity-source configuration v1. |
| GitLab | The [issue](https://docs.gitlab.com/api/issues/#list-all-project-issues) and [merge request](https://docs.gitlab.com/api/merge_requests/#list-project-merge-requests) list endpoints use offset pagination unless keyset pagination is available. Total-count headers can be absent for large result sets. The change-proposal `closed` preset needs two native states. | Use keyset pagination, keyset bounds on `updated_after`, `updated_before`, `created_after`, or `created_before`, or overlap and reconciliation. Never use a missing total count as evidence. Complete both native state requests. |
| Forgejo | List endpoints use offset pagination with a server-configured maximum page size. | Report `unsupported` until a Forgejo adapter passes conformance. ADR 0079 keeps Forgejo disabled until then. |
| Jira | Enhanced JQL search pages with `nextPageToken` and `isLast` and does not return a total count. Its results may not yet reflect recent writes. JQL supports relative date functions. Requests can return HTTP 429 with `Retry-After`. | Never treat `isLast` alone as evidence. Combine it with keyset bounds over a stable key, or reconciliation, and a lag margin. Mark plans with relative date functions as time-dependent. Respect `Retry-After`. |

The [JQL match API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-search/#api-rest-api-3-jql-match-post)
checks the membership of known entities. It does not enumerate candidates, so
it cannot provide coverage evidence for discovery.

## Activation

This contract is one prerequisite for entity-source discovery under ADR 0149.
It is not enough on its own. Entity-driven execution and resident eventless
runs also require:

- The new major authorization contract and source-query admission gate that
  ADR 0149 requires.
- Provider adapters that pass both this contract's conformance cases and
  entity-source configuration v1's conformance cases.
- Model-context support for the provider and entity kind, as ADR 0079
  requires.
- Discovery readiness evidence for the current plan, as entity-source
  configuration v1 requires.
- The separate implementation and scope decisions tracked under
  [#8418](https://github.com/fullsend-ai/fullsend/issues/8418), including the
  combined discovery coordinator in
  [#9587](https://github.com/fullsend-ai/fullsend/issues/9587).

## Conformance cases

An adapter conforms only when it passes every case below for each provider and
entity kind it supports. Each case states the observable behavior and the
evidence the adapter must provide.

### Outcomes and completeness

**DC-1. Complete-empty.** A plan whose partitions all match zero entities
reports `complete` with zero candidates. Evidence: the end-of-results signal,
proof that no result cap applies or that the match count is below it, and the
settled boundary for each partition.

**DC-2. Empty or last page without proof.** A provider returns an empty page
with a next-page token, or a last page while a cap could apply and the match
count is unknown. The partition reports `incomplete`. Evidence: the page
sequence and the missing proof.

**DC-3. Failed pass.** An authentication error or a server error that
persists through the retry budget reports `failed`. Evidence: the error class.
A pass with no candidates is not reported as complete-empty.

**DC-4. Unsupported plan.** A selection, kind, or provider version that the
adapter cannot plan reports `unsupported`. The adapter issues no unrestricted
search and no separate search per harness. Evidence: the unsupported input.

**DC-5. Absence is not non-membership.** After any outcome, including
complete-empty, no consumer records a non-match, false result, or handled
state for an entity because discovery did not return it. Evidence: the
recorded decisions for an entity that a pass did not return.

### Caps, pagination, and partitioning

**DC-6. Result cap.** A partition whose matches reach a provider cap is split
until each part is below the cap, or reports `incomplete` with the reason
`capped`. Candidates found before the cap MAY still be processed. Evidence:
the cap, the partition bounds, and the match counts.

**DC-7. Movement during traversal.** While the adapter pages through a
partition, another entity moves ahead of the traversal position. In a second
scenario, an entity before the traversal position is deleted or stops
matching, which shifts offsets. For example, with IDs 1, 2, 3, and 4 and a
page size of 2, entity 1 is removed after the first page. In both scenarios,
no entity that matched throughout the traversal is skipped (here, entity 3),
or the partition reports `incomplete`. Evidence: the traversal method from
[Page traversal](#page-traversal) and the duplicate removal by canonical
identity.

**DC-8. Sort-key ties.** More entities than one page share a sort-key value
across a page boundary. No entity with that value is skipped. Evidence: the
re-request for the tied value, or further narrowing.

**DC-9. Exhaustive partitions.** The union of a plan's partitions equals the
logical union of effective selections, with every selection condition kept.
Evidence: the partition bounds and keys.

**DC-10. Native request splits.** A selection that needs several native
requests, such as the GitLab change-proposal `closed` preset, is complete only
when every request completes. Evidence: the outcome of each native request.

### Concurrent change and delayed indexing

**DC-11. Concurrent change.** An entity that changes during a pass may be
absent from that pass. A later pass returns it while it still matches. No
pass records it as a non-member. Evidence: the settled boundary and the later
pass's results.

**DC-12. Delayed indexing.** An entity written after the settled boundary is
missing from the provider index. The pass MAY still report `complete` with
its settled boundary. A later pass returns the entity. Evidence: the lag
margin and the later pass's results.

### Ordering and rate limits

**DC-13. Freshness-first ordering.** Where the adapter supports
freshness-first ordering, it requests the most recently updated candidates
first. An adapter that also routes candidates as pages arrive routes them
before later pages are fetched. An adapter that waits for the full pass is
exempt from the early-routing check. The complete set of returned entities is
the same as without this ordering. Evidence: the request order, the routing
order where early routing is used, and set equality.

**DC-14. Ordering unsupported.** A partition that cannot be ordered by update
time reports that freshness-first ordering is unsupported, and all coverage
rules still apply. Evidence: the report.

**DC-15. Rate-limit interruption.** A rate limit mid-pass causes the adapter
to follow provider backoff guidance. When the budget runs out, the affected
partitions report `incomplete` with the reason `rate_limited`. The pass is
not complete-empty, and candidates already found are not recorded as
non-matches. An adapter that processes already-found candidates after the
interruption does so under the MAY in
[Ordering and processing](#ordering-and-processing), and the case checks that
processing only where it occurs. Evidence: the request timing and the backoff
headers.

### Recovery

**DC-16. In-memory retry.** An unresolved candidate is retried in the same
process with bounded backoff, alongside new passes. Retries starve neither
new passes nor each other. Where the process schedules oldest first, as the
retry rule recommends, the case checks that order, and a different order is
a permitted exception. Exhausting its retries reports it as unresolved, not as
a non-match. Evidence: the retry schedule and the final report.

**DC-17. Restart with in-memory retry only.** After a process restart, the
next process starts a fresh pass. No stored hint marks a range complete. A
candidate that was unresolved before the restart is returned again by a later
pass while it still matches. Evidence: the reports of both processes.

**DC-18. Older-candidate backfill.** In a scope too large for any single pass
to complete, with a new process for every pass, every partition is visited
again within a bounded number of passes. A candidate deeper than the first
pages of a repeatedly visited partition is still rediscovered, because every
backfill range is eventually traversed exhaustively. Evidence: the backfill
partitions recorded across passes and the pass that returned the deep
candidate.

### Plan changes

**DC-19. Plan change during a pass.** When a source query, harness filter,
binding, or enabled harness changes, the active pass reports `incomplete`
with the reason `plan_changed`. Its coverage does not count toward the new
plan. The new plan's first pass covers its whole search space and finds
newly eligible entities. Evidence: the plan identities and both reports.

**DC-20. Scope or credential change.** A changed trusted scope, endpoint, or
credential scope causes the same replan as DC-19. No coverage evidence
crosses credential scopes. Evidence: the plan identities.

**DC-21. Provider behavior change.** A change in detected search mode or
provider version causes a replan, or `unsupported` when no conforming plan
exists. The adapter does not silently use different query semantics.
Evidence: the detected behavior and the plan identity.

**DC-22. No entity-cache invalidation.** After DC-19, DC-20, or DC-21, cached
normalized entities stay valid under their own freshness and revision policy.
Cached membership results and discovery readiness for the old plan do not
carry over. Evidence: the cache state before and after the change.

**DC-23. Time-dependent rediscovery.** An entity enters a time-dependent
selection without any update. A later pass returns it through recurring full
rediscovery or backfill, not through freshness-first ordering. Evidence: the
time-dependent flag and the pass that returned the entity.

### Boundaries

**DC-24. Known-entity run.** A one-cycle `fullsend run` for a known entity
needs no discovery pass or discovery readiness. It still checks current
source membership and CEL eligibility before its sandbox starts. Evidence: a
run without any discovery report.

**DC-25. Event-only harness.** A harness without an entity source behaves the
same with or without this contract. Evidence: unchanged dispatch for that
harness.

## Versioning

Breaking changes to these guarantees require a new major version directory
under [ADR 0015](../../../ADRs/0015-normative-specifications-directory.md).
Defining durable checkpoints, persistent candidate inventories, or shared
retry records is a breaking change. Adding a provider row, a reason code, or a
conformance case that does not weaken an existing guarantee is not.
