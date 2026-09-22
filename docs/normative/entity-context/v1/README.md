# Entity context v1

This specification defines the deterministic, filtered entity snapshot adopted
by [ADR 0107](../../../ADRs/0107-deterministic-filtered-entity-context-staging.md).
It is the contract between forge adapters, `fullsend run`, harness scripts, and
agent runtimes.

Here, *entity context* means the staged data snapshot for a handled Git-forge
entity. [ADR 0076](../../../ADRs/0076-slash-command-entity-context-separation.md)
uses the same phrase for the distinct routing category that determines which
agent or slash command may handle an issue, change proposal, or conversation.
Entity-context v1 applies only to GitHub, GitLab, and Forgejo work items and
change proposals. A non-forge `work_item`, such as Jira, does not start v1
snapshot assembly; the runner records an `unsupported_entity` diagnostic
outside the tree and continues under that integration's existing input
contract. Runtime entity-context forge reads remain denied.

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
├── relations/reviews.json
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
`relations/reviews.json`, `history/agent-runs.json`, check metadata,
`state/actors.json`, and `state/threads.json` conform respectively to
[`collection.schema.json`](collection.schema.json),
[`entity.schema.json`](entity.schema.json),
[`reviews.schema.json`](reviews.schema.json),
[`agent-runs.schema.json`](agent-runs.schema.json),
[`check.schema.json`](check.schema.json),
[`actor-state.schema.json`](actor-state.schema.json), and
[`thread-state.schema.json`](thread-state.schema.json). Files under `threads/`
contain only ordered relative paths to records. Files under `views/` are
deterministic projections containing those same paths, one per LF-terminated
line. `views/timeline.order` and `views/unresolved-review.order` exist even when
empty.

The canonical empty singleton documents are `{"reviews":[],"threads":[]}` for
`relations/reviews.json`, `{"runs":[]}` for `history/agent-runs.json`,
`{"actors":[]}` for `state/actors.json`, and `{"threads":[]}` for
`state/threads.json`, serialized under RFC 8785. An empty order view is a
zero-byte file.

## Stable records and mutable state

A record key is the lowercase hexadecimal SHA-256 of these four UTF-8 strings
joined by a single NUL byte, with no trailing NUL:

```text
<source.forge>://<source.host>, source.repository_id, record kind, forge record ID
```

`source.forge` is `github`, `gitlab`, or `forgejo`. `source.host` is the
lowercase DNS name, with a port from 1 through 65535 appended as decimal
`:<port>` if and only if the HTTPS port is not 443, and no scheme or path.
The default port is always omitted. `source.repository_id` and each forge
record ID use these positive mappings:

- GitHub uses the GraphQL node ID exposed as `node_id` for the repository and
  each entity, comment, review, check run, or review thread.
- GitLab uses the global project or record `id`, serialized in base-10 without
  leading zeroes; a discussion thread uses its opaque API `id`. Project-scoped
  issue or merge-request `iid` values are display numbers and are not IDs here.
- Forgejo uses the numeric repository or record `id`, serialized in base-10
  without leading zeroes. Issue and change-proposal `number` values are display
  numbers and are not IDs here.

Actor IDs follow the same rule: GitHub `node_id`, or the GitLab/Forgejo numeric
user `id` in base-10 without leading zeroes. References to a deleted actor
retain the immutable ID when the forge exposes it and otherwise use `null`.

`source.repository` is a navigation value, not a hash input: GitHub uses
lowercase `owner/repository`; GitLab uses the API's canonical
`path_with_namespace`; Forgejo uses lowercase API `full_name`. All forms use
literal `/` separators with no leading/trailing slash or percent encoding.
Repository transfers and renames therefore do not rewrite record keys.

Source kinds used in key derivation are `comment`, `review`, `check`, `thread`,
and `entity`. Manifest record entries use only `entity`, `comment`, `review`,
and `check`; `thread` keys identify relationship and ordering data under
`relations/` and `threads/`, not files under `records/`. A selected object that
lacks its required canonical ID produces an `invalid_metadata` gap and is not
assigned an adapter-specific substitute.

The tree contains forge entity state that is not reconstructible from the
target Git checkout. Diffs, commits, changed-file lists, branches, and revision
topology are repository context and are not staged here. Fullsend provides the
required Git objects and refs separately, and controllers may derive filtered
diff or history projections outside this tree. Git object IDs occur here only
as relationship values in review, thread, and agent-run documents.

