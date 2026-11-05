# Normalized entity v1

This specification defines the logical, forge-neutral `entity` value that
harness CEL predicates receive under
[ADR 0098](../../../ADRs/0098-entity-first-harness-evaluation.md). It also
defines the partial-data and coverage invariants that every producer of that
value must uphold. It is the contract between entity sources (event resolution
and `fullsend poll` discovery), the platform authorization gate, and harness
CEL evaluation.

Three later accepted ADRs refine ADR 0098 and govern this contract:

- [ADR 0147](../../../ADRs/0147-evaluate-entity-cel-over-selectively-acquired-data.md)
  evaluates entity CEL over selectively acquired data. A predicate result is
  conclusive only when it holds for every complete entity consistent with the
  evidence. This contract defines how that partial evidence and its coverage
  are represented.
- [ADR 0148](../../../ADRs/0148-reuse-verified-actor-roles-for-entity-cel.md)
  allows bounded reuse of verified actor-role results.
- [ADR 0151](../../../ADRs/0151-harness-human-role-minimums.md) sets each
  harness's effective `human_min_role` (`triage`, `write`, `maintain`, or
  `admin`).

[Authorization Contract v2](../../authorization/v2/README.md) specifies the
authorization, audit, and retry rules these ADRs establish. This contract
applies to entity-source harnesses on the GitHub provider; it does not change
legacy non-CEL, non-entity workflows, which remain under
[Authorization Contract v1](../../authorization/v1/README.md).

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
- **Evaluable document** (`phase: evaluable`): the per-harness form that
  records a conclusive predicate result over selectively acquired data (ADR
  0147). Its `basis` is either `scope_complete` (every facet and category in
  the harness's acquisition plan is `complete`, and every other one is
  `not_loaded`) or `witness` (a true result settled by retained witness
  elements). The elements it carries have been filtered against that harness's
  `human_min_role`. An unresolved (`UNKNOWN`) result never forms an evaluable
  document.
- **Acquisition plan:** the conservative set of facets and categories that
  could change the harness predicate's result. The platform derives it from the
  compiled predicate and this contract (Authorization Contract v2), and it
  covers every field, history range, or derived fact the predicate can depend
  on, including counts and other aggregates over actor-originated records. A
  facet or category outside the plan is `not_loaded` and is not fetched. This
  contract does not choose how the plan is derived or how reads are issued.
- **Witness:** a retained action-indicating element that by itself settles an
  existence test as true. Only existence can be settled by a witness. A false
  or negative result needs `complete` coverage of its relevant search space.
- **Facet:** a unit of resolution with its own coverage status. v1 defines the
  `state`, `change_proposal`, and `actions` facets. Identity (`kind`, `source`,
  `id`, `number`, `url`) is required in every phase and has no coverage entry.
  A candidate without resolved identity is not a candidate.
- **Action-indicating element:** an actor-originated history element whose
  content or state may be treated as a request for a stage to run (ADR 0098).
