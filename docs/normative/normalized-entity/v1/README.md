# Normalized entity v1

This specification defines the logical, forge-neutral `entity` value that
harness CEL predicates receive under
[ADR 0098](../../../ADRs/0098-entity-first-harness-evaluation.md). It also
defines the partial-data invariants that every producer of that value must
uphold. It is the contract between entity sources (event resolution and
`fullsend poll` discovery), the platform authorization gate, and harness CEL
evaluation.

ADR 0098 requires this versioned contract to exist before entity-first
execution is enabled. The
[Authorization Contract v1](../../authorization/v1/README.md) denies
entity-first execution while the contract is missing or incomplete.
Publishing this specification satisfies the contract-existence precondition.
It does not enable entity-first execution: the implementation is tracked
separately, and until it ships, existing event-only dispatch under
[NormalizedEvent v1](../../normalized-event/v1/README.md) remains
authoritative.

> **Target contract — not yet implemented:** No shipped dispatch or poll path
> binds `entity` in CEL today. The rules below are normative for the
> implementation that adopts them.

This contract specifies logical content and invariants only. It does not
choose a Go type layout, a lazy-loading mechanism, or a provider query
strategy. Entity-source discovery and query planning are tracked separately
in [#8421](https://github.com/fullsend-ai/fullsend/issues/8421).

## Contract

- **Entity document schema:** [`normalized-entity.schema.json`](normalized-entity.schema.json)
  describes one normalized entity document. Its `entity` member is the value
  bound to the CEL variable `entity`. Its `coverage` and `evaluation` members
  are host-only.
- **Evaluation input schema:** [`evaluation-input.schema.json`](evaluation-input.schema.json)
  describes one entity-first predicate evaluation. It pairs an evaluable entity
  document with a [NormalizedEvent v1](../../normalized-event/v1/normalized-event.schema.json)
  or `null`.
- **Examples:** [`examples/`](examples/) contains conformance fixtures; see
  [Conformance examples](#conformance-examples).

The two schemas and the NormalizedEvent v1 schema form one local bundle.
Validators load all three into the same resolver registry, keyed by `$id`, and
never fetch `$id` values over the network. An unresolved reference is a
validation error.

The serialized document exists so that partial-data states can be expressed
and checked exactly. Implementations may hold the same logical content in any
in-memory form, provided every invariant below holds for the values a
predicate can observe.

## Scope (v1)

v1 supports exactly one provider and two entity kinds:

| `entity.source.system` | `entity.kind` | Native object |
|------------------------|---------------|---------------|
| `github` | `work_item` | Issue |
| `github` | `change_proposal` | Pull request |

The following are deliberately deferred. Each requires its own complete
mapping (identity, state, action-indicating categories, actor provenance, and
current-permission resolution) before it can be added. Until then,
resolving an out-of-scope entity yields `unsupported` coverage, which denies
entity-first evaluation:

- GitLab issues and merge requests. Formal reviews need an immutable record
  mapping (approvals have none; see entity-context-snapshot v1), and label
  application provenance needs resource label events.
- Forgejo issues and pull requests.
- Jira work items. Jira's authorization roster is per poll cycle, and Jira is
  outside entity-context-snapshot v1.
- The `conversation` entity kind
  ([ADR 0086](../../../ADRs/0086-conversation-surface-for-agent-participation.md)).

## Terms

- **Candidate document** (`phase: candidate`): a discovered or partially
  resolved entity. It always carries identity and may carry any facet whose
  coverage is `complete`. A candidate is never bound to CEL.
- **Evaluable document** (`phase: evaluable`): the per-harness form that may be
  bound to CEL. Every applicable facet has `complete` coverage, and the actions
  list has been filtered against that harness's threshold.
- **Facet:** a unit of resolution with its own coverage status. v1 defines the
  `state`, `change_proposal`, and `actions` facets. Identity (`kind`, `source`,
  `id`, `number`, `url`) is required in every phase and has no coverage entry.
  A candidate without resolved identity is not a candidate.
- **Action-indicating element:** an actor-originated history element whose
  content or state may be treated as a request for a stage to run (ADR 0098).
- **Retained element:** an action-indicating element whose actor's verified
  current permission meets the candidate harness's threshold.

## CEL context

Entity-first predicates are evaluated with two root variables:

| Variable | Type | Presence |
|----------|------|----------|
| `entity` | object defined by [CEL-visible fields](#cel-visible-fields) | Required. Always the evaluable per-harness form. |
| `event` | NormalizedEvent v1 object, or CEL `null` | Non-null for event-driven dispatch (`evaluation.origin: event`). `null` for scheduled discovery (`evaluation.origin: discovery`). |

Overlay `when` expressions receive the same `entity` and nullable `event`.
Overlays used for entity discovery select source-specific configuration from
`entity.source.system`, not `event.source.system`.

**Harnesses with entity sources** may be evaluated without an event. Their
predicates MUST test `event != null` before accessing any `event` field. That
guard provides crash safety only. A harness intended to match during
scheduled discovery MUST also contain an entity-state condition that can
succeed when `event` is null.

**Event-only compatibility.** A harness that declares no entity sources stays
event-triggered only and always receives a non-null `event`. Existing
predicates that reference only `event` need no change. Fullsend MUST NOT deny
such a predicate because entity resolution failed, was not attempted, or is
out of v1 scope (for example, a Jira event). If such a predicate does access
`entity`, the same fail-closed rules apply as for any other access: an
unresolved `entity` makes the evaluation an error, and the error denies.

Field access follows ordinary CEL semantics. The evaluable form carries every
field in the table below, except fields that do not apply to the entity kind
or to an element's category. Those are absent because they do not apply, not
because they are unresolved. Accessing an absent field is an ordinary CEL
"no such key" error. Guard with `entity.kind` or `category` checks, or with
`has()`, which is reliable for these fields.

## CEL-visible fields

Every field below is present in an evaluable document. Nullable fields carry
CEL `null` only when the source system authoritatively reports that no value
exists, never because data was unavailable. Timestamps are CEL strings in the
canonical form `YYYY-MM-DDTHH:MM:SS.nnnnnnnnnZ` (UTC, exactly nine fractional
digits), so lexical string comparison equals chronological comparison.

| Field | JSON / CEL type | Facet | Meaning |
|-------|-----------------|-------|---------|
| `entity.kind` | string: `work_item`, `change_proposal` | identity | Forge-neutral kind |
| `entity.source.system` | string: `github` | identity | Source system (ADR 0098 overlay selector) |
| `entity.source.host` | string | identity | Lowercase host, plus `:port` only when not 443 |
| `entity.source.repository` | string | identity | Lowercase `owner/repository` navigation path |
| `entity.source.repository_id` | string | identity | Immutable repository ID |
| `entity.id` | string | identity | Immutable opaque entity ID |
| `entity.number` | int | identity | Display number |
| `entity.url` | string | identity | Canonical entity URL |
| `entity.state` | string: `open`, `closed`, `merged` | state | Current state; `merged` only for `change_proposal` |
| `entity.author.id` | string | state | Immutable creator ID |
| `entity.author.login` | string or null | state | Current login; null when withheld |
| `entity.author.kind` | string: `human`, `bot` | state | Provider classification |
| `entity.created_at` | timestamp string | state | Creation time |
| `entity.updated_at` | timestamp string | state | Source last-update time |
| `entity.labels` | list(string) | state | Current labels, unique, UTF-8 byte order |
| `entity.change_proposal.base_repository` | string | change_proposal | Lowercase base repository path |
| `entity.change_proposal.head_repository` | string or null | change_proposal | Lowercase head repository path; null only when the source reports it deleted |
| `entity.change_proposal.base_ref` | string | change_proposal | Target branch |
| `entity.change_proposal.head_ref` | string | change_proposal | Head branch |
| `entity.change_proposal.head_sha` | string | change_proposal | Lowercase head commit ID |
| `entity.change_proposal.is_fork` | bool | change_proposal | Source-reported cross-repository flag |
| `entity.change_proposal.draft` | bool | change_proposal | Draft status |
| `entity.actions` | list(action) | actions | Retained action-indicating elements, sorted by `created_at`, then `id` |

`entity.change_proposal` is present only when `entity.kind` is
`change_proposal`. A predicate MUST check `entity.kind` before accessing it.

Each `action` element has these fields:

| Field | JSON / CEL type | Meaning |
|-------|-----------------|---------|
| `id` | string | Immutable source record ID; the `body` element uses `entity.id` |
| `category` | string: `body`, `comment`, `review`, `label_application` | Platform-defined category |
| `created_at` | timestamp string | Body/comment creation, review submission, or label application time. Immutable; never changes when the record is edited. |
| `revised_at` | timestamp string | Time of the current revision: the latest authenticated edit when the source records one, otherwise equal to `created_at`. Never earlier than `created_at`. Always equal to `created_at` for `label_application`. `actor`, `command`, and `review_state` describe the revision at this time. |
| `actor.id` | string | Immutable ID of the source-authenticated actor of the current revision |
| `actor.login` | string or null | Current login; untrusted display value |
| `actor.kind` | string: `human`, `bot` | Provider classification |
| `actor.role` | string | Current forge permission ([Authorization representation](#authorization-representation)) |
| `actor.bot_role` | string or null | Provider-resolved Fullsend bot role |
| `actor.role_verified` | bool | Always `true` for a retained element |
| `actor.is_entity_author` | bool | Routing aid; never authorization evidence |
| `command` | string or null | `body`, `comment`, `review` only. The first whitespace-delimited token of the first line of the current revision when that token is a [command token](#command-tokens), otherwise null |
| `label` | string | `label_application` only. Applied label name |
| `review_state` | string: `approved`, `changes_requested`, `commented`, `dismissed` | `review` only |

Fields that do not apply to a category are absent, so a predicate MUST check
`category` before accessing them. Free text (titles, bodies, comment and
review text, instructions) is not CEL-visible in v1; see
[CEL-visible data and model-facing context](#cel-visible-data-and-model-facing-context).

### Command tokens

A **command token** is a first-line token that begins with `/`, is at least 2
and at most 64 characters long (counted in Unicode code points, as JSON Schema
does), and contains no whitespace. A first token that begins with `/` but is
only `/`, or is longer than 64 characters (for example, a filesystem path or a
URL path pasted at the start of a comment), is not a command token. Its
element's `command` is `null`. This is an authoritative derived value, not an
unresolved field, and the element's coverage is unaffected. Such a token cannot
match any platform command, so nulling it loses no routing information and
keeps the entity document schema-valid. A producer MUST NOT truncate a token
or substitute another value to fit the bounds. Whitespace includes line
breaks, so a `command` value never ends in a newline. The schema expresses the
no-whitespace rule as a separate constraint rather than an end-anchored
pattern, so validators using different regular-expression dialects reject the
same values.

## Presence and coverage semantics

Each facet in a document, and each action-indicating category, has exactly one
coverage status:

| Status | Meaning | Reasons |
|--------|---------|---------|
| `complete` | Resolved. Values, including an empty list or a documented null, are authoritative for the resolution time. | none |
| `not_loaded` | Not requested or not yet acquired. | `not_requested` |
| `unsupported` | The provider or entity kind cannot supply this facet under v1. | `provider_unsupported`, `kind_unsupported` |
| `incomplete` | Some data was read, but the read was bounded, truncated, or paginated short of the end. | `truncated`, `pagination_incomplete` |
| `unknown` | The producer cannot establish whether the read is complete. | `consistency_unknown` |
| `failed` | The read, attribution, or permission resolution failed or was unavailable. | `fetch_failed`, `timeout`, `rate_limited`, `source_too_large`, `invalid_metadata`, `actor_unattributed`, `permission_unverifiable` |

These values are kept distinct as follows:

- A **value** is present in its field.
- **Absent** (the source authoritatively has no value) is CEL `null` for a
  nullable field, or an empty list for a collection, and only inside a
  `complete` facet. Within a `complete` actions facet, an empty
  `entity.actions` list means that successful enumeration found nothing, or
  retained nothing after filtering.
- **Every other state** (`not_loaded`, `unsupported`, `incomplete`, `unknown`,
  `failed`) is represented only by its coverage entry. The facet's fields MUST
  be omitted from `entity`. They MUST NOT be encoded as `null`, as an empty
  list, as a default value, or as a partial list. The schema enforces this:
  facet fields are present if and only if the facet's coverage is `complete`.
- The actions facet is `complete` if and only if every applicable category is
  `complete`.

**CEL observes no coverage state.** An evaluable document requires `complete`
coverage for every applicable facet, so a predicate only ever sees resolved
values. Coverage is not part of `entity`, and no CEL expression can
distinguish "not loaded" from "absent", because "not loaded" never reaches
CEL.

**Logical evaluation boundary.** Evaluation is allowed only if every
applicable facet resolves to `complete`, whether or not the predicate reads
it. An applicable facet that does not resolve denies the evaluation even if the
predicate never accessed that facet, and even if the predicate would otherwise
return a boolean. This matches the evaluable-document requirement above: an
evaluation with any unresolved applicable facet has no evaluable document.

**Deferred acquisition invariant.** An implementation may defer acquiring the
`state` or `change_proposal` facet until a predicate first accesses it,
provided the observable outcome is identical to eager resolution under the
boundary above. A deferred facet that was never accessed MUST still be resolved
before the evaluation is allowed. If it does not resolve to `complete`, the
evaluation is denied. If an accessed facet does not resolve to `complete`, the
access is an evaluation error, and the error denies. This includes `has()`
tests on that facet's fields: an unresolved facet MUST NOT make `has()` return
false, a field evaluate to `null`, or a list evaluate to empty. The actions
facet cannot be deferred, because ADR 0098 requires complete enumeration
before CEL whether or not the predicate reads `entity.actions`.

If a predicate evaluates to an error instead of a boolean, evaluation for
that entity and harness is denied. Implementations MUST NOT substitute `true`
or `false` for an error result. An access to an unresolved facet is stricter
still. It MUST deny the whole evaluation even when CEL's error-absorbing `&&`
and `||` would otherwise return a boolean. The platform records the failed
resolution outside the CEL result. A predicate's outcome therefore never
depends on data that did not resolve.

## Action-indicating elements

### Closed category superset

For each entity kind, the platform defines a closed superset of
action-indicating categories. A harness cannot exclude a supported category,
and enumeration MUST cover every category of the kind:

| `entity.kind` | Categories |
|---------------|------------|
| `work_item` | `body`, `comment`, `label_application` |
| `change_proposal` | `body`, `comment`, `review`, `label_application` |

A category that does not apply to the kind (`review` on a `work_item`) is
neither enumerated nor represented in coverage. It is not an `unsupported`
gap.

Enumeration MUST NOT be bounded. A page limit, record cap, or time cap that
stops short of the end makes the category `incomplete`. Enumeration happens
before CEL, whether or not the predicate reads `entity.actions`.

### Actor provenance

The element's actor is the principal that the source system authenticates as
having produced the exact current revision (ADR 0133): the creator of a
never-edited record, or the latest authenticated editor when the source
records an edit. An event sender, poller, or retrieval credential never
substitutes for that actor. Self-asserted metadata (such as Git author or
committer fields) is not provenance.

If the source cannot establish the current-revision actor for any enumerated
element, that element's category is `failed` with reason
`actor_unattributed`. The actions facet therefore fails, and evaluation of
that entity is denied. An element without an actor cannot be represented: the
schema requires a non-null `actor.id`.

### Ordering and identity

`entity.actions` is sorted by `created_at`, then by `id` in ASCII byte order.
Editing a record never reorders it. The `body` element uses the entity's own
`id` and `created_at`. Producers MUST NOT synthesize elements from a prompting
event, from a receipt, or from any source other than the entity's history.

`revised_at` carries the revision-aware time. The `actor` and `command` of an
element describe the revision current at `revised_at`, so a predicate that
orders a request against other activity MUST use the request's `revised_at`,
not its `created_at`. Otherwise an edit that turns an older record into a
request would look as if it predated activity that actually preceded the
request. A producer MUST NOT report a `revised_at` earlier than `created_at`.
When the source records no edit time, `revised_at` equals `created_at`.

## Evaluation boundary

For each candidate entity and each candidate harness, Fullsend MUST complete
these steps before evaluating the harness predicate:

1. Authorize the origin. Event-driven dispatch authorizes the prompting event
   actor under ADR 0054. Scheduled discovery requires trusted Fullsend
   invocation provenance (Authorization Contract v1, *Fullsend-originated
   entity discovery*).
2. Resolve identity and every applicable facet, subject to the deferred
   acquisition invariant above.
3. Enumerate every applicable action-indicating category completely.
4. Resolve the current permission of every enumerated element's actor.
5. Remove every element whose actor's verified current permission does not
   meet the harness's threshold. The result is the evaluable document for that
   harness, and its `evaluation.removed_elements` records the count removed.
6. Bind `entity` and `event`, then evaluate the predicate.

Each evaluable document is specific to one harness, because thresholds differ
by harness. Elements retained for one harness are never reused for another
without applying that harness's threshold.

| Condition | Outcome |
|-----------|---------|
| Identity cannot be resolved | No candidate; nothing is evaluated |
| Provider or entity kind outside v1 scope | Entity-first evaluation denied (`unsupported`) |
| Any applicable category is `not_loaded`, `unsupported`, `incomplete`, `unknown`, or `failed` | Evaluation of that entity denied |
| An element's actor cannot be attributed | Category `failed` (`actor_unattributed`); evaluation denied |
| An element actor's current permission is missing or unverifiable | Category `failed` (`permission_unverifiable`); evaluation denied |
| An element actor's verified current permission is below the threshold | Element removed; evaluation proceeds |
| Any applicable `state` or `change_proposal` facet does not resolve, whether or not the predicate accesses it | Evaluation denied |
| The predicate errors | Evaluation denied |

An unverifiable permission denies the whole evaluation rather than removing
only that element. The Authorization Contract requires that such an element
cannot trigger execution, and denial satisfies that. Denial also prevents an
unverifiable element from silently disappearing: silent removal would make
"no qualifying request" indistinguishable from "could not check", and could
remove handled-state evidence a predicate relies on. A verified permission
that falls below the threshold is a definite answer, so that element is
removed and evaluation continues.

Denial applies to one entity and harness pair. Other candidates and other
harnesses are evaluated independently. A denied scheduled evaluation is
reconsidered on a later discovery cycle.

## Authorization representation

Element actors use the [ADR 0107](../../../ADRs/0107-bot-identity-resolution-for-dispatch-authorization.md)
target representation from NormalizedEvent v1, with no legacy form:

- `kind: human`: `role` is the current forge permission in the Authorization
  Contract v1 vocabulary (`admin`, `maintain`, `write`, `triage`, `read`,
  `none`, `external`). `role_verified: true` means it was verified. `bot_role`
  is `null`.
- `kind: bot`: `role` is `none`. `role_verified: true` means the provider
  bot-role lookup completed, including a successful no-match. `bot_role` is
  the provider-resolved Fullsend bot role, or `null` for an unrecognized bot.

The threshold is the selected harness's observation (`triage`) or mutation
(`write`) category from Authorization Contract v1, recorded as
`evaluation.human_min_role`. A retained element satisfies one of the
following:

- a human whose verified `role` is at or above `human_min_role`; or
- a bot with `role_verified: true` and a non-null `bot_role`. The human role
  threshold does not apply to a recognized bot (ADR 0133).

A verified human below the threshold, or a verified bot lookup with no
recognized role, is a definite negative, and the element is removed. A
lookup that fails or is unverifiable denies evaluation, as described above.
The schema enforces the retained-element rules for both thresholds.

Permissions are resolved at entity evaluation time. Historical, cached
cross-evaluation, event-carried, or relationship-derived evidence (such as
`author_association`, contribution history, or `is_entity_author`) MUST NOT
substitute for current resolution. A resolution may serve every harness
evaluated against the same resolved entity structure in one evaluation pass.
It ends at the cache-lifetime boundaries of Authorization Contract v1: a
re-read, retry, or requeue requires fresh resolution. `evaluation.resolved_at`
records when that happened, for audit.

CEL may restrict further using retained elements and their actor fields. CEL
cannot weaken the platform gate, re-admit a removed element, or select an
element for a later authorization decision. The label-added and bot-review
event exceptions in Authorization Contract v1 authorize event paths only. They
do not admit an element to `entity.actions`.

## Handled-state evidence

A harness decides whether qualifying activity is already handled. Only the
following count as evidence:

- **Retained entity history.** For example, a later retained element from a
  recognized bot role, a review in a particular state, or a retained
  `label_application`. Because evidence must be retained, an actor below the
  threshold cannot forge a "handled" signal by posting a lookalike comment.
- **Entity state.** For example, `entity.state` or `entity.labels`. Labels are
  state, not requests: a harness that treats a label as a request to run MUST
  test a retained `label_application` element. Testing `entity.labels` alone
  would bypass the per-element gate.

Explicit per-harness receipts and poll checkpoints remain allowed by ADR
0098, but v1 does not expose them in `entity`. Their representation belongs to
the entity-source discovery work.

Example predicates (illustrative only):

```cel
// Triage: an open issue with a retained /fs-triage request and no retained
// triage-bot element created after the request's current revision. The
// request is ordered by revised_at, so a comment edited into a request after
// a bot response is still unhandled. Works with or without an event.
entity.kind == "work_item" && entity.state == "open"
  && entity.actions.exists(r, r.category == "comment" && r.command == "/fs-triage"
       && !entity.actions.exists(h, h.actor.bot_role == "triage"
            && h.created_at > r.revised_at))

// Fix: a retained /fs-fix on a non-fork change proposal, or the live event
// for one. The event guard comes first.
entity.kind == "change_proposal" && !entity.change_proposal.is_fork
  && ((event != null && event.transition.kind == "comment_added"
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-fix")
      || entity.actions.exists(a, a.category == "comment" && a.command == "/fs-fix"))

// State-only staleness: an open issue with no retained activity and no
// update since a fixed date.
entity.kind == "work_item" && entity.state == "open"
  && size(entity.actions) == 0
  && entity.updated_at < "2026-09-01T00:00:00.000000000Z"
```

## Relationship to NormalizedEvent v1

`NormalizedEvent.entity` identifies an event's target for routing.
Normalized-entity v1 is the resolved, current representation of that target.
Neither is derived by copying the other.

| NormalizedEvent v1 | Normalized-entity v1 |
|--------------------|----------------------|
| `entity.kind` (`work_item`, `change_proposal`, `conversation`) | `entity.kind` (`work_item`, `change_proposal`) |
| `entity.id` (integer display number) | `entity.number` |
| — | `entity.id` (immutable opaque ID) |
| `entity.url` | `entity.url` |
| `repo` | `entity.source.repository` (compared case-insensitively on GitHub) |
| `source.system` (`github`, `gitlab`, …) | `entity.source.system`; equal for forge-native events, the hosting forge for `manual` and `schedule` events |
| `actor` (prompting actor, checked by ADR 0054) | not represented; element actors are in `entity.actions` |
| `state.labels`, `state.change_proposal` (event-time snapshot) | `entity.labels`, `entity.change_proposal` (current at `resolved_at`) |

When `event` is non-null, the resolved entity MUST correspond to the event's
target:

- `entity.source.repository` equals `event.repo`.
- For a forge-native event (`event.source.system` is `github` or `gitlab`),
  `entity.source.system` equals `event.source.system`. For a `manual` or
  `schedule` event, `event.source.system` names the trigger, not a forge, so
  no equality applies. `entity.source.system` is the forge that hosts
  `event.repo`, as established by the trusted driver's own repository
  configuration. It is never derived from event payload fields. If that forge
  is outside v1 scope, the entity is `unsupported`, as for any other
  out-of-scope target.
- For an event whose `entity.kind` is `change_proposal`, the resolved entity
  is that change proposal and `entity.number == event.entity.id`.
- For a `work_item` event that carries `entity.linked_change_proposal` (for
  example a GitHub comment on a pull request), the resolved entity is the
  change proposal and `entity.number == event.entity.linked_change_proposal.id`.
- For any other `work_item` event, the resolved entity is that work item and
  `entity.number == event.entity.id`.

A `manual` or `schedule` event resolves its entity in the event's repository
on the forge that hosts it (see above). A `jira` event, or any target outside v1 scope,
cannot produce a normalized entity: harnesses with entity sources are denied
for that event, and event-only harnesses are unaffected. A mismatched
resolution is an error and denies evaluation. JSON Schema cannot express this
cross-field equality, so producers enforce it.

The event and the entity may disagree in time. `event.state` is an event-time
snapshot, while `entity` reflects `resolved_at`, and a source read may not yet
show the event's own record. Producers MUST NOT patch the entity from the
event. A harness that needs the prompting record reads it from `event`.

The [trusted-field](../../normalized-event/v1/README.md#trusted-fields)
classification for guarded overlays remains defined by NormalizedEvent v1,
which already lists `entity.source.system`. Every other `entity` field is
untrusted for that purpose until that section lists it.

## Relationship to entity-context-snapshot v1

[Entity-context-snapshot v1](../../entity-context-snapshot/v1/README.md) is
the filtered, model-facing data staged for a run that routing has already
selected. Normalized entity v1 is pre-CEL routing input. The two share
conventions but are produced and validated independently:

- **Shared identity:** `kind`, the immutable `id`, the display `number`, the
  `source.host`, `source.repository`, and `source.repository_id` mappings,
  GitHub actor IDs (`node_id`), and the GitHub state mapping are identical.
- **Different actor representation:** bots carry `role: none` with
  `bot_role` here, following NormalizedEvent v1. The snapshot's
  `role: bot` marker is not valid here and MUST NOT be projected in either
  direction.
- **Different completeness rules:** snapshot profiles may bound records and
  report partial `profile_bound` or `history_unavailable` gaps. Action
  enumeration here is never bounded, and any partial read denies evaluation.
  A snapshot `authorization_failed` gap records omitted unauthorized records.
  Here, a verified below-threshold actor is a normal removal, and only an
  unverifiable permission is `failed`.
- **No reuse of authorization results:** a producer may share source fetches
  between the two. Permission results follow each consumer's checkpoints under
  Authorization Contract v1 and ADR 0133. Admission to `entity.actions` never
  admits content to the model.

## CEL-visible data and model-facing context

The CEL-visible entity is limited to identity, state, structural change
proposal facts, label names, and, per element, category, actor, command
token, label, and review state. Titles, bodies, comment and review text,
instructions, and check data are not CEL-visible. Model-facing context comes
only from entity-context-snapshot v1 or other paths governed by
[ADR 0133](../../../ADRs/0133-actor-attributed-data-security-boundary.md).

All CEL-visible values, including retained elements and their command tokens,
remain untrusted input for routing. They cannot alter the run's configured
identity, permissions, harness configuration, or capabilities. Retention in
`entity.actions` is a routing-time authorization result, not model admission:
model-bound content still passes the ADR 0133 attribution, fresh
authorization, and filtering checkpoints. Further trust and injection
filtering may run after CEL routing and before the harness pre-script.

The audit record of a scheduled run identifies the verified invocation
identity, the target repository, the effective policy, the harness revision,
the resolved entity, and every retained element and its actor (ADR 0098). The
`evaluation` block carries the per-harness audit inputs available at
evaluation time.

## GitHub mapping

| Normalized field | GitHub source |
|------------------|---------------|
| `id` | Issue or pull request GraphQL `node_id` |
| `number` | Issue or pull request number |
| `url` | `html_url` |
| `source.host` | `github.com`, or the GitHub Enterprise Server host |
| `source.repository` | Lowercase `owner/repository` |
| `source.repository_id` | Repository `node_id` |
| `state` | Issue `OPEN`/`CLOSED` → `open`/`closed`. Pull request with `merged: true` → `merged`, otherwise `open`/`closed`. Any other value is `invalid_metadata`. |
| `author` | Creator `node_id`, current login, and `Bot` versus `User` classification |
| `labels` | Current label names |
| `change_proposal.base_repository`, `head_repository` | Lowercase base and head repository `full_name`. Head is `null` when the head repository was deleted. |
| `change_proposal.base_ref`, `head_ref`, `head_sha` | `baseRefName`, `headRefName`, `headRefOid` |
| `change_proposal.is_fork` | `isCrossRepository` |
| `change_proposal.draft` | `isDraft` |
| `body` element | The issue or pull request itself. The actor is the latest authenticated body editor, if the body was edited, otherwise the author. `revised_at` is `lastEditedAt` when non-null, otherwise `created_at`. |
| `comment` elements | Every issue comment, and for pull requests every review-thread comment, including minimized comments. `revised_at` is `lastEditedAt` when non-null, otherwise `created_at`. |
| `review` elements | Every submitted pull request review. `PENDING` reviews are unsubmitted and are not elements. `created_at` is `submittedAt`. `revised_at` is `lastEditedAt` when non-null, otherwise `submittedAt`. |
| `label_application` elements | For each currently applied label, its most recent `LabeledEvent`. `revised_at` equals `created_at`. |
| Human `role` | Collaborator permission resolution in Authorization Contract v1, including `owners_file` when enabled |
| Bot `bot_role` | ADR 0107 provider-backed bot-role resolution |

A currently applied label with no attributable `LabeledEvent` makes
`label_application` `failed` (`actor_unattributed`). A record whose actor
GitHub reports as deleted (a null actor) has no established provenance and
makes its category `failed` (`actor_unattributed`).

## Conformance examples

Files prefixed `valid-` validate against their schema, and files prefixed
`invalid-` must be rejected. Files with a top-level `entity_document` use
[`evaluation-input.schema.json`](evaluation-input.schema.json). The others use
[`normalized-entity.schema.json`](normalized-entity.schema.json).

| Example | Shows |
|---------|-------|
| [`valid-candidate-partial.json`](examples/valid-candidate-partial.json) | Partial candidate. State resolved, change-proposal and actions facets `not_loaded` and omitted. Not evaluable. |
| [`valid-candidate-incomplete-history.json`](examples/valid-candidate-incomplete-history.json) | History explicitly `incomplete` or `unknown`. `entity.actions` omitted, never empty. Not evaluable. |
| [`valid-event-backed-fs-fix.json`](examples/valid-event-backed-fs-fix.json) | Event-backed evaluation (`event != null`). A pull-request comment event resolves to the change proposal through `linked_change_proposal`. Mutation threshold. |
| [`valid-scheduled-unhandled-request.json`](examples/valid-scheduled-unhandled-request.json) | Scheduled evaluation (`event == null`) with a retained `/fs-triage` request and no handled-state evidence |
| [`valid-scheduled-edited-request.json`](examples/valid-scheduled-edited-request.json) | Scheduled evaluation (`event == null`) where a triage-bot comment is created after a comment's creation but before the comment is edited into a `/fs-triage` request. `revised_at` places the request after the bot comment, so the request is unhandled. |
| [`valid-scheduled-empty-history.json`](examples/valid-scheduled-empty-history.json) | Genuinely empty retained history. Complete enumeration whose one element was removed below threshold. |
| [`valid-manual-event.json`](examples/valid-manual-event.json) | Event-backed evaluation for a `manual` event. `event.source.system` is `manual` while `entity.source.system` is the hosting forge `github`. |
| [`valid-command-token-bounds.json`](examples/valid-command-token-bounds.json) | Command-token boundaries. A bare `/` and an over-64-character slash-prefixed token yield `command: null`. Tokens of exactly 2 and 64 characters are retained. |
| [`invalid-command-bare-slash.json`](examples/invalid-command-bare-slash.json) | A bare `/` encoded as `command` instead of `null` |
| [`invalid-command-trailing-newline.json`](examples/invalid-command-trailing-newline.json) | A command token with a trailing newline. Must be rejected under every regex dialect. |
| [`invalid-missing-actor-provenance.json`](examples/invalid-missing-actor-provenance.json) | A retained element with no actor ID |
| [`invalid-unverifiable-authorization.json`](examples/invalid-unverifiable-authorization.json) | A retained element whose actor permission is unverified |
| [`invalid-history-omitted.json`](examples/invalid-history-omitted.json) | An evaluable document that claims complete enumeration but omits `entity.actions` |
| [`invalid-incomplete-history-as-empty.json`](examples/invalid-incomplete-history-as-empty.json) | Incomplete enumeration encoded as an empty list |
| [`invalid-unresolved-required-field.json`](examples/invalid-unresolved-required-field.json) | An evaluable change proposal whose required change-proposal facet `failed` |

## Versioning

This is a normative specification under
[ADR 0015](../../../ADRs/0015-normative-specifications-directory.md). The
schemas are closed (`additionalProperties: false`). `schema_version` is
`normalized-entity/v1`. Consumers MUST reject any other value.

| Change | v1 impact |
|--------|-----------|
| **Breaking** (requires `docs/normative/normalized-entity/v2/`): remove, rename, or retype a field; make a field nullable or optional; remove an enum value; remove a category from a kind's superset; allow CEL to observe a non-`complete` coverage state; allow bounded enumeration; weaken a retained-element or fail-closed rule | Producers and harnesses must migrate |
| **Additive** (allowed in v1 with a README mapping): add a provider or entity kind together with its complete mapping, add a category to a kind's superset, add a coverage reason, add a CEL-visible field to a facet (present whenever that facet is `complete`) | Fullsend-controlled producers and validators update together. Existing predicates keep their meaning. |
| **Non-breaking:** clarify documentation, add examples | None |

Adding a category or provider tightens what must be enumerated before
evaluation. It never relaxes the boundary. As with NormalizedEvent v1, a
consumer that vendors a strict schema copy must refresh it before a producer
emits additive values.

## Known limitations

- Entities with very long histories require unbounded enumeration before each
  evaluation. Efficient enumeration, shared provider queries, and indexing
  are tracked in [#8421](https://github.com/fullsend-ai/fullsend/issues/8421).
- Any action-indicating element authored by a deleted account denies
  evaluation of that entity, because v1 defines no verified tombstone actor.
  A later revision may add one as an additive change.
- Receipts and poll checkpoints are not CEL-visible in v1.
