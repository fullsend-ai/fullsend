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
├── changes/
│   ├── diff.patch
│   └── commits.json
├── checks/<record-key>/
│   ├── metadata.json
│   └── log.txt
└── state/
    └── threads.json
```

`index.json` conforms to
[`index.schema.json`](index.schema.json) and enumerates every other staged file.
`entity/metadata.json`, `changes/commits.json`, check metadata, and
`state/threads.json` conform respectively to
[`entity.schema.json`](entity.schema.json),
[`commits.schema.json`](commits.schema.json),
[`check.schema.json`](check.schema.json), and
[`thread-state.schema.json`](thread-state.schema.json). `summary.md` is a
bounded navigation view generated only from the manifest and state documents;
it must not duplicate record bodies or logs. Files under `threads/` contain
only ordered relative paths to records.

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
resolution, outdated, or minimized state; those properties belong in
`index.json` or `state/threads.json`. Consequently, resolving a thread or
inserting an earlier record must not rename or rewrite an unchanged record.
Changing its body or attribution fields changes that record's bytes and digest.

The initial issue or change-proposal body uses the same layout with
`Fullsend-Record: "entity"`, the entity's stable ID and URL, and its author
attribution and creation time. This makes it the first self-contained turn.

## Filename order and prompt caching

An ordinary record filename is
`records/<order-key>-<record-key>.md`. `<order-key>` is its source `created_at`
normalized to UTC as `YYYYMMDDTHHMMSSnnnnnnnnnZ`, with exactly nine fractional
second digits and no punctuation other than `T` and `Z`. The entity-body record
uses the reserved key `00000000T000000000000000Z`, so it always sorts first.
Creation time is immutable forge data; edits do not rename a record.

Because all path components are restricted to these ASCII forms,
`LC_ALL=C cat records/*.md` concatenates the entire conversation in canonical
order without an intermediate file. Each `threads/<thread-key>.order` contains
the relative record path for each thread member followed by LF, in forge thread
order. From the context root, `xargs cat < threads/<thread-key>.order`
concatenates one thread; paths contain no whitespace or shell metacharacters.

A normal later reply adds one lexically later file and appends one path to its
thread order file, leaving all earlier record bytes and the whole-conversation
prefix unchanged for prompt-cache reuse. A backfilled earlier record, edit,
deletion, or attribution change necessarily invalidates the assembled context
from the first affected record onward. Consumers place mutable state after the
record concatenation and never infer resolution from record content.

Check status is observation state in `checks/<record-key>/metadata.json`; its
log file contains only filtered log bytes. A growing or replaced forge log is
changed content and may change `log.txt`. A new check attempt has a new forge
record ID and therefore a new record key.

## Canonical bytes

JSON is UTF-8 serialized with the JSON Canonicalization Scheme (RFC 8785), with
no byte-order mark or trailing newline. Arrays use the order defined below;
objects use RFC 8785 member ordering.

Text bodies, patches, and logs are UTF-8 after the v1 filter pipeline, use LF
line endings, have no byte-order mark, and end in exactly one LF. The pipeline
applies size bounds, Unicode safety normalization, secret/sensitive-data
redaction, and injection scanning in that order. `filter.status` is:

All attacker-controlled strings in JSON metadata pass through the same pipeline
before canonical serialization.

- `unchanged`: emitted bytes equal normalized source bytes;
- `modified`: one or more replacements or redactions were applied;
- `truncated`: a size bound removed source bytes, whether or not other filters also changed them;
- `rejected`: no content file is emitted because the source could not be represented safely.

Every emitted file has a manifest `sha256` over its emitted bytes. A rejected
source has a record but no content path or file entry.
Findings contain codes and counts, not rejected source text. Filters and bounds
are identified by `filter_version`; changing emitted bytes for the same input
requires a new filter version. Removing or reinterpreting a status requires v2.

## Ordering and determinism

Manifest record arrays and record filenames are sorted by source `created_at`,
then by the record key's ASCII byte order. The reserved entity-body order key
sorts before them. Thread arrays use thread creation time and then thread ID;
`comment_ids` and `.order` lines preserve forge thread order. Commit arrays
preserve forge history order. Other arrays state their ordering in their owning
schema before being added to v1.

`generated_at` or another runner-clock value is forbidden anywhere under the
context root. Acquisition timing belongs in run telemetry outside the staged
tree. Source-provided timestamps, entity update time, PR head SHA, and check
attempt IDs are permitted because they describe forge state. With identical
forge responses, size bounds, and `filter_version`, the complete tree has
identical paths and bytes.

## Lifecycle and access

The host tree is created outside both the repository and retained run-output
tree with directory mode `0700` and file mode `0600`. The sandbox copy is
read-only. Fullsend removes the sandbox copy after the runtime's last use and
the host copy after the post-script on every controlled exit, including skip,
failure, and cancellation. Fullsend also scavenges abandoned context trees on
startup after an unclean termination.

Artifact collectors must exclude entity-context trees. Retained diagnostics
may contain bounded record counts, content digests, filter codes/counts, and
cleanup errors, but never entity bodies, comment/review bodies, diffs, or logs.

## Compatibility

Consumers must reject an unsupported `schema_version` or `filter_version`; they
must ignore unknown object properties within v1. Adding an optional record kind
or property is compatible. Changing existing path derivation, canonical bytes,
required fields, field meaning, or ordering requires
`docs/normative/entity-context/v2/` and a superseding ADR.