- **Retained element:** an action-indicating element whose actor's verified
  current permission meets the candidate harness's effective `human_min_role`
  (or whose actor is a recognized bot, see
  [Authorization representation](#authorization-representation)).

## CEL context

Entity-first predicates are evaluated with two root variables:

| Variable | Type | Presence |
|----------|------|----------|
| `entity` | object defined by [CEL-visible fields](#cel-visible-fields) | Required. Always the evaluable per-harness form. Facets and categories outside the acquisition plan are omitted. |
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

Field access follows ordinary CEL semantics over the fields the document
carries. The evaluable form carries the fields of every facet in the
acquisition plan, except fields that do not apply to the entity kind or to an
element's category. Those are absent because they do not apply, not because
they are unresolved. Accessing a field that does not apply is an ordinary CEL
"no such key" error. Guard with `entity.kind` or `category` checks, or with
`has()`, which is reliable for these fields. A facet outside the plan is not
observable at all; see [Decision evidence boundary](#presence-and-coverage-semantics).

## CEL-visible fields

Every field below is present in an evaluable document whenever its facet is
`complete`. Nullable fields carry
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
| `entity.updated_at` | timestamp string | state | Raw source last-update time, advanced by any actor, including one below the harness minimum. It is not the time of the last authorized update. |
| `entity.labels` | list(string) | state | Current labels, unique, UTF-8 byte order |
| `entity.change_proposal.base_repository` | string | change_proposal | Lowercase base repository path |
| `entity.change_proposal.head_repository` | string or null | change_proposal | Lowercase head repository path; null only when the source reports it deleted |
| `entity.change_proposal.base_ref` | string | change_proposal | Target branch |
| `entity.change_proposal.head_ref` | string | change_proposal | Head branch |
| `entity.change_proposal.head_sha` | string | change_proposal | Lowercase head commit ID |
| `entity.change_proposal.is_fork` | bool | change_proposal | Source-reported cross-repository flag |
| `entity.change_proposal.draft` | bool | change_proposal | Draft status |
| `entity.actions` | list(action) | actions | Retained action-indicating elements of the acquired categories, sorted by `created_at`, then `id` |

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
does), and contains no whitespace. Whitespace is exactly this set of code
points: U+0009 through U+000D, U+0020, U+0085, U+00A0, U+1680, U+2000 through
U+200A, U+2028, U+2029, U+202F, U+205F, U+3000, and U+FEFF. That is the Unicode
`White_Space` property plus the byte-order mark U+FEFF, which some producers
leave at the start of a text. A first token that begins with `/` but is
only `/`, or is longer than 64 characters (for example, a filesystem path or a
URL path pasted at the start of a comment), is not a command token. Its
element's `command` is `null`. This is an authoritative derived value, not an
unresolved field, and the element's coverage is unaffected. Such a token cannot
match any platform command, so nulling it loses no routing information and
keeps the entity document schema-valid. A producer MUST NOT truncate a token
or substitute another value to fit the bounds. Whitespace includes line
breaks, so a `command` value never ends in a newline. The schema expresses the
no-whitespace rule as a separate constraint with this explicit character class
rather than a dialect-dependent `\s` or an end-anchored pattern, so validators
using different regular-expression dialects reject the same values (for
example, U+0085 and U+FEFF are rejected by every dialect).

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
- The actions facet is `complete` if and only if every acquired category is
  `complete`: each category is `complete` or `not_loaded` (outside the
  acquisition plan), and at least one is `complete`. A candidate that carries
  `entity.actions` carries the full enumerated superset, so every applicable
  category is `complete`. An evaluable document may leave categories outside
  its acquisition plan `not_loaded`.

**Decision evidence boundary.** CEL observes no coverage state. Coverage is
host-only, and no CEL expression can distinguish "not loaded" from "absent",
because a facet that is `not_loaded` is outside the acquisition plan and is not
observable. An evaluation produces one of three results (ADR 0147):

- `true` or `false`, only when the fetched facts and their proven coverage
  establish that result for every complete entity consistent with the
  evidence. This is the only result that forms an evaluable document.
- `UNKNOWN`, when a facet or history range in the acquisition plan has not been
  acquired, or when its coverage is `incomplete`, `unknown`, or `failed`, and
  the result could still change. An `UNKNOWN` result is not an evaluable
  document and is never `false`, `true`, empty, or an error substituted for
  either. The producer fetches more relevant data and re-evaluates, or leaves
  the candidate unresolved for a later discovery cycle or bound run. No
  sandbox starts from an unresolved result.
- Denied, when an unsupported source, failed provider read, unattributable
  actor, or unverifiable permission affects a decision dependency and the
  producer cannot establish the result within allowed bounds. A denial fails
  closed. It is recorded outside the CEL result and is never recorded as a
  `false` result.

An evaluable document has one of two bases:

- **`scope_complete`.** Every facet and every action category in the
  acquisition plan is `complete`. Everything else is `not_loaded` and is not
  fetched. Because the plan covers every field, history range, and derived
  fact the predicate can depend on, the outcome is the same as it would be over
  the complete entity. A `not_loaded` facet is therefore unobservable but
  immaterial. A planned read that is `incomplete`, `unknown`, `failed`, or
  `unsupported` yields `UNKNOWN` or a denial instead, so no evaluable document
  exists. The outcome is `true` or `false`.
- **`witness`.** A `true` outcome settled by one or more retained witness
  elements, recorded in `evaluation.witnesses`, while other planned reads may
  still be `incomplete` or `not_loaded`. A witness settles only existence. A
  `false` or negative result (the absence of a request, a count, or another
  aggregate) requires `complete` coverage of every category its search space
  covers, so it uses `scope_complete`.

A producer MUST NOT let an unacquired facet or category change a conclusive
result. An access to a planned facet that is not `complete` is never an
evaluation error that CEL's error-absorbing `&&` and `||` could absorb into a
boolean. It MUST NOT make `has()` return false, a field evaluate to `null`, or
a list evaluate to empty. If a predicate evaluates to an error instead of a
boolean, evaluation for that entity and harness is denied, and implementations
MUST NOT substitute `true` or `false` for the error.

The actions facet is acquired by category. The plan includes a category when
the predicate could depend on it, including through `size(entity.actions)`,
`exists`, or another aggregate. A category outside the plan is not fetched and
contributes no elements to `entity.actions`. A state-only predicate acquires
no history, so its `actions` facet is `not_loaded`.

Raw provider aggregates and signals are not authorized facts. `entity.updated_at`
and `entity.labels` come from the source without actor filtering. A predicate
that uses a raw field only to defer an action, such as a staleness check
`updated_at < cutoff`, is actor-neutral, because an unauthorized update can only
postpone it. A predicate that uses a raw field as evidence that something
happened, such as `updated_at >= cutoff` or the presence of a label, MUST also
establish that signal from retained elements (ADR 0147 and Authorization
Contract v2), or the result remains `UNKNOWN`. This contract does not define
those causal-occurrence proofs.

## Action-indicating elements

### Closed category superset

For each entity kind, the platform defines a closed superset of
action-indicating categories. A harness cannot exclude a supported category
from that superset. The acquisition plan needs only the categories that could
affect the predicate's result, but a predicate that depends on the whole action
history (for example `size(entity.actions)`, a negative test, or a dynamic
access the plan cannot analyze) puts every category of the kind in the plan:

| `entity.kind` | Categories |
|---------------|------------|
| `work_item` | `body`, `comment`, `label_application` |
| `change_proposal` | `body`, `comment`, `review`, `label_application` |

A category that does not apply to the kind (`review` on a `work_item`) is
neither enumerated nor represented in coverage. It is not an `unsupported`
gap.

Enumeration of an acquired category MUST NOT be bounded. A page limit, record
cap, or time cap that stops short of the end makes the category `incomplete`,
and an `incomplete` category cannot support a `false` or negative result (an
authorized witness can still settle an existence test as true). A category
outside the acquisition plan is `not_loaded` and is neither enumerated nor
audited. Unacquired history is never treated as empty.

### Actor provenance

The element's actor is the principal that the source system authenticates as
having produced the exact current revision (ADR 0133): the creator of a
never-edited record, or the latest authenticated editor when the source
records an edit. An event sender, poller, or retrieval credential never
substitutes for that actor. Self-asserted metadata (such as Git author or
committer fields) is not provenance.

If the source cannot establish the current-revision actor for any enumerated
element, that element's category is `failed` with reason
`actor_unattributed`. The actions facet therefore fails, and a decision that
depends on that category is denied. An element without an actor cannot be
represented: the schema requires a non-null `actor.id`.

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
   actor under ADR 0054 and ADR 0151: the actor must meet the platform
   `triage` floor and the selected harness's effective `human_min_role`.
   Scheduled discovery requires trusted Fullsend invocation provenance
   (Authorization Contract v2, *Fullsend-originated entity discovery*).
2. Resolve identity and derive the harness's acquisition plan from its
   predicate.
3. Acquire the planned facets, and enumerate each planned action-indicating
   category completely. Acquisition may be progressive: the evaluator may
   evaluate over the partial view and fetch more only when the result is
   `UNKNOWN`, and it MAY stop as soon as a witness settles a true result.
4. Resolve the current permission of the actor of every element in an acquired
   category.
5. Remove every element whose actor's verified current permission does not
   meet the harness's `human_min_role`. The result is the evaluable document
   for that harness, and its `evaluation.removed_elements` records the count
   removed from the acquired categories.
6. Bind `entity` and `event`, then evaluate the predicate. Only a conclusive
   `true` or `false` result forms an evaluable document.

Each evaluable document is specific to one harness, because minimums differ by
harness. Elements retained for one harness are never reused for another
without applying that harness's minimum.

| Condition | Outcome |
|-----------|---------|
| Identity cannot be resolved | No candidate; nothing is evaluated |
| Provider or entity kind outside v1 scope | Entity-first evaluation denied (`unsupported`) |
| A planned facet or category is `not_loaded` (not yet acquired) | Result `UNKNOWN`; acquire more or leave the candidate unresolved |
| A planned category is `incomplete` or `unknown` | `UNKNOWN`, or denied when it cannot be completed within allowed bounds. It supports no `false` or negative result. A witness may still settle `true`. |
| A planned facet or category is `unsupported` or `failed` | Evaluation of that entity denied for any result that depends on it |
| A facet or category outside the acquisition plan is `not_loaded` | Not an error; it does not affect the result |
| An element's actor cannot be attributed | Category `failed` (`actor_unattributed`); a result depending on it is denied |
| An element actor's current permission is missing or unverifiable | Category `failed` (`permission_unverifiable`); a result depending on it is denied |
| An element actor's verified current permission is below the harness minimum | Element removed; evaluation proceeds |
| The predicate errors | Evaluation denied |

An unverifiable permission denies the dependent result rather than removing
only that element. The Authorization Contract requires that such an element
cannot trigger execution, and denial satisfies that. Denial also prevents an
unverifiable element from silently disappearing: silent removal would make
"no qualifying request" indistinguishable from "could not check", and could
remove handled-state evidence a predicate relies on. A verified permission
that falls below the minimum is a definite answer, so that element is removed
and evaluation continues.

Denial applies to one entity and harness pair. Other candidates and other
harnesses are evaluated independently. A denied or `UNKNOWN` scheduled
evaluation is reconsidered on a later discovery cycle or in a bound run; it
stays retryable and never becomes a `false` result.

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

The threshold is the selected harness's effective `human_min_role` (ADR 0151,
Authorization Contract v2), recorded as `evaluation.human_min_role`. It
defaults to `triage` for a `triage` or `review` harness role and to `write` for
every other role. An explicit value may be `triage`, `write`, `maintain`, or
`admin`, and the platform `triage` floor is never waived. The role order is
`admin` > `maintain` > `write` > `triage`. A retained element satisfies one of
the following:

- a human whose verified `role` is at or above `human_min_role`. The retained
  roles are `admin`, `maintain`, `write`, and `triage` for `triage`;
  `admin`, `maintain`, and `write` for `write`; `admin` and `maintain` for
  `maintain`; and `admin` for `admin`; or
- a bot with `role_verified: true` and a non-null `bot_role`. The human role
  minimum does not apply to a recognized bot (ADR 0133 and ADR 0151).

A verified human below the minimum, or a verified bot lookup with no
recognized role, is a definite negative, and the element is removed. A
lookup that fails or is unverifiable denies a dependent result, as described
above. The schema enforces the retained-element rules for all four minimums,
for both `entity.actions` elements and `evaluation.witnesses`.

The minimum is resolved from trusted configuration before the harness actor
gate, and CEL cannot set or relax it. Each harness applies its own minimum to
shared role evidence. A retained element for one minimum is not valid for a
stronger one. [Entity-context-snapshot v1](../../entity-context-snapshot/v1/README.md)
keeps its fixed `triage`/`write` threshold under ADR 0151, so a snapshot
cannot stand in for the retained elements of a `maintain` or `admin` harness.

Permissions are resolved from the current provider source. Historical,
event-carried, or relationship-derived evidence (such as `author_association`,
contribution history, or `is_entity_author`) MUST NOT substitute for current
resolution. A completed, provider-verified role result may be reused across
progressive checks, harnesses, and `poll` and bound `run` processes, but only
within the bounded maximum age and under the scoping, invalidation, and audit
rules of ADR 0148 and Authorization Contract v2. Reuse is disabled when the
platform policy that bounds it is missing. An expired, invalidated, or
mismatched verdict requires a fresh lookup, and if that lookup cannot complete,
the dependent result stays `UNKNOWN` or is denied. The cache stores a role, not
a harness decision. Reuse never satisfies event-actor admission or model-bound
record admission, which resolve fresh authority. `evaluation.resolved_at`
records when the entity facts and actor roles were resolved, for audit.

CEL may restrict further using retained elements and their actor fields. CEL
cannot weaken the platform gate, re-admit a removed element, or select an
element for a later authorization decision. The label-added and bot-review
event exceptions in Authorization Contract v1 authorize event paths only. They
do not admit an element to `entity.actions`. Authorization Contract v2 also
lets an exact, verified label-transition grant admit a `label_application` fact
for a `triage` minimum without a verified role. v1 does not represent that
grant: it admits `label_application` elements only through a verified actor
role, which is stricter (see Known limitations).

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
  report partial `profile_bound` or `history_unavailable` gaps. Enumeration of
  an acquired action category here is never bounded, and a partial read of a
  planned category cannot support a `false` or negative result. A snapshot
  `authorization_failed` gap records omitted unauthorized records. Here, a
  verified below-minimum actor is a normal removal, and only an unverifiable
  permission is `failed`.
- **Different admission minimum:** the snapshot keeps its observation/mutation
  derived `triage`/`write` threshold, while this contract applies the harness's
  effective `human_min_role` (`triage`, `write`, `maintain`, or `admin`).
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
the resolved entity, the acquisition plan, and the decision outcome. Under ADR
0147 it records bounded decision evidence, namely the query scopes, their
coverage, and the retained witnesses and their actors, rather than a roster of
every history element and actor examined. Unrelated history is neither fetched
nor listed. The `evaluation` block carries the per-harness audit inputs
available at evaluation time. Authorization Contract v2 defines the remaining
audit requirements, including actor-verdict reuse evidence.

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
| [`valid-event-backed-fs-fix.json`](examples/valid-event-backed-fs-fix.json) | Event-backed evaluation (`event != null`). A pull-request comment event resolves to the change proposal through `linked_change_proposal`. `write` minimum. |
| [`valid-scheduled-unhandled-request.json`](examples/valid-scheduled-unhandled-request.json) | Scheduled evaluation (`event == null`) with a retained `/fs-triage` request and no handled-state evidence |
| [`valid-scheduled-edited-request.json`](examples/valid-scheduled-edited-request.json) | Scheduled evaluation (`event == null`) where a triage-bot comment is created after a comment's creation but before the comment is edited into a `/fs-triage` request. `revised_at` places the request after the bot comment, so the request is unhandled. |
| [`valid-scheduled-empty-history.json`](examples/valid-scheduled-empty-history.json) | Genuinely empty retained history. Complete enumeration whose one element was removed below the harness minimum. |
| [`valid-manual-event.json`](examples/valid-manual-event.json) | Event-backed evaluation for a `manual` event. `event.source.system` is `manual` while `entity.source.system` is the hosting forge `github`. |
| [`valid-command-token-bounds.json`](examples/valid-command-token-bounds.json) | Command-token boundaries. A bare `/` and an over-64-character slash-prefixed token yield `command: null`. Tokens of exactly 2 and 64 characters are retained. |
| [`valid-state-only-decision.json`](examples/valid-state-only-decision.json) | A `scope_complete` result from a state-only predicate. The state facet is `complete`, and the whole actions facet and its categories are `not_loaded` because they are outside the acquisition plan. No history is fetched. |
| [`valid-witness-true.json`](examples/valid-witness-true.json) | A `witness` result. A retained `/fs-triage` comment settles an existence test as `true` while the comment category is still `incomplete` and `entity.actions` is omitted. |
| [`valid-maintain-minimum.json`](examples/valid-maintain-minimum.json) | A `maintain` harness minimum. Maintain and admin humans and a recognized bot are retained, and a lower-role element was removed. |
| [`valid-admin-minimum.json`](examples/valid-admin-minimum.json) | An `admin` harness minimum. Only an admin human is retained. |
| [`invalid-witness-false-outcome.json`](examples/invalid-witness-false-outcome.json) | A `witness` result with outcome `false`. A witness settles existence only. |
| [`invalid-maintain-minimum-write-actor.json`](examples/invalid-maintain-minimum-write-actor.json) | A `write` human retained under a `maintain` minimum |
| [`invalid-admin-minimum-maintain-actor.json`](examples/invalid-admin-minimum-maintain-actor.json) | A `maintain` human retained under an `admin` minimum |
| [`invalid-command-bare-slash.json`](examples/invalid-command-bare-slash.json) | A bare `/` encoded as `command` instead of `null` |
| [`invalid-command-trailing-newline.json`](examples/invalid-command-trailing-newline.json) | A command token with a trailing newline. Must be rejected under every regex dialect. |
| [`invalid-command-next-line.json`](examples/invalid-command-next-line.json) | A command token ending in U+0085. Python's `\s` matches it and ECMAScript's does not, so the schema lists whitespace explicitly. |
| [`invalid-command-zero-width-no-break-space.json`](examples/invalid-command-zero-width-no-break-space.json) | A command token ending in U+FEFF. ECMAScript's `\s` matches it and Python's does not. |
| [`invalid-missing-actor-provenance.json`](examples/invalid-missing-actor-provenance.json) | A retained element with no actor ID |
| [`invalid-unverifiable-authorization.json`](examples/invalid-unverifiable-authorization.json) | A retained element whose actor permission is unverified |
| [`invalid-history-omitted.json`](examples/invalid-history-omitted.json) | An evaluable document that claims complete enumeration but omits `entity.actions` |
| [`invalid-incomplete-history-as-empty.json`](examples/invalid-incomplete-history-as-empty.json) | Incomplete enumeration encoded as an empty list |
| [`invalid-unresolved-required-field.json`](examples/invalid-unresolved-required-field.json) | An evaluable change proposal whose planned change-proposal facet `failed`. A `scope_complete` result allows only `complete` or `not_loaded` facets. |

## Versioning

This is a normative specification under
[ADR 0015](../../../ADRs/0015-normative-specifications-directory.md). The
schemas are closed (`additionalProperties: false`). `schema_version` is
`normalized-entity/v1`. Consumers MUST reject any other value.

| Change | v1 impact |
|--------|-----------|
| **Breaking** (requires `docs/normative/normalized-entity/v2/`): remove, rename, or retype a field; make a field nullable or optional; remove an enum value (including a `human_min_role` value); remove a category from a kind's superset; allow CEL to observe a non-`complete` coverage state; allow bounded enumeration of an acquired category; allow an `UNKNOWN`, `false` from a partial read, or a `false` from a `witness` to form an evaluable document; weaken a retained-element or fail-closed rule | Producers and harnesses must migrate |
| **Additive** (allowed in v1 with a README mapping): add a provider or entity kind together with its complete mapping, add a category to a kind's superset, add a coverage reason, add a CEL-visible field to a facet (present whenever that facet is `complete`) | Fullsend-controlled producers and validators update together. Existing predicates keep their meaning. |
| **Non-breaking:** clarify documentation, add examples | None |

Adding a category or provider tightens what must be enumerated before
evaluation. It never relaxes the boundary. As with NormalizedEvent v1, a
consumer that vendors a strict schema copy must refresh it before a producer
emits additive values.

## Known limitations

- Negative and aggregate predicates over entities with very long histories
  require unbounded enumeration of the categories they depend on. Efficient
  enumeration, shared provider queries, and indexing are tracked in
  [#8421](https://github.com/fullsend-ai/fullsend/issues/8421). State-only and
  witness-settled predicates do not pay that cost.
- The acquisition-plan derivation, the proof of causal occurrence for raw
  signals such as `updated_at` and labels, and the bounded query-scope audit
  evidence are defined by Authorization Contract v2 and the entity-source
  discovery work. This contract records the plan identity and the resulting
  coverage, not the derivation.
- An exact label-transition grant (ADR 0151, Authorization Contract v2) is not
  represented. v1 retains a `label_application` element only through a
  verified actor role.
- The `human_min_role` field is target behavior until the harness loader
  validates and enforces it (ADR 0151).
- Any action-indicating element authored by a deleted account denies
  evaluation of that entity, because v1 defines no verified tombstone actor.
  A later revision may add one as an additive change.
- Receipts and poll checkpoints are not CEL-visible in v1.
