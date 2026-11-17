# Entity-source discovery v1

Protocol version: `entity-source-discovery/v1`

This specification defines the discovery and query protocol that
[ADR 0149](../../../ADRs/0149-coalesce-entity-source-discovery-queries.md)
requires before entity-source discovery can be enabled. It covers trusted
source declarations, the logical query model and its operators, routing facts,
source scopes, combined-search planning, local routing, provider mappings and
limits, pagination and coverage evidence, retry behavior, and the conformance
cases provider adapters must pass.

It records decisions already accepted in
[ADR 0098](../../../ADRs/0098-entity-first-harness-evaluation.md),
[ADR 0133](../../../ADRs/0133-actor-attributed-data-security-boundary.md),
[ADR 0144](../../../ADRs/0144-reuse-poll-and-entity-bound-run-processes.md),
[ADR 0147](../../../ADRs/0147-evaluate-entity-cel-over-selectively-acquired-data.md),
[ADR 0148](../../../ADRs/0148-reuse-verified-actor-roles-for-entity-cel.md),
[ADR 0149](../../../ADRs/0149-coalesce-entity-source-discovery-queries.md),
and [ADR 0151](../../../ADRs/0151-harness-human-role-minimums.md). It does not
reopen them. Where this document and an accepted ADR appear to differ, the ADR
governs and this document needs correction.

