# Trace context v1

This normative specification defines the immutable identity, deterministic
fallback, signed-envelope encoding, and admission contract adopted by
[ADR 0154](../../../ADRs/0154-correlate-work-item-and-behaviour-scenario-traces.md).
See the [Trace Context Reference](../../../reference/trace-context.md) for
Go API guidance and implementation scope. Carrier publication, key
provisioning, and runtime integration are outside this contract.

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
| Entity | `work_item`: issue REST `id`; `change_proposal`: pull-request REST `id` = GraphQL `fullDatabaseId` (`BigInt`), never the issue-space ID | issue or MR database `id` (not the IID) | issue `id` |

A GitHub PR has both a pull-request ID and a different issue-space ID.
The `change_proposal` identity must use the pull-request ID from
`/repos/{owner}/{repo}/pulls/{number}`, also exposed as GraphQL
`fullDatabaseId`. A PR's `/issues/{number}` response and the `issue` object
in an `issue_comment` event carry its issue-space ID, which must never be
used for `change_proposal` identity. For a PR event carrying only that issue
object, resolve the pull-request ID through a trusted forge-adapter
`/pulls/{number}` read before declaring the identity resolved. Until that
read succeeds, keep the identity unresolved rather than substituting the
issue-space ID or display number.

The instance string is the normalized, trusted deployment base URL: lowercase
`scheme://host`, using `http` or `https`, followed by the case-sensitive,
URL-escaped deployment path prefix, with trailing slashes removed. For
example, `https://GitLab.example.com/gitlab/` becomes
`https://gitlab.example.com/gitlab`; it is distinct from both the root
installation and `/GitLab`. Path segments and existing escape spelling are
preserved, never cleaned or decoded into separators. Use the same configured
deployment URL on every launch, rather than an entity or API URL.

Remove an empty port and the scheme's default port (`443` for HTTPS, `80` for
HTTP). Strip leading zeros from other ports, preserving an explicit zero as
`:0` rather than treating it as the default, and reject ports above `65535`.
For example, `https://host:` becomes `https://host`, and
`https://host:08443` becomes `https://host:8443`. Hosts must be ASCII: use
the punycode form for internationalized domain names, and reject Unicode
host spellings instead of converting them. For example,
`https://GitHub.com:443/` becomes `https://github.com`, and
`https://XN--BCHER-KVA.example` becomes `https://xn--bcher-kva.example`.
Bracketed IPv6 hosts follow the same case and port rules, without zone
identifiers: `https://[FE80::1%25EN0]` rejects — use a routable address —
so a normalized instance always re-normalizes to itself. Trailing DNS root
dots are stripped: `https://github.com.` and `https://github.com` are one
deployment with one fallback identity, never two.

`NormalizeInstance` rejects queries, fragments, user info, missing hosts,
invalid UTF-8 or NUL in the decoded path, and normalized URLs over 253 bytes
(including the escaped prefix). Identity resolution, signing, and parsing
require the already canonical value; they never silently rewrite an
authenticated field. Root-instance vectors, wire layout, and size limits
are unchanged; prefixed deployments use the same v1 length-delimited field
and fallback derivation with the full normalized instance bytes.

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
test are linked from the [implementation reference](../../../reference/trace-context.md).

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
- **Pending**: a verified pending marker matched the expected scope —
  installation, instance, container, and kind, never the entity, because a
  pending marker carries no entity binding by design. A marker replayed
  onto another entity in the same container still grants nothing beyond a
  bounded wait. It grants no context; callers must bound that wait by a
  window from the entity's trusted creation metadata (for example
  `created_at`) before rereading and falling back, per
  [ADR 0154](../../../ADRs/0154-correlate-work-item-and-behaviour-scenario-traces.md).
- **Fallback**: nothing was admissible. The caller uses the
  entity-derived fallback.

The zero-value result selects fallback and grants no context or provenance.

Fallback reason codes are redacted diagnostics — `absent`, `malformed`,
`oversized`, `unknown-version`, `unknown-key`, `bad-signature`,
`binding-mismatch`, `conflicting-carrier`, `provenance`, `unresolved` —
never key material or raw bytes. A plain W3C `traceparent` is never an
envelope.

Any extra or differing candidate deliberately forces fallback, even when
one candidate alone would admit: an actor who can append one carrier to an
editable body must not be able to choose or suppress the admitted context.
Publishing a bound envelope must remove the pending marker it replaces;
leaving both present produces conflicting carriers and fallback, never a
pick between them.

The result structurally cannot carry sampling flags, `tracestate`,
baggage, or dispatch permission. Trace admission and dispatch
authorization are independent decisions; a denied actor never dispatches
regardless of admission, and an admitted envelope never authorizes
anything.

An unresolved identity (missing instance, container, kind, or entity) is
explicit: admission returns fallback with reason `unresolved` rather than
pending, and callers must postpone dispatch with diagnostics rather than
derive a fallback or guess.

## Compatibility

The [conformance vectors](../../../reference/trace-context.md#conformance-evidence)
pin the exact signed bytes of one envelope per domain and provenance kind.
Any change to the wire layout, derivation, or key-selector rules is a
compatibility break that requires a new version byte and a reviewed migration
path. Unknown versions must reject cleanly.
