# Normalized entity v1

This specification defines the source-neutral `entity` data made available to
harness CEL predicates under [ADR 0098](../../../ADRs/0098-entity-first-harness-evaluation.md).
It defines the values, partial-data states, collection coverage, and binding
invariants. It does not define how fullsend fetches data, plans provider queries,
evaluates CEL, retries, or records a final decision. CEL supplies the expression
semantics; [ADR 0147](../../../ADRs/0147-evaluate-entity-cel-over-selectively-acquired-data.md)
and [Authorization Contract v2](../../authorization/v2/README.md) govern
sound acquisition, admission, audit, and retry.
[ADR 0148](../../../ADRs/0148-reuse-verified-actor-roles-for-entity-cel.md)
governs reuse of role evidence, and [ADR 0151](../../../ADRs/0151-harness-human-role-minimums.md)
governs the harness's effective human role minimum.

> **Target contract — not yet implemented:** No shipped dispatch or poll path
> binds `entity` in CEL. Publishing this data contract satisfies ADR 0098's
> contract-existence precondition; implementation is separate. Existing
> event-only dispatch remains governed by [NormalizedEvent v1](../../normalized-event/v1/README.md).

The [schema](normalized-entity.schema.json) describes a serialized logical
entity document. Its `entity` member contains field values. `coverage` and
`view` are host-only metadata. The serialized representation is not a CEL
activation: a binding MUST translate unknown markers and partial collections
as described below. A Go struct layout, lazy loader, entity discovery
mechanism, provider query plan, and CEL adapter implementation remain
implementation choices.

## Scope and identity

v1 covers GitHub issues (`work_item`), pull requests (`change_proposal`), and
Discussions (`conversation`); GitLab issues and merge requests; and Jira
issues. The schema rejects other provider/kind pairs. These are target data
mappings: GitHub Discussion runtime ingress and conversation handling remain
unimplemented under [ADR 0086](../../../ADRs/0086-conversation-surface-for-agent-participation.md).
An unsupported target cannot become a successfully empty entity.

Identity is required and concrete in every document:

| Field | Type | Meaning |
|-------|------|---------|
| `entity.kind` | `work_item`, `change_proposal`, or `conversation` | Domain kind; a Discussion is a conversation |
| `entity.source.system` | `github`, `gitlab`, or `jira` | Authoritative source and overlay selector |
| `entity.source.host` | string | Lowercase source host, with a non-default port if needed |
| `entity.source.repository` | string | Forge navigation path: lowercase GitHub `owner/repository` or GitLab canonical `path_with_namespace` |
| `entity.source.repository_id` | string | Immutable GitHub GraphQL repository ID or GitLab global project ID |
| `entity.source.project` | string | Jira project key, a navigation value |
| `entity.source.project_id` | string | Immutable Jira project database ID |
| `entity.id` | string | Canonical fullsend entity ID, derived from source identity as specified below |
| `entity.source_id` | string | Immutable provider entity ID: GitHub GraphQL node ID or GitLab/Jira global database ID |
| `entity.key` | string | Contextual human reference: Jira `PROJ-123`, GitHub `#123`, GitLab issue `#123` or MR `!123` |
| `entity.number` | integer | Positive number in the current reference: GitHub number, GitLab project-scoped IID, or Jira issue-key suffix |
| `entity.url` | URI | Canonical source URL |

Every entity requires `id`, `source_id`, `key`, and `number`. Forge entities
require `repository` and `repository_id` and omit `project` and `project_id`.
Jira entities require `project` and `project_id` and omit `repository` and
`repository_id`. `entity.source_id` identifies the entity record within its
provider, not its containing repository or project. `entity.key` is the short
reference in its source context,
not a globally unique locator; `entity.url` carries the source URL. Jira's
`number` is the decimal suffix of its current key, not its database ID. A
Jira move can change both `key` and `number` without changing `source_id` or
`id`.

The canonical ID is the exact string
`fullsend:entity:v1:<system>:<host>:<kind>:<source_id>`, where each bracketed
component is independently percent-encoded from its UTF-8 bytes. RFC 3986
unreserved bytes (`A-Z`, `a-z`, `0-9`, `-`, `.`, `_`, `~`) remain literal;
every other byte becomes `%` followed by two uppercase hexadecimal digits.
Use the normalized `source.system`, lowercase `source.host` (including its
non-default port when present), `entity.kind`, and `entity.source_id` exactly
as recorded; do not include the repository or project path or ID. For example,
`github`, `github.com`, `work_item`, and `I_kwDOExample53` yield
`fullsend:entity:v1:github:github.com:work_item:I_kwDOExample53`. GitLab
record IDs are unique across projects within a kind; Jira issue IDs are
site-wide; GitHub node IDs are host-wide. Including the system, host, and
kind prevents cross-source and cross-kind collisions while preserving identity
through a repository or Jira project move. A source migrated to a different
host is a different identity unless a separate migration mapping relates it.
The snapshot's hashed `record_key` identifies a snapshot record and is not
this CEL-facing `entity.id`.
A Jira routing event's `repo` names the target code repository, not the Jira
source project. GitLab and Jira database IDs use base-10 strings without
leading zeroes. Paths and keys are navigation values, never substitutes for
immutable IDs. A document with unresolved identity is not a normalized
entity document. `entity.source.system` is the hosting system even when an
event source is `manual` or `schedule`. The entity and any non-null event MUST
identify the same target; the matching rules below cannot be expressed by
this JSON Schema.

## Fields

Every applicable scalar field is present with either its concrete value or
`{"$unknown": "<reason>"}`. This marker is a serialization of an unresolved
value, **not** an object visible to CEL. Reasons are `not_loaded`, `partial`,
`unsupported`, `failed`, and `consistency_unknown`. They are diagnostics for
the host; all map to CEL unknown. An absent source value is represented by
`null` only on a nullable field. A field that does not apply to the kind or
category is omitted. Inapplicable and unknown are distinct.