> **Target contract — not yet implemented:** `fullsend poll` does not yet
> perform entity-source discovery, and bound `fullsend run` does not yet check
> source membership. Publishing this protocol satisfies ADR 0149's
> protocol-existence precondition for discovery. It does not enable discovery
> for any provider or entity kind; see [Enablement gates](#enablement-gates).

## Relationship to other contracts

| Contract | What it governs | What this protocol adds |
|----------|-----------------|-------------------------|
| [Normalized entity v1](../../normalized-entity/v1/README.md) | Entity identity, the canonical `entity.id`, field values, unknown markers, and collection coverage | Selects the subset of entity fields usable as routing facts, and defines how discovery uses them |
| [Authorization Contract v2](../../authorization/v2/README.md) | Event-actor admission, eventless provenance, actor-fact admission, the bounded actor-verdict cache, decision evidence, and inconclusive-candidate retry | Applies those rules to source-query evaluation; it does not relax or replace any of them |
| [Entity-context-snapshot v2](../../entity-context-snapshot/v2/README.md) | Model-facing data staged after routing | Nothing; discovery output never enters model input directly |
| [NormalizedEvent v1](../../normalized-event/v1/README.md) | Event payloads and event-only CEL routing | Nothing; event-only harnesses are unaffected |

This protocol does not define harness CEL evaluation, the acquisition plan
for CEL facts, the bound-run lifecycle, or model-bound admission. A source
query and the harness CEL predicate are separate conditions. Both must be true
before a sandbox starts.

ADR 0149 leaves query syntax open. This version defines a **logical query
model**, not a harness-configuration syntax. A configuration loader that
accepts any concrete syntax MUST lower each declaration losslessly to the
logical model below. If it cannot, it MUST reject the declaration.

## Terms

| Term | Meaning |
|------|---------|
| Source declaration | A trusted harness-configuration entry that states which entities the harness may consider: a source scope selector and a logical query |
| Source scope | The trusted boundary for one combined search; see [Source scopes](#source-scopes) |
| Logical query | A predicate over routing facts built from the operators in [Operators](#operators) |
| Routing fact | An entity fact that a logical query may test; see [Routing facts](#routing-facts) |
| Adapter | The provider-specific component that admits queries, plans provider requests, reads results, and produces coverage evidence for one provider and entity kind |
| Discovery plan | The adapter's provider requests for one source scope in one discovery cycle, covering every admitted query in that scope |
| Plan identity | A digest that identifies a discovery plan; see [Discovery plans](#discovery-plans) |
| Candidate | A canonical entity returned by a discovery plan and verified to be inside its source scope |
| Membership result | The outcome of locally checking one candidate against one harness's logical query: `match`, `non_match`, or `unknown` |
| Covered range | A part of a source scope's search space, such as an `updated_at` interval, for which the adapter holds complete coverage evidence |
| Durable retry record | Persisted state that keeps an unresolved candidate or range retryable across process exits; see [Retry behavior](#retry-behavior) |

The key words MUST, MUST NOT, SHOULD, and MAY are normative.

## Enablement gates

Discovery for a provider and entity kind MUST remain disabled until all of the
following hold:

1. This protocol version is supported by the running `fullsend poll` and bound
   `fullsend run`.
2. [Normalized entity v1](../../normalized-entity/v1/README.md), or a later
   version, defines the provider/kind pair, its canonical identity, and every
   routing fact the adapter supports for it.
3. The adapter passes every conformance case in
   [Conformance cases](#conformance-cases) that applies to the pair.
4. The effective platform policy permits eventless evaluation for the
   harness and repository, and discovery runs with trusted fullsend
   invocation provenance, as Authorization Contract v2 requires.

ADR 0149 separately requires a new major authorization contract that defines
the source-query admission gate and actor evidence before **entity-driven
execution** is enabled. This protocol does not satisfy that requirement.
Discovery enabled under this protocol can select candidates. It cannot start a
sandbox until the execution gates in Authorization Contract v2 and its required
successor are also met.

Normalized entity v1 covers GitHub issues, pull requests, and Discussions;
GitLab issues and merge requests; and Jira issues. Forgejo is not yet covered,
so Forgejo discovery stays disabled under gate 2. The Forgejo mapping below is
the target mapping for when a normalized-entity contract covers it.

An unsupported provider/kind pair is never treated as an empty source. A
harness that declares one is rejected for scheduled evaluation.

## Source declarations

A source declaration has these logical fields:

| Field | Required | Meaning |
|-------|----------|---------|
| `system` | yes | Provider: `github`, `gitlab`, `forgejo`, or `jira` |
| `kind` | yes | Normalized entity kind: `work_item`, `change_proposal`, or `conversation` |
| `query` | yes | Logical query over routing facts for this system and kind |
| `rediscovery_interval` | no | Maximum interval between full rediscovery passes for a time-dependent query; when absent, the platform default applies; see [Time-dependent queries](#time-dependent-queries) |

A declaration does not name its host, tenant, repository, project, or
credential. Those come from trusted platform and repository configuration.
That keeps the scope guard separate from harness queries, as ADR 0149 requires.

Each declaration has a stable **source ID**: the harness identity, the
harness revision, and the declaration's position in that harness. A changed
harness revision is a new source ID.

Trust rules:

- Source declarations come only from trusted harness configuration at a
  verified revision. Entity content, event payloads, CLI flags, request
  headers, and other caller-supplied claims cannot add, remove, or change a
  declaration.
- A harness with missing or malformed source declarations is skipped for
  scheduled evaluation, as Authorization Contract v2 requires. Other harnesses
  are unaffected.
- A harness with no source declarations is event-only. It is never part of a
  discovery plan, and its event-triggered behavior is unchanged.
- An adapter that cannot admit a declaration's query MUST reject that source.
  It MUST NOT silently omit the source, and it MUST NOT run a separate search
  for it. The rejection is visible in diagnostics and audit, and the harness
  is not scheduled from that source.
- A declaration cannot widen the source scope. A query that names a scope
  dimension, such as a repository, is rejected unless the value equals the
  trusted scope value. Even then it adds nothing to the scope.

## Source scopes

A source scope is the tuple:

| Dimension | GitHub | GitLab | Forgejo | Jira |
|-----------|--------|--------|---------|------|
| Provider | `github` | `gitlab` | `forgejo` | `jira` |
| Tenant | Source host | Source host | Source host | Jira site host |
| Container | Immutable repository ID | Global project ID | Immutable repository ID | Immutable project ID |
| Entity kind | Normalized kind | Normalized kind | Normalized kind | Normalized kind |
| Effective credential scope | Identity and grant of the discovery credential | Same | Same | Same |

Rules:

- For each discovery cycle and source scope, `fullsend poll` MUST run one
  combined logical search covering every admitted query in that scope. It MUST
  NOT run a separate full search for each harness.
- Two declarations are in the same scope only when every dimension is equal.
  A different effective credential scope is a different source scope, even
  for the same repository.
- Before routing a result, `poll` MUST verify its canonical scope: provider,
  tenant, immutable container ID, and entity kind. Compare immutable IDs, not
  navigation paths or keys. A result outside the scope is discarded, recorded
  as a scope anomaly, and never routed. It is not a candidate and adds
  nothing to the scope's coverage evidence.
- Neither a query nor the combination of queries may widen the scope. An
  adapter MAY search more broadly than the union of admitted queries only
  inside the trusted scope and within provider limits.

## Routing facts

A logical query may test only these routing facts. Field names and meanings
are those of [Normalized entity v1](../../normalized-entity/v1/README.md).

| Routing fact | Type | Applies to | Class |
|--------------|------|------------|-------|
| `entity.kind` | enum | all | Structural |
| `entity.source.system` | enum | all | Structural |
| `entity.state` | `open`, `closed`, `merged` | all; `merged` only on change proposals | Source state |
| `entity.created_at` | timestamp | all | Source state |
| `entity.updated_at` | timestamp | all | Source state |
| `entity.labels` | list of strings | all kinds whose normalized mapping supports labels | Actor-originated |
| `entity.work_item.source_status` | object (`name`, `category`) | Jira work items | Source state |
| `entity.work_item.issue_type` | object (`id`, `name`) | Jira work items | Source state |
| `entity.change_proposal.draft` | bool | change proposals | Source state |
| `entity.change_proposal.base_ref` | string | change proposals | Source state |
| `entity.conversation.category` | object (`id`, `name`, `slug`) | GitHub Discussions | Source state |

Classes:

- **Structural** facts identify the entity and its scope. They are not
  actor-attributed and need no actor gate.
- **Source state** facts follow Authorization Contract v2's causal rule. A
  fact is actor-neutral for a particular use only when an actor-originated
  change cannot make the query newly match. For example, raw `updated_at`
  compared with a moving lower bound (`updated_at < now - 30d`) is
  actor-neutral; `updated_at >= cutoff` is not. When the causal rule makes a
  use actor-dependent, the fact needs the same admission as an
  actor-originated fact.
- **Actor-originated** facts require actor admission before they can make a
  query match. For `entity.labels`, each label used as a witness needs an
  admitted label application, or the exact verified label-transition grant,
  under Authorization Contract v2 and the harness's effective
  `human_min_role` from ADR 0151. Raw current label state from a provider is
  not admitted evidence.

A routing fact that is unknown, partially loaded, unsupported, or failed, as
defined in Normalized entity v1, produces an `unknown` membership result
wherever the outcome depends on it. It never counts as absent or false.

Adding a routing fact is a non-breaking change within v1 only when it adds a
complete normalized-entity mapping, a class, provider mappings, and
conformance cases. Changing a fact's meaning or class requires a new major
version.

## Operators

A logical query is a boolean expression. Its leaves compare routing facts
with literals from the declaration. The operators in this table are the only
ones allowed in v1:

| Operator | Form | Local meaning |
|----------|------|---------------|
| `eq` | `fact eq literal` | Scalar fact equals the literal |
| `in` | `fact in [literal, ...]` | Scalar fact equals one of a non-empty finite set of literals |
| `has` | `fact has literal` | List-valued fact contains the literal as a member |
| `before` | `fact before bound` | Timestamp fact is strictly earlier than the bound |
| `at_or_after` | `fact at_or_after bound` | Timestamp fact is equal to or later than the bound |
| `and` | `and(q1, q2, ...)` | All operands match |
| `or` | `or(q1, q2, ...)` | At least one operand matches |
| `not` | `not(q)` | The operand does not match |

Literal rules:

- String literals compare exactly, after the normalization that Normalized
  entity v1 defines for the fact. Label comparison follows the provider's
  label-name identity as recorded in that mapping. There is no pattern,
  prefix, fuzzy, or case-folding match unless the normalized mapping defines
  it.
- A timestamp bound is either a fixed RFC 3339 timestamp or a **moving
  bound** of the form `now - <duration>`. `now` is the trusted host clock at
  evaluation time. A query with a moving bound is **time-dependent**.
- Structured facts compare by named member, for example
  `entity.work_item.source_status.category eq "done"`.

Evaluation is three-valued. Each leaf evaluates to `match`, `non_match`, or
`unknown`. `and`, `or`, and `not` follow Kleene three-valued logic:
`and` is `non_match` if any operand is `non_match`, otherwise `unknown` if any
operand is `unknown`; `or` is `match` if any operand is `match`, otherwise
`unknown` if any operand is `unknown`; `not` swaps `match` and `non_match` and
keeps `unknown`. An actor-originated witness that is excluded from the
harness's authorized view does not exist for that harness; a witness whose
admission is unresolved makes its leaf `unknown`.

`has` over a list-valued fact follows Normalized entity v1 collection
coverage. It is `match` when an admitted known member equals the literal. It
is `non_match` only when the collection's coverage is `complete` and no
member that equals the literal remains after unadmitted members are removed.
Otherwise it is `unknown`. A partial or empty partial list never proves
absence, so `not(fact has literal)` cannot be `match` on incomplete coverage.

### Admission rules

An adapter MAY admit a query only if all of these hold:

1. **Superset search.** The provider requests in the discovery plan return
   every entity in the scope that could produce `match` or `unknown` for the
   query after local evaluation. A provider filter may narrow the search only
   when it provably keeps that superset.
2. **Local reproducibility.** The adapter can evaluate the query locally over
   routing facts, using follow-up reads when needed, and get the result the
   provider filter was meant to approximate.
3. **No provider-only semantics.** The query relies on no operator, ranking,
   relevance score, sort-limited or top-N result, saved filter, server-side
   function, or full-text match that local evaluation cannot reproduce.
4. **Authorized-view safety.** No provider filter excludes an entity that
   could match after facts outside the harness's authorized view are removed
   locally. In particular, the adapter MUST NOT push a negated actor-originated
   condition, such as `not(entity.labels has "blocked")`, into a provider
   filter. An entity with an unadmitted `blocked` label still matches that
   query once the unadmitted label is removed.
5. **Supported facts.** Every routing fact the query uses is supported for the
   provider and kind. An unsupported fact makes the query inadmissible.

A query that fails any rule is rejected under the trust rules above.

## Discovery plans

For each discovery cycle and source scope, the adapter builds one discovery
plan from the admitted queries in that scope. The provider requests' combined
result MUST be a superset of the disjunction of those queries, within the
scope.

- A plan MAY use several provider requests. Examples are pagination,
  disjoint time windows, and separate provider endpoints for kinds that
  share a scope. The number of full searches MUST NOT grow with the number of
  harnesses, and a plan MUST NOT contain a provider request that serves only
  one harness because its query could not be combined.
- When the provider cannot express the disjunction, the adapter broadens the
  provider filter, for example by dropping a label filter and keeping the
  scope and an `updated_at` window, and relies on local routing.
- The **plan identity** is a digest over the protocol version, the source
  scope, the sorted source IDs of the admitted declarations, the adapter
  version, and the provider requests' filter parameters. Coverage evidence is
  bound to a plan identity.
- **Plan changes.** When the plan identity changes because a source was
  added, widened, or re-revisioned, earlier coverage evidence does not prove
  coverage for the new or changed sources. Before marking any range complete
  for them, the adapter MUST scan the scope for newly eligible entities: a
  full pass over the new provider filter, not only an incremental window from
  the old watermark. Coverage for unchanged sources may continue from their
  earlier evidence.

## Local routing

After reading results, `poll`:

1. Verifies each result's canonical scope, as described in
   [Source scopes](#source-scopes).
2. Computes the canonical `entity.id` exactly as Normalized entity v1 defines.
3. Removes duplicates by canonical `entity.id`. Results from different
   provider requests or pages for the same entity merge into one candidate.
   If two results disagree on a routing fact, the fact is `unknown` until an
   authoritative read resolves it at one source revision.
4. Evaluates each relevant harness's logical query against the candidate.
5. Sends the candidate, for CEL evaluation, to every harness whose result is
   `match`. A `match` selects a candidate for CEL. It does not launch a
   sandbox.

For an `unknown` result, `poll` MAY make follow-up reads of the routing facts
needed, MAY hand the candidate to a bound `run`, or MUST keep a durable retry
record. An `unknown` result is never recorded as `non_match`.

A `non_match` is final for the current cycle only if:

- the facts that decided it are bound to a source revision, and the coverage
  method guarantees that a later change to the entity will be rediscovered,
  for example because any change advances `updated_at` into a future window;
  and
- no actor-originated fact outside the harness's authorized view decided it.
  A check that stops early on a proven non-match MUST NOT use unverified
  actor facts to prove it.

If a provider guarantees that one page holds every result the query can return
for an entity, `poll` MAY route that entity once it has processed that page,
while it fetches later pages. Early routing does not establish coverage for
the combined search.

Discovery reads only the facts needed to decide source membership, with
enough coverage to decide a match. It need not fetch every entity field or
history needed for the harness's final CEL check.

## Bound-run membership checks

Before each sandbox cycle for an entity-driven harness, bound `run` MUST:

1. Read current routing facts for its canonical entity from an authoritative
   provider read, not from a search index or an earlier discovery result.
2. Evaluate the harness's logical query, from its current trusted revision,
   with the same evaluator and authorization rules that `poll` uses.
3. Evaluate the harness CEL predicate under Authorization Contract v2.

The membership check and the CEL check may run in either order. Both MUST be
`match` or true before a sandbox starts. This applies to cycles started by an
event, an eventless wakeup, or a timer. A verified event supplies a candidate
and still passes its separate event-actor authorization. It cannot replace the
membership check.

If membership is `non_match`, the cycle does not start a sandbox. If
membership is `unknown`, the cycle does not start a sandbox, and the bound run
keeps a retry or rediscovery path under [Retry behavior](#retry-behavior). A
stale search result never proves current membership.

## Pagination and coverage evidence

The combined search MUST NOT miss candidates because of pagination, provider
result caps, delayed indexing, or concurrent changes. **Reaching the last page
does not prove the search is complete.**

### Coverage evidence record

For each range it claims, an adapter produces a coverage evidence record:

| Field | Meaning |
|-------|---------|
| `protocol_version` | `entity-source-discovery/v1` |
| `plan_identity` | The plan the evidence belongs to |
| `scope` | The source scope tuple |
| `range` | The search-space part covered, such as an `updated_at` half-open interval `[low, high)` or adapter-defined keyset bounds |
| `method` | `stable_view` or `overlap_reconciliation` |
| `settled_before` | The latest time the adapter treats as settled: the read time minus the provider's indexing-delay allowance and clock-skew allowance |
| `pages_read` | Number of provider pages read |
| `result_cap_reached` | Whether any provider request hit a result cap |
| `status` | `complete`, `incomplete`, `unknown`, or `failed` |
| `reason` | Diagnostic for any status other than `complete` |

### Rules for `complete`

A range is `complete` only when every condition holds:

1. Every provider request in the plan for that range finished without error.
2. No request hit a provider result cap. A capped request leaves the range
   `incomplete` until the adapter splits it into narrower ranges that are each
   below the cap, or records a durable retry.
3. The range's upper bound is at or before `settled_before`, so entities still
   being indexed belong to a later range.
4. One of these methods established the result set:
   - **`stable_view`**: the provider guarantees a consistent snapshot or a
     keyset over an immutable ordering for the whole range, and the adapter
     used it; or
   - **`overlap_reconciliation`**: each new range overlaps the previous one by
     at least the provider's indexing-delay allowance, clock-skew allowance,
     and timestamp precision. Entities seen in the overlap are reconciled by
     canonical ID, so an entity that moved position during pagination is
     found in the overlap or a later range. Overlap alone does not recover
     an unchanged entity that a moved entity's departure shifted past a page
     boundary. To close that gap, the adapter MUST also detect that the
     range changed during pagination. It reads the whole range in successive
     full passes, and a pass counts as validated only when it and the pass
     immediately before it (the baseline) both return exactly the same set of
     canonical IDs and no entity returned by the later pass was updated after
     the baseline pass began. Requiring identical sets catches departures
     during either pass, because an entity that left the range after being
     read is missing from the next pass. If the two passes differ, the later
     pass becomes the baseline and the adapter reads the range again,
     keeping the entities it has discovered. If it cannot obtain a validated
     pair of passes within its bounded scan budget, the range stays
     `incomplete` with a durable retry record, and the retry starts a new
     first pass with no baseline.
5. Every candidate in the range has a final membership result for every
   relevant harness, or a durable retry record.

Otherwise the range is `incomplete`, `unknown`, or `failed`. If coverage is
uncertain, the adapter MUST NOT mark the range complete unless it records a
durable retry for the range or can prove the affected entities will be found
by a later range.

A discovery checkpoint or watermark MUST advance only across `complete`
ranges. Failure in one source scope does not change coverage in another.

### Time-dependent queries

A time-dependent query can start to match an entity that has not changed,
because its moving bound advances. Incremental `updated_at` windows do not
find such entities. For each time-dependent source, the platform MUST either:

- run a full rediscovery pass of that source over its scope at least once per
  effective rediscovery interval: the declared `rediscovery_interval`, or the
  platform default when none is declared; or
- keep durable due records keyed by source ID, canonical `entity.id`, and the
  time at which the entity's membership can next change, and recheck each
  record when it is due.

A time-dependent declaration is malformed when it has no declared
`rediscovery_interval`, no platform default applies, and the platform keeps
no due records for it.

## Retry behavior

Partial scans and uncertain results stay visible and retryable. They cannot
silently drop candidates or start a sandbox.

A durable retry record holds:

| Field | Meaning |
|-------|---------|
| `scope` and `plan_identity` | Where the uncertainty arose |
| `subject` | A canonical `entity.id`, or a range when no entity is known |
| `source_ids` | The declarations whose membership is unresolved |
| `reason` | `unknown_routing_fact`, `unresolved_actor_evidence`, `provider_error`, `rate_limited`, `result_cap`, `incomplete_coverage`, or `handoff_pending` |
| `first_seen`, `attempts`, `next_attempt` | Retry scheduling state |

Rules:

- Create a retry record whenever a candidate's membership stays `unknown`, a
  range is not `complete`, or a candidate is handed to a bound `run`.
- Retry with bounded backoff that respects provider rate limits and
  `Retry-After` or equivalent signals. Rate limiting defers work; it never
  advances a checkpoint.
- A handoff to a bound `run` is provisional. Scheduling a run, delivering a
  wakeup, or receiving a process-start receipt is not completion. The record
  stays until the bound run reports a definitive non-match with any
  cache-dependent recheck arranged, completes actionable work, or safely
  defers it under ADR 0144, or until rediscovery after checkpoint advancement
  is proven.
- If a `non_match` depended on a cached actor verdict, schedule the recheck
  that Authorization Contract v2's bounded actor-verdict cache rules require.
- Remove a record only when its subject has a final result or rediscovery is
  proven. Reaching a retry limit does not convert a record to `non_match`,
  empty, or handled. The subject stays unresolved, visible to operators, and
  cannot start a sandbox.

## Provider mappings and limits

Each adapter publishes a **limit profile** for its provider and kind: the
maximum page size, any per-query result cap, the indexing-delay allowance,
the clock-skew allowance, the timestamp precision of its range filter, and
the pagination method. Conformance tests use the profile. A missing or
unverifiable value is treated as the most conservative choice: no stable
view, unknown result cap, and a non-zero delay allowance.

The provider endpoints below are examples of mechanisms an adapter may use.
An adapter may choose others if it meets every rule in this document.

### GitHub

| Item | Mapping |
|------|---------|
| Scope | Host, repository GraphQL node ID, kind, discovery credential |
| Kinds | `work_item` (issues), `change_proposal` (pull requests), `conversation` (Discussions) |
| Listing | Repository issue and pull-request listings sorted by update time with an update-time lower bound, or GraphQL connections ordered by `UPDATED_AT`; Discussions through GraphQL |
| `state` | Provider open/closed state filters; `merged` decided locally from merge status |
| `labels` | Positive `has` conditions may narrow the provider filter; negated label conditions are never pushed down |
| `draft`, `base_ref`, `category` | Local evaluation; may narrow the provider filter only where the endpoint supports exact filtering |
| Limits | The REST issue listing returns pull requests as well as issues, so the adapter separates kinds locally. Page-based listings over a mutable sort need `overlap_reconciliation`. The Search API returns at most 1,000 results per query, its index is eventually consistent, and its default order is relevance; an adapter that uses it MUST set an explicit sort, keep each window below the cap, and apply an indexing-delay allowance |

### GitLab

| Item | Mapping |
|------|---------|
| Scope | Host, global project ID, kind, discovery credential |
| Kinds | `work_item` (issues), `change_proposal` (merge requests) |
| Listing | Project issue and merge-request listings with `updated_after`/`updated_before` and update-time ordering |
| `state` | Provider state filters; `merged` decided locally |
| `labels` | Positive `has` conditions may narrow the provider filter. Provider label negation exists, but negated label conditions are never pushed down |
| `draft`, `base_ref` | Local evaluation, or exact provider filters where supported |
| Limits | Offset pagination over a mutable sort needs `overlap_reconciliation` unless the endpoint documents keyset pagination for that ordering. Project-scoped IIDs are not identities; deduplicate by canonical ID |

### Forgejo

Target mapping only. Discovery stays disabled until a normalized-entity
contract covers Forgejo.

| Item | Mapping |
|------|---------|
| Scope | Host, immutable repository ID, kind, discovery credential |
| Kinds | `work_item` (issues), `change_proposal` (pull requests) |
| Listing | Repository issue listing with `type`, `state`, `labels`, and `since`/`before` filters |
| Limits | Page and limit pagination with a server-configured maximum page size; the adapter reads the effective maximum and needs `overlap_reconciliation`. Forgejo must provide a verified effective role, or the actor-admission path is denied, as ADR 0151 states |

### Jira

| Item | Mapping |
|------|---------|
| Scope | Jira site host, immutable project ID, kind `work_item`, discovery credential |
| Kinds | `work_item` (issues) |
| Listing | JQL search through `/rest/api/3/search/jql` with cursor pagination (`nextPageToken`, `isLast`), ordered by `updated` then a stable key |
| `state`, `source_status`, `issue_type` | JQL `statusCategory`, `status`, and `issuetype` clauses may narrow the provider filter with exact values; local evaluation uses Normalized entity v1 mappings |
| `labels` | Positive `has` conditions may narrow the provider filter; negated label conditions are never pushed down |
| Limits | JQL date comparisons have minute precision and use the searching account's time zone, so overlap MUST be at least one minute plus any time-zone uncertainty. The adapter MUST assume search-index delay unless the provider documents read-after-write consistency for the result set. JQL functions, text search (`~`), and saved filters are provider-only semantics and are not admitted |

The existing Jira poll driver's `--jql` override, and every other
event-reconstructing poll driver, remains under
[ADR 0063](../../../ADRs/0063-polling-based-work-discovery.md). It is not an
entity-source declaration and is unaffected by this protocol.

## Event-only harnesses

Harnesses without entity sources are unaffected. They are never part of a
discovery plan, never receive `event == null`, and need no change to their
event-based predicates. The membership check in
[Bound-run membership checks](#bound-run-membership-checks) applies only to
entity-driven harnesses.

## Conformance cases

Each adapter MUST pass every case that applies to its provider and kind.
Each case states its setup and the required outcome. Cases that need no
provider are tests of the shared planner, router, and retry logic, and every
implementation MUST pass them.

### Declarations and trust

| ID | Setup | Required outcome |
|----|-------|------------------|
| ESD-01 | Harness has no source declarations | Harness is excluded from every discovery plan; its event-triggered routing is unchanged |
| ESD-02 | Harness has a malformed source declaration | Harness is skipped for scheduled evaluation with a visible diagnostic; other harnesses in the scope are still planned |
| ESD-03 | Event payload, entity content, or a CLI flag supplies a source declaration or query | Ignored; plan identity is unchanged |
| ESD-04 | Query uses an operator not in [Operators](#operators), such as a full-text match, ranking, or top-N | Source rejected visibly; no separate provider search is issued for it |
| ESD-05 | Query uses a routing fact unsupported for the provider/kind | Source rejected visibly; not treated as an empty source |
| ESD-06 | Declaration names a provider/kind pair outside the normalized-entity contract, such as any Forgejo kind under v1 | Source rejected; discovery stays disabled for the pair |
| ESD-07 | Time-dependent query with no `rediscovery_interval`, no platform default, and no due records | Declaration is malformed |

### Scope

| ID | Setup | Required outcome |
|----|-------|------------------|
| ESD-10 | Three harnesses declare compatible queries in one scope | One combined logical search; no provider request serves only one harness |
| ESD-11 | Two harnesses declare the same query but resolve to different discovery credentials | Two scopes, each with its own plan |
| ESD-12 | Provider returns an entity from another repository, project, host, or kind | Result discarded and recorded as a scope anomaly; not routed; not counted as coverage |
| ESD-13 | Query names a repository other than the trusted scope | Source rejected; scope unchanged |
| ESD-14 | Repository is renamed during discovery | Scope check uses the immutable container ID; results stay in scope |

### Query admission and provider filters

| ID | Setup | Required outcome |
|----|-------|------------------|
| ESD-20 | `entity.labels has "ready"` | Provider filter may include the label; local routing confirms an admitted label application before `match` |
| ESD-21 | `not(entity.labels has "blocked")` | Provider filter does not exclude `blocked`; with complete label coverage, an entity whose only `blocked` label was applied by a verified actor below the harness minimum routes as `match`; with partial label coverage it is `unknown` |
| ESD-22 | `or(q1, q2)` where the provider cannot express the disjunction | Adapter broadens the provider filter to a superset; local routing separates the matches |
| ESD-23 | Two harnesses use different label filters in one scope | Combined provider filter returns the union; each harness receives only its local matches |

### Local routing and deduplication

| ID | Setup | Required outcome |
|----|-------|------------------|
| ESD-30 | The same entity appears on two pages and in two provider requests | One candidate by canonical `entity.id` |
| ESD-31 | Candidate matches harnesses A and B but not C | Routed to A and B for CEL evaluation; C records `non_match` |
| ESD-32 | A routing fact the query depends on is `not_loaded`, `partial`, `unsupported`, or `failed` | Membership is `unknown`; follow-up read, handoff, or durable retry record; never `non_match` |
| ESD-33 | Two results for one entity disagree on a routing fact | Fact is `unknown` until an authoritative read at one source revision |
| ESD-34 | Label witness's actor role lookup fails | Leaf is `unknown`; candidate retained for retry; no `non_match` |
| ESD-35 | Label witness's actor is a verified human below the harness's `human_min_role` | Witness excluded for that harness; another harness with a lower minimum may still match |
| ESD-36 | Candidate is `match` in `poll` | Selected for CEL only; no sandbox starts from discovery alone |

### Bound-run membership

| ID | Setup | Required outcome |
|----|-------|------------------|
| ESD-40 | Event-backed cycle for an entity-driven harness; current facts no longer match the source query | No sandbox, even though the event actor is authorized and CEL is true |
| ESD-41 | Eventless cycle; membership `unknown` because of a provider error | No sandbox; retry or rediscovery path retained |
| ESD-42 | Discovery returned `match`; label removed before the bound run checks | Bound run's authoritative read yields `non_match`; no sandbox |
| ESD-43 | Membership `match` and CEL true from current facts | Sandbox may start, subject to every other gate |
| ESD-44 | Event-only harness receives an event | No membership check; existing event routing applies |

### Pagination and coverage

| ID | Setup | Required outcome |
|----|-------|------------------|
| ESD-50 | Provider reports the last page, but an entity was updated mid-scan and moved to an earlier page position | Range is not `complete` without overlap reconciliation; the entity is found in the overlap or a later range |
| ESD-51 | Provider request hits its result cap | Range is `incomplete` until split below the cap or retried; checkpoint does not advance |
| ESD-52 | Entity is updated just before the read but is not yet indexed | Range upper bound respects `settled_before`; the entity is found by a later range |
| ESD-53 | Provider error on page 3 of 5 | Range `failed` or `incomplete`; durable retry record; checkpoint does not advance |
| ESD-54 | Jira entity updated within the same minute as the window boundary | Overlap of at least one minute finds it |
| ESD-55 | New source added to a scope with an existing watermark | Full scan for newly eligible entities before coverage is claimed for the new source |
| ESD-56 | Provider guarantees per-entity page completeness | Entity may be routed after its page; the range is still not `complete` until all rules hold |
| ESD-57 | Rate limit response with a retry hint | Work deferred with backoff; no coverage claimed; checkpoint does not advance |
| ESD-58 | Range holds entities A, B, C, D with page size two; after page one, A is updated past the range's upper bound, so page two returns D and skips unchanged C; C lies outside the next range's boundary overlap | Overlap alone does not complete the range; the next full read returns a different ID set from the first pass, so it becomes the baseline and the range is read again, or the range stays `incomplete` with a durable retry record; checkpoint does not advance |
| ESD-59 | Range holds entities A to F with page size two; entity E is skipped in the first pass because A departs after page one, and in the validating pass B and C depart after page one, so E is skipped again | The two passes return different ID sets, so the range is not validated and stays `incomplete` unless a later pair of passes returns identical ID sets with no entity updated after the baseline began; checkpoint does not advance until then |

### Time-dependent queries and retry

| ID | Setup | Required outcome |
|----|-------|------------------|
| ESD-60 | `entity.updated_at before now - 30d`; entity unchanged for 31 days | Found by the next full rediscovery pass or due record, not by incremental windows |
| ESD-61 | `non_match` depended on a cached actor verdict | Recheck scheduled within Authorization Contract v2's bound |
| ESD-62 | Candidate handed to a bound run that exits before a definitive result | Retry record retained; candidate not recorded as handled |
| ESD-63 | Retry limit reached for an unresolved candidate | Candidate stays unresolved and visible; not recorded as `non_match`; no sandbox |
| ESD-64 | Process restarts with outstanding retry records | Records survive and are retried |

### Rule-to-case map

| Rule | Cases |
|------|-------|
| Trusted source declarations | ESD-01 to ESD-07 |
| One combined search per scope; scope guard | ESD-10 to ESD-14 |
| Admission and authorized-view safety | ESD-04, ESD-05, ESD-20 to ESD-23 |
| Deduplication and local routing | ESD-30 to ESD-36 |
| Unknown is never non-match | ESD-32 to ESD-34, ESD-41, ESD-63 |
| Bound-run membership before every sandbox cycle | ESD-40 to ESD-44 |
| Last page is not completeness | ESD-50 to ESD-59 |
| Time-dependent rediscovery | ESD-07, ESD-60 |
| Durable retry | ESD-53, ESD-57, ESD-61 to ESD-64 |
| Event-only harnesses unaffected | ESD-01, ESD-44 |

## Versioning

The protocol version string is `entity-source-discovery/v1`. Implementations
MUST reject coverage evidence, retry records, or plans that carry another
version.

These changes are non-breaking within v1 when each adds complete mappings and
conformance cases: a new routing fact, a new operator with defined local
semantics, a new provider or kind mapping, or a stricter limit profile.

These changes require `v2`: changing the meaning or class of a routing fact
or operator, relaxing an admission or coverage rule, or changing the plan
identity or coverage evidence fields incompatibly. Major versions follow
[ADR 0015](../../../ADRs/0015-normative-specifications-directory.md).