Comment and review Markdown files are self-contained records with this exact
UTF-8 layout; header values are canonical JSON strings (or `null`) on one line:

```text
Fullsend-Record: "comment"
Source-ID: "opaque-forge-id"
Author-ID: "opaque-actor-id"
Created-At: "2026-09-08T10:15:30.000000000Z"

Filtered Markdown body.
```

`Fullsend-Record` is `"comment"` or `"review"`. The header order and blank
line are fixed. `Author-ID` may be `null` when the forge withholds or has
deleted the actor. The immutable actor ID is included because attribution is
part of the source record. The current login stays in `index.json` and
`state/actors.json`: it may change after an account rename without the record
changing. Navigation URLs are intentionally omitted from the staged tree;
forge, host, repository, and immutable record IDs provide provenance, while a
host-side adapter can construct a link when a human needs one. Effective
repository role is also excluded because permissions can change without the
comment changing.
The file contains no update time, ordering, thread membership, review location,
resolution, outdated, or minimized state; those properties belong in
`index.json`, `relations/`, or `state/`. Consequently, resolving a thread,
renaming an actor or repository, changing an actor's permission, or inserting
an earlier record must not rename or rewrite an unchanged record. Changing its
body or immutable attribution ID changes that record's bytes and digest.

The initial issue or change-proposal body uses the same layout with
`Fullsend-Record: "entity"`, the entity's stable ID, immutable author ID, and
creation time. This makes it the first self-contained turn.

## Ordering files and prompt assembly

An ordinary record filename is
`records/<order-key>-<record-key>.md`. `<order-key>` is its source `created_at`
normalized to UTC as `YYYYMMDDTHHMMSSnnnnnnnnnZ`, with exactly nine fractional
second digits and no punctuation other than `T` and `Z`. The entity-body record
uses the reserved key `00000000T000000000000000Z`, so it always sorts first.
Creation time is immutable forge data; edits do not rename a record.

Every order/view file lists only records that have an emitted `content_path`;
rejected comments and reviews never create dangling order lines. The entity
record is always present because an entity-body rejection aborts assembly.
`views/timeline.order` lists that entity record and retained comment/review
records in canonical chronology. Each `threads/<thread-key>.order` lists that
thread's retained records in forge order. `views/unresolved-review.order`
lists retained records in unresolved, non-outdated review threads, ordered by
thread creation time and key and then forge thread order. A
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
v1 conformance guarantee. Reading the same files through agent tools avoids
forge calls but still incurs tool-result tokens.

A normal later reply adds one lexically later file and extends applicable order
files, leaving earlier record blocks byte-identical. A backfilled earlier
record, edit, deletion, or attribution change invalidates reuse from the first
affected block onward. Consumers never infer resolution from record content.

Check status is observation state in `checks/<record-key>/metadata.json`; its
log file contains only filtered log bytes. A growing or replaced forge log is
changed content and may change `log.txt`. A new check attempt has a new forge
record ID and therefore a new record key; v1 has no separate synthesized
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
`cancelled`. This includes GitHub `startup_failure`, which maps to the v1
`failure` conclusion. `never_started: false` requires `started_at`; adapters
must not set it on an ordinary `failure` merely because `started_at` is absent.
Consumers must use the explicit flag and must not infer never-started state from
an omitted timestamp.
Check manifest records carry `created_at` but no `author_id` or `author`.

Adapters use these exhaustive v1 native-status mappings:

- GitHub `queued`, `waiting`, `requested`, and `pending` map to `queued`;
  `in_progress` maps to `in_progress`; `completed` maps to `completed` and its
  native conclusion maps identically to the v1 conclusion vocabulary, except
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

`relations/reviews.json` preserves normalized formal review outcomes and the
association between reviews, replies, reviewed revisions, and code locations
separately from mutable resolution state. Review states use `approved`,
`changes_requested`, `commented`, and `dismissed`, matching normalized-event
v1. Forge sides such as GitHub `LEFT` and `RIGHT` are lowercased to `left` and
`right`. Reviewed revisions and location fields are optional because forges
expose different subsets. `state/threads.json` references the actor that
resolved each resolved thread, and `relations/reviews.json` references the
actor that dismissed each dismissed review. A nullable reference explicitly
records that the forge did not expose the actor; a nullable dismissal timestamp
does the same when the forge exposes the disposition but not its time. Actor
references resolve through `state/actors.json`.

Review adapters use these exhaustive v1 mappings:

