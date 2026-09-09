# Entity context v1

This specification defines the deterministic, filtered entity snapshot adopted
by [ADR 0107](../../../ADRs/0107-deterministic-filtered-entity-context-staging.md).
It is the contract between forge adapters, `fullsend run`, harness scripts, and
agent runtimes.

## Tree

Entries that do not apply to an entity are omitted. JSON documents use their
linked schemas.

```text
context/
├── index.json
├── summary.md
├── entity/metadata.json
├── records/<order-key>-<record-key>.md
├── threads/<thread-key>.order
├── views/
│   ├── conversation.order
│   ├── unresolved-review.order
│   └── reviews/<record-key>.order
├── relations/reviews.json
├── history/agent-runs.json
├── checks/<record-key>/
│   ├── metadata.json
│   └── log.txt
└── state/
    └── threads.json
```

`index.json` conforms to
[`index.schema.json`](index.schema.json) and enumerates every other staged file.
`entity/metadata.json`, `relations/reviews.json`,
`history/agent-runs.json`, check metadata, and
`state/threads.json` conform respectively to
[`entity.schema.json`](entity.schema.json),
[`reviews.schema.json`](reviews.schema.json),
[`agent-runs.schema.json`](agent-runs.schema.json),
[`check.schema.json`](check.schema.json), and
[`thread-state.schema.json`](thread-state.schema.json). `summary.md` is a
bounded navigation view generated only from the manifest and state documents;
it must not duplicate record bodies or logs. Files under `threads/` contain
only ordered relative paths to records. Files under `views/` are deterministic
projections containing those same paths, one per LF-terminated line.

## Stable records and mutable state

A record key is the lowercase hexadecimal SHA-256 of these UTF-8 strings joined
by a single NUL byte, with no trailing NUL:

```text
forge identifier, canonical repository identifier, record kind, forge record ID
```

The forge record ID is the platform's immutable opaque ID, not a mutable URL,
ordinal, database row position, or display number. Record kinds are `comment`,
`review`, `check`, `thread`, and `entity`. This derivation makes paths safe and
stable without requiring consumers to parse forge-specific IDs.

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
Source-URL: "https://forge.example/..."
Author-ID: "opaque-actor-id"
Author: "forge-login"
Created-At: "2026-09-08T10:15:30Z"

