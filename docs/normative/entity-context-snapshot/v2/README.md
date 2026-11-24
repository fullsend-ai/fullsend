# Entity-context-snapshot v2

This specification defines the deterministic, filtered entity-context-snapshot
adopted by [ADR 0141](../../../ADRs/0141-deterministic-filtered-entity-context-staging.md)
under the actor-attributed data boundary in [ADR 0133](../../../ADRs/0133-actor-attributed-data-security-boundary.md),
as revised by [ADR 0170](../../../ADRs/0170-align-entity-context-snapshot-with-normalized-entity.md).
It is the contract between source adapters, `fullsend run`, harness scripts, and
agent runtimes, and it is the model-facing field gate that
[Authorization Contract v2](../../authorization/v2/README.md#entity-source-cel-evaluation)
requires before entity-first execution is enabled for a source and kind.

> **Target contract — not yet implemented:** No shipped producer emits v2.
> [Entity-context-snapshot v1](../v1/README.md) remains the contract for any
> v1 producer. v2 supersedes v1 for new producers; the two versions are not
> mixed within one tree.

Here, *entity-context-snapshot* means the staged data snapshot for a handled
entity. The qualified name distinguishes this data structure from
the dispatch-layer routing category called *entity context* in
[ADR 0076](../../../ADRs/0076-slash-command-entity-context-separation.md),
which determines the agent or slash command that may handle an issue, change
proposal, or conversation. It is also distinct from the CEL-facing normalized
entity value defined in [Normalized entity v1](../../normalized-entity/v1/README.md),
which harness predicates evaluate. The snapshot is a filtered, model-facing
subset: shared fields use Normalized entity v1 names and meanings, as listed in
[Relationship to Normalized entity v1](#relationship-to-normalized-entity-v1).

v2 covers every provider/kind pair in Normalized entity v1, plus the Forgejo
pairs carried forward from v1:

| `source.system` | `work_item` | `change_proposal` | `conversation` |
|-----------------|-------------|-------------------|----------------|
| `github` | Issue | Pull request | Discussion |
| `gitlab` | Issue | Merge request | — |
| `jira` | Issue | — | — |
| `forgejo` | Issue | Pull request | — |

Forgejo pairs are snapshot-only: Normalized entity v1 has no Forgejo mapping,
so they cannot satisfy the entity-first gate until that contract adds one. Any
other pair does not start v2 snapshot assembly; the runner records a host-only
`unsupported_entity` diagnostic outside the tree. That diagnostic never appears
in a schema document. Runtime entity-context-snapshot source reads remain
denied.

## Tree

The singleton JSON documents and the two top-level order views shown below are
always emitted. Array-valued sections are empty when there is nothing to
report. Record, thread, review-view, check, and log entries are emitted only
when selected by `collection.json`. JSON documents use their linked schemas.

```text
context/
├── index.json
├── collection.json
├── entity/metadata.json
├── records/<order-key>-<record-key>.md
├── threads/<thread-key>.order
├── views/
│   ├── timeline.order
│   ├── unresolved-review.order
│   └── reviews/<record-key>.order
├── relations/
│   ├── reviews.json
│   └── threads.json
├── history/agent-runs.json
├── checks/<record-key>/
│   ├── metadata.json
│   └── log.txt
└── state/
    ├── actors.json
    └── threads.json
```

`index.json` conforms to [`index.schema.json`](index.schema.json) and enumerates
every other staged file. `collection.json`, `entity/metadata.json`,
`relations/reviews.json`, `relations/threads.json`, `history/agent-runs.json`,
check metadata, `state/actors.json`, and `state/threads.json` conform
respectively to
[`collection.schema.json`](collection.schema.json),
[`entity.schema.json`](entity.schema.json),
[`reviews.schema.json`](reviews.schema.json),
[`threads.schema.json`](threads.schema.json),
[`agent-runs.schema.json`](agent-runs.schema.json),
[`check.schema.json`](check.schema.json),
[`actor-state.schema.json`](actor-state.schema.json), and
[`thread-state.schema.json`](thread-state.schema.json). Files under `threads/`
contain only ordered relative paths to records. Files under `views/` are
deterministic projections containing those same paths, one per LF-terminated
line. `views/timeline.order` and `views/unresolved-review.order` exist even when
empty.

Manifest `media_type` values are closed by file role: JSON documents use
`application/json`, comment/review/entity records use `text/markdown`, and
order views plus check logs use `text/plain`. Producers must emit the value
specified for the role; media-type parameters and alternate spellings are not
valid v2 values.

The canonical empty singleton documents are `{"reviews":[]}` for
`relations/reviews.json`, `{"threads":[]}` for `relations/threads.json` and
`state/threads.json`, `{"runs":[]}` for `history/agent-runs.json`, and
`{"actors":[]}` for `state/actors.json`, serialized under RFC 8785. An empty
order view is a zero-byte file. Every singleton is emitted for every entity
kind, so an empty document alone never says whether its source was collected;
the `index.json` `scopes` array does (see
[Scope status and gaps](#scope-status-and-gaps)).

## Stable records and mutable state

The entity identity in `index.json` `entity` and `entity/metadata.json` uses
the Normalized entity v1 names, values, and derivation:

- `kind` is `work_item`, `change_proposal`, or `conversation`.
- `source.system` is `github`, `gitlab`, `jira`, or the snapshot-only
  `forgejo`. `source.host` is the lowercase DNS name, with a port from 1
  through 65535 appended as decimal `:<port>` if and only if the HTTPS port is
  not 443, and no scheme or path. The default port is always omitted. A Jira
  host is the site host, not a separate API host.
- Forge entities carry `source.repository` and `source.repository_id`; Jira
  entities carry `source.project` and `source.project_id` instead.
- `source_id` is the immutable provider entity ID.
- `id` is the canonical fullsend entity ID
  `fullsend:entity:v1:<system>:<host>:<kind>:<source_id>`, computed with the
  component percent-encoding defined by
  [Normalized entity v1](../../normalized-entity/v1/README.md#scope-and-identity).
  Forgejo IDs use the same construction.

A record key is the lowercase hexadecimal SHA-256 of three UTF-8 strings. In
order, the strings are the canonical entity `id`, the record kind, and the
source record ID. Encode each string as UTF-8 and concatenate them with exactly
one U+0000 byte between adjacent fields and no trailing U+0000. Commas and
spaces shown in prose are not part of the input. For example, the three fields
`fullsend:entity:v1:github:github.com:work_item:I_kwDOExample53`, `comment`,
and `IC_kwDOExample` produce the byte sequence
`fullsend:entity:v1:github:github.com:work_item:I_kwDOExample53\0comment\0IC_kwDOExample`
and the key
`53f351695a209e33cc093be79eb4a59983809a51743d27dd4693d767566895cb`.
Because the canonical ID excludes the repository and project, repository
transfers, renames, and Jira project moves do not rewrite record keys.

`source.repository_id`, `source.project_id`, `source_id`, and each source
record ID use these positive mappings:

- GitHub uses the GraphQL node ID exposed as `node_id` for the repository and
  each issue, pull request, Discussion, comment, Discussion comment, review,
  check run, or review thread.
- GitLab uses the global project or record `id`, serialized in base-10 without
  leading zeroes; a discussion thread uses its opaque API `id`. Project-scoped
  issue or merge-request `iid` values are display numbers and are not IDs here.
- Forgejo uses the numeric repository or record `id`, serialized in base-10
  without leading zeroes. Issue and change-proposal `number` values are display
  numbers and are not IDs here.
- Jira uses the numeric project, issue, and comment database `id`, serialized
  in base-10 without leading zeroes. The issue key and its project key are
  navigation values and are not IDs here.

Actor IDs follow the same rule: GitHub `node_id`, the GitLab/Forgejo numeric
user `id` in base-10 without leading zeroes, or the Jira `accountId`. A Jira
display name is not an identity. References to a deleted actor retain the
immutable ID when the source exposes it and otherwise use `null`.

`source.repository` and `source.project` are navigation values, not hash
inputs: GitHub uses lowercase `owner/repository`; GitLab uses the API's
canonical `path_with_namespace`; Forgejo uses lowercase API `full_name`; Jira
uses the current project key. Repository forms use literal `/` separators with
no leading/trailing slash or percent encoding. `key` is the contextual human
reference, as in Normalized entity v1: `#<number>` for GitHub, Forgejo, and
GitLab issues, `!<iid>` for GitLab merge requests, and the current Jira issue
key. `number` is its positive numeric component.

Source kinds used in key derivation are `comment`, `review`, `check`, `thread`,
and `entity`. Manifest record entries use only `entity`, `comment`, `review`,
and `check`; `thread` keys identify relationship and ordering data under
`relations/` and `threads/`, not files under `records/`. A selected object that
lacks its required canonical ID produces an `invalid_metadata` gap and is not
assigned an adapter-specific substitute.

The tree contains source entity state that is not reconstructible from the
target Git checkout. Diffs, commits, changed-file lists, branches, and revision
topology are repository context and are not staged here. Fullsend provides the
required Git objects and refs separately, and controllers may derive filtered
diff or history projections outside this tree. Git object IDs occur here as
relationship values in review, thread, and agent-run documents, and as the
collected target revision in check metadata `head_sha`.

Comment and review Markdown files are self-contained records with this exact
UTF-8 layout; header values are canonical JSON strings (or `null`) on one line:

```text
Fullsend-Record: "comment"
Source-ID: "opaque-source-id"
Actor-ID: "opaque-actor-id"
Created-At: "2026-09-08T10:15:30.000000000Z"

Filtered Markdown body.
```

`Fullsend-Record` is `"entity"`, `"comment"`, or `"review"`. The header order
and blank line are fixed. `"entity"` is required for the reserved entity-body
record; `"comment"` and `"review"` identify ordinary conversation records.
`Actor-ID` is the immutable ID of the source-authenticated actor of the
record's current revision: the editor of an edited record, otherwise its
creator. This is the same actor as the Normalized entity v1 action `actor`,
and it is the actor whose admission is checked below. `Actor-ID` may be
`null` only when the source withholds or has deleted that actor. The current
login stays in `index.json` and `state/actors.json`: it may change after an
account rename without the record changing. Navigation URLs are intentionally
omitted from the staged tree; source system, host, repository or project, and
immutable record IDs provide provenance, while a host-side adapter can
construct a link when a human needs one. Effective role is also excluded
because permissions can change without the comment changing.
The file contains no revision time, ordering, thread membership, review location,
resolution, outdated, or minimized state; those properties belong in
`index.json`, `relations/`, or `state/`. Consequently, resolving a thread,
renaming an actor, repository, or project, changing an actor's permission, or
inserting an earlier record must not rename or rewrite an unchanged record.
Changing its body or current-revision actor ID changes that record's bytes and
digest.

Before a comment is admitted to the snapshot, the producer applies
[Authorization Contract v2](../../authorization/v2/README.md) record admission
with the selected harness's effective `human_min_role` from
[ADR 0151](../../../ADRs/0151-harness-human-role-minimums.md), which is
`triage`, `write`, `maintain`, or `admin` above the immutable `triage` floor.
It does not apply event-specific `transition.kind` rules or exceptions
because comment admission is not event authorization. A human current-revision
actor whose verified role meets that minimum is trusted as an eligible command
source and the comment is admitted; lower or unverified human roles are
omitted and count in the `comments` scope's `authorization_failed` gap. Each
source uses the verified role mapping that Authorization Contract v2 defines
for it: repository permission for GitHub (including Discussions) and
Forgejo, effective project or group membership for GitLab, and the configured
Jira provider lists or project-role roster for Jira. For a bot actor, the
producer MUST establish provider-positive bot identity and resolve the exact
registered `actor.bot_role` under [ADR 0107](../../../ADRs/0107-bot-identity-resolution-for-dispatch-authorization.md).
The bot's snapshot actor state has `role: none` and its resolved `bot_role`;
the human role minimum does not apply. A bot with no recognized role, or with an
unavailable or unverifiable lookup, is omitted and counted in the same
authorization gap. This is the intentional trust boundary: users who meet the
harness minimum, and fullsend-registered bots admitted by ADR 0107, may provide
its command inputs. A record whose current-revision actor cannot be
established is treated as unverifiable and omitted.
The handled entity body is actor-attributed content and MUST pass the same
current-revision attribution and authorization rule before it is staged. A
missing, unverifiable, unauthorized, or unsafe entity body emits an
entity-scope `authorization_failed` or filtering gap and aborts assembly; the
required entity body is required only after successful admission and filtering.
Label-transition and bot-review event exceptions authorize an event path only;
they do not by themselves authorize arbitrary bot-authored comment content. An
omitted formal review is counted in the `reviews` scope's
`authorization_failed` gap by the same rule; it is not charged only to
`comments`. Every admitted entity, comment, or review still passes the complete
filter pipeline below, including Unicode safety that removes invisible text and
unnecessary control characters, secret redaction, prompt-injection scanning,
and byte bounds.

The initial issue, change-proposal, or Discussion body uses the same layout
with `Fullsend-Record: "entity"`, the entity's `source_id`, the immutable
current-revision actor ID of the body, and the entity creation time. This makes
it the first self-contained turn. The body actor can differ from the entity
`author` (the creator) when someone else edited the body. A Jira description
in Atlassian Document Format or wiki markup is converted to text by the
adapter's converter before filtering; the converter is part of
`filter_version`, so changing its output for the same input requires a new
filter version.

## Ordering files and prompt assembly

An ordinary record filename is
`records/<order-key>-<record-key>.md`. `<order-key>` is its source `created_at`
normalized to UTC as `YYYYMMDDTHHMMSSnnnnnnnnnZ`, with exactly nine fractional
second digits and no punctuation other than `T` and `Z`. The entity-body record
uses the reserved key `00000000T000000000000000Z`, so it always sorts first.
Creation time is immutable source data; edits do not rename a record.

Every order/view file lists only records that have an emitted `content_path`;
rejected comments and reviews never create dangling order lines. The entity
record is always present because an entity-body rejection aborts assembly.
`views/timeline.order` lists that entity record and retained comment/review
records in canonical chronology. Each `threads/<thread-key>.order` lists that
thread's retained records in source thread order. `views/unresolved-review.order`
lists retained records in threads whose state has `resolved: false` and
`outdated` not `true`, ordered by thread creation time and key and then source
thread order. A thread with `resolved: null`, such as a GitHub Discussion
thread, has no resolution concept and never appears in that view. A
`views/reviews/<record-key>.order` file is emitted only for a retained formal
review; it lists that review followed by retained records in threads associated
with it. A record path appears at most once in any one order file. Paths contain
no whitespace or shell metacharacters, so a host consumer may materialize a
projection with `xargs cat`, but runtimes use the segmented contract below.

A runtime that injects a projection into a model request emits each referenced
record as a distinct, ordered content block. Relationship, history, mutable
state, and run-specific instruction blocks follow the stable record blocks.
The runtime must not concatenate all records into one content block when
prompt-cache reuse is intended: appending to that block would change its digest
and lose the otherwise reusable record prefix. A runtime may mark boundaries
using provider-specific cache controls, but provider cache behavior is not a
v2 conformance guarantee. Reading the same files through agent tools avoids
source calls but still incurs tool-result tokens.

A normal later reply adds one lexically later file and extends applicable order
files, leaving earlier record blocks byte-identical. A backfilled earlier
record, edit, deletion, or attribution change invalidates reuse from the first
affected block onward. Mutable metadata — labels, state, title, logins, roles,
thread resolution, and scope status — lives only in JSON documents, so changing
it never rewrites a record file. Consumers never infer resolution from record
content.

Reviews, checks, and check logs are change-proposal-only sources. A collection
profile for a `work_item` or `conversation` cannot select them, and their scopes
are absent from that entity's `scopes` array rather than reported as empty or
failed. Check status is observation
state in `checks/<record-key>/metadata.json`; its
log file contains only filtered log bytes. A growing or replaced forge log is
changed content and may change `log.txt`. A new check attempt has a new forge
record ID and therefore a new record key; v2 has no separate synthesized
attempt number. Its `created_at` is the forge's
immutable check-run creation time and is also the check manifest record's
`created_at`; a runner first-observed time is forbidden. Check status is
normalized to `queued`, `in_progress`, or `completed`; conclusions are
`success`, `failure`, `neutral`, `cancelled`, `skipped`, `timed_out`,
`action_required`, or `stale`.
Queued checks have no start, completion, or conclusion fields. In-progress
checks require `started_at` and have no completion or conclusion. Completed
checks require `completed_at`, `conclusion`, and `never_started`.
`never_started: true` is emitted only when the forge explicitly reports that
the job never started; for a completed `skipped` or `cancelled` outcome, the
adapter treats a missing native `started_at` as that explicit never-started
report. It forbids `started_at` and is limited to `failure`, `skipped`, or
`cancelled`. This includes GitHub `startup_failure`, which maps to the v2
`failure` conclusion. `never_started: false` requires `started_at`; adapters
must not set it on an ordinary `failure` merely because `started_at` is absent.
Consumers must use the explicit flag and must not infer never-started state from
an omitted timestamp.

At collection start, the producer captures the target change-proposal HEAD
revision and retains only checks whose forge-reported revision equals that
`head_sha`. Every retained check records that exact SHA in its check metadata;
the field is required even when a forge exposes only a generic check-run
endpoint. A missing, malformed, or mismatched check revision is
`invalid_metadata` and the check is not emitted. The producer must not fetch
logs or synthesize a check for any other revision.
Check manifest records carry `created_at` but no `author_id` or `author`.

Adapters use these exhaustive native-status mappings, unchanged from v1:

- GitHub `queued`, `waiting`, `requested`, and `pending` map to `queued`;
  `in_progress` maps to `in_progress`; `completed` maps to `completed` and its
  native conclusion maps identically to the v2 conclusion vocabulary, except
  `startup_failure`, which maps to `failure` with `never_started: true`.
  Completed `skipped` and `cancelled` runs with no native `started_at` also
  emit `never_started: true`; all other completed conclusions require
  `never_started: false` and a native `started_at`.
- GitLab `created`, `waiting_for_resource`, `waiting_for_callback`, `preparing`, `pending`, `scheduled`,
  and `manual` map to `queued`; `running` and `canceling` map to `in_progress`;
  `success`, `failed`, `canceled`, and `skipped` map to `completed` with
  conclusions `success`, `failure`, `cancelled`, and `skipped`, respectively.
  For completed `canceled` and `skipped` jobs, a null native `started_at` is
  the explicit never-started report and emits `never_started: true`; otherwise
  they emit `never_started: false`. A `failed` job with no native `started_at`
  remains ordinary failure and is `invalid_metadata`.
- Forgejo `pending`, `waiting`, and `blocked` map to `queued`; `running` maps to
  `in_progress`; `success`, `failure`, `error`, `warning`, `cancelled`,
  `canceled`, and `skipped` map to `completed` with conclusions `success`,
  `failure`, `failure`, `neutral`, `cancelled`, `cancelled`, and `skipped`,
  respectively. For completed `cancelled`, `canceled`, and `skipped` jobs, a
  null native `started_at` is the explicit never-started report and emits
  `never_started: true`; otherwise they emit `never_started: false`.

Any native status or conclusion not listed above is `invalid_metadata`; an
adapter must not invent another mapping. Any mapped check whose forge data
cannot satisfy `check.schema.json`'s status-specific timestamp requirements is
also `invalid_metadata`. Completed checks with `never_started: true` require
`completed_at` and omit `started_at`; all other completed checks require both
timestamps. `completed_at` is the native terminal timestamp when present. If a
never-started completed check has no native terminal timestamp, the adapter
uses its immutable source `created_at` as the canonical fallback; this records
the only source-derived time available and is not a runner-observed completion
time. If either required source timestamp is absent or invalid, the check is
`invalid_metadata`. Native timestamp fields
forbidden for the mapped status are omitted rather than copied.

## Relationships, history, and collection profiles

`relations/reviews.json` preserves normalized formal review outcomes and
reviewed revisions separately from mutable state. `review_state` uses
`approved`, `changes_requested`, `commented`, and `dismissed`, with the same
name and values as the Normalized entity v1 review action and matching
normalized-event v1. `relations/threads.json` preserves the association between
threads, replies, reviews, and code locations separately from mutable
resolution state. A thread `location` uses the Normalized entity v1 thread
location field names; source sides such as GitHub `LEFT` and `RIGHT` are
lowercased to `left` and `right`. Reviewed revisions and location fields are
optional because sources expose different subsets, and a general discussion
thread has no `location`. `state/threads.json` references the actor that
resolved each resolved thread, and `relations/reviews.json` references the
actor that dismissed each dismissed review. A nullable reference explicitly
records that the source did not expose the actor; a nullable dismissal
timestamp does the same when the source exposes the disposition but not its
time. Actor references resolve through `state/actors.json`.

Review adapters use these exhaustive v2 mappings:

- GitHub `APPROVED`, `CHANGES_REQUESTED`, `COMMENTED`, and `DISMISSED` map to
  their lowercase v2 equivalents. `PENDING` reviews are unsubmitted and are
  omitted.
- GitLab approval summaries and current-approver lists do not have immutable
  review record IDs and are not formal review records. GitLab merge-request
  `reviews` scope status is `unsupported`, as Normalized entity v1 GitLab
  `reviews` coverage is. GitLab discussion notes are staged as comments and
  threads.
- Forgejo `APPROVED`, `REQUEST_CHANGES`, `COMMENT`, and `DISMISSED` map to
  `approved`, `changes_requested`, `commented`, and `dismissed`, respectively.
  `PENDING` and `REQUEST_REVIEW` rows are unsubmitted reviewer requests and are
  omitted.

Formal review timestamp mapping is also exhaustive. The record's
`created_at`, its filename order key, and the relationship `submitted_at` all
use the canonical submission time. For GitHub this is native `submittedAt`;
native `createdAt` is the start of a pending review and is not used for a
submitted record. A submitted GitHub review without `submittedAt` is
`invalid_metadata`. Forgejo uses native `submitted_at` when present and uses
native `created_at` only when the completed review exposes no separate
submission field; if neither is present it is `invalid_metadata`. GitLab has
no formal review records, so discussion notes use their native note
`created_at` as comment time and have no `submitted_at`. Adapters must not use
runner-observed time as a fallback.

Any other native formal-review state is `invalid_metadata`.

Thread adapters use these mappings, matching Normalized entity v1:

- GitHub pull-request review threads map their GraphQL thread node ID,
  `isResolved`, `isOutdated`, comments, review association when source-backed,
  and diff location.
- GitHub Discussion root comments each start a thread whose ID is the root
  comment's node ID; every reply belongs to that thread and has a reply edge to
  the root. Discussion threads have `resolved: null`, `outdated: null`, and no
  review association or `location`.
- GitLab merge-request discussions map their opaque discussion ID, member note
  IDs, and native resolution, with `outdated: null`. Forgejo review comments
  map their native resolution when exposed, otherwise `resolved: null`, and use
  `outdated: null` unless the API exposes a native outdated flag.
- Work-item comments on every source are unthreaded. A work item has no
  `threads` scope, and its `relations/threads.json` and `state/threads.json`
  are the canonical empty documents.

`null` in `resolved` or `outdated` means the source has no such concept; it is
not inferred from the current diff and is never rewritten as `false`.

The `threads` arrays in `relations/threads.json` and `state/threads.json` have
exactly the same unique `thread_key` set. Every relationship thread therefore
has an explicit state row with explicit `resolved` and `outdated` values.
Missing, duplicate, or extra thread-state rows make the `threads` scope
unusable and add an `invalid_metadata` gap. The `threads` source requires
`comments`: a thread lists only retained comment records. A thread's
`review_id` and `review_record_key` are emitted only when the associated
review record is retained.

`state/actors.json` contains the current, source-verified actor state for each
referenced author, actor, resolver, and dismisser, using the Normalized entity
v1 actor shape: `id`, `login`, `kind`, `role`, `role_verified`, and `bot_role`
only for a bot. Roles use the
[ADR 0054](../../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md)
vocabulary shared with Normalized entity v1 and normalized-event v1: `admin`,
`maintain`, `write`, `triage`, `read`, `none`, and `external`.

- A human has no `bot_role`. `role_verified: true` requires a concrete verified
  effective role. `role_verified: false` is paired with `role: null` and means
  consumers must fail closed rather than infer authority from source
  association labels such as GitHub `authorAssociation`; a permission lookup
  failure, including a collaborator API 404, uses that representation.
  Normalized entity v1 represents the same failed lookup as an unknown role.
- A provider-classified bot has `role: none` and a required `bot_role`.
  `role_verified: true` means the ADR 0107 lookup completed: `bot_role` is the
  registered role, or `null` after a completed no-match. `role_verified: false`
  means the lookup failed and requires `bot_role: null`. Only a bot with
  `role_verified: true` and a non-null `bot_role` can be admitted.

The authorization contract's narrow transition-specific exceptions for GitHub
label changes and submitted bot reviews remain event-path exceptions; they do
not authorize arbitrary bot comments without the applicable bot-role
admission. Each document carries an independently versioned copy of the role
vocabulary, and each value must validate against the schema of the document
that uses it.

Every non-null entity `author.id`, record `actor.id`, `resolved_by_actor_id`,
and `dismissed_by_actor_id` referenced anywhere in the snapshot appears exactly
once in the actor array. JSON Schema cannot express uniqueness by one object
property, so producers enforce this cross-document invariant. Consumers treat
a duplicate or missing actor row exactly like `role_verified: false`. The actor
array sorts by actor ID. Because permissions and dispositions are mutable
observation state, changing them changes state or relationship documents but
not record files. Role values are observed during snapshot assembly, not
copied from a historical comment event.

`history/agent-runs.json`
contains only immutable, source-observable fullsend run receipts and relates an
agent result to its input revision, result records, and resulting commit. A
receipt's `input_revision` is the lowercase SHA-256 of the RFC 8785
`index.json` bytes consumed by that run; `input_head_sha`, when present, is the
separate Git revision checked out for the run. Receipt status is `success`,
`failure`, `cancelled`, `skipped`, or `timed_out`. A mutable sticky comment may
be a human-facing summary, but it is not canonical
run history and its overwritten versions cannot be reconstructed from a source
that does not expose edit history. v2 defines receipt mappings only for forge
work items and change proposals; the `agent_runs` scope of a Jira issue or a
GitHub Discussion has status `unsupported` until a source-backed immutable
receipt mapping is specified.

`collection.json` is the exact collection configuration and conforms to
`collection.schema.json`. Its source-kind array sorts by ASCII byte order.
The required `admission.human_min_role` records the selected harness's
effective human minimum under ADR 0151: `triage`, `write`, `maintain`, or
`admin`. Registered bot roles follow ADR 0107 and do not use that human
minimum. The producer resolves this value from the selected harness, as
Authorization Contract v2 specifies, before serializing and hashing
`collection.json`; a harness whose minimum cannot be resolved does not start
assembly. A different minimum is a different collection profile and requires a
separately filtered snapshot. `entity_kind` must match the entity metadata kind.
Source applicability depends only on the entity kind:

| `entity_kind` | Selectable non-entity sources |
|---------------|-------------------------------|
| `work_item` | `comments`, `agent_runs` |
| `conversation` | `comments`, `threads`, `agent_runs` |
| `change_proposal` | `comments`, `threads`, `reviews`, `checks`, `check_logs`, `agent_runs` |

`threads` requires `comments`, and `check_logs` requires `checks`. A profile
that violates these rules is invalid before fetching or assembly and aborts
collection; it is never represented as a scope status or a gap. A selectable
source that the provider cannot supply, such as GitLab merge-request
`reviews`, is valid in a profile and is reported with scope status
`unsupported`.
Non-entity records first enter the corresponding canonical order from
[Ordering and determinism](#ordering-and-determinism); `record_selection`
retains either the oldest or newest `max_records_per_source` entries for each
included source, then output ordering follows the same chronology rules.
When a bound drops records, the producer emits exactly one `profile_bound` gap
for each affected scope, with `count` equal to the number dropped. Counts from
multiple bounds for the same scope and code are aggregated, and the gaps array
contains at most one row for each `(scope, code)` pair, sorted by scope and
code. Dropping labels because of `max_labels`, or because a label does not fit
`max_metadata_string_bytes` (see [Canonical bytes](#canonical-bytes)), emits a
`labels`-scope `profile_bound` gap whose count is the number of dropped labels
for both causes together, sets the `labels` scope status to `partial`, and sets
the entity-metadata filter status to `truncated`; it is never silent.
After comment and review record selection, thread `record_keys`, reply edges,
thread order files, and review views contain only retained records that have an
emitted `content_path`. A reply edge is emitted only when both endpoints remain
and have content paths. A thread with no such records is omitted from both
relationship and state arrays and has no order or view file; dangling record
keys and order lines are forbidden.
`check_log_selection` is `none`, `failed`, or `all`. `failed` means the parent
check conclusion is `failure`, `timed_out`, `action_required`, or `stale`;
`cancelled`, `skipped`, and `neutral` checks are not failed. `check_logs` can be
included only with `checks`. Check record selection and
`max_records_per_source` are applied first. Logs are then selected only from
the retained parent checks and use the parent's `created_at` and `record_key`
for deterministic order; logs are not a second independently counted record
source. Bounds apply after the
full-source security pipeline described below: `max_record_bytes` bounds each
record body, `max_log_bytes` bounds each log, and
`max_metadata_string_bytes` bounds each source-derived display string, except
that a label is dropped rather than truncated when it does not fit. Labels
first sort canonically and then retain the first `max_labels` entries.
Structural strings cannot be truncated.
Exceeding `max_metadata_string_bytes` makes a structural value invalid.
`max_source_bytes` is the pre-decode acquisition cap described below.
`collection_profile` equals the lowercase SHA-256 of the exact RFC 8785
`collection.json` bytes and therefore also equals that file's manifest digest.
Profiles must not vary based on runner time or an agent's intermediate choices.
For a retained check, the index record has `log_path` if and only if
`check_log_selection` selected its log and filtering produced the file without
an unusable `invalid_metadata` or `unsafe_content` gap.

### Scope status and gaps

Scopes are `entity`, `labels`, `comments`, `threads`, `reviews`, `checks`,
`check_logs`, `agent_runs`, and `actors`. `index.json` `scopes` contains
exactly one row for each scope applicable to the entity kind, sorted by scope,
and no row for an inapplicable scope. `entity`, `labels`, `comments`,
`agent_runs`, and `actors` apply to every kind; `threads` applies to
conversations and change proposals; `reviews`, `checks`, and `check_logs`
apply only to change proposals. Each row's `status` uses the Normalized entity
v1 coverage vocabulary:

| Status | Meaning | Gaps for the scope |
|--------|---------|--------------------|
| `complete` | Selected, supported, and exhaustively staged; an empty document or array is truly empty | None |
| `partial` | Some records are staged, but more may exist | Only `profile_bound` or `history_unavailable` |
| `not_loaded` | Applicable, but the profile did not select it; nothing from the source is staged | None |
| `unsupported` | Selected, but this provider/kind pair cannot supply it | None |
| `failed` | Selected, but at least one unusable gap prevents an authority or completeness decision | At least one unusable gap |

`entity` is always `complete` in an emitted snapshot, because any unusable
entity gap aborts assembly. `labels` is `complete`, `partial`, or `failed`;
it is `failed` when filtering dropped an unsafe label (see
[Canonical bytes](#canonical-bytes)). `actors` is
`complete` or `failed` and is derived from the actor references rather than
selected. The status and the gaps must agree as shown above; JSON Schema cannot
express that cross-array invariant, so producers enforce it and consumers
treat a disagreement as `failed`. Normalized entity v1's `unknown` status is
not used: a producer that cannot establish completeness reports a gap and
`partial` or `failed`.

The scope status is what distinguishes an omitted value from a complete empty
value. An empty `entity/metadata.json` `labels` array with a `complete`
`labels` scope proves the entity has no labels; an empty `{"threads":[]}` with
a `not_loaded` or `unsupported` `threads` scope proves nothing. Consumers MUST read the scope
status before treating an empty document, array, or order view as absence.

Missing data from a valid selected source is represented in `index.json.gaps`;
it is never silently treated as an empty history. Inapplicable sources are
rejected by the collection profile before fetching and therefore do not create
a scope row or a gap. `profile_bound` and
`history_unavailable` gaps have `usability: partial`; consumers may use present
records but must not claim the scope is complete.
`authorization_failed`, `fetch_failed`, `source_too_large`,
`invalid_metadata`, and `unsafe_content` have `usability: unusable`; consumers
must not make an authority or completeness decision from that scope. Any
unusable `entity`-scope gap for the required entity body or metadata aborts
snapshot assembly, including `authorization_failed`, `fetch_failed`,
`source_too_large`, `invalid_metadata`, and `unsafe_content`; fullsend must not
launch scripts or an agent with that snapshot. The same gap codes remain
non-aborting unusable gaps for optional scopes. Any `actors` gap makes
authorization unusable and therefore fails closed. Runtimes
and host scripts
must inspect relevant gaps before consuming records or state.

## Canonical bytes

JSON is UTF-8 serialized with the JSON Canonicalization Scheme (RFC 8785), with
no byte-order mark or trailing newline. Arrays use the order defined below;
objects use RFC 8785 member ordering.

Every JSON timestamp is normalized to UTC and serialized with exactly nine
fractional-second digits as `YYYY-MM-DDTHH:MM:SS.nnnnnnnnnZ`. Offset forms,
omitted fractions, and other equivalent RFC 3339 spellings are not canonical.
Git object IDs are serialized as lowercase hexadecimal strings.

Properties marked required by a schema are always emitted. If a required
property is nullable and its normalized source value is unavailable, it is
emitted as JSON `null`. A non-required property is emitted only when its
normalized source value is available; otherwise it is omitted and is never
synthesized as `null` or with a default value.

Text bodies and logs are UTF-8 after the filter pipeline, use LF line
endings, and have no byte-order mark. Line-ending normalization canonicalizes a
non-empty body or log to exactly one trailing LF before `filter.status` is
computed. The byte bound includes that LF. To bound non-empty text, remove all
trailing LFs, retain the longest prefix ending at a UTF-8 code-point boundary
whose bytes plus one LF fit the bound, then append one LF. An empty body
contributes zero bytes after the record header's required blank line; an empty
log is a zero-byte file.

The pipeline operates on the complete selected source in this order:

1. decode and normalize line endings, then apply Unicode safety normalization;
2. redact secrets and sensitive data from the complete normalized source;
3. scan the complete redacted source for prompt injection and reject it when
   policy says it cannot be represented safely;
4. for record bodies and logs, apply the configured byte bound at a UTF-8
   code-point boundary and enforce the single trailing LF rule; and
5. if step 4 removed bytes, scan the exact emitted bytes again for prompt
   injection and reject them if unsafe in isolation.

Secret and sensitive-data detectors must never run only on a truncated prefix.
Because redaction precedes the bound, a secret match cannot straddle an
uninspected truncation boundary. Injection detection covers both the complete
redacted source and the exact truncated bytes the consumer would receive.
An input exceeding the profile's `max_source_bytes` is not decoded and adds an
unusable `source_too_large` gap; no prefix is emitted. A pipeline component
error, timeout, or unavailable detector aborts snapshot assembly; it never
passes through original bytes.

Every `filter_version` strips and records findings for, at minimum, Unicode tag
characters U+E0000--U+E007F; zero-width U+200B--U+200D and U+FEFF;
bidirectional controls U+202A--U+202E and U+2066--U+2069; variation selectors
U+FE00--U+FE0F and U+E0100--U+E01EF; and interlinear annotations
U+FFF9--U+FFFB. A filter version may cover more classes, but it cannot weaken
these invariants.

Fullsend's shipped filter version must not be weaker than its Unicode safety
scanner at the commit that accepts this specification. That scanner also
removes nulls, terminal escapes, LRM/RLM, word joiners, other format (`Cf`)
characters, and applies reported NFKC normalization. Those additional
transformations are bound to the shipped `filter_version`; they are not
silently added to the portable character-class floor above, and changing their
output requires a new filter version.

The redaction floor, unchanged from v1, detects exact run credentials registered by fullsend;
OpenAI, Anthropic, GitHub, GitLab, Slack, Google, AWS, Stripe, SendGrid,
Hugging Face, npm, PyPI, Vault, and age secret prefixes; private-key blocks;
authorization headers; secret-named environment and JSON values; database URL
passwords; and structured SSN, payment-card, and IBAN values. Exact pattern and
validation semantics are part of `filter_version`. A later filter version may
add detectors but must not remove or narrow this floor.

The deterministic injection floor rejects matches for instruction override,
system-prompt replacement, unrestricted-role requests, concealment from the
user, credential-file or environment exfiltration, hidden HTML instructions,
and translate-then-execute requests. Exact patterns and thresholds are part of
`filter_version`; ML detectors may supplement them only when their model,
threshold, and deterministic execution contract are also versioned. No
detector reliably blocks every visible prompt injection. The entire
entity-context-snapshot tree is therefore untrusted snapshot-derived data:
this includes `index.json`, entity metadata, relations, history, state,
check metadata and logs, order files, and all record bodies. Runtimes must
keep those blocks distinct from run-specific instructions, prohibit following
instructions from any snapshot-derived block, and retain least-privilege tool
and credential boundaries as the primary defense. Filtering and authorization
are not bot bypasses: a bot identity is subject to the same comment admission
and content filtering rules as a human identity.

`filter.status` is:

- `unchanged`: emitted bytes equal normalized source bytes;
- `modified`: one or more replacements or redactions were applied;
- `truncated`: a size bound removed source bytes, whether or not earlier filters
  also changed them; findings still report every replacement and redaction;
- `rejected`: no content file is emitted because the source could not be
  represented safely.

Source-derived display strings in JSON metadata pass through Unicode safety,
redaction, and complete-source injection scanning, then are bounded at a UTF-8
code-point boundary with no trailing LF added. Bound display strings first, except labels; drop
empty optional values, drop labels that do not fit the bound, re-unique labels,
then sort labels and apply `max_labels`. A required singleton title that becomes empty or unsafe aborts
assembly like a rejected entity body. Required nullable entity `author.login`,
record `actor.login`, and actor-state `login` values become JSON `null` when
unsafe or empty. An unsafe or empty check name, agent name, or optional
Discussion category `slug` rejects only its affected optional value or record
with an `unsafe_content`/`invalid_metadata` gap; it never aborts an otherwise
valid entity snapshot. A required Discussion category `name` that becomes empty
or unsafe aborts assembly like the title. An unsafe label is dropped and its
findings are retained, and the producer emits a `labels`-scope `unsafe_content`
gap with `usability: unusable` whose `count` is the number of dropped unsafe
labels, and sets the `labels` scope status to `failed`. This holds even when no
labels remain: an empty `labels` array with a `failed` scope is never evidence
that the entity has no labels. A label is an identifier used for exact membership checks, so
it is never truncated: a label whose filtered value exceeds
`max_metadata_string_bytes` (including a multibyte label whose first code point
does not fit) is dropped, never shortened or emitted empty, and counts toward
the `labels`-scope `profile_bound` gap and `partial` status described in
[collection profiles](#relationships-history-and-collection-profiles). A scope with that gap never proves the
absence of a label. A label that is already empty before bounding is an empty
optional value and is dropped without a gap.
A truncated display string is scanned again exactly as
emitted before canonical serialization. This includes entity titles, labels,
and Discussion category names and slugs; entity author, manifest-record actor,
and actor-state logins; check names; and agent names.
Redaction reduces exposure but does not promise anonymization;
provider retention and prompt caching of remaining content are governed by the
configured provider trust boundary, not by this cleanup guarantee.

Structural strings are not rewritten because doing so could change identity or
target a different resource. Canonical IDs use the mappings above. Repository
paths and review file paths MUST undergo the complete source-value filter
pipeline, including Unicode safety, secret redaction, and prompt-injection
scanning, before strict structural validation. An unsafe path is never rewritten:
the producer omits the affected optional record or relationship, and aborts when
a required singleton depends on it. The `repository_path` and `source.host` schema patterns
use only RE2-compatible syntax and enforce their printable alphabet; producers
additionally enforce the documented segment, leading-slash, trailing-slash,
port-range, and default-port rules. A validator that cannot compile or
evaluate any security pattern must report a validation error and abort required
snapshot assembly; it must never ignore the pattern or fall back to a weaker
constraint. v2, like v1, deliberately restricts `repository_path` to the
printable-ASCII allow-list encoded by `common.schema.json` (including its
explicit punctuation set and excluding non-ASCII letters and apostrophes); this
is an explicit compatibility and security boundary, not an implicit Unicode
normalization. Non-ASCII or other out-of-alphabet values are structurally
invalid. Repository file paths are relative,
slash-separated, contain no `.` or `..` segment, percent sign or percent-encoded
dot, slash, or backslash, control character, or non-rendering class named above.
The host port is 1 through 65535, and `:443` is rejected because the default
port is omitted. These values are untrusted source data and must never be
interpolated into shell commands; generated order-file paths have a separate
shell-safe contract.
Source URLs
are intentionally not part of the agent-visible tree; source system, host,
repository or project, and immutable record IDs provide provenance without
copying navigation URLs into staged metadata. This is an explicit difference
from Normalized entity v1, which carries `entity.url`. An unsafe structural
value, whether its property is required or optional, produces an unusable
`invalid_metadata` or `unsafe_content` gap. For optional records and their
related files, that gap omits the selected source as described above. Required
singleton structural values cannot be omitted: an unsafe, missing, or
schema-invalid entity `id`, `source_id`, `key`, or `source` field (`host`,
`repository_id`, `repository`, `project`, or `project_id`), aborts snapshot
assembly rather than emitting an invalid singleton or dangling manifest. The
producer also recomputes the canonical `id` from `source.system`,
`source.host`, `kind`, and `source_id`, checks that `number` equals the numeric
component of `key`, and checks that a Jira `key` prefix equals
`source.project`; a mismatch aborts assembly. Structural values are never
emitted after filtering or truncation.

All structural IDs use the schema's `common.schema.json` `structural_id`
definition, which excludes whitespace, control characters, slashes, and shell
separators. Adapters apply the narrower source mapping before schema validation:
GitHub node IDs use `[A-Za-z0-9][A-Za-z0-9_=-]*` (the schema's
`github_node_id` definition); GitLab, Forgejo, and Jira numeric IDs use
`[1-9][0-9]*` (the schema's `numeric_id` definition); GitLab discussion IDs use
`[A-Za-z0-9][A-Za-z0-9._:-]*`; Jira account IDs use the `structural_id`
alphabet; and filter finding codes use
`[a-z][a-z0-9._-]*`. Every GitHub-sourced structural ID uses the same
`github_node_id` mapping, including entity, record, actor, review, thread,
check, and agent-run IDs; the standalone child schemas rely on the adapter
check because they do not carry `source.system`. A value outside its source mapping is `invalid_metadata`;
it is rejected or the affected optional record is omitted rather than
rewritten. Unsafe required IDs abort assembly.

`index.json.filter` records filtering of source-derived values in the index.
Manifest file entries require `filter` for entity, review, thread-relationship,
agent-run, check metadata, check logs, and actor JSON documents. A `check_log` entry's filter
describes the emitted log bytes only; the parent check record's filter remains
the separate summary of filtered check metadata. Each manifest value summarizes
the filtered bytes for its own document or file.
Generated collection, state-without-display-text, and order documents do not
invent filter findings.

Every emitted file other than the root `index.json` has a manifest `sha256`
over its emitted bytes. The assembler retains the exact root `index.json` bytes,
or a digest of them, in host-private run state outside the context tree. After
the host pre-script, fullsend compares the on-disk root index with that retained
assembly value and aborts on any difference; it never treats a rewritten
on-disk index as the root of trust. Only the retained assembly index is used to
verify child digests and the exact manifest path set. A rejected non-entity
source has a record but no content path or file entry and adds an
`unsafe_content` gap for its scope. A rejected or missing entity-body record
aborts snapshot assembly, and fullsend must not launch scripts or an agent with
that snapshot.

Verification, upload, and cleanup MUST reject symlinks and special files at the
context root and every path component, and MUST require regular files beneath
real directories. After the root-index comparison, fullsend MUST verify child
bytes into an immutable, digest-verified upload snapshot (for example, a private
copy with concurrent writers excluded and immutable/read-only enforcement), or
hold an equivalent exclusive writer-blocking capability. No-follow handles alone
are insufficient because an already-open regular file can still be truncated or
overwritten in place. The uploaded bytes MUST be the exact bytes verified against
the retained assembly manifest; if immutability or writer exclusion cannot be
enforced, assembly aborts before scripts or the agent launch.

Findings contain codes and counts, not rejected source text. Filters and bounds
are identified by `filter_version`; changing emitted bytes for the same input
requires a new filter version. Removing or reinterpreting a status requires v3.

## Ordering and determinism

Manifest `records[]` entries are sorted by each record's real source
`created_at`, then by the record key's ASCII byte order; the reserved
entity-body order key does not change this array order. Record filenames are
sorted by their filename order key, which reserves
`00000000T000000000000000Z` for the entity body so it sorts first. `files[]`
remains path-sorted, and check paths do not embed an order key. Formal review
arrays sort by `submitted_at` and then review ID. Relationship thread arrays
sort by `created_at` and then thread ID; each
thread's `record_keys`, `replies`, and `.order` lines preserve source thread
order. Mutable thread-state arrays sort by the thread key's ASCII byte order.
Actor-state arrays sort by actor ID's UTF-8 byte order. Agent receipts sort by
completion time and ID; each receipt's `result_record_keys` sort by ASCII byte
order. Entity labels sort by their filtered UTF-8 byte order. Scope rows sort
by scope. Manifest files
sort by path, gaps by scope and code, and filter findings by code. Other arrays
state their ordering in their owning schema before being added to v2.

`generated_at` or another runner-clock value is forbidden anywhere under the
context root. Acquisition timing belongs in run telemetry outside the staged
tree. Source-provided timestamps, entity update time, Git object IDs used as
relationship values, and source check record IDs are permitted. Determinism is
defined over the complete normalized acquisition inputs: source responses,
authorization-provider results, effective admission policy, collection
profile, size bounds, filter version, and the active credential-redaction
registry. With those inputs identical, the complete tree has identical paths
and bytes; stale authorization or filtering state MUST NOT be reused.

## Lifecycle and access

The host tree is created outside both the repository and retained run-output
tree with directory mode `0700` and file mode `0600`. The sandbox copy is
agent-immutable: fullsend exposes it through a read-only bind mount, or, when
that is unavailable, uses a root-owned tree with mode `0555` directories and
`0444` files while the agent runs unprivileged with `CAP_FOWNER` and equivalent
write-overriding capabilities removed. A plain mode-`0600` copy owned by the
agent does not satisfy this contract; if neither mechanism can be enforced,
fullsend aborts before launching the agent. Fullsend removes the sandbox copy
after the runtime's last use and the host copy after the post-script on every
controlled exit, including skip, failure, and cancellation. Fullsend also
scavenges abandoned context trees on startup after an unclean termination.

Artifact collectors must exclude entity-context-snapshot trees. Retained diagnostics
may contain bounded record counts, content digests, filter codes/counts, and
cleanup errors, but never entity bodies, comment/review bodies, or logs.

## Compatibility

Consumers must reject an unsupported `schema_version` or `filter_version`; they
must validate `index.json`, `collection.json`, `entity/metadata.json`,
`relations/reviews.json`, `relations/threads.json`, `history/agent-runs.json`,
check metadata documents, `state/actors.json`, and `state/threads.json` against
their v2 schemas. Other
emitted files follow the byte contracts above. The files under
`docs/normative/entity-context-snapshot/v2/` form one local schema bundle: consumers
must load every sibling `*.schema.json` file into the same resolver registry,
keyed by its `$id` and filename, before validating any document. Relative
references such as `common.schema.json#/$defs/timestamp` resolve to sibling
files within that directory. `$id` values are stable schema identifiers, not
documented HTTP endpoints; consumers must not fetch them over the network, and
an unavailable sibling or unresolved reference is a validation error. The
schemas are closed:
adding a property, record kind, enum value, or status is a breaking change.
Changing path derivation, canonical bytes, required fields, field meaning, or
ordering likewise requires
`docs/normative/entity-context-snapshot/v3/` and a superseding ADR.

The schema committed with [ADR 0170](../../../ADRs/0170-align-entity-context-snapshot-with-normalized-entity.md)
defines v2. Subsequent additions,
even an optional property, require v3: older closed-schema consumers would
reject them, and `schema_version` has no minor-version representation. A
consumer that supports both v1 and v2 selects the schema bundle by
`schema_version` and never validates a document against the other bundle.

The [examples](examples/) are conformance fixtures. Each file is a JSON object
that maps every context-relative path of one tree, including `index.json`, to
a JSON string holding that file's exact emitted bytes. JSON documents therefore
appear as their RFC 8785 serialization, and record and order files as their
exact UTF-8 text. A conformance check parses each JSON document from its
string, validates it against its schema, and recomputes every manifest
`bytes` and `sha256` value, every record key, and `collection_profile` from
those bytes. Files named `valid-*` cover each supported Normalized entity v1
provider/kind pair, a truncated label set, a label that does not fit the
metadata string bound (`partial` `labels` scope), a label set emptied by an
unsafe label (`failed` `labels` scope), and the `not_loaded` and `unsupported` scope
states; files named `invalid-*` each violate one schema
rule, such as an inapplicable scope row, an omitted applicable scope row, or an
unsupported provider/kind pair.

### Changes from v1

v2 changes field names, field meanings, path derivation, and schema closure, so
it is a new major version:

| v1 | v2 | Reason |
|----|----|--------|
| `index.json` root `source` with `forge` | `entity.source` with `system`; Jira `project` and `project_id` | Normalized entity v1 names; Jira coverage |
| `entity.id` is the provider ID | `entity.id` is the canonical fullsend ID; `entity.source_id` is the provider ID | Normalized entity v1 identity |
| Record key from forge, host, repository ID, kind, and record ID | Record key from canonical entity `id`, kind, and record ID | Stable across repository and Jira project moves |
| `author_id` and `author` strings | `author` object on the entity; `actor` object on each record | Normalized entity v1 author and action actor |
| Record header `Author-ID` | `Actor-ID`, the current-revision actor | The admitted actor is the one the model sees |
| Record `updated_at` | Record `revised_at` | Normalized entity v1 action field |
| Bot actor `role: bot`, `role_verified: false` | Bot actor `role: none`, `bot_role`, and lookup-completion `role_verified` | Normalized entity v1 actor shape |
| Threads inside `relations/reviews.json` under the `reviews` scope | `relations/threads.json` and a `threads` scope | Discussion threads; independent coverage |
| Review `state` | `review_state` | Normalized entity v1 review action field |
| Flat thread location fields | `location` object | Normalized entity v1 thread location |
| `outdated: false` when a source has no equivalent | `resolved` and `outdated` may be `null` | Normalized entity v1 thread state |
| Absence of a gap implied completeness; non-selected sources looked empty | Required `scopes` status array; no `unsupported` gap code | Distinguish omitted from complete empty |
| Labels bound charged to the `entity` scope | `labels` scope | Normalized entity v1 `coverage.labels` |
| `human_min_role` is `triage` or `write` | `triage`, `write`, `maintain`, or `admin` | ADR 0151 |
| GitHub, GitLab, and Forgejo work items and change proposals | Adds GitHub Discussions and Jira issues | Normalized entity v1 coverage |

## Relationship to Normalized entity v1

[Normalized entity v1](../../normalized-entity/v1/README.md) is the CEL-facing
value that harness predicates evaluate; this snapshot is the model-facing tree
staged after routing. They have different authorization checkpoints and
neither substitutes for the other, but shared fields use the same names,
semantic types, and meanings:

| Normalized entity v1 field | Snapshot v2 location | Notes |
|----------------------------|----------------------|-------|
| `entity.kind` | `index.json` `entity.kind`; `entity/metadata.json` `kind` | Same values |
| `entity.source.system`, `.host`, `.repository`, `.repository_id`, `.project`, `.project_id` | `entity.source` in both documents | Same values; snapshot adds `forgejo` |
| `entity.id` | `entity.id` in both documents | Same canonical string; also the record-key input |
| `entity.source_id` | `entity.source_id` in both documents; entity record `Source-ID` | Same value |
| `entity.key`, `entity.number` | `entity/metadata.json` `key`, `number` | Same values |
| `entity.title`, `entity.state`, `entity.created_at`, `entity.updated_at`, `entity.labels` | `entity/metadata.json` fields of the same names | Same filtered values and mappings |
| `entity.author.id`, `.login` | `entity/metadata.json` `author.id`, `author.login` | Creator identity |
| `entity.author.kind`, `.role`, `.role_verified`, `.bot_role` | `state/actors.json` row with the same `id` | Mutable state staged separately |
| `entity.conversation.category` | `entity/metadata.json` `conversation.category` | Same names |
| `entity.body` | Entity record and its `index.json` record | Record `id` equals `source_id` |
| Action `id`, `created_at`, `revised_at`, `actor` | `index.json` record `id`, `created_at`, `revised_at`, `actor`; record headers | `actor` carries `id` and `login`; role state is in `state/actors.json` |
| Action `text` | Record body | Filtered and possibly bounded; see below |
| Action `review_state` | `relations/reviews.json` `review_state` | Same values |
| `entity.threads[]` `id`, `created_at`, `review_id`, `location` | `relations/threads.json` fields of the same names | Members are record keys, not action IDs |
| `entity.threads[]` `resolved`, `outdated`, `resolved_by` | `state/threads.json` `resolved`, `outdated`, `resolved_by_actor_id` | Same null semantics |
| `entity.checks[]` status, conclusion, and times | `checks/<record-key>/metadata.json` | Same vocabulary |
| `coverage.<collection>` status | `index.json` `scopes[].status` | Same vocabulary except `unknown` |

These shape differences are intentional:

- **Separate records and state.** Bodies are self-contained Markdown records
  and mutable values (logins, roles, resolution, labels, scope status) live in
  JSON documents, so unchanged records stay byte-identical for prompt-cache
  reuse. Normalized entity v1 nests them in one document.
- **Actor references.** `author` and `actor` objects carry only `id` and
  `login`; the rest of the shared actor shape is in `state/actors.json`. A
  failed human role lookup is `role: null` here and unknown there.
- **Omitted, not unknown.** The snapshot has no `$unknown` marker. A
  non-required property is omitted when its source value is unavailable, and a
  required singleton that cannot be established aborts assembly. Collection
  completeness is stated by `scopes` and `gaps`.
- **Bounded text.** A truncated record body is valid model context with
  `filter.status: truncated`; Normalized entity v1 binds such text as unknown.
- **Narrower subset.** Assignees, reactions, lifecycle details, source status,
  issue type, extensions, reviewers, current approvals, label applications,
  changed files, commits, check reports and annotations, and URLs are not
  staged in v2. A later snapshot version may add them through its own gate.
- **Work-item threads.** Normalized entity v1 gives a work item a complete
  empty `threads` list. The snapshot treats `threads` as inapplicable to work
  items, which carries the same information.

## Relationship to normalized-event v1

Snapshot `kind` uses normalized-event's `work_item`, `change_proposal`, and
`conversation` values. The snapshot's canonical `entity.id` is never compared
with normalized-event `entity.id`: for GitHub, GitLab, and Forgejo, the
snapshot `number` matches the numeric normalized-event `entity.id` used for
routing, and for Jira, the snapshot `source_id` matches it and `key` matches
the event's `entity.key` at the same source revision. Adapters must preserve
these explicit mappings rather than treating the `id` fields as
interchangeable.

Entity state is normalized to `open` or `closed` for work items and
conversations and to `open`, `closed`, or `merged` for change proposals.
Adapters use these exhaustive v2 mappings:

- GitHub issue `OPEN` and `CLOSED` map to `open` and `closed`. A pull request
  with `merged: true` maps to `merged`; otherwise `OPEN` and `CLOSED` map to
  `open` and `closed`.
- GitLab issue and merge-request `opened` and `closed` map to `open` and
  `closed`; merge-request `merged` maps to `merged`. Merge-request `locked`
  is GitLab's merge-in-progress lock and maps to `open`; the separate
  `discussion_locked` field is omitted from entity metadata.
- Forgejo issue and pull-request `open` and `closed` map to `open` and `closed`;
  a pull request with `merged: true` maps to `merged` regardless of its native
  closed state.
- A GitHub Discussion with `closed: false` maps to `open`; `closed: true` maps
  to `closed`.
- A Jira issue whose status category key is `done` maps to `closed`; `new` or
  `indeterminate` maps to `open`. The status name is not used.

Any other native state on an optional record is `invalid_metadata`; adapters
must not invent another mapping. Because entity metadata and body are required
singletons, an unrepresentable entity state aborts snapshot assembly instead
of emitting an entity gap.