- GitHub `APPROVED`, `CHANGES_REQUESTED`, `COMMENTED`, and `DISMISSED` map to
  their lowercase v1 equivalents. `PENDING` reviews are unsubmitted and are
  omitted.
- GitLab approval summaries and current-approver lists do not have immutable
  review record IDs and are not formal v1 review records. GitLab discussion
  notes are staged as comments and threads. When immutable approval history is
  unavailable, the `reviews` scope records a partial `history_unavailable` gap.
- Forgejo `APPROVED`, `REQUEST_CHANGES`, `COMMENT`, and `DISMISSED` map to
  `approved`, `changes_requested`, `commented`, and `dismissed`, respectively.
  `PENDING` and `REQUEST_REVIEW` rows are unsubmitted reviewer requests and are
  omitted.

Any other native formal-review state is `invalid_metadata`. GitHub uses its
native outdated observation. For GitLab and Forgejo thread APIs that expose no
outdated equivalent, producers emit `outdated: false`; they never infer it from
the current diff.

The `threads` arrays in `relations/reviews.json` and `state/threads.json` have
exactly the same unique `thread_key` set. Every relationship thread therefore
has an explicit state row, including unresolved threads with `resolved: false`
and an explicit `outdated` boolean. Missing, duplicate, or extra thread-state
rows make the `reviews` scope unusable and add an `invalid_metadata` gap.

`state/actors.json` contains the current, forge-verified effective repository
permission for each referenced author, resolver, and dismisser. Roles use the
[ADR 0054](../../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md)
and normalized-event v1 vocabulary: `admin`, `maintain`, `write`,
`triage`, `read`, `none`, and `external`. `role_verified: false` is paired with
`role: null` and means consumers must fail closed rather than infer authority
from forge association labels such as GitHub `authorAssociation`. Each row
states whether the actor is a `human` or `bot`. A bot's role is that bot
identity's own effective permission on the target repository, never Fullsend's
installation permission. A permission lookup failure, including a collaborator
API 404, emits `role_verified: false` and `role: null`.

Entity-context and normalized-event v1 carry independently versioned copies of
the actor-role vocabulary. They match when this version is accepted, but a
non-breaking role addition to normalized-event v1 does not extend this closed
schema; a value must validate against the schema of every document that uses
it.

Every non-null `author_id`, `resolved_by_actor_id`, and
`dismissed_by_actor_id` referenced anywhere in the snapshot appears exactly
once in the actor array. JSON Schema cannot express uniqueness by one object
property, so producers enforce this cross-document invariant. Consumers treat
a duplicate or missing actor row exactly like `role_verified: false`. The actor
array sorts by actor ID. Because permissions and dispositions are mutable
observation state, changing them changes state or relationship documents but
not record files. Role values are observed during snapshot assembly, not
copied from a historical comment event.

`history/agent-runs.json`
contains only immutable, forge-observable Fullsend run receipts and relates an
agent result to its input revision, result records, and resulting commit. A
receipt's `input_revision` is the lowercase SHA-256 of the RFC 8785
`index.json` bytes consumed by that run; `input_head_sha`, when present, is the
separate Git revision checked out for the run. Receipt status is `success`,
`failure`, `cancelled`, `skipped`, or `timed_out`. A mutable sticky comment may
be a human-facing summary, but it is not canonical
run history and its overwritten versions cannot be reconstructed from a forge
that does not expose edit history.

