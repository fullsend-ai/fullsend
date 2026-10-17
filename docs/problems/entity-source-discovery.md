# Entity-Source Discovery and Query Planning

How should Fullsend find candidate entities for scheduled harness evaluation,
decide which provider reads to make for each candidate, and prove that what it
read is complete enough to evaluate safely?

**Related:**
- [ADR 0098](../ADRs/0098-entity-first-harness-evaluation.md) — entity-first
  harness evaluation (the decision this document gathers requirements for)
- [Authorization Contract v1](../normative/authorization/v1/README.md) —
  Fullsend-originated entity discovery and the pre-CEL action-indicating gate
- [ADR 0133](../ADRs/0133-actor-attributed-data-security-boundary.md) —
  actor-attributed data admission
- [entity-context-snapshot v1](../normative/entity-context-snapshot/v1/README.md)
  and [ADR 0141](../ADRs/0141-deterministic-filtered-entity-context-staging.md)
  — bounded, gap-annotated acquisition for a run that has already been
  dispatched
- [ADR 0063](../ADRs/0063-polling-based-work-discovery.md),
  [ADR 0125](../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md) and
  [ADR 0132](../ADRs/0132-occurrence-aware-shared-poller-state.md) — existing
  poll drivers, poller backstop, and poller state
- [ADR 0106](../ADRs/0106-serialize-agent-runs-and-coalesce-subsequent-events.md)
  — per-harness, per-subject run serialization
- [agent-architecture.md](agent-architecture.md) — how forge events and
  scheduled discovery feed agent coordination
- [security-threat-model.md](security-threat-model.md) — attribution of
  scheduled runs and resource exhaustion
