# Steer envelope v1

This specification defines what a steer makes visible inside a running
sandbox: the steer files and the message the agent receives. It is the
contract between `fullsend run`, the agent runtimes, and the agent definitions
in [fullsend-ai/agents](https://github.com/fullsend-ai/agents).

- [ADR 0117](../../../ADRs/0117-steer-a-running-cycle-with-admitted-entity-updates.md)
  decides when a steer happens and which records it may carry.
- ADR 0118 (proposed) decides how a runtime delivers the message.
- [ADR 0141](../../../ADRs/0141-deterministic-filtered-entity-context-staging.md)
  requires a versioned schema before extension output becomes
  sandbox-visible. This document is that schema for steer output. Its
  manifest stands in for the snapshot manifest under the scoped partial
  supersession in ADR 0117, with the same filtering, digest, permissions,
  exact-path, and cleanup rules.

This document is normative. The key words MUST, MUST NOT, and MAY are used as
in RFC 2119.

## Overview

Each steer produces two things:

| Part | What it is | Who reads it |
|---|---|---|
| Steer directory | One manifest, one filtered file per admitted record, and any review relation files, in an agent-immutable tree | The agent, through file reads |
| Envelope | A short message that lists the steer's records by key and path, with the new head | The agent, as a new user turn |

Record bodies appear only in the steer directory. The envelope carries only
manifest fields, including the filtered actor login, so a record body cannot
forge envelope structure.

## Steer directory

### Location

Fullsend reserves one sandbox path per cycle, a sibling of the snapshot path:

```text
/sandbox/entity-context-steer/
├── 0001/
│   ├── manifest.json
│   ├── records/<order-key>-<record-key>.md
│   ├── relations/reviews.json   (only when a steered record is part of a review)
│   ├── state/threads.json       (same)
│   ├── state/actors.json        (same)
│   └── threads/<thread-key>.keys  (same)
└── 0002/
    └── ...
```

- `FULLSEND_STEER_DIR` points to `/sandbox/entity-context-steer` inside the
  sandbox.
- Each steer writes a new directory named by its one-based sequence number
  within the cycle, zero-padded to four digits so names sort in steer order.
  Four digits bound a cycle to 9999 steers, so the configured steer cap MUST
  NOT exceed 9999. Earlier steer directories stay until
  the cycle ends.
- Fullsend MUST reject a checkout or `host_files` destination that overlaps
  this path, and MUST decline steering if the path exists with unexpected
  contents at sandbox start.

### Record files

Each admitted record is one file. Its name and bytes follow the
[entity-context-snapshot v1](../../entity-context-snapshot/v1/README.md)
record rules exactly: the `records/<order-key>-<record-key>.md` name, the
fixed `Fullsend-Record`, `Source-ID`, `Author-ID`, and `Created-At` headers,
and the filtered body. A record key derived for a steer is the same key the
snapshot derives for that forge record.

- A changed version of a record that is already in the snapshot keeps its
  record key. The steer file holds the current filtered revision; the snapshot
  copy is not modified.
- A record whose filter status is `rejected`, or whose admission failed, has
  no file. It appears only in the manifest's `omitted` list.
- If a newer revision of a record the agent already holds, in the snapshot or
  an earlier steer, fails admission or filtering, Fullsend MUST decline the
  whole steer. The follow-up cycle handles it.
- Admission uses the same threshold as the cycle snapshot's records (ADR
  0117), so one cycle never admits a record at two thresholds. For steer
  envelope v1 that is entity-context-snapshot v1's threshold (see
  [Actor](#actor)).

### Review relation files

A review record or a review comment means little without its review outcome,
reviewed revision, and thread location, which the snapshot keeps outside the
record files. When a steer carries a record that belongs to a formal review
or a review thread, the steer directory also holds `relations/reviews.json`
and `state/threads.json`.

- Each file uses the entity-context-snapshot v1 schema of the same name and
  holds only the reviews and threads that the steer's admitted records belong
  to, in their current state.
- The steer directory also holds a `threads/<thread-key>.keys` file for
  each thread, and `state/actors.json` with a row for every actor the
  relations name, such as a thread's resolver or a review's dismisser, built
  by the snapshot's actor-state rules.
- **Order files name record keys, not paths.** Unlike a snapshot v1 `.order`
  file, each line of a steer's `.keys` file is a record key. A key resolves to the newest
  revision of that record across the chain: the latest steer that carries it,
  else the snapshot. A new reply whose parent exists only in the snapshot
  therefore lists the parent's key, which resolves to the snapshot's file.
- Every other relation that names a record resolves it by record key the same
  way.
- **Validation.** `relations/reviews.json`, `state/threads.json`, and
  `state/actors.json` validate against the entity-context-snapshot v1 JSON
  schemas of the same names, with one exception: a thread's `order_path`
  names its `threads/<thread-key>.keys` file instead of a `.order` file.
  Cross-file checks resolve record keys across the
  chain as above, not within the steer directory alone, so a subset of
  reviews and threads is valid.
- Each of these files is listed in the manifest's `relations` array.
- A steer with no such record has neither file and an empty `relations`
  array.

### Manifest

`manifest.json` is UTF-8 JSON serialized with RFC 8785, with no byte-order
mark and no trailing newline. Timestamps and Git object IDs use the
entity-context-snapshot v1 canonical forms.

| Field | Type | Meaning |
|---|---|---|
| `schema` | string | Always `"steer-envelope"` |
| `schema_version` | integer | Always `1`, the snapshot's integer convention |
| `snapshot_schema_version` | integer | The cycle snapshot's `index.json` `schema_version`; always `1` (see [Versioning](#versioning)) |
| `filter_version` | string | The pipeline version; MUST equal the cycle snapshot's `filter_version` |
| `sequence` | integer | This steer's sequence number, from 1 |
| `snapshot_input_revision` | string | SHA-256 of the cycle snapshot's `index.json` bytes |
| `previous_manifest_sha256` | string or null | SHA-256 of the previous steer's manifest bytes; `null` for the first |
| `entity` | object | The bound entity; see [Entity](#entity) |
| `head` | object or null | For a change proposal whose head moved and was provisioned: `sha`, `previous_sha`, and `base_sha` (the change proposal's current base); otherwise `null` |
| `records` | array | One entry per admitted record, sorted by `created_at` then record key |
| `relations` | array | One entry per review relation file, thread order file, and actor-state file, sorted by `path`; each has `path` and `sha256` (see [Review relation files](#review-relation-files)) |
| `omitted` | array | One entry per scope and code, sorted by scope then code |

A manifest MUST hold at least one `records` entry or a non-null `head`.
Fullsend MUST NOT publish a steer that carries only `omitted` counts.

Each `records[]` entry:

| Field | Type | Meaning |
|---|---|---|
| `record_key` | string | The snapshot record key |
| `kind` | string | `entity`, `comment`, or `review` |
| `path` | string | Path relative to the steer directory, for example `records/<order-key>-<record-key>.md` |
| `sha256` | string | SHA-256 of the file bytes |
| `created_at` | string | Source creation time |
| `revision` | string | The source's current-revision time (its edit time, or `created_at` when never edited) |
| `replaces_snapshot` | boolean | `true` when the snapshot holds an earlier revision of the same record |
| `actor` | object | The record's current-revision actor; see [Actor](#actor) |
| `filter` | object | `status` (`unchanged`, `modified`, or `truncated`) and `findings` (codes and counts), as in the snapshot |

#### Omitted

Each `omitted[]` entry:

| Field | Type | Meaning |
|---|---|---|
| `scope` | string | `entity`, `comments`, or `reviews` |
| `code` | string | A snapshot v1 gap code: `authorization_failed`, `unsafe_content`, `fetch_failed`, `source_too_large`, `invalid_metadata`, `profile_bound`, `history_unavailable`, or `unsupported` |
| `count` | integer | At least 1 |

The snapshot's collection-profile bounds also apply to steers; a record
dropped by a profile bound is counted as `profile_bound`. Omitted entries hold
no record text, keys, or logins. Unlike snapshot gaps they carry no usability
class: a steer is all-or-nothing, so any omission that would make the update
unusable declines the whole steer instead.

#### Entity

| Field | Type | Meaning |
|---|---|---|
| `source` | object | Exactly the snapshot `index.json` `source` object: `forge`, `host`, `repository_id`, `repository` |
| `kind` | string | `work_item` or `change_proposal`, the snapshot v1 entity kinds |
| `id` | string | The immutable forge object ID, as in `index.json` `entity.id` |
| `record_key` | string | The snapshot record key of the entity-body record (64 lowercase hex) |

#### Actor

The actor object follows a snapshot v1 `state/actors.json` row, plus
`bot_role`:

| Field | Type | Meaning |
|---|---|---|
| `id` | string | Immutable actor ID. A record whose actor the forge no longer exposes cannot be attributed, so it is omitted, never admitted |
| `kind` | string | `human` or `bot` |
| `login` | string or null | Current login; `null` when unsafe or empty after filtering |
| `role` | string | Human: the verified effective role, one of the Authorization role values `admin`, `maintain`, `write`, `triage`. Bot: `bot` |
| `role_verified` | boolean | `true` for a human, `false` for a bot, as in snapshot actor state |
| `bot_role` | string | Present only for a bot: its registered ADR 0107 canonical role |

**Minimum role.** Admission and the release check use the same rule: a human
record passes only when its role is verified fresh and meets
entity-context-snapshot v1's threshold for the cycle's agent category
(`triage` or above for observation agents, `write` or above for mutation
agents), and a bot record passes only with a registered `bot_role`. A record
that fails at admission has no actor object and is counted in `omitted`. A
record that fails at release declines the whole steer (see Release check).

#### Example

A complete manifest for the second steer of a cycle, shown indented for
reading; the real bytes are RFC 8785 with no whitespace. Digests other than
the two record keys are illustrative.

```json
{
  "entity": {
    "id": "PR_kwDOExample",
    "kind": "change_proposal",
    "record_key":
      "4d2e2874971944bca1f0c97f533bafe5c78a1a09ee69851c94965398b795aa9a",
    "source": {
      "forge": "github",
      "host": "github.com",
      "repository": "example-org/example-repo",
      "repository_id": "R_kgDOExample"
    }
  },
  "filter_version": "1",
  "head": {
    "base_sha": "0a1b2c3d4e5f60718293a4b5c6d7e8f901234567",
    "previous_sha": "9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d",
    "sha": "4f1c2e9d0b7a65c3e8f1a2b4c6d8e0f1a3b5c7d9"
  },
  "omitted": [
    { "code": "authorization_failed", "count": 1, "scope": "comments" }
  ],
  "previous_manifest_sha256": "891809c735ff5e4e047caeba0599a3213ebf6cdb7f519b68237a5ca09a5b1812",
  "records": [
    {
      "actor": {
        "id": "U_kgDOExample",
        "kind": "human",
        "login": "example-maintainer",
        "role": "write",
        "role_verified": true
      },
      "created_at": "2026-11-02T10:15:30.000000000Z",
      "filter": { "findings": [], "status": "unchanged" },
      "kind": "comment",
      "path": "records/20261102T101530000000000Z-c73645a72a9c5de28d71e3e1efe235b9a5d02ee6dfe7dc0ef06cf3d0e5ee3421.md",
      "record_key":
        "c73645a72a9c5de28d71e3e1efe235b9a5d02ee6dfe7dc0ef06cf3d0e5ee3421",
      "replaces_snapshot": false,
      "revision": "2026-11-02T10:15:30.000000000Z",
      "sha256": "c147efcfc2d7ea666a9e4f5187b115c90903f0fc896a56df9a6ef5d8f3fc9f31"
    }
  ],
  "relations": [],
  "schema": "steer-envelope",
  "schema_version": 1,
  "sequence": 2,
  "snapshot_input_revision": "1bc04b5291c26a46d918139138b992d2de976d6851d0893b0476b85bfbdfc6e6",
  "snapshot_schema_version": 1
}
```

The manifest's own path set is exact: every file under the steer directory is
`manifest.json` or listed in `records[].path` or `relations[].path`.

### Write, permissions, and cleanup

| Rule | Requirement |
|---|---|
| Assembly | The host builds the directory in host-private state, then verifies every `sha256` and the exact path set before upload |
| Release check | The fresh authorization check for every record, and for the actor of a non-null `head`, runs after assembly and immediately before upload, under the sandbox write lock. Any failure MUST decline the whole steer ([ADR 0117](../../../ADRs/0117-steer-a-running-cycle-with-admitted-entity-updates.md)): nothing is uploaded, and the updates wait for the follow-up cycle |
| Upload | Fullsend MUST reject symlinks and special files at every path component, matching the entity-context-snapshot v1 rule |
| Head objects | When `head` is not `null`, Fullsend MUST install the head before delivering the envelope, as ADR 0117's head provisioning describes, and MUST decline the steer if the install fails. The head is never staged as a file under `/sandbox/entity-context-steer` |
| Permissions | Agent-immutable, with the snapshot's modes: a read-only bind mount, or root-owned `0555` directories and `0444` files with the agent unprivileged and without `CAP_FOWNER`. If neither can be enforced, Fullsend MUST NOT steer |
| Host copy | The snapshot's host modes, `0700` directories and `0600` files (owner-only), outside the clone and the retained run-output tree |
| Cleanup | The sandbox copy is removed with the cycle's snapshot copy; the host copy is removed after the post-script, on every controlled exit. Startup scavenges abandoned copies |
| Artifacts | Steer directories are excluded from retained artifacts. Diagnostics MAY keep counts, digests, and filter codes, never bodies |

## Envelope

The envelope is the message appended to the running session. It is built only
from manifest fields, never from record bodies.

```text
Runner update: your task inputs changed after this run started.

Steer: 0002
Manifest: /sandbox/entity-context-steer/0002/manifest.json
Head: 4f1c2e9d0b7a65c3e8f1a2b4c6d8e0f1a3b5c7d9 (was 9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d)
Omitted: 1

[steer-record 1/2]
Record: c73645a72a9c5de28d71e3e1efe235b9a5d02ee6dfe7dc0ef06cf3d0e5ee3421
Kind: comment
Actor: @example-maintainer (write)
Revision: 2026-11-02T10:15:30.000000000Z
Filter: unchanged
Path: /sandbox/entity-context-steer/0002/records/20261102T101530000000000Z-c73645a72a9c5de28d71e3e1efe235b9a5d02ee6dfe7dc0ef06cf3d0e5ee3421.md
[/steer-record]

[steer-record 2/2]
...
[/steer-record]
```

The values above are illustrative; the record key is the worked example from
entity-context-snapshot v1.

### Envelope rules

- **Opening line.** Every envelope begins with
  `Runner update: your task inputs changed after this run started.`, byte for
  byte, followed by one blank line.
- **Header lines.** `Steer:` and `Manifest:` are always present. `Head:` is
  present only when the manifest `head` is not `null`, as
  `Head: <head.sha> (was <head.previous_sha>)`. `Omitted:` is present
  only when the omitted count is above zero, and gives the sum of `omitted[].count`.
- **One block per record.** Each admitted record is one
  `[steer-record N/M]` ... `[/steer-record]` block, in manifest order. Lines
  inside a block are, in order: `Record`, `Kind`, `Actor`, `Revision`,
  `Filter`, `Path`. `Actor` shows `@<login>`, or `(unknown)` when `login`
  is `null`, followed by `(role)` for a human or `(bot: <bot_role>)` for a
  bot.
- **No other text.** The envelope contains no record body, title, or label
  text. A runtime that splits the message into content blocks MUST keep each
  record block whole.
- **Field safety.** Every field value comes from the manifest. Fullsend MUST
  refuse to steer if any value contains a line break, `[steer-record`,
  `[/steer-record]`, or the opening line.

### The opening line inside record bodies

Agent definitions treat the opening line in two ways:

| Where it appears | Meaning |
|---|---|
| First line of a delivered message | A runner update. The agent reads the listed records |
| Inside a record body | An injection attempt. The agent should flag it |

Fullsend does not rewrite the opening line inside record bodies, because that
would remove the evidence the agent definitions act on. The filter pipeline
still applies to the rest of the body.

### What the agent may do with a steer

- Read the listed files. They are filtered, attributed data under
  [ADR 0133](../../../ADRs/0133-actor-attributed-data-security-boundary.md):
  an admitted record may guide the task within the harness's scope, but cannot
  change tools, role, credentials, or routing.
- When `Head:` is present, work from `<sha>` without calling the forge. On a
  read-only working tree the files already show `<sha>`; on a writable one,
  build new commits on `refs/fullsend/head`. Diff against
  `refs/fullsend/base`, which holds the change proposal's current base. A
  harness whose result schema carries `head_sha`, such as review, reports the
  head it actually worked on there; other harnesses add no such field.
- Ignore a block whose `Path` does not exist. The agent MAY check a file
  against the manifest `sha256` with a tool such as `sha256sum` and ignore a
  mismatch. Such a block was not written by Fullsend.

## Versioning

This is v1. A breaking change requires `docs/normative/steer-envelope/v2/`.

- **Snapshot pairing.** Steer envelope v1 pairs only with
  entity-context-snapshot v1. Consumers MUST reject a manifest whose
  `snapshot_schema_version` is not `1` or differs from the cycle snapshot's
  `index.json` `schema_version`. Entity-context-snapshot v2, which the ADR
  0151 `human_min_role` threshold needs, requires steer envelope v2, so one
  cycle always admits at the snapshot's single threshold.
- **Schema file.** The prose tables in this document are authoritative for
  v1. A `manifest.schema.json` in this directory ships with the first
  implementation and MUST match them exactly, with no added or changed fields.
  Nothing may claim conformance to this version before it exists.

These changes are breaking:

- the opening line, the header line names, the block delimiters, or the block
  line order;
- the steer directory path, the record file rules, or a manifest field's name,
  type, or meaning;
- any change to the permission, exact-path, or cleanup rules that weakens
  them.

Adding a manifest field is also breaking: consumers validate a closed field
set, as entity-context-snapshot v1 consumers do.

## Open questions

- **One block per record.** ADR 0141 asks for one content block per record
  "when cache reuse is intended". Appending a steer keeps the earlier prompt
  prefix cacheable. Which content-block and cache-boundary layout should a
  runtime use for the steer turn so that later turns keep reusing it?
- **In-band version.** The opening line carries no version token. Adding one
  later changes the opening line, which is itself a v2 change.