`collection.json` is the exact collection configuration and conforms to
`collection.schema.json`. Its source-kind array sorts by ASCII byte order.
Non-entity records first enter the corresponding canonical order from
[Ordering and determinism](#ordering-and-determinism); `record_selection`
retains either the oldest or newest `max_records_per_source` entries for each
included source, then output ordering follows the same chronology rules.
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
`max_metadata_string_bytes` bounds each source-derived display string. Labels
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

Missing selected data is represented in `index.json.gaps`; it is never silently
treated as an empty history. Scopes are `entity`, `comments`, `reviews`,
`checks`, `check_logs`, `agent_runs`, and `actors`. `profile_bound` and
`history_unavailable` gaps have `usability: partial`; consumers may use present
records but must not claim the scope is complete. `unsupported`,
`authorization_failed`, `fetch_failed`, `source_too_large`,
`invalid_metadata`, and `unsafe_content` have `usability: unusable`; consumers
must not make an authority or completeness decision from that scope. Any
unusable `entity`-scope gap for the required entity body or metadata aborts
snapshot assembly, including `authorization_failed`, `fetch_failed`,
`source_too_large`, `invalid_metadata`, and `unsafe_content`; Fullsend must not
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

Text bodies and logs are UTF-8 after the v1 filter pipeline, use LF line
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

The v1 redaction floor detects exact run credentials registered by Fullsend;
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
detector reliably blocks every visible prompt injection. Runtimes therefore
mark staged records as untrusted data in distinct content blocks, prohibit
following instructions from those blocks, and retain least-privilege tool and
credential boundaries as the primary defense.

`filter.status` is:

- `unchanged`: emitted bytes equal normalized source bytes;
- `modified`: one or more replacements or redactions were applied;
- `truncated`: a size bound removed source bytes, whether or not earlier filters
  also changed them; findings still report every replacement and redaction;
- `rejected`: no content file is emitted because the source could not be
  represented safely.

Source-derived display strings in JSON metadata pass through Unicode safety,
redaction, and complete-source injection scanning, then are bounded at a UTF-8
code-point boundary with no trailing LF added. Bound display strings first; drop
empty optional values, re-unique labels, then sort labels and apply
`max_labels`. A required singleton title that becomes empty or unsafe aborts
assembly like a rejected entity body. Required nullable entity.author,
record.author, and actor.login values become JSON `null` when unsafe or empty.
An unsafe or empty check name or agent name rejects only its affected optional
record with an `unsafe_content`/`invalid_metadata` gap; it never aborts an
otherwise valid entity snapshot. Unsafe labels are dropped and findings are
retained. A truncated display string is scanned again exactly as emitted
before canonical serialization. This includes entity titles and labels; entity
and manifest-record author logins; actor logins; check names; and agent names.
Redaction reduces exposure but does not promise anonymization;
provider retention and prompt caching of remaining content are governed by the
configured provider trust boundary, not by this cleanup guarantee.

Structural strings are not rewritten because doing so could change identity or
target a different resource. Canonical IDs use the mappings above. Repository
paths and review file paths first undergo the Unicode-safety scan and then strict
structural validation. v1 deliberately restricts `repository_path` to the
printable-ASCII allow-list encoded by `common.schema.json` (including its
explicit punctuation set and excluding non-ASCII letters and apostrophes); this
is an explicit compatibility and security boundary, not an implicit Unicode
normalization. Non-ASCII or other out-of-alphabet values are structurally
invalid. Repository file paths are relative,
slash-separated, contain no `.` or `..` segment, percent sign or percent-encoded
dot, slash, or backslash, control character, or non-rendering class named above.
These values are untrusted forge data and must never be interpolated into shell
commands; generated order-file paths have a separate shell-safe contract.
Source URLs
are intentionally not part of the agent-visible v1 tree; forge, host,
repository, and immutable record IDs provide provenance without copying
navigation URLs into staged metadata. An unsafe structural value, whether its
property is required or optional, produces an unusable `invalid_metadata` or
`unsafe_content` gap. For optional records and their related files, that gap
omits the selected source as described above. Required singleton structural
values cannot be omitted: an unsafe, missing, or schema-invalid entity `id`, or
index `source` field
(`host`, `repository_id`, or `repository`), aborts snapshot assembly rather
than emitting an invalid singleton or dangling manifest. Structural values are
never emitted after filtering or truncation.

`index.json.filter` records filtering of source-derived values in the index.
Manifest file entries require `filter` for entity, review, agent-run, check
metadata, check logs, and actor JSON documents. A `check_log` entry's filter
describes the emitted log bytes only; the parent check record's filter remains
the separate summary of filtered check metadata. Each manifest value summarizes
the filtered bytes for its own document or file.
Generated collection, state-without-display-text, and order documents do not
invent filter findings.

Every emitted file other than the root `index.json` has a manifest `sha256`
over its emitted bytes. The assembler retains the exact root `index.json` bytes,
or a digest of them, in host-private run state outside the context tree. After
the host pre-script, Fullsend compares the on-disk root index with that retained
assembly value and aborts on any difference; it never treats a rewritten
on-disk index as the root of trust. Only the retained assembly index is used to
verify child digests and the exact manifest path set. A rejected non-entity
source has a record but no content path or file entry and adds an
`unsafe_content` gap for its scope. A rejected or missing entity-body record
aborts snapshot assembly, and Fullsend must not launch scripts or an agent with
that snapshot.

Findings contain codes and counts, not rejected source text. Filters and bounds
are identified by `filter_version`; changing emitted bytes for the same input
requires a new filter version. Removing or reinterpreting a status requires v2.

## Ordering and determinism

Manifest record arrays and record filenames are sorted by source `created_at`,
then by the record key's ASCII byte order. The reserved entity-body order key
sorts before them. Formal review arrays sort by `submitted_at` and then review
ID. Relationship thread arrays sort by `created_at` and then thread ID; each
thread's `record_keys`, `replies`, and `.order` lines preserve forge thread
order. Mutable thread-state arrays sort by the thread key's ASCII byte order.
Actor-state arrays sort by actor ID's UTF-8 byte order. Agent receipts sort by
completion time and ID; each receipt's `result_record_keys` sort by ASCII byte
order. Entity labels sort by their filtered UTF-8 byte order. Manifest files
sort by path, gaps by scope and code, and filter findings by code. Other arrays
state their ordering in their owning schema before being added to v1.

`generated_at` or another runner-clock value is forbidden anywhere under the
context root. Acquisition timing belongs in run telemetry outside the staged
tree. Source-provided timestamps, entity update time, Git object IDs used as
relationship values, and forge check record IDs are permitted. With identical
forge responses, `collection_profile`, size bounds, and `filter_version`, the
complete tree has identical paths and bytes.

## Lifecycle and access

The host tree is created outside both the repository and retained run-output
tree with directory mode `0700` and file mode `0600`. The sandbox copy is
agent-immutable: Fullsend exposes it through a read-only bind mount, or, when
that is unavailable, uses a root-owned tree with mode `0555` directories and
`0444` files while the agent runs unprivileged with `CAP_FOWNER` and equivalent
write-overriding capabilities removed. A plain mode-`0600` copy owned by the
agent does not satisfy this contract; if neither mechanism can be enforced,
Fullsend aborts before launching the agent. Fullsend removes the sandbox copy
after the runtime's last use and the host copy after the post-script on every
controlled exit, including skip, failure, and cancellation. Fullsend also
scavenges abandoned context trees on startup after an unclean termination.

Artifact collectors must exclude entity-context trees. Retained diagnostics
may contain bounded record counts, content digests, filter codes/counts, and
cleanup errors, but never entity bodies, comment/review bodies, or logs.

## Compatibility

Consumers must reject an unsupported `schema_version` or `filter_version`; they
must validate `index.json`, `collection.json`, `entity/metadata.json`,
`relations/reviews.json`, `history/agent-runs.json`, check metadata documents,
`state/actors.json`, and `state/threads.json` against their v1 schemas. Other
emitted files follow the byte contracts above. The files under
`docs/normative/entity-context/v1/` form one local schema bundle: consumers
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
`docs/normative/entity-context/v2/` and a superseding ADR.

The schema committed with the accepting ADR defines v1. Subsequent additions,
even an optional property, require v2: older closed-schema consumers would
reject them, and `schema_version` has no minor-version representation.

## Relationship to normalized-event v1

Entity context stages only work items (issues) and change proposals (PRs or
MRs); normalized-event's `conversation` entity kind from ADR 0086 is outside
this version's scope. Entity-context `kind` uses normalized-event's `work_item`
and `change_proposal` values. Entity-context `id` is the forge's immutable
opaque object ID because it participates in stable path derivation, while
`number` is the positive display number projected into normalized-event
`entity.id` for routing. Adapters must preserve that explicit mapping rather
than treating the two `id` fields as interchangeable.

Entity state is normalized to `open` or `closed` for work items and to `open`,
`closed`, or `merged` for change proposals. Adapters use these exhaustive v1
mappings:

- GitHub issue `OPEN` and `CLOSED` map to `open` and `closed`. A pull request
  with `merged: true` maps to `merged`; otherwise `OPEN` and `CLOSED` map to
  `open` and `closed`.
- GitLab issue and merge-request `opened` and `closed` map to `open` and
  `closed`; merge-request `merged` maps to `merged`. Merge-request `locked`
  is GitLab's merge-in-progress lock and maps to `open`; the separate
  `discussion_locked` field is omitted from entity metadata v1.
- Forgejo issue and pull-request `open` and `closed` map to `open` and `closed`;
  a pull request with `merged: true` maps to `merged` regardless of its native
  closed state.

Any other native state on an optional record is `invalid_metadata`; adapters
must not invent another mapping. Because entity metadata and body are required
singletons, an unrepresentable entity state aborts snapshot assembly instead
of emitting an entity gap.
