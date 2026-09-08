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
├── entity/
│   ├── metadata.json
│   └── body.md
├── comments/<record-key>.md
├── reviews/<record-key>.md
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
it must not duplicate record bodies or logs.

## Stable records and mutable state

A record key is the lowercase hexadecimal SHA-256 of these UTF-8 strings joined
by a single NUL byte, with no trailing NUL:

```text
forge identifier, canonical repository identifier, record kind, forge record ID
```

The forge record ID is the platform's immutable opaque ID, not a mutable URL,
ordinal, database row position, or display number. Record kinds are `comment`,
`review`, and `check`. This derivation makes paths safe and stable without
requiring consumers to parse forge-specific IDs.

Comment and review Markdown files contain only the filtered body. Their
manifest records map forge IDs and metadata to body paths, while manifest file
entries carry byte counts, digests, media types, and filter results. Bodies do not
contain author, timestamps, ordering, thread membership, resolution, outdated,
or minimized state. Those properties belong in `index.json` or
`state/threads.json`. Consequently, resolving a thread or inserting an earlier
record can change the index, state, and generated summary but must not rename or
rewrite an unchanged content file. An edited body changes only that record's
body bytes, digest, filtering result, and source-provided update metadata.

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

Manifest record arrays and thread arrays are sorted by source `created_at`, then
by the forge record ID's UTF-8 byte order. Thread `comment_ids` preserve forge
thread order. Commit arrays preserve forge history order. Other arrays state
their ordering in their owning schema before being added to v1.

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