- Issues: [#8421](https://github.com/fullsend-ai/fullsend/issues/8421) (this
  record), [#8420](https://github.com/fullsend-ai/fullsend/issues/8420)
  (normalized entity v1), [#8418](https://github.com/fullsend-ai/fullsend/issues/8418)
  (implementation), [#313](https://github.com/fullsend-ai/fullsend/issues/313)
  (scheduled-run use cases)

## Scope of this document

This is a requirements record, not a decision. It collects first-slice
scenarios, provider capabilities, safety invariants, option trade-offs, and
unresolved choices so that a follow-up ADR can choose an acquisition strategy
and a later versioned normative discovery/query protocol can specify it. It
does not choose an implementation.

*Query planning* here means deciding which provider reads discover and resolve
candidates, which fields are read for evaluation, and how completeness is
proved. The logical normalized entity, its CEL field names, and the
CEL-observable partial-data semantics belong to
[#8420](https://github.com/fullsend-ai/fullsend/issues/8420). This document
says what information must be available and what acquisition state must be
preserved, not how CEL sees it. No provider query implementation is in scope.

Terms used below:

- **Candidate identity** — the stable cross-system identity of an entity that
  an entity source has enumerated, possibly with cheap hints such as an update
  time. It is not yet evaluable.
- **Resolution** — reading the entity's current normalized representation for
  an identity.
- **Acquisition** — all provider reads made for one candidate before and
  during predicate evaluation, including action-indicating history and actor
  permissions.
- **Coverage** — whether a read set, collection, or candidate list is known to
  be complete, known to be bounded, or unknown.

## The problem

ADR 0098 replaces event reconstruction with one harness CEL predicate over a
required `entity` and a nullable `event`. During scheduled discovery `event`
is null, so everything the predicate needs comes from provider reads. ADR 0098
also requires that entity sources "enumerate candidate identities and resolve
an identity to its current normalized representation", that Fullsend MAY
combine compatible sources into shared provider queries, and that the
field-level contract and query-planning protocol exist before entity-first
execution is enabled.

Reading every field and every activity record for every candidate before
testing predicates does not scale. A repository can have thousands of open
issues, and one long-lived change proposal can have hundreds of comments,
reviews, and label events. Reading too little is worse than reading too much:
a predicate that sees a truncated comment list may conclude that an
authorized request is absent or already handled, and a discovery cycle that
silently loses a page of candidates may conclude that there is no work. ADR
0098's authorization rules forbid both. "Failed, unavailable, or incomplete
enumeration denies evaluation of that entity; only successful enumeration may
produce an empty set."

## Where the code is today

Entity-first evaluation is not implemented. The current code constrains which
options are cheap to adopt:

- **No entity sources.** The harness schema has no entity-source field, and
  nothing in Go implements one.
- **CEL sees only events.** `NewTriggerEnv` in `internal/harness/trigger.go`
  declares a single `event` variable of dynamic type. `EvaluateTrigger`
  compiles the expression and evaluates it over a fully materialized
  `map[string]any` from `normevent.Event.ToMap()`. Nothing is loaded lazily and
  no field-dependency analysis takes place. `MatchHarnesses` treats an
  evaluation error as "skip this harness".
- **Polling reconstructs events.** `fullsend poll` has GitLab (REST v4,
  `updated_after` filters, page-numbered lists, notes, and resource label
  events) and Jira (`/search/jql` with `nextPageToken`) drivers. GitHub has
  no poll driver; it relies on webhook dispatch. GitLab state is an
  HMAC-signed `state.json` on a poll-state branch with occurrence-aware keys
  and watermarks (ADR 0132). Jira uses per-issue `lastCheck` entity properties
  (ADR 0063).
- **Silent truncation exists today.** A failed page returns an error, and a
  failed per-item read holds the GitLab watermark back. But the GitLab poll
  client's list loops stop after 100 pages, and Jira `SearchIssues` stops
  after 200 pages. In both cases the client returns what it has with no error
  and no truncation signal. This is an existing correctness gap, not an
  acceptable trade-off, and a watermark does not cover it. GitLab note lists
  always start at page 1 in ascending order, so in thread-list-based
  all-events polling, once a thread passes the cap its newer notes are never
  seen on any later poll. The fast slash-command path is different: it reads
  note bodies from the project events feed rather than listing threads, so a
  newer slash command can still be found there. That path has its own
  project-event pagination cap with the same silent truncation. The poll
  client contract also requires list methods to exhaust pagination. The gap
  therefore affects event reconstruction as well as entity-first discovery.
  It is exactly the "partial result looks complete" failure that
  entity-first discovery must not inherit.
- **Permission lookups differ by provider.** GitHub uses the
  collaborator-permission endpoint and GitLab uses `members/all`, one call
  per actor with no cross-call caching. Jira reads the project role
  structure (a role-list request plus one detail request per role) and
  resolves per-actor group membership, with per-cycle maps that reuse role
  and group resolution across issues. ADR 0107 bot-role recognition is not
  yet implemented.
- **The snapshot is specified but not built.** Entity-context-snapshot v1
  already specifies bounded acquisition with explicit `gaps[]` (`profile_bound`
  and `history_unavailable` are partial; `fetch_failed`, `unsupported`,
  `authorization_failed` and others are unusable). It runs after dispatch,
  not before CEL, and has no Go implementation yet.

## First-slice scenarios

ADR 0098 and #313 motivate several scheduled-discovery use cases. They impose
very different acquisition requirements, so choosing the first slice is the
largest single lever on scope.

| # | Scenario | Entity kind | What the predicate needs | Acquisition pressure |
|---|----------|-------------|--------------------------|----------------------|
| S1 | **Unhandled authorized request.** An authorized slash command such as `/fs-fix` has no matching agent result, for example because its webhook was missed or its dispatch failed ([ADR 0098](../ADRs/0098-entity-first-harness-evaluation.md) context). | change proposal, work item | Action-indicating comments with current actor permission, plus handled-state evidence such as a later agent comment or receipt | Full comment enumeration per candidate; permission per distinct actor |
| S2 | **Missed label transition.** A trigger label such as `ready-to-code` is present but no run followed. This is the backstop role ADR 0125 gives the GitLab poller. | work item, change proposal | Current labels; the actor of each label application, with current permission; handled-state evidence | Label-event history per candidate. The current label set alone does not identify who applied a label. |
| S3 | **Periodic reconsideration.** Re-triage aged issues ([agents#1137](https://github.com/fullsend-ai/agents/issues/1137)) or nudge stale change proposals. | work item, change proposal | State and timestamps; possibly whether a recent agent comment exists | State-only predicates. ADR 0098 still requires successful action-indicating enumeration before CEL, even if the result is empty. |
| S4 | **Repository-level schedule.** For example, a daily scribe or weekly activity summary from #313. | none, or the repository itself | No per-entity state | ADR 0098's predicate requires an `entity`. A repository-level run either needs a repository entity kind or stays on the existing schedule/manual dispatch path, which is authorized as a `NormalizedEvent` with a service actor. |

Requirements that hold for whichever slice is chosen:

- A first-slice entity kind is admissible only if the platform can enumerate
  every action-indicating category the authorization contract names for it
  (bodies, comments, reviews, label applications) from that provider. A kind
  or provider that cannot do so is unsupported and denied. It must not be
  approximated.
- The first slice should name its providers. ADR 0141 limits
  entity-context-snapshot v1 to GitHub, GitLab, and Forgejo forge entities.
  Jira `work_item` entities, GitHub Discussions (`entity.kind: conversation`,
  [ADR 0086](../ADRs/0086-conversation-surface-for-agent-participation.md)),
  and repository-level runs each add capabilities the forge slice does not
  need.
- S1 and S2 are the cases where silent truncation causes missed or duplicated
  work. They should drive the completeness requirements even if S3 is the
  cheapest to ship first.

### What a harness may declare as an entity source

ADR 0098 says harnesses declare entity sources and that missing or malformed
sources deny scheduled evaluation for that harness. The declaration's shape is
open. Requirements on any shape:

- **Data, not code.** The declaration is validated configuration. It names an
  entity kind and selection criteria within the harness's target repository
  (per-repo mode). It cannot carry credentials, choose an identity, or widen
  the run's permissions.
- **Narrowing only.** A source can only narrow the candidate set and the
  fields read. It cannot expand the platform's CEL-eligible field allowlist,
  and it cannot exclude an action-indicating category from the pre-CEL gate.
  The authorization contract says "a harness cannot exclude a supported
  category from classification".
- **Separate from scheduling and eligibility.** Schedule metadata and
  platform eligibility policy are separate inputs under ADR 0098. A source
  says *what* to enumerate, not *when* or *whether* the platform permits it.
- **Comparable for sharing.** For Fullsend to combine compatible sources into
  shared provider queries, it must be able to decide mechanically when two
  sources overlap or contain one another.

Candidate declaration shapes:

| Option | Benefits | Costs |
|--------|----------|-------|
| **Platform-defined presets.** Fixed named sources such as "open change proposals" or "open work items with label L". | Easy to prove complete per provider; trivially comparable and shareable; small validation surface | Less expressive; every new need requires a platform change |
| **Structured forge-neutral filter.** A small typed subset: kind, state, labels, updated-since window, author association. | Portable across forges; comparable; each filter term can be mapped to a provider capability or rejected | Every term needs a per-provider mapping and a completeness argument; terms that some providers cannot filter server-side must be applied client-side over a larger enumeration |
| **Provider-native query string.** For example, a forge search query or JQL. | Maximum expressiveness; reuses user knowledge | Not portable; hard to compare for sharing; some search endpoints cap total results (the GitHub REST search API returns at most 1,000 results per query), so completeness is unprovable for large result sets; query text becomes an injection-adjacent configuration surface |

## Provider capabilities

The cross-forge contract should be stated as capabilities, not mechanisms.
GraphQL is one way to provide several of these in fewer round-trips. REST
list endpoints, search endpoints, and event or timeline APIs are others. A
provider adapter advertises which capabilities it supports for which entity
kinds. A harness whose sources need an unsupported capability is denied
scheduled evaluation.

| Capability | Needed for | If missing or failing |
|------------|-----------|-----------------------|
| **C1. Enumerate candidate identities** for a declared source with stable ordering and an explicit end-of-results signal: a final page, an exhausted cursor, or an authoritative total. | All scenarios | Candidate list incomplete (see invariant I2) |
| **C2. Resolve current state** for one identity, or a batch: kind, state, labels, timestamps, head revision for change proposals. | All scenarios | Candidate cannot be evaluated; denied |
| **C3. Enumerate action-indicating history** completely for each category the entity kind supports: body, comments, reviews, and label applications *with the applying actor*. | ADR 0098 pre-CEL gate; S1 and S2 | Entity denied (ADR 0098) |
| **C4. Current-revision attribution.** The creator, or the latest authenticated editor where the source records edits ([ADR 0133](../ADRs/0133-actor-attributed-data-security-boundary.md)). | Pre-CEL gate and later model admission | The element cannot trigger execution; for model admission the record is redacted |
| **C5. Current actor authorization.** Repository permission for humans; provider-backed exact bot-role recognition for bots ([ADR 0107](../ADRs/0107-bot-identity-resolution-for-dispatch-authorization.md)). | Pre-CEL gate | The element cannot trigger execution |
| **C6. Change hints.** An updated-since filter or comparable cursor to skip unchanged candidates. | Cost control, where skipping is safe (see below) | Full re-enumeration every cycle |
| **C7. Budget visibility.** Rate-limit or quota state for the credentials in use. | Bounding a cycle (invariant I10) | Budget exhaustion surfaces only as request failures. These must still be classified as incomplete, not empty. |
| **C8. Batched reads.** Several identities or several collections in one request. | Sharing reads across candidates and harnesses | More round-trips; correctness is unaffected |

C1 through C5 are correctness requirements. C6 through C8 are efficiency
requirements and can be absent in a first slice.

C6 is predicate-aware. A complete C1 result does not show that the entities
a change hint excludes need no work. Unchanged entities may be skipped only
when time, handled state, authorization changes, and retry obligations cannot
change their eligibility. S3 can cross an age or staleness threshold with no
provider update, and S1 covers a failed dispatch where the entity may never
change again. Periodic rescans and explicit next-evaluation scheduling are
unresolved alternatives for such cases.

## Data that must be fetched regardless of CEL field use

Some reads are required before any harness predicate runs, independent of
which fields the predicate accesses. Under ADR 0098 and the authorization
contract's Fullsend-originated entity discovery section, these are:

1. **Invocation provenance and policy.** Verified, non-user-assertable poll
   invocation provenance; valid entity sources for the harness; the effective
   platform eligibility policy. None of these are entity reads, but each one
   gates whether any entity read happens.
2. **Candidate identity and resolution.** Stable identity, kind, and
   `entity.source.system`. Overlays used during discovery select
   source-specific configuration from `entity.source.system`.
3. **The complete action-indicating closed superset** for the entity kind:
   every element in every supported category, with actor provenance for the
   current revision. Successful enumeration may be empty; failed, unavailable,
   or incomplete enumeration denies the entity.
4. **Current permission for each distinct actor** of those elements, compared
   per candidate harness against that harness's observation or mutation
   threshold. Elements below the threshold, or whose permission is missing or
   unverifiable, are removed before CEL.

Item 3 dominates acquisition cost and does not depend on the predicate. For
an entity kind with comments, the full comment list, with authors, must be
read for every evaluated candidate even if the predicate reads only `state`.
Any acquisition strategy therefore saves reads only on fields outside the
action-indicating superset: element content bodies where classification does
not need them, check runs, diffs, linked entities, and the like. Savings on
which candidates reach item 3 come from narrowing at C1, through source
filters and change hints. This matters for the option comparison below. The
lazy and planned options cannot avoid the mandatory floor.

One permission lookup per distinct actor can serve every candidate harness
evaluated for the same entity within the logical lifetime the authorization
contract allows. The comparison against each harness's threshold differs; the
looked-up permission does not. The lookup cannot be reused across poll cycles
or carried forward to model-admission checkpoints. The contract requires
fresh resolution there.

## Acquisition strategy options

The issue asks for a comparison of loading fields on demand during CEL
evaluation, statically analysing CEL field dependencies and planning reads,
and a hybrid. A fixed per-kind profile, which mirrors entity-context-snapshot
collection profiles, is included as a baseline.

### Option A: On-demand loading during evaluation

The `entity` value presented to CEL fetches a field or collection the first
time the predicate touches it. CEL runtimes can resolve variables lazily
through custom activations or type providers.

- **Dynamic access.** Handled naturally; only touched paths are read.
- **Short-circuiting.** Reads follow the evaluated branch. CEL's logical
  operators are commutative and absorb errors (`false && <error>` is `false`;
  `true || <error>` is `true`). Whether a failed read changes the result
  therefore depends on operand values, not textual order. This is the
  intended CEL semantics, but the platform must not convert a load failure
  into a value. Because
  CEL's error absorption means a result can be `true` even when a read
  failed (for example `entity.optional_field == 'x' || true`), the
  evaluation result alone cannot enforce denial. Acquisition failures must be
  tracked separately from the CEL result and checked before a match is
  accepted. #8420 must decide whether any partial state is CEL-observable.
- **Nested collections.** Macros such as `exists` or `all` over a collection
  force the whole collection, so the savings vanish exactly where history is
  large.
- **Caching.** Needs a per-candidate cache whose lifetime matches the
  authorization contract's logical lifetime. Sharing across candidate
  harnesses is possible; sharing across candidates is hard.
- **Errors and cost.** Network I/O inside evaluation makes latency, budget
  use, and failure points depend on data. Batching across candidates (C8) is
  largely lost (N+1 reads). Evaluation is no longer a pure function of
  pre-read inputs, which complicates audit and reproducibility.

### Option B: Static dependency analysis, then planned reads

At harness validation time, compute the set of entity paths each predicate
may access from the checked CEL AST. Union the sets across candidate
harnesses and sources, plan provider reads (including shared and batched
reads), then evaluate over a fully materialized, read-only value.

- **Dynamic access.** Computed keys, map indexing by variable, or
  `has()`-guarded optional paths cannot always be resolved statically. The
  analysis must over-approximate (read the whole subtree), or validation must
  reject such access. Under-approximation is unsafe as a silent default. It
  must at least turn into a not-loaded error that denies.
- **Short-circuiting.** Fields on every branch are read even when only one
  branch is evaluated, so cost is an upper bound.
- **Nested collections.** A whole-collection read is planned once and can be
  bounded and coverage-annotated before evaluation.
- **Caching.** The plan is computable per harness revision and reusable;
  reads can be shared across candidates and harnesses (ADR 0098's "compatible
  sources").
- **Errors and cost.** All I/O happens before evaluation, so failures are
  attributed to a read, not to an expression. Budget is predictable. Evaluation
  stays pure and replayable from recorded inputs.

### Option C: Hybrid (staged partial evaluation)

Read a cheap tier first: identity, state, labels, timestamps, and the
mandatory action-indicating floor. Evaluate each predicate with the remaining
fields marked unknown. CEL implementations such as cel-go support partial
evaluation over declared unknown attributes and return either a decided
result or a residual expression. Fetch only what the residual needs, then
finish evaluation.

- Saves reads when cheap-tier state decides most candidates, such as closed
  entities or a missing label. Combining with planning keeps batching.
- Adds a second evaluation path, and its soundness must be shown: a residual
  must never be decided by treating an unknown as absent. Debugging and audit
  must cover both stages.
- Inherits Option B's limits on dynamic access when computing which unknowns
  to fetch, and Option A's rule that a failed second-stage read denies.

### Option D: Fixed per-kind profile (baseline)

Read a fixed, bounded, platform-defined field profile per entity kind,
analogous to an entity-context-snapshot collection profile. Harness
validation rejects predicates that access fields outside the profile.

- Simplest to implement and to prove complete; reads are batchable and
  predictable.
- Over-reads for simple predicates and limits expressiveness to the profile,
  though the mandatory floor already dominates cost.
- Static analysis is still needed for the validation-time rejection, but only
  as an allowlist check, not as a planner.

### Comparison

| Concern | A: on demand | B: planned | C: hybrid | D: fixed profile |
|---------|-------------|-----------|-----------|------------------|
| Dynamic access | Natural | Over-approximate or reject | Over-approximate or reject for residual | Reject outside profile |
| Short-circuiting saves reads | Yes | No | Partly (cheap tier decides) | No |
| Nested collections | Whole collection on touch | Planned and bounded | Planned and bounded | Planned and bounded |
| Cross-candidate batching | Poor | Good | Good for both tiers | Good |
| Cost predictability | Low | High (upper bound) | Medium | High |
| Failure attribution | Inside evaluation | Before evaluation | Both stages | Before evaluation |
| Pure, replayable evaluation | No | Yes | Yes per stage | Yes |
| Savings vs. mandatory floor | Fields outside floor | Fields outside floor | Fields outside floor, plus early decisions | None |

All four options share the same safety rule. A field the predicate accesses
must resolve, or the candidate is denied. A failed load is tracked outside
CEL's result, so error-absorbing operators cannot mask it. None may treat
"not loaded" as "absent".

## Safety invariants

These hold regardless of the strategy chosen. Each failure case is
classified as **fail-closed** (the affected entity, element, or cycle
conclusion is denied) or **unsupported** (rejected at configuration or
capability-check time, before any read).

| Invariant | Failure case | Classification |
|-----------|--------------|----------------|
| **I1. No silent truncation.** A read that stops at a page cap, byte cap, record cap, or budget is *incomplete*, never complete-and-shorter. The existing poll-client page caps do not meet this. | Bound reached while reading any collection used for a decision | Fail-closed for that decision |
| **I2. Incomplete candidate enumeration never proves absence.** A failed page, search result cap, cursor error, or exhausted budget marks the candidate list incomplete. Fullsend must not conclude "no work" for unseen candidates or advance a cursor or watermark past them. As ADR 0132 puts it, "a watermark is not dispatch evidence". Whether candidates already retrieved may still be evaluated is an open question below. | Incomplete enumeration | Fail-closed for the cycle's completeness claim |
| **I3. Incomplete action-indicating enumeration denies the entity.** This is ADR 0098's rule. Only a successful enumeration may be empty. | Failure, unavailability, or a bound reached on any category | Fail-closed for the entity |
| **I4. Unbounded activity is bounded explicitly.** An entity whose history exceeds the platform bound is incomplete under I3, not truncated to fit. | History larger than the bound | Fail-closed, with a visible diagnostic so it is not a silent permanent skip |
| **I5. A kind or provider without full category coverage is unsupported.** | Provider cannot enumerate a required category, such as label-application actors | Unsupported: denied before reads |
| **I6. Missing authorization removes the element.** Missing actor provenance, missing or unverifiable current permission, or an unrecognized bot cannot trigger execution. Cached or historical relationships are insufficient. | Unattributed element; permission lookup failure; ADR 0107 lookup failure | Fail-closed for the element; for the entity if its required body is affected (ADR 0141) |
| **I7. Malformed configuration denies.** Missing or malformed entity sources, or missing, malformed, or unverifiable eligibility policy, deny scheduled evaluation. | Invalid declaration or policy | Unsupported for that harness or policy scope |
| **I8. Deduplication never hides work.** Scheduled candidates for the same identity arriving from several sources or pages are evaluated once per harness per cycle, keyed by stable cross-system identity, only when their evaluation contexts are equivalent. A webhook and a poll overlap is not such a case: webhook evaluation carries a prompting event and scheduled evaluation has `event = null`, and predicates may inspect either. Each distinct prompting event is still evaluated, and matching runs are coalesced afterward under [ADR 0106](../ADRs/0106-serialize-agent-runs-and-coalesce-subsequent-events.md). Pagination must not skip items when the underlying set changes. Prefer keyset or cursor pagination over offset pages, or re-scan an overlap window. Duplicates are harmless after dedup; skips are not. | Pagination drift; duplicate candidates | Skips: fail-closed (incomplete); duplicates: coalesced |
| **I9. Resolution is authoritative over enumeration hints.** State in a list response may be stale by evaluation time. The resolved entity, not the hint, feeds CEL, and [ADR 0106](../ADRs/0106-serialize-agent-runs-and-coalesce-subsequent-events.md) runs reconcile current state again. | Entity changed between enumeration and resolution | Use resolved state; a deleted or inaccessible entity is not evaluated |
| **I10. Budget for authorization is reserved.** A cycle must not spend its budget on optional reads and then fail permission lookups. Exhaustion is incomplete (I2/I3), not empty. | Rate limit or quota reached | Fail-closed |

### Partial acquisition state that Go representations must preserve

Whatever the strategy, the in-memory representation that acquisition produces
must keep enough state for the invariants above to be enforced and audited.
How, and whether, CEL observes these states is for #8420. The representation
must not collapse them:

- **Per candidate.** Discovered only (identity and hints), resolved, or
  evaluable (mandatory floor complete); the source or sources that produced
  it; the provider and entity kind.
- **Per field and per collection.** Loaded value; successfully loaded and
  empty; not requested; unsupported by the provider; failed, with an error
  class (not found, forbidden, transient, budget); or incomplete or bounded,
  with dropped count or continuation cursor where known.
- **Per action-indicating category.** Coverage of the enumeration, separately
  from the elements retrieved, so "complete and empty" is distinguishable
  from "failed".
- **Per actor.** Current-revision attribution, permission or bot-role result,
  and its resolution status: resolved, not found, failed, or unverifiable.
  Also the logical lifetime the result is valid for, so it cannot be carried
  across checkpoints.
- **Per cycle.** Whether candidate enumeration for each source was complete,
  so cursors, watermarks, and "nothing to do" conclusions advance only on
  complete enumeration.

Entity-context-snapshot v1's `gaps[]` vocabulary (`profile_bound`,
`history_unavailable`, `fetch_failed`, `unsupported`, `authorization_failed`,
and the partial/unusable split) is a natural starting point, but it describes
a staged run input with different lifetimes. Pre-CEL acquisition results
cannot satisfy the snapshot's model-admission authorization checks, because
the authorization contract requires fresh resolution at those checkpoints.

## Unresolved choices

The questions in #8421 and the choices above fall into three destinations.

**Needs a follow-up ADR** (architectural, decided before implementing field
acquisition):

- Which acquisition strategy (A, B, C, or D) to adopt, or which combination.
- The entity-source declaration shape: presets, a structured filter, or
  provider-native queries.
- The first-slice scenarios, entity kinds, and providers, including whether
  repository-level schedules (S4) need an entity kind or stay on the existing
  schedule/manual dispatch path.
- Whether candidates retrieved before candidate enumeration failed may be
  evaluated and dispatched in that cycle (I2), or the whole cycle is held.
- The policy for entities whose activity exceeds the bound (I4): permanent
  denial with a diagnostic, an operator override, or a larger bound for
  specific kinds.
- The sharing scope for reads and permission lookups: across candidate
  harnesses for one entity only, or across entities within a cycle, subject
  to the authorization contract's logical lifetime.
- How entity-first discovery relates to the existing event-reconstructing
  drivers and their state (ADR 0063 `lastCheck`, ADR 0132 poll-state
  branches): migration, coexistence, or replacement.

**Belongs in the versioned normative discovery/query protocol** (specified
once the ADR is decided):

- The exact entity-source schema and validation rules.
- The provider capability interface (C1–C8), per-provider capability
  declarations, and the mapping from each source term to provider reads.
- Default and maximum bounds: page sizes, record and history limits, and
  per-cycle budgets.
- Coverage and error codes for partial acquisition, and how they relate to
  entity-context-snapshot gap codes.
- Dedup keys, pagination requirements (cursor or keyset, overlap windows),
  and cursor or watermark advancement rules.
- The audit record for a scheduled evaluation: which reads were made, their
  coverage, and which elements were retained or removed.
- Conformance examples covering complete-empty, incomplete, unsupported, and
  authorization-failure cases, plus an error-absorbing `||` or `&&`
  predicate that reads a field whose load failed.

**Belongs in normalized entity v1 (#8420):**

- CEL-visible field names and types for `entity`.
- Which partial-data states, if any, CEL may observe, and how.
- The CEL-eligible field allowlist that entity sources may narrow but not
  widen.

## Relationship to other problem areas

- **[Agent Architecture](agent-architecture.md)** — Scheduled entity
  discovery is one of the two ways entities reach harness predicates. This
  document covers how those entities are found and read.
- **[Security Threat Model](security-threat-model.md)** — Fail-closed
  enumeration and the pre-CEL permission gate keep scheduled discovery from
  acting on unauthorized entity history. Explicit bounds and budget
  reservation keep a single large entity or repository from exhausting
  provider quota (resource exhaustion).
- **[Operational Observability](operational-observability.md)** —
  Incompleteness must be visible. An entity that is permanently denied
  because its history exceeds a bound, or a cycle held because enumeration
  failed, is an operational signal, not a silent skip.

## Open questions

- Which scenario should the first slice optimize for: S1/S2 (reconciling
  missed requests, where completeness is critical) or S3 (state-only
  reconsideration, where the action-indicating floor is the main cost)?
- Is cost dominated by the mandatory action-indicating floor in practice? If
  it is, the simplest strategy (Option D) may be adequate for the first slice,
  and planning or lazy loading matters only later.
- Can every first-slice provider enumerate label applications with the
  applying actor, or does some provider force label-triggered scenarios to be
  unsupported there?
- Should a harness be able to request fewer action-indicating categories than
  the platform superset for *reading* content, while the gate still
  enumerates all of them for authorization?
- Anyone who can comment on an entity can push its history past the bound
  (I4) and force a denial. That keeps the system fail-closed, but it is a
  targeted denial of service. Should comments from actors below every
  candidate harness's threshold count toward the bound, given that their
  permissions must still be resolved to prove they are below it?
- How should provider search result caps interact with source declarations?
  Reject sources that may exceed the cap, split them by time window, or treat
  overflow as incomplete?
- How much of the existing poll state (ADR 0132 occurrence keys, ADR 0063
  `lastCheck`) can serve as handled-state evidence or change hints, and how
  much is specific to event reconstruction?