| Field | Concrete type | Meaning |
|-------|---------------|---------|
| `entity.state` | `open`, `closed`, `merged` | Current state; `merged` applies only to a change proposal |
| `entity.title` | string | Current filtered issue, proposal, or Discussion title |
| `entity.author.id` | string | Source-authenticated creator ID |
| `entity.author.login` | string or null | Current login, or null when withheld |
| `entity.author.kind` | `human` or `bot` | Provider-backed classification under ADR 0107 |
| `entity.author.role` | role string or unknown | Verified effective human role; `none` for a bot |
| `entity.author.bot_role` | string, null, or unknown | Registered bot role, or null after a successful no-match lookup; absent for a human |
| `entity.author.role_verified` | bool or unknown | Whether the applicable role lookup completed; false on lookup failure |
| `entity.created_at` | timestamp string | Creation time |
| `entity.updated_at` | timestamp string | Raw source last-update time, including updates by actors below the harness minimum |
| `entity.labels` | list of strings | Current label names, independently acquired from label-application provenance |
| `entity.assignees` | list of actors | Current source-backed assignees; assignment provenance is independent of the assignee's role |
| `entity.lifecycle` | object | Source-backed close and merge times and actors; unresolved actors remain unknown |
| `entity.work_item.source_status` | object or unknown | Source-native status name and category, alongside portable `entity.state` |
| `entity.work_item.issue_type` | object or unknown | Source-native issue type ID, name, and subtask flag |
| `entity.work_item.state_reason` | string, null, or unknown | Source-native reason for the current state, when reported |
| `entity.extensions` | extension maps | Selected source-defined custom fields and API properties on the entity |
| `entity.body` | action, null, or unknown | Current body/description record; null when the source confirms no body |
| `entity.comments` | list of actions | Known admitted comments and replies; see [Actions and threads](#actions-and-threads) |
| `entity.reviews` | list of actions | Submitted formal reviews on change proposals only |
| `entity.reviewers` | list of reviewer standings | Current source-backed reviewer assignments or standings on change proposals; not formal review history |
| `entity.current_approvals` | list of approvals | Currently recorded approvals admitted for the selected harness; not the universe eligible to approve |
| `entity.label_applications` | list of actions | Attributable applications of current labels |
| `entity.threads` | list of threads | Comment-thread membership, state, and location; see [Actions and threads](#actions-and-threads) |
| `entity.reactions` | list of reactions | Source-backed reactions on the entity, with admitted actors |
| `entity.change_proposal.base_repository` | string | Base repository |
| `entity.change_proposal.head_repository` | string or null | Head repository; null only when source reports deletion |
| `entity.change_proposal.base_ref` | string | Target branch |
| `entity.change_proposal.head_ref` | string | Head branch |
| `entity.change_proposal.head_sha` | string | Head commit ID |
| `entity.change_proposal.changed_files` | list of files | Current changed paths with independently unknown diff patches |
| `entity.change_proposal.commits` | list of strings | Current commit SHAs in source order |
| `entity.change_proposal.is_fork` | bool | Cross-repository status |
| `entity.change_proposal.draft` | bool | Draft status |
| `entity.change_proposal.approval.gate` | `not_required`, `satisfied`, `unsatisfied`, or unknown | Current approval requirement for this proposal revision; other merge blockers are separate |
| `entity.checks` | list of checks | Current-head check results; change proposals only |
| `entity.checks[].report` | object | Forge-published title, summary, and details for a check |
| `entity.checks[].report.annotations` | list | Forge-published file findings attached to that check |
| `entity.checks[].url` | URI, null, or unknown | Direct forge URL when source-bound to this check |
| `entity.checks[].run_attempt` | positive integer, null, or unknown | Source run attempt when this check has one |
| `entity.checks[].trigger_event` | string, null, or unknown | Source workflow event when source-bound to this check |
| `entity.conversation.category.name` | string | Current exclusive discussion category |
| `entity.conversation.category.id` | string | Source category ID when available |
| `entity.conversation.category.slug` | string | Source category slug when available |
| `entity.conversation.category.format` | string | Source-backed format hint when known |

`entity.author`, `entity.work_item`, `entity.change_proposal`, and `entity.conversation` can
themselves be unknown or contain individually unknown fields.
`entity.work_item` applies only to a work item;
`entity.change_proposal` applies only to a change proposal;
`entity.conversation` applies only to a conversation. A category is an
exclusive partition, not a synthetic label. `format` is omitted when the
provider has no reliable value; GitHub `isAnswerable: true` maps to
`question_answer`. Other format values are omitted in v1.

The creator identity is routing data, not an authorization verdict for the
entity's current content. `entity.author.kind` comes from authoritative source
metadata, and bot-role lookup follows
[ADR 0107](../../../ADRs/0107-bot-identity-resolution-for-dispatch-authorization.md):
a bot has `role: none` and a provider-resolved `bot_role` only after a completed
lookup; a human has no `bot_role` and has a concrete `role` only after verified
permission resolution. `role_verified` is unknown before a lookup, true when it
completes (including a bot no-match), and false if it fails. An unresolved role
is unknown, never an inferred grant. Author fields alone do not admit the title,
body, comments, or other actor-originated facts into a harness view; each fact
still needs its applicable current-revision authorization check.

Every person in this contract uses one ADR 0107 actor shape: `id`, `login`,
`kind`, `role`, `role_verified`, and `bot_role` only for a bot. A human omits
`bot_role`; a provider-classified bot has `role: none` and a resolved,
no-match, or unknown `bot_role`. An admitted actor is this same shape with a
concrete immutable ID, concrete kind and role, and `role_verified: true`;
non-label bot actions additionally need a recognized `bot_role`. The
selected harness's authorization policy still determines admission. Authors,
assignees, reviewer people, approvers, action actors, lifecycle actors, thread
resolvers, reaction actors, and actor-valued extensions all use this shape.
Team review subjects retain their distinct `team_ref` shape. The contextual
`is_entity_author` routing aid belongs to an action or approval record, not
inside its actor.

Timestamps are UTC strings in
`YYYY-MM-DDTHH:MM:SS.nnnnnnnnnZ` form. Title and action text are CEL-visible
when their current-revision attribution, authorization, and filtering are
established. An unresolved text field is unknown, not empty.

### Filtered text

`entity.title` and concrete `text` in `entity.body`, `entity.comments`, or
`entity.reviews` are current-revision text. A concrete value is the complete result of the
same versioned Unicode-safety, secret-redaction, prompt-injection, and size
pipeline required by [entity-context-snapshot v1](../../entity-context-snapshot/v1/README.md#canonical-bytes).
The active `filter_version` is recorded in host-only `view` metadata. CEL
observes the filtered value, not raw source bytes. Empty action text is a
valid concrete string; a known title is nonempty.

Before exposing concrete text, the producer MUST authenticate the source
record and the actor responsible for that text's current revision, verify
authorization under the selected harness's effective minimum, and apply the
filter to the complete source before any size bound. The entity creator alone
does not prove the current title editor. The title and body may have different
editors and must not borrow each other's attribution. If the title revision
actor is unauthorized or unverifiable, `entity.title` is unknown. A body with
unresolved actor admission is unknown; a comment or review with unverifiable
admission is omitted and leaves its collection coverage non-complete. A filtering
rejection, detector failure, or unavailable detector leaves an otherwise
admitted text field unknown and cannot authorize a text-dependent CEL result.
Host diagnostics retain the reason without raw text and audit the verified
actor of each concrete text field. A model-facing snapshot has its own later
admission checkpoint.

A bound may emit a truncated filtered prefix for model context, but that
prefix MUST NOT be bound as a complete CEL string. For CEL, the text field
remains unknown until the complete filtered value is available within its
applicable bound. Known `command` tokens are derived from the same authorized,
filtered revision; resolving a command without materializing all of `text`
must still enforce the filter pipeline on the source used to derive it.
Check names, report text, annotation text, and log lines are also CEL-visible
on change proposals when their source, revision, and access to the proposal
are verified. They use the same
versioned content-safety pipeline, but a check is not an actor-authored action:
the action actor-admission rule does not invent an author for a check.
Other non-entity sources remain outside this v1 entity text scope.

## Partial collections

`entity.labels`, `entity.assignees`, `entity.reactions`, `entity.comments`,
`entity.label_applications`, `entity.threads`, and, on change proposals,
`entity.reviews`, `entity.reviewers`, `entity.current_approvals`,
`entity.checks`, `entity.change_proposal.changed_files`, and
`entity.change_proposal.commits` are
logically CEL lists. The serialized arrays contain
**known** members; they do not claim that no other members exist. Their
corresponding `coverage` fields describe the remainder:

| Status | Meaning |
|--------|---------|
| `complete` | The collection is exhaustively known at the stated source revision; an empty known array is truly empty for that coverage. |
| `partial` | Some members are known, but more may exist. |
| `not_loaded` | No members were requested; the known array contains none from this collection. |
| `unknown` | Completeness cannot be established, including an inconsistent source read. |
| `unsupported` | This source or kind cannot provide the collection. |
| `failed` | Fetch, provenance, or permission resolution failed. |

`reason` and `source_revision` are optional host diagnostics. Every concrete
value and known member MUST be authoritative for the
view's `resolved_at` check or a compatible source revision. If its current
revision cannot be established, it is unknown rather than a known witness.
A producer MUST establish a consistent source view before claiming `complete`.
A page limit, truncation, or uncertain pagination leaves coverage
non-complete. Each collection has its own coverage so a producer can acquire
comments without reading reviews or label history. `entity.body` is singular:
an admitted body record, concrete `null` when the source confirms there is
no description, or unknown while its current revision or admission is
unresolved. It has no collection-coverage entry.

`coverage.assignees` and `coverage.reactions` are independent of comment,
reviewer, and label coverage. A current assignee is an identified person, not
the actor who assigned that person. An assignment used as an actionable CEL
fact requires attribution and admission of the assigning action; unresolved
assignment provenance leaves the relevant coverage non-complete. A known
reaction requires an immutable source reaction ID and an admitted actor. The
current GitHub reaction adapter returns only a login, so an entity producer
must resolve the immutable actor identity before admitting that reaction.
Reactions here are on the entity itself; comment reactions are not inferred
from them. A conversation has an empty assignee array with `unsupported`
coverage.

`entity.change_proposal.changed_files` and `.commits` are independent current
head collections. Each changed file has a safe repository-relative `path` and
`patch`: a complete filtered diff string, `null` when the source confirms no
patch, or unknown when not loaded, truncated, or unsafe to bind. Commit IDs
are source SHAs in source order, not a permanent history of rebased or
force-pushed commits. Provider result limits and uncertain current-head
consistency prevent complete coverage. Paths and patches are untrusted source
content and pass the applicable source and content-safety checks.

| Reaction field | Concrete type | Meaning |
|----------------|---------------|---------|
| `id` | string | Immutable source reaction ID |
| `content` | string or unknown | Source reaction kind, such as `+1` or `heart` |
| `actor` | admitted actor | Source-authenticated reactor using the shared ADR 0107 actor shape |
| `created_at` | timestamp or unknown | Source creation time when available |

### Extensible fields and API properties

An entity or source record may carry the same optional `extensions` shape.
The schema permits it on the entity, actions, threads, and checks, so future
providers can add data without inventing a new representation for every
record kind. `extensions.custom_fields` holds selected schema-defined source
fields; `extensions.properties` holds selected API key/value properties.
Jira `customfield_*` issue values belong to the first map. Jira issue and
comment entity properties belong to the second. These are distinct source
constructs, even when they project to the same CEL value type. The maps are
keyed by configured CEL aliases; each entry retains the stable source field
ID or property key and may include a JSON Pointer for a selected leaf of a
structured source value. Unselected keys are absent. A configured key is
present with a concrete value, `null` when the source confirms it is unset,
or unknown when acquisition, type decoding, admission, or filtering is
unresolved. An alias referenced by a harness but absent from its trusted
source declaration is invalid configuration, not evidence of absence.

Concrete extension values are typed strings, numbers, booleans, options
(`id` and `label`), actors, or lists of those atoms. Text, including selected
leaves of JSON properties, uses the same complete-source filter as other CEL
text. Arbitrary JSON objects are not directly bound to CEL: a source-specific
adapter and trusted declaration select typed leaves. Actor-valued fields use
the shared ADR 0107 actor shape, with an immutable source ID; an arbitrary
object resembling a user is not automatically an actor. An actor selected
*as a value* is distinct from the actor who *changed* the field.

Each entry has `changed_by`, which is the admitted source-authenticated actor
of that value's current revision or unknown. It never inherits the containing
entity or comment actor. A source that reports a value without trustworthy
change attribution can retain it for host-side diagnostics, but binds its CEL
`value` as unknown when actor admission is required. Jira's issue changelog
can attribute selected field changes when its relevant history is complete;
the issue and comment property read APIs return keys and values without a
writer, so a property needs independent trusted provenance before becoming an
actionable known CEL value. Fullsend's operational lock, checkpoint, and
comment-marker properties stay in host-side coordination state by default;
their API visibility does not establish trustworthy authorship or a dispatch
grant.

List-valued extensions have independent host-only
`coverage.extension_lists` entries keyed by owning record kind and ID,
extension category, and alias. Known list members and an unknown remainder
follow the same partial-list CEL rules as other collections. Scalar unknown
markers need no list-coverage row. Source declarations and field/type mapping
belong beside the harness's entity source in trusted layered configuration;
the precise declaration syntax is part of entity-source implementation, not
this data model.

GitLab MR `reviews` coverage is `unsupported` in v1. The GitLab
[approvals API](https://docs.gitlab.com/api/merge_request_approvals/) exposes
current approvers and approval times, but not individually identified formal
review submissions with review text and disposition. Approvals can be revoked
or reset, so the current approver list cannot stand in for a review history.
GitLab [system notes](https://docs.gitlab.com/user/project/system_notes/)
describe activity but are not structured formal review records in this mapping.
GitHub Discussion
`label_applications` coverage is `unsupported` because this contract does
not map attributable application records. Current labels may still be known
independently. `reviews` is inapplicable to work items and conversations, so
both the field and its coverage entry are absent. Issue entities have a
complete empty `threads` list because their comments are unthreaded in this
v1 mapping. Known `entity.labels` values are unique and sorted by UTF-8 byte
order. Each known comment, review, or label application MUST NOT come from a
`not_loaded` or `unsupported` collection. Known action and thread elements
are sorted by `created_at`, then `id` in ASCII byte order; reviewer and
current-approval ordering is specified below. Unknown elements
may precede, follow, or fall between them. An index into a partial list
cannot be inferred from the known array's position.

A CEL binding MUST expose the known members **and** an unknown remainder when
coverage is non-complete. It MUST NOT bind the serialized known array as an
ordinary complete CEL list. This is necessary for ordinary expressions such as
`entity.comments.exists(c, c.command == "/fs-fix")` to see a known authorized
match while keeping unexamined elements unknown. A known matching element can
settle an existence test. An empty or non-matching known subset cannot prove
absence while the relevant search space is incomplete. Each collection's
unknown remainder is independent; a complete `comments` list can prove no
matching comment even while `reviews` remains unresolved. Operations that
depend on the total membership or order, including
`size`, indexing, `filter`, `all`, and `exists_one`, must preserve CEL's
standard unknown behavior for the information they lack. A binding MUST NOT
manufacture a false, empty, zero, or stable index from partial coverage. It
may use a custom CEL list value, partial activation, or another sound
implementation; this specification does not prescribe one. Conformance is
judged against CEL evaluation over every complete list consistent with the
known members and coverage, not by the implementation technique.

The same rule applies to `entity.labels`. A label name in the known subset is
usable, while an unobserved label cannot be assumed absent. Raw source
aggregates and labels are not automatically authorized action signals; the
actor and causal-evidence rules of Authorization Contract v2 still apply.

### Lifecycle and source issue details

`entity.state` is the portable current state. `entity.lifecycle.closed_at`
and `.closed_by` record a source-backed close when present; change proposals
also have `.merged_at` and `.merged_by`. A concrete `null` means the source
confirms no such event for the current lifecycle, while unknown means the
time or actor is unresolved. A reopen can clear current close fields without
erasing the historical source record. Known lifecycle actors use the shared
admitted actor shape. A source-provided login alone must be resolved to an
immutable actor ID. A known state does not invent a lifecycle actor.

For work items, `entity.work_item.source_status` retains the source's status
name and category without changing the portable `state`. Jira's status name
and `new`/`indeterminate`/`done` category map here. `issue_type` retains a
source ID, name, and subtask flag when available. `state_reason` retains a
source-native reason, such as GitHub's `completed`, `not_planned`, or
`duplicate`; this field is not a cross-provider enum. A provider without an
equivalent reports an unknown `unsupported` value, rather than fabricating a
reason or type. Custom fields are record extensions, not members of this
fixed work-item object.

### Reviewers and current approvals

These are current source facts, acquired independently of submitted formal
`entity.reviews`. A reviewer entry describes a source-identified person or
team with a current review request, assignment, or source-backed reviewer
standing. It does not assert that the person belongs to every eligible
approver group. A current approval records a person's presently active
approval, which may be optional or may not satisfy an applicable rule.
Neither collection is a history of review submissions, revocations, or
assignment changes.

| Field | Concrete type | Meaning |
|-------|---------------|---------|
| `entity.reviewers[].subject` | actor reference or team reference | Source-identified reviewer; team references have `kind: team`, immutable source `id`, and `name` |
| `entity.reviewers[].state` | `unreviewed`, `review_started`, `reviewed`, `approved`, `changes_requested`, `unapproved`, `dismissed`, or unknown | Current source-backed reviewer standing; unknown when no equivalent state is reported |
| `entity.current_approvals[].actor` | admitted actor | Person whose approval is currently recorded; the actor ID is the entry's identity, without an invented approval-record ID |
| `entity.current_approvals[].approved_at` | timestamp or unknown | Approval time when the source supplies it |
| `entity.change_proposal.approval.gate` | `not_required`, `satisfied`, `unsatisfied`, or unknown | Whether applicable approval rules are satisfied, distinct from overall mergeability |

`coverage.reviewers` and `coverage.current_approvals` are independent. Known
reviewers are unique and sorted by subject kind then source ID; known current
approvals are unique and sorted by actor ID. An empty complete
`current_approvals` list proves no admitted current approval; a nonempty
partial list can prove existence without proving the total count. There is no
separate `has_current_approval` field: CEL can test the approval list with
ordinary `exists`, subject to its coverage. A known `approval.gate` is scoped
to the current proposal revision and applicable target-branch rules. It may
remain unknown while the lists are known, or vice versa. `not_required` means
no mandatory approval rule; an optional approval can still be present.
`satisfied` means the approval requirement is met after the selected harness's
actor admission. Raw forge approval summaries may support conservative
`unsatisfied` or `not_required` results, but cannot turn an unauthorized
approval into a positive CEL signal under Authorization Contract v2. If the
producer cannot establish the current head, rule state, or relevant actor
admission, the dependent field or collection coverage remains unknown.

GitHub maps current requests and latest per-user review standing into
`reviewers`; team requests remain team subjects. It derives
`current_approvals` from current, nondismissed approving reviews, checking
revision and policy validity rather than treating every historical approval
as current. GitLab maps its MR
[`/reviewers`](https://docs.gitlab.com/api/merge_requests/#retrieve-merge-request-reviewers)
response and
[`/approvals`](https://docs.gitlab.com/api/merge_request_approvals/#retrieve-approval-state-for-a-merge-request)
`approved_by` response separately. The latter includes people whose
approvals do not satisfy a rule. The GitLab Community Edition `approved`
boolean means at least one approval exists, while Enterprise Edition uses
approval-rule satisfaction; neither boolean alone defines this contract's
`gate`. GitLab Free has `gate: not_required` when no mandatory approval
policy applies, even if `approved_by` is nonempty. Current GitLab approvals
do not become formal `entity.reviews`.
The full population eligible to approve, historical unassigned reviewers,
and source-hidden group membership are outside these lists.
Reviewer assignment or standing can itself be an actor-originated action
signal. A known member needs the applicable source-revision actor admission;
if the provider exposes an assignment without enough provenance to authorize
an actionable use, the member cannot be asserted in the CEL view and
`coverage.reviewers` remains non-complete. The reviewer's own role does not
authorize a different actor's assignment.

### Checks and log lines

`entity.checks` applies only to a change proposal. It contains known check
results for the proposal's current `head_sha`, sorted by `created_at` and then
`id`; `coverage.checks` describes the unknown remainder. A known check has
source-backed `id`, `created_at`, and `head_sha`. Its `name`, `status`,
`conclusion`, `started_at`, `completed_at`, and `never_started` can each be
concrete or unknown. `conclusion` is concrete `null` while a known check is
not completed; `started_at` and `completed_at` are concrete `null` when the
corresponding time does not apply. The status and conclusion vocabulary and
source mappings are those of [entity-context-snapshot v1](../../entity-context-snapshot/v1/README.md#ordering-files-and-prompt-assembly).
A check for another head revision is not a known member. If the current head
cannot be established, no check can be admitted as known and checks cannot be
claimed complete.

`check.report.title`, `.summary`, and `.details` are independent nullable
strings or unknown. Concrete strings are complete, filtered, forge-published
values; `null` means the forge confirms that field is absent. GitHub maps
them from check-run `output.title`, `output.summary`, and `output.text`.
`check.report.annotations` contains known file findings attached to that
check, with source-backed `path`, `start_line`, `end_line`, and `level`.
`start_column`, `end_column`, `title`, and `details` are nullable or unknown;
`message` is a filtered string or unknown. `coverage.check_annotations` has
one row per known check and describes the unknown remainder. An empty known
array with incomplete coverage does not prove that the check has no findings.
The list's source order is preserved when known; an index into a partial list
is unknown, while a known matching finding can settle `exists`. The same
complete-source filter and access gate used for check logs apply to report
and annotation text. Paths pass the snapshot v1 path-safety pipeline before
structural validation; an unsafe path cannot become a known annotation.

GitHub check-run annotations are fetched from the forge's
[paginated annotation API](https://docs.github.com/en/rest/checks/runs#list-check-run-annotations).
GitLab CI jobs have no corresponding job-bound report or annotation
fields in this v1 mapping: their report scalars are unknown with reason
`unsupported`, and annotation coverage is `unsupported`. GitLab pipeline or
merge-request [code-quality findings](https://docs.gitlab.com/user/project/merge_requests/reports/#code-quality-report)
are not assigned to an individual job
without a source-backed relationship. A GitLab `failure_reason` code is not
silently promoted to a prose summary. The report fields and annotation list
have the same CEL shape on both forges; unsupported data stays unknown.

Each known check has `log.ascending` and `log.descending` CEL list accessors.
An entry represents one source log line; its `text` is a complete
filtered line or unknown, and `timestamp` is a source timestamp, `null` when
the source has none, or unknown when unresolved. The source's line order is
the chronology; timestamps never reorder entries and are not synthesized.
`ascending[0]` is the first line and `descending[0]` the last line when
known. Each serialized array contains a contiguous known prefix from its
respective end. Host-only `coverage.check_logs` has one row per known check,
with independent `ascending` and `descending` coverage. A missing or
unfinished log is not a proven complete empty list. Either accessor can be
partially known while the other remains `not_loaded`; a complete accessor
defines the full log, and the two complete views must be exact reversals.
Known prefixes from both ends must refer to one compatible log revision.
`complete` means complete at that observed revision; a growing log requires
a new observation before the view can be called current again.

Check metadata does not establish that a forge-readable log exists.
[GitHub Actions jobs](https://docs.github.com/en/rest/actions/workflow-jobs)
and [GitLab CI jobs](https://docs.gitlab.com/api/jobs/#retrieve-a-log-file-for-a-job)
have log-download APIs, but a GitHub check run from another app has no
general-purpose log endpoint in the [Checks API](https://docs.github.com/en/rest/checks/runs). A
known check with no forge-readable log source has empty arrays and
`unsupported` coverage in both directions. `not_loaded` means acquisition
was not requested; a missing, erased, or inaccessible expected log is not a
complete empty list. A failed retrieval uses `failed`, while uncertain source
availability or revision uses `unknown`. If the forge or runner reports
truncation, coverage cannot be `complete`; only a prefix whose actual source
end is proven may be exposed as known. In particular, a retained prefix cut
off by a runner is not evidence of the last execution line for `descending`.
The v1 check source boundary is the forge API. A `details_url` for an external
check MUST NOT cause a fetch from that third-party service under this contract.
Fields returned by the forge but absent from this closed schema are not
implicitly CEL-visible.

The CEL binding preserves each accessor's unknown remainder. Indexing within
a known prefix is valid, and a known matching line can settle `exists`;
absence, total size, and indexes beyond the prefix remain unknown until the
relevant view is complete. The host may materialize one view from a complete
opposite view without another provider read. The producer applies the
snapshot v1 safety stages to the **complete selected log** before exposing
any line, so secrets or instructions spanning lines cannot evade the filter.
It preserves source-line boundaries through filtering; if it cannot safely
associate filtered text with a source line, that entry's text is unknown.
It must not bind a truncated log prefix or a truncated line as a complete CEL
string. The snapshot's bounded `log.txt` is therefore not by itself evidence
of a complete log view.
An implementation MAY fetch the whole available forge log when needed, apply
the full-source filter, and derive both views from that one result. The model
does not require a line-range API. Selective source reads are an optional
future optimization; they would need a filtering contract that remains safe
without the full source.

## Actions and threads

Source activity is exposed through separate fields, each with its own record
shape and acquisition state:

| Entity field | Source record | Coverage |
|--------------|---------------|----------|
| `body` | Current description or opening post, as one admitted action or null | Singular unknown marker; no list coverage |
| `comments` | Source comments, notes, review-thread comments, and Discussion replies | `coverage.comments` |
| `reviews` | Submitted formal reviews on change proposals | `coverage.reviews` |
| `label_applications` | Attributable applications of current labels | `coverage.label_applications` |
| `threads` | Source comment-thread relationships and state | `coverage.threads` and per-thread `coverage.thread_comments` |

The lists are independent; fetching one never implies completeness of
another. An empty known list does not prove absence unless its own coverage
is `complete`.

Use `entity.labels` to test current label membership, for example
`entity.labels.exists(l, l == "ready")`. `label_applications` contains the
most recent attributable application of each currently applied label where
the source provides it; it is not a change log to replay to derive the
current label set. `coverage.labels` and `coverage.label_applications` are
independent. A known label proves membership even if provenance is unresolved;
absence is conclusive only when `coverage.labels` is `complete`.

### Action fields

Each known body, comment, review, or label-application action has a concrete
`id`, `created_at`, and `actor`. Its `revised_at` and applicable payload
fields can be unknown while a later fetch resolves them.

| Action field | Concrete type | Meaning |
|--------------|---------------|---------|
| `id` | string | Stable source-backed action ID; `body` uses `entity.source_id` |
| `created_at` | timestamp string | Creation, review submission, or label application time |
| `revised_at` | timestamp string | Current revision time; not before creation |
| `actor.id` | string | Source-authenticated current-revision actor ID |
| `actor.login` | string or null | Display login |
| `actor.kind` | `human` or `bot` | Source classification |
| `actor.role` | role string | Verified provider permission; bots use `none` |
| `actor.bot_role` | string | Provider-resolved fullsend bot role for an admitted bot; absent for a human |
| `actor.role_verified` | true | Admission check completed |
| `is_entity_author` | bool or unknown | Contextual routing aid on the action, never authorization evidence |
| `extensions` | extension maps | Selected source-defined fields or properties on this record, when declared |
| `text` | string | Complete filtered current-revision text on body, comment, and review records; empty string is valid |
| `command` | string or null | First-line command token on body, comment, and review records |
| `label` | string | Applied name on label-application records |
| `review_state` | `approved`, `changes_requested`, `commented`, `dismissed` | Formal review disposition |
| `parent_id` | string | Discussion comment thread-root ID; equals `id` for a root comment |
| `thread_id` | string, null, or unknown | Source thread ID on a comment, null when the source confirms no thread |
| `url` | URI, null, or unknown | Source comment URL when supplied; null when no direct URL exists |

An inapplicable action field is absent. A source-authenticated actor and a
verified provider authorization verdict are required before `body` is concrete
or an action enters a known collection. The actor's
display login and `is_entity_author` routing aid may remain unknown. An
unattributable record or an unverifiable permission leaves dependent
collection coverage non-complete, or leaves `body` unknown; it is not silently
discarded. The
per-harness `view.human_min_role` identifies the threshold applied to known
actions. Humans below it and bots without a recognized `bot_role` are removed
from collections; a source body they authored remains unknown to CEL.
A recognized bot is admitted under ADR 0151. A stronger harness minimum
requires a separately filtered view. The role order and all admission rules
remain in Authorization Contract v2. Known actions are routing data; model
admission is a separate checkpoint under [ADR 0133](../../../ADRs/0133-actor-attributed-data-security-boundary.md).

A command token starts with `/`, contains 2–64 Unicode code points, and has
no whitespace. The whitespace set is U+0009–U+000D, U+0020, U+0085, U+00A0,
U+1680, U+2000–U+200A, U+2028, U+2029, U+202F, U+205F, U+3000, and U+FEFF.
A bare slash, overlong token, or text with no qualifying first token yields
concrete `null`. An unfetched revision yields unknown instead. Producers
MUST NOT truncate a token to fit the limit.

The actor of an edited record is the source-authenticated editor of its
current revision; otherwise it is the creator. If the provider does not expose
the current editor, the action remains unresolved. Jira label actions may
use deterministic IDs derived from immutable changelog entry IDs plus item
and added-label ordinals; those IDs must remain stable across repeated reads.
The event sender, poller,
retrieval credential, and self-asserted metadata cannot substitute. The
`revised_at` field reflects that revision, so an edited request should be
ordered by `revised_at` when compared with later activity. The `body` action
uses `entity.source_id` and the entity's creation time.

### Thread fields

`entity.threads` contains source comment threads as records separate from the
comment list. A known comment's `thread_id` joins it to a known thread
with the same `id`; `null` means the source confirms the comment is unthreaded.
An unknown `thread_id` means membership still needs resolution. A known
thread can precede fetching any of its comments, and a known comment can
precede fetching its thread. Neither absence from a partial array nor an
unresolved join proves that a thread or comment does not exist.

| Thread field | Concrete type | Meaning |
|--------------|---------------|---------|
| `id` | string | Stable source thread ID, scoped to this entity |
| `created_at` | timestamp string | Source thread creation time |
| `comment_ids` | string list | Known admitted comment action IDs in source thread order |
| `resolved` | boolean or null | Native resolution state; null when the source has no resolution concept |
| `outdated` | boolean or null | Native outdated state; null when the source has no equivalent |
| `review_id` | string or null | Associated formal review action ID, when source-backed |
| `resolved_by` | actor, null, or unknown | Admitted source resolver, null when the thread is not resolved or has no resolver |
| `location` | object or null | Source-backed diff position, or null for a general discussion |
| `location.path` | string | Safe repository-relative path |
| `location.line` | positive integer or null | Current line when the source identifies one |
| `location.side` | `left`, `right`, or null | Current diff side when the source identifies one |
| `location.start_line`, `location.start_side` | line and side or null | Current start of a source-backed multiline range |
| `location.original_line`, `location.original_start_line` | line or null | Original diff coordinates when supplied |
| `location.old_line`, `location.new_line` | line or null | Current old/new coordinates when supplied |
| `extensions` | extension maps | Selected source-defined fields or properties on this thread, when declared |

`resolved`, `outdated`, `review_id`, `location`, and the applicable location
subfields can be unknown when not fetched or not attributable. `null` means
the source confirms that the value does not apply; it is not a substitute for
unknown. `coverage.threads` describes whether more threads may exist.
`resolved_by` is unknown when the source reports a resolver only by mutable
login, or when its actor admission cannot be verified; it never borrows a
comment actor. The range fields retain source-reported coordinates and may
be independently unknown when an outdated thread no longer maps to the
current diff. They are not reconstructed from an unrelated patch.
`coverage.thread_comments` contains exactly one entry per known thread,
keyed by `thread_id`, and describes whether its ordered `comment_ids` list
has an unknown remainder. Only IDs of known, admitted `entity.comments` may
appear in `comment_ids`. An empty partial list cannot prove that a thread has
no admitted comments. The CEL binding preserves the unknown remainder of
both the thread list and each thread's comment list.

GitHub pull-request review threads map their GraphQL thread node ID,
`isResolved`, `isOutdated`, comments, review association when source-backed,
and diff location. GitLab MR discussions map their opaque discussion ID,
member note IDs, and native resolution; `outdated` is null because the API
provides no corresponding thread state. GitHub Discussion root comments each
start a thread: the root comment ID is the thread ID and every reply has
`thread_id` and `parent_id` equal to that root ID. These Discussion threads
have null resolution, outdated, review, and diff-location fields. GitHub
issue comments, Jira issue comments, and source-confirmed unthreaded PR/MR
comments have `thread_id: null`; issue entities have a complete empty thread
list. Thread records and comments are subject to the same entity
authorization and source-revision checks. A producer claims complete thread
or thread-comment coverage only after it has assessed all relevant source
records and applied the action admission gate; unresolved records keep the
corresponding coverage non-complete.

## CEL binding

Entity-source CEL receives required `entity` and nullable `event`.
Event-driven checks receive a [NormalizedEvent v1](../../normalized-event/v1/README.md)
value; scheduled discovery receives CEL `null`. Event-only harnesses retain
their existing non-null `event` behavior. Overlay `when` expressions use the
same roots and select source configuration from `entity.source.system`.

The binding maps every `$unknown` marker to a CEL unknown value at that
attribute path, including nested paths. `has()` on an applicable unresolved
field cannot turn uncertainty into false. An inapplicable field remains
absent and follows ordinary CEL missing-key behavior. For example, a work
item has no `change_proposal` field, and a comment has no `review_state`.
Predicates can guard these by kind or category.

The binding maps all applicable collections, check annotations, and both log
directions to CEL list values with coverage-aware unknown membership.
Host-only `coverage` and `view` are not CEL variables.
The serialized arrays alone are insufficient to evaluate a predicate. The
adapter must preserve these invariants even if its Go representation differs
from the JSON document.

This specification adds no operator truth tables or bespoke CEL evaluation
rules. CEL's [language definition](https://github.com/google/cel-spec/blob/master/doc/langdef.md)
and [partial evaluation APIs](https://cel.dev/reference/api-reference/)
govern evaluation. The fullsend-specific obligation is that the binding
faithfully represents the known values and unresolved possibilities; ADR 0147
and Authorization Contract v2 govern when a result may authorize a run.
No complete acquisition plan must be materialized in this data document.
Deferred and nested fetches can fill individual fields or collection
coverage over time without changing the document's meaning.

## Event and snapshot relationships

`NormalizedEvent.entity` is a routing reference; this `entity` is current
source state. They are not copied from one another, and `event.entity.id` is
never compared directly with the fullsend canonical `entity.id`.
NormalizedEvent carries one transition with separate comment, label, and
review payloads; this entity document carries independently fetched current
body, comments, reviews, and label applications. For a native event,
`entity.source.system` equals the event's source system; for `manual` and
`schedule`, it is the configured hosting system. GitHub and GitLab forge
events must resolve to the same repository identity as the entity. Jira
events resolve the configured Jira project, while their `event.repo` remains
the target code repository. For GitHub and GitLab, a change-proposal or
conversation event's numeric `event.entity.id` matches `entity.number`.
A work-item event with `linked_change_proposal` resolves to that proposal
and its linked number; other forge work-item events use the work-item number.
For Jira, the decimal `event.entity.id` matches `entity.source_id`, and
`event.entity.key` matches `entity.key` at the same source revision. A Jira
key mismatch after a move requires re-resolution by the immutable ID. A
mismatch is an error. Event-time fields can differ from current entity fields;
producers MUST NOT patch entity state from an event.

[Entity-context-snapshot v1](../../entity-context-snapshot/v1/README.md)
is model-facing data staged after routing. It shares GitHub/GitLab source IDs
with this document but uses a distinct hashed snapshot `record_key` and has
different coverage and authorization checkpoints. Its v1 scope excludes Jira
issues and GitHub Discussions. Neither document substitutes for the other.
The resolved entity may be partially fetched for CEL while a later snapshot fetches separate
model-facing context.

## Provider mappings

### GitHub issues and pull requests

| Normalized field | GitHub source |
|------------------|---------------|
| `source_id`, `source.repository_id` | Issue/pull-request and repository GraphQL `node_id` |
| `key`, `number`, `url` | `#` plus the number, the number, and `html_url` |
| `state` | Issue `OPEN`/`CLOSED`; pull request `merged` takes precedence |
| `work_item.source_status`, `.state_reason` | Native issue state and `state_reason` when the issue API supplies them; `issue_type` is unknown with reason `unsupported` |
| `author` | Creator ID, login, and `Bot`/`User` classification |
| `labels` | Current label names |
| `assignees` | Current issue or pull-request assignees, after assignment provenance and actor identity are established; the current forge adapter needs an assignee read |
| `lifecycle` | Source close/merge times and source-authenticated actors when available; unresolved provenance remains unknown |
| `change_proposal` | Base/head repositories and refs, head OID, cross-repository and draft flags |
| `change_proposal.changed_files`, `.commits` | Changed paths and current PR commit SHAs from the existing forge file/commit APIs; patches are independently acquired |
| `body` | Issue or pull request itself, with latest authenticated editor if edited |
| `comments` | Issue comments and pull-request review-thread comments, including minimized comments |
| `threads` | Pull-request review threads with comments, resolution and resolver, outdated state, review association, current range and original diff location; issue comments are unthreaded |
| `reactions` | Entity-level issue or pull-request reactions; the current login-only adapter requires immutable actor enrichment |
| `reviews` | Submitted pull-request reviews; pending reviews are not actions |
| `reviewers` | Current `reviewRequests` plus latest per-user submitted review standing; a team request is a team subject |
| `current_approvals`, `change_proposal.approval.gate` | Current valid approving reviews and current review decision plus applicable approval rules; unresolved rule or actor evidence leaves the gate unknown |
| `label_applications` | Most recent attributable `LabeledEvent` for each applied label |
| `checks` | Current-head check runs, with status, conclusion, times, check-run output and annotations, and optional line views of logs |
| `checks[].url`, `.run_attempt`, `.trigger_event` | Workflow-run URL, attempt and event only when the check can be source-bound to that run; otherwise null or unknown |
| `extensions.custom_fields`, `.properties` | Selected source-defined issue fields and API properties, independently typed and admitted |
| Human `role` | Provider collaborator permission, including `owners_file` when enabled |
| Bot `bot_role` | ADR 0107 provider-backed bot-role resolution |

An unavailable actor or required label application provenance leaves the
affected collection unresolved. Exact label-transition grants are not
represented in v1; known `label_applications` require a verified role.

### GitLab issues and merge requests

| Normalized field | GitLab source |
|------------------|---------------|
| `source.repository`, `source.repository_id` | Project `path_with_namespace` and global numeric project ID |
| `source_id`, `key`, `number`, `url` | Global numeric issue/MR ID, `#` plus issue IID or `!` plus MR IID, project-scoped IID, and `web_url` |
| `state` | Issue/MR open or closed; merged MR takes precedence |
| `work_item.source_status`, `.state_reason` | Native issue state; no current native close-reason or issue-type mapping, so those fields are unsupported |
| `author`, timestamps, `labels` | Source creator, source times, and current labels |
| `assignees`, `lifecycle` | Current assignees and close/merge actors and times already decoded by the poll adapter; each actor requires immutable identity and admission |
| `change_proposal` | Target/source project paths, branches, head SHA, project comparison, and native draft flag |
| `change_proposal.changed_files`, `.commits` | Current MR paths, optional patches, and commit SHAs from the existing forge APIs |
| `body`, `comments` | Issue/MR description and user-authored notes, including MR discussion notes, with current-editor provenance |
| `label_applications` | Current applied label backed by its attributable [resource-label event ID](https://docs.gitlab.com/api/resource_label_events/) |
| Human `role` | Effective project/group membership per Authorization Contract v2 |
| `reviews` | `unsupported` coverage: current approvals are not source-backed formal review submissions |
| `reviewers` | Current MR reviewer assignments and source-backed reviewer state, separately acquired from approvals |
| `current_approvals`, `change_proposal.approval.gate` | Current `/approvals` `approved_by` and applicable approval rules; optional approvals are distinct from satisfying a mandatory gate |
| `threads` | MR discussions with member notes and native resolution; `outdated` is inapplicable; issue comments are unthreaded |
| `checks` | Current-head CI jobs and their trace logs on merge requests; job-bound report text and annotations are unsupported |
| `reactions` | `unsupported` in the current forge adapter; no positive reaction fact is invented |

An MR approval is not invented as a formal `review` action. System-generated
notes are not user comment actions.
An unresolved editor, missing resource-label event, or unverified role keeps
the corresponding collection non-complete. Project-scoped IIDs are never
used as immutable record IDs.

### Jira issues

| Normalized field | Jira source |
|------------------|-------------|
| `source.project`, `source.project_id` | Issue project key and immutable database ID |
| `source_id`, `key`, `number`, `url` | Numeric issue database ID, current issue key, its decimal suffix, and browse URL |
| `state` | Status category `done` → `closed`; `new` or `indeterminate` → `open`; unavailable status → unknown |
| `work_item.source_status`, `.issue_type` | Jira status name/category and issue type ID/name/subtask flag already decoded by the Jira client |
| `work_item.state_reason` | Unsupported until a source-backed Jira reason is mapped; status name and category remain available separately |
| `author`, timestamps, `labels` | Reporter, source times, and current label strings |
| `assignees` | Current assignee as a zero-or-one list, with independent assignment provenance |
| `extensions.custom_fields`, `.properties` | Selected `customfield_*` issue fields and issue API properties; comment properties use the same reusable extension shape on each comment |
| `body`, `comments` | Issue description and comments, using authenticated current editor when edited |
| `threads` | No native issue-thread collection; comments have `thread_id: null` |
| `label_applications` | Most recent attributable addition of each current label from changelog entries and label diffs, with stable entry/item/label identity |
| Human `role` | Configured Jira provider lists or project-role roster under Authorization Contract v2 |

Jira actor IDs are source account IDs. A display name is not an identity or
bot signal. A complete `label_applications` collection requires full relevant
changelog coverage and an attributable application for every current label;
a bounded poll window alone cannot establish completeness. Description edits
without reliable revision-editor provenance leave the `body` action unresolved.
The Jira issue's project is the authorization boundary; the target Git
repository in a routing event does not grant Jira actors a role.

### GitHub Discussions

| Normalized field | GitHub source |
|------------------|---------------|
| `source_id`, `key`, `number`, `source.repository_id` | Discussion and repository GraphQL node IDs, `#` plus the Discussion number, and that number |
| `conversation.category` | Exclusive DiscussionCategory; `name` required, `id`/`slug` when available; `isAnswerable: true` yields `question_answer` |
| `state`, `labels` | Current open/closed status when available, and current Discussion labels |
| `body` | Discussion opening post, with current revision attribution |
| `comments`, `parent_id` | Discussion comments and replies; a root points to itself, a reply points to its root |
| `threads` | One source thread per root comment, with admitted comment IDs in reply order |
| `assignees`, `lifecycle`, `reactions` | Assignee array is empty with `unsupported` coverage; close details and entity reactions remain unknown until source-backed mappings are defined |
| `label_applications` | `unsupported` coverage in v1; no source-backed action mapping is specified |
| Human `role` | Effective repository permission under Authorization Contract v2 |

Comments use immutable [GitHub GraphQL node IDs](https://docs.github.com/en/graphql/reference/discussions)
for `id` and `parent_id`; numeric
IDs in normalized events are routing references to those source records.
Replies always point to the thread root. Category metadata is routing data,
not authorization evidence. GitHub Discussion ingress and `conversation.Client`
remain future runtime work; this target mapping does not imply they are shipped.

## Conformance and versioning

The [examples](examples/) contain valid and invalid documents. They cover
source and canonical identity, ADR 0107 author-role outcomes, contextual keys,
comments and source threads, current reviewers and approvals,
assignees, lifecycle and issue-source details, entity reactions, changed
files and commits, reusable extensions, and the shared actor shape,
partial thread and comment membership, unknown and concrete filtered
title/action text, nested fields, partially fetched lists, complete empty
lists, unauthorized or malformed known actions, independent first/last
log-line coverage, forge-published check reports and partial annotations,
and command-token Unicode boundaries. JSON Schema validation
checks the serialized shape. Additional conformance checks MUST verify:

- known comment, review, and label-application ordering and per-harness
  admission; singular body admission and concrete absence;
- unique current reviewer subjects and approver actor IDs, source-backed
  reviewer standing, current approval times, independent collection coverage,
  assignment/review actor admission, and no synthetic formal GitLab reviews;
  known approval-gate results require current-head and applicable-rule evidence;
- provider-backed author classification and role evidence under ADR 0107;
  unresolved author role fields never imply permission or registered-bot status;
  all person-valued positions use the same actor shape, humans omit
  `bot_role`, and contextual `is_entity_author` is outside that shape;
- independently complete assignee, reaction, changed-file, and commit lists;
  current-head binding for proposal lists, immutable IDs for reactions and
  actors, admitted assignment and reaction provenance, and no complete claim
  when a provider result limit or source revision leaves a remainder;
- lifecycle times and actors from the same current source lifecycle; native
  status and issue-type identity remain distinct from portable `entity.state`;
- extension aliases match trusted source declarations and stable source keys;
  declared value types, selected JSON paths, and actor-valued leaves are
  verified; each concrete value has its own admitted current-revision
  provenance and text filtering; list values have exactly one independent
  `coverage.extension_lists` row, while host-only operational properties do
  not silently become CEL facts;
- current-head check identity and ordering, status/timestamp consistency,
  source-backed check names, and check-log source revision; independent
  contiguous prefixes and complete reversal of ascending and descending log
  accessors; empty views for unsupported log sources and no complete coverage
  for reported truncation; complete-source filtering and source-line mapping
  for concrete log text;
- one annotation-coverage row per known check, source-backed annotation
  location and severity, valid line/column spans, empty known lists for
  `not_loaded` or `unsupported`, pagination and revision consistency before
  `complete`, complete-source filtering of every concrete report or annotation
  text, and GitLab job-bound diagnostic fields remaining unsupported;
- current-revision title and action-text attribution, authorization, and
  complete-source filtering under `view.filter_version`; rejected or truncated
  text remains CEL unknown rather than a concrete empty or prefix string, and
  a concrete `command` agrees with the filtered text's first token when both
  fields are known;
- source consistency for any `complete` collection;
- thread identity, comment-to-thread joins, unique per-thread admitted comment
  references and source order, one `coverage.thread_comments` row per known
  thread, non-complete coverage for unresolved thread/comment membership,
  resolver actor identity, and source-backed current/original coordinates;
- canonical `entity.id` recomputation from source, kind, and `source_id`;
  provider-specific `key` syntax and equality between `number` and its
  numeric component; provider/kind compatibility and event/entity identity
  equality when present;
- Jira issue-key/project consistency and source-project authorization;
- Discussion root/reply parent IDs and category mapping;
- GitLab MR formal-review and Discussion label-application unsupported coverage;
- CEL binding behavior for partial collections and nested unknown fields.

In particular, a partially fetched list containing a known authorized match
must permit that match through ordinary CEL `exists`, while an empty partial
list must not prove absence or a size of zero. A complete empty list can.
A comment absence may be conclusive when `coverage.comments` is complete,
even when reviews or label applications remain partial.
A known element with an unresolved applicable field propagates CEL unknown
for predicates that depend on that field. These cases test the binding,
rather than defining new evaluation operators.

The schema is closed and `schema_version` is `normalized-entity/v1`.
Consumers MUST reject other versions. Changing a field's meaning or its CEL
binding requires a new version. Additive provider, kind, collection, or field
support needs a complete mapping and synchronized producer/consumer update.
