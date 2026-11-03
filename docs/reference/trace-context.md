# Trace Context Reference

Version 1 of the work-item trace-context identity and signed-envelope
protocol from [ADR 0154](../ADRs/0154-correlate-work-item-and-behaviour-scenario-traces.md).
This page documents the implemented contract surfaces: the immutable
identity tuple, the deterministic fallback derivation, the v1 wire format,
and the pure admission operation. Carrier publication (hidden body
metadata, Jira properties), key provisioning, and runtime admission wiring
are separate delivery slices; this page marks them where they are not yet
implemented.

The Go implementation is
[`internal/telemetry/tracecontext`](https://github.com/fullsend-ai/fullsend/blob/main/internal/telemetry/tracecontext).
It is pure: no network I/O, no configuration reads, and it never grants
dispatch permission.

## Identity

Every forge entity is addressed by an immutable five-part tuple. Display
names, URLs, GitHub numbers, GitLab IIDs, and Jira keys are not identity
components.

| Component | GitHub | GitLab | Jira |
|-----------|--------|--------|------|
| Installation | 128-bit ID provisioned with the signing key pair | same | same |
| Forge instance | web base URL (`https://github.com`, or a GHES host) | web base URL (`https://gitlab.com`, or a self-hosted host) | site base URL |
| Container | repository `id` | project `id` | project `id` |
| Entity kind | `work_item` (issue), `change_proposal` (PR) | `work_item` (issue), `change_proposal` (MR) | `work_item` (issue; Jira has no change proposal) |
| Entity | issue or PR REST `id`; PR GraphQL `fullDatabaseId` (`BigInt`) | issue or MR database `id` (not the IID) | issue `id` |

The instance string is the normalized base URL: lowercase
`scheme://host`, using `http` or `https`, at most 253 UTF-8 bytes, no path or
trailing slash. `NormalizeInstance` builds it (accepting and removing a bare
`/`) and rejects URLs with a longer path, query, fragment, user info, or
missing host. Identity resolution, signing, and parsing require the already
canonical value; they never silently rewrite an authenticated field.

Jira issue reads expose the project ID as `tracker.Issue.ContainerID`; search
requests include `project`. Missing or invalid project IDs stay zero
(unresolved). Jira's create response contains no project ID, so a subsequent
trusted read must resolve the container before trace admission. Forge-backed
tracker reads resolve their repository separately through `forge.Client`.
GitHub cross-repository PR creation decodes `fullDatabaseId` directly into
an `int64`, accepting numeric or string JSON without floating-point rounding;
the deprecated 32-bit GraphQL `databaseId` is not used. Missing or invalid
IDs leave identity unresolved without discarding a successful PR creation.

The installation ID is generated during key provisioning and identifies
one install's key set. It is authenticated inside every envelope and must
match the verifier's configured installation at admission. It is
deliberately **not** part of fallback derivation, so entities keep stable
traces regardless of key state.

### Rename and move rules

- Renames (issue titles, repository names, Jira project keys) never
  change identity: every component is a database ID.
- Repository transfers on GitHub and GitLab preserve the database ID, so
  identity survives a transfer.
- An entity moved between containers (a GitHub issue transfer, a Jira
  project move) has a **new binding** — a different container or entity
  ID — and therefore a new identity. A previously published envelope
  fails the binding check and the moved entity falls back. A move is
  never treated as a rename.

### Fallback derivation (v1)

When no admissible stored context exists, the entity's context is derived
deterministically:

- Input: the forge instance, container, kind, and entity — never the
  installation.
- SHA-256 over the exact concatenation below. Only strings are
  length-prefixed; all integers are big-endian:
  `uint16(33) || "fullsend.tracecontext.identity.v1" || uint16(N) ||
  instance[N] || uint64(container) || byte(kind) || uint64(entity)`.
  `N` counts UTF-8 bytes, container/entity are in `[1, 2^63−1]`, and kind
  is `0x01` (work item) or `0x02` (change proposal).
- TraceID = the first 16 digest bytes; the anchor SpanID = the next 8.
- An all-zero result (probability 2⁻¹²⁸ for the trace, 2⁻⁶⁴ for the span)
  replaces only that ID's final byte with `0x01`, so derived IDs are valid.

The derivation is pure, so independent launches and concurrent first
events converge on identical nonzero IDs without shared state. The anchor
SpanID is a synthetic parent reference by protocol choice: no exported span
carries the anchor ID, the anchor is never repointed, and operation spans
keep distinct SDK-generated IDs. OpenTelemetry supports custom ID generators
(`WithIDGenerator`); this contract deliberately uses an unexported anchor
rather than creating a real root span with that ID.

For `https://github.com`, container `274390431`, kind `0x01`, and entity
`2468913570`, the exact input is:

```text
002166756c6c73656e642e7472616365636f6e746578742e6964656e746974792e7631
001268747470733a2f2f6769746875622e636f6d00000000105add9f01000000009328a1a2
```

Concatenate those hex lines before hashing. The TraceID is
`c9bdb0e38247ebbe2e0f1039250c32d4`; the anchor is `892a346c55356aca`.
Additional independent GitLab/Jira vectors and an SDK exporter lifecycle
test are in `identity_test.go`; runtime integration remains a later slice.

## Signed envelope (wire format v1)

One fixed-layout binary encoding serves both message domains; all
integers are big-endian, and the Ed25519 signature covers every byte from
the magic through the nonce — there are no unsigned fields.

| Offset | Size | Field | Constraints |
|--------|------|-------|-------------|
| 0 | 4 | Magic `FSTC` | exact bytes |
| 4 | 1 | Version | `0x01`; anything else rejects |
| 5 | 1 | Message domain | `0x01` bound envelope, `0x02` pending marker |
| 6 | 8 | Key ID | first 8 bytes of SHA-256(public key) |
| 14 | 16 | Installation ID | nonzero |
| 30 | 2 | Instance length N | `1 ≤ N ≤ 253` |
| 32 | N | Forge instance | normalized form, UTF-8, no NUL |
| 32+N | 8 | Container ID | `int64 ≥ 1` |
| 40+N | 1 | Entity kind | `0x01` work item, `0x02` change proposal |
| 41+N | 8 | Entity ID | `int64 ≥ 1`; bound envelopes only — pending markers omit these 8 bytes, retaining the kind byte |
| · | 16 | Trace ID | nonzero |
| · | 8 | Parent/anchor Span ID | nonzero |
| · | 1 | Provenance kind | `0x00` none, `0x01` source link, `0x02` scenario |
| · | 0/24/32 | Provenance fields | none; source link = 16B source TraceID + 8B source SpanID; scenario = 16B scenario TraceID + 16B invocation ID |
| · | 16 | Nonce | `crypto/rand`, nonzero, unique per creation intent |
| end−64 | 64 | Ed25519 signature | over bytes `[0, end−64)` |

The maximum legal size is 439 bytes (bound envelope, scenario provenance,
253-byte instance); carriers hold the envelope as unwrapped standard
base64 (≤ 588 characters, no line breaks).

Strict parsing rules:

- The total length must equal the exact structural length for the parsed
  domain and provenance kind; trailing bytes reject.
- Duplicate and unknown fields are structurally impossible in the fixed
  layout.
- Every enumerated byte and ID must satisfy its constraint; invalid
  values reject rather than degrade.
- Unknown versions reject — never a partial parse.
- Identical duplicate carriers are acceptable (idempotent publication);
  any differing carriers reject — never first-match.

### Domains, keys, and nonces

The domain byte is covered by the signature, so a pending marker cannot
be replayed as a bound envelope and vice versa. A pending marker carries
the creation's intended context and provenance as signed intent, omits
the entity ID (the destination does not exist yet), and grants neither
context, parent, nor source link. The nonce links a pending marker to
the bound envelope published after creation; admission never depends on
having seen the marker.

The key selector resolves **only** against the verifier's configured
eligible keys (active plus retained). Envelopes never carry public keys
or key locations, and metadata can never select a signer or a remote key
URL. After rotation, metadata signed by an older retained key remains
verifiable while new metadata uses the active key.
Missing or malformed-length verification keys yield `unknown-key` fallback
without context or provenance; they never reach Ed25519 verification.

### Provenance consistency

- Source-link provenance (ordinary created work) requires the envelope
  TraceID to differ from the source TraceID: the destination keeps its
  own trace and the source context is a link, never a parent.
- Scenario provenance (behaviour-test work) requires the envelope
  TraceID to equal the scenario TraceID: descendants inherit the
  scenario trace with distinct operation spans.

## Admission

`Admit(expected Identity, keys, candidates...)` is the pure decision
operation. It takes the trusted expected binding, the configured eligible
verification keys, and bounded candidate carrier bytes, and returns
exactly one outcome:

- **Admitted**: a verified bound envelope matched every binding
  component. The result carries the admitted TraceID, parent SpanID, and
  authenticated provenance.
- **Pending**: a verified pending marker matched the expected scope. It
  grants nothing; bounded rereads are caller policy.
- **Fallback**: nothing was admissible. The caller uses the
  entity-derived fallback.

Fallback reason codes are redacted diagnostics — `absent`, `malformed`,
`oversized`, `unknown-version`, `unknown-key`, `bad-signature`,
`binding-mismatch`, `conflicting-carrier`, `provenance` — never key
material or raw bytes. A plain W3C `traceparent` is never an envelope.

The result structurally cannot carry sampling flags, `tracestate`,
baggage, or dispatch permission. Trace admission and dispatch
authorization are independent decisions; a denied actor never dispatches
regardless of admission, and an admitted envelope never authorizes
anything.

An unresolved identity (missing instance, container, kind, or entity) is
explicit: callers must postpone dispatch with diagnostics rather than
derive a fallback or guess.

## Go API

| Symbol | Purpose |
|--------|---------|
| `Identity` | The immutable binding tuple |
| `Identity.Resolved` | Whether forge-side identity components are present |
| `DeriveFallback` | The v1 deterministic fallback context |
| `NormalizeInstance` | Canonical `scheme://host` instance form |
| `Envelope` | A decoded v1 envelope or pending marker |
| `Envelope.Sign` | Validate and sign with an Ed25519 private key (host-side only) |
| `Envelope.Marshal` | Canonical wire bytes |
| `Parse` | Strict bounded decode |
| `DecodeCarrier` | Strict base64 carrier decode |
| `KeyID` | The 8-byte key selector for a public key |
| `Admit` | The pure admission decision |
| `Result`, `Outcome` | Admission outcome and admitted IDs |

The package must stay importable from public behaviour-test code: it
must not depend on `internal/cli`, `internal/repos`, `internal/layers`,
or the nested mintcore module.

## Compatibility

The golden test vectors in
[`envelope_test.go`](https://github.com/fullsend-ai/fullsend/blob/main/internal/telemetry/tracecontext/envelope_test.go)
pin the exact signed bytes of one envelope per domain and provenance
kind. Any change to the wire layout, derivation, or key-selector rules is
a compatibility break that requires a new version byte and a reviewed
migration path. Unknown versions already reject cleanly.