Filtered Markdown body.
```

`Fullsend-Record` is `"comment"` or `"review"`. The header order and blank
line are fixed. `Author-ID` and `Author` may be `null` when the forge withholds
or has deleted the actor. All header strings are filtered before JSON-string
serialization. The file contains no update time, ordering, thread membership,
review location, resolution, outdated, or minimized state; those properties
belong in `index.json`, `relations/`, or `state/`. Consequently, resolving a thread or
inserting an earlier record must not rename or rewrite an unchanged record.
Changing its body or attribution fields changes that record's bytes and digest.

The initial issue or change-proposal body uses the same layout with
`Fullsend-Record: "entity"`, the entity's stable ID and URL, and its author
attribution and creation time. This makes it the first self-contained turn.

## Ordering files and prompt assembly

An ordinary record filename is
`records/<order-key>-<record-key>.md`. `<order-key>` is its source `created_at`
normalized to UTC as `YYYYMMDDTHHMMSSnnnnnnnnnZ`, with exactly nine fractional
second digits and no punctuation other than `T` and `Z`. The entity-body record
uses the reserved key `00000000T000000000000000Z`, so it always sorts first.
Creation time is immutable forge data; edits do not rename a record.

`views/conversation.order` lists the entity record and all comment and review
records in canonical chronology. Each `threads/<thread-key>.order` lists that
thread's records in forge order. `views/unresolved-review.order` lists records
in unresolved, non-outdated review threads, ordered by thread creation time and
key and then forge thread order. Each `views/reviews/<record-key>.order` lists
one formal review followed by records in threads associated with it. A record
path appears at most once in any one order file. Paths contain no whitespace or
shell metacharacters, so a host consumer may materialize a projection with
`xargs cat`, but runtimes use the segmented contract below.

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
record ID and therefore a new record key.

## Relationships, history, and collection profiles

`relations/reviews.json` preserves formal review outcomes and the association
between reviews, replies, reviewed revisions, and code locations separately
from mutable resolution state. Location fields are optional because forges
expose different subsets. `history/agent-runs.json` contains only immutable,
forge-observable Fullsend run receipts and relates an agent result to its input
revision, result records, and resulting commit. A mutable sticky comment may be
a human-facing summary, but it is not canonical run history and its overwritten
versions cannot be reconstructed from a forge that does not expose edit
history.

The required `collection_profile` in `index.json` is the lowercase SHA-256 of
the canonically serialized collection configuration: included source kinds,
selection rules, and bounds. For example, a profile may include failed-check
logs without fetching all successful logs. Given the same forge responses,
profile, bounds, and filter version, the tree is identical. Profiles must not
vary collection based on runner time or an agent's intermediate choices.
Missing data within the selected profile is represented as a bounded manifest
gap with a stable code; history unavailable from the forge, including
overwritten edits or unreachable force-pushed commits, is not silently treated
as an empty history.

## Canonical bytes

JSON is UTF-8 serialized with the JSON Canonicalization Scheme (RFC 8785), with
no byte-order mark or trailing newline. Arrays use the order defined below;
objects use RFC 8785 member ordering.

Text bodies and logs are UTF-8 after the v1 filter pipeline, use LF
line endings, have no byte-order mark, and end in exactly one LF. The pipeline
applies size bounds, Unicode safety normalization, secret/sensitive-data
redaction, and injection scanning in that order. `filter.status` is:

- `unchanged`: emitted bytes equal normalized source bytes;
- `modified`: one or more replacements or redactions were applied;
- `truncated`: a size bound removed source bytes, whether or not other filters also changed them;
- `rejected`: no content file is emitted because the source could not be represented safely.

All attacker-controlled strings in JSON metadata pass through the same pipeline
before canonical serialization.

Every emitted file has a manifest `sha256` over its emitted bytes. A rejected
source has a record but no content path or file entry.
Findings contain codes and counts, not rejected source text. Filters and bounds
are identified by `filter_version`; changing emitted bytes for the same input
requires a new filter version. Removing or reinterpreting a status requires v2.

## Ordering and determinism

Manifest record arrays and record filenames are sorted by source `created_at`,
then by the record key's ASCII byte order. The reserved entity-body order key
sorts before them. Thread arrays use thread creation time and then thread ID;
`record_keys` and thread `.order` lines preserve forge thread order. Agent
receipts sort by completion time and ID; manifest files sort by path, gaps by
scope and code, and filter findings by code. Other arrays state their ordering
in their owning schema before being added to v1.

`generated_at` or another runner-clock value is forbidden anywhere under the
context root. Acquisition timing belongs in run telemetry outside the staged
tree. Source-provided timestamps, entity update time, Git object IDs used as
relationship values, and check attempt IDs are permitted. With identical
forge responses, `collection_profile`, size bounds, and `filter_version`, the
complete tree has identical paths and bytes.

## Lifecycle and access

The host tree is created outside both the repository and retained run-output
tree with directory mode `0700` and file mode `0600`. The sandbox copy is
read-only. Fullsend removes the sandbox copy after the runtime's last use and
the host copy after the post-script on every controlled exit, including skip,
failure, and cancellation. Fullsend also scavenges abandoned context trees on
startup after an unclean termination.

Artifact collectors must exclude entity-context trees. Retained diagnostics
may contain bounded record counts, content digests, filter codes/counts, and
cleanup errors, but never entity bodies, comment/review bodies, or logs.

## Compatibility

Consumers must reject an unsupported `schema_version` or `filter_version`; they
must validate every document against the v1 schemas. The schemas are closed:
adding a property, record kind, enum value, or status is a breaking change.
Changing path derivation, canonical bytes, required fields, field meaning, or
ordering likewise requires
`docs/normative/entity-context/v2/` and a superseding ADR.
