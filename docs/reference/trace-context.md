# Trace Context Reference

The [trace-context v1 specification](../normative/trace-context/v1/README.md)
defines the identity, fallback, wire format, admission, and compatibility
contract for [ADR 0154](../ADRs/0154-correlate-work-item-and-behaviour-scenario-traces.md).
This page covers the Go API and adapter integration.

## Implementation scope

The v1 implementation lives in
[`internal/telemetry/tracecontext`](https://github.com/fullsend-ai/fullsend/tree/main/internal/telemetry/tracecontext):
[`identity.go`](https://github.com/fullsend-ai/fullsend/blob/main/internal/telemetry/tracecontext/identity.go)
implements identity resolution and fallback derivation;
[`envelope.go`](https://github.com/fullsend-ai/fullsend/blob/main/internal/telemetry/tracecontext/envelope.go)
implements signing, strict parsing, and admission;
[`keys.go`](https://github.com/fullsend-ai/fullsend/blob/main/internal/telemetry/tracecontext/keys.go)
parses and generates installation IDs and keys and builds the admission key
map. The package performs no network I/O or configuration reads and never
grants dispatch permission.
The signing key's name, `FULLSEND_TRACE_SIGNING_KEY`, is reserved.
`fullsend run` keeps it from harnesses and their host-side scripts.
`fullsend lint` reports a harness that sets it in `env.sandbox`.
`fullsend lock` gives the same report when it composes that harness.
Fullsend's GitLab poll, dispatcher and agent jobs unset it on the first line
of their `before_script`. `fullsend repos uninstall` deletes it.
Carrier publication, key provisioning, production creation activation,
runtime admission wiring, and scenario collection remain later delivery
slices.

The package must stay importable from public behaviour-test code: it must not
depend on `internal/cli`, `internal/repos`, `internal/layers`, or the nested
mintcore module.

## Adapter integration

[`JiraClient.GetIssue`](https://github.com/fullsend-ai/fullsend/blob/main/internal/tracker/jira_client.go)
exposes the project ID as `tracker.Issue.ContainerID`; Jira
[search requests](https://github.com/fullsend-ai/fullsend/blob/main/internal/forge/jira/client.go)
include `project`. Missing or invalid project IDs stay zero (unresolved).
Jira's create response contains no project ID, so a subsequent trusted read
must resolve the container before trace admission. Forge-backed tracker
callers must resolve their repository separately through `forge.Client`.

[`CreateCrossRepoChangeProposal`](https://github.com/fullsend-ai/fullsend/blob/main/internal/forge/github/github.go)
decodes GitHub's `fullDatabaseId` into an `int64`, accepting numeric or string
JSON without floating-point rounding; it does not use the deprecated 32-bit
GraphQL `databaseId`. Missing or invalid IDs leave identity unresolved without
discarding a successful PR creation.

GitHub `GetIssue` preserves the issue-space ID even for a PR number. Before
constructing a `KindChangeProposal` identity for an `issue_comment` event on
a PR, the runtime integration must resolve the pull-request ID through a
trusted forge read, following the [identity contract](../normative/trace-context/v1/README.md#identity).
`GetPullRequestInfo` currently returns branch/repository context without that
database ID; its result alone cannot resolve trace identity.

`KindConversation` identifies a GitHub Discussion by its repository ID and
Discussion database ID (webhook `discussion.id`, GraphQL `databaseId`). No
forge adapter reads Discussions yet: GitHub Discussion ingress is tracked
under the conversation-surface work for
[ADR 0086](../ADRs/0086-conversation-surface-for-agent-participation.md).
Until a trusted read supplies that database ID, the conversation identity
stays unresolved. The package cannot tell forges apart, so callers must
never construct `KindConversation` for GitLab or Jira entities.

## Go API

| Symbol | Purpose |
|--------|---------|
| `Identity` | The immutable binding tuple |
| `EntityKind` | `KindWorkItem`, `KindChangeProposal`, or `KindConversation` (GitHub Discussion) |
| `Identity.Resolved` | Whether the forge-side components are present and valid: a canonical instance, IDs ≥ 1, and a defined kind |
| `DeriveFallback` | The v1 deterministic fallback context |
| `NormalizeInstance` | Canonical deployment base URL, including its path prefix |
| `Envelope` | A decoded, unverified v1 envelope or pending marker; fields are safe only after `Admit` admits |
| `Envelope.Sign` | Validate and sign with an Ed25519 private key (host-side only) |
| `Envelope.Marshal` | Canonical wire bytes |
| `Parse` | Strict bounded structural decode; does not verify the signature or binding |
| `DecodeCarrier` | Strict base64 carrier decode |
| `KeyID` | The 8-byte key selector for a public key |
| `Admit` | The pure admission decision |
| `Result`, `Outcome` | Admission outcome and admitted IDs; the zero value selects fallback |
| `ParseInstallationID`, `FormatInstallationID`, `NewInstallationID` | The installation ID as exactly 32 lowercase hex characters, not all zero; a new ID is 16 random bytes |
| `ParseVerificationKey`, `FormatVerificationKey` | A verification key as strict standard base64 of a 32-byte Ed25519 public key |
| `ParseKeyID`, `FormatKeyID` | A key ID as exactly 16 lowercase hex characters |
| `SigningKey` | A private signing key expanded from a 32-byte seed; `SigningKey.SignEnvelope` signs with it, and `String`, `GoString`, `Format` (every verb), `MarshalJSON`, `MarshalText` and `LogValue` return `[redacted]` |
| `ParseSigningSeed`, `NewSigningKey` | A signing key from a standard base64 seed, or a new random one |
| `SigningKey.EncodeSeed` | The seed as standard base64, the only way to read it out |
| `KeySet` | The admission key map of the active and retained keys, keyed by `KeyID`; rejects keys that are not 32 bytes and duplicate key IDs |

## Conformance evidence

- [`identity_test.go`](https://github.com/fullsend-ai/fullsend/blob/main/internal/telemetry/tracecontext/identity_test.go)
  contains independent fallback vectors (including a GitHub conversation),
  conversation convergence and kind-separation checks,
  deployment-prefix separation checks,
  cross-process convergence, root-dot, port, and IPv6 spelling
  canonicalization and zone rejection, mixed-escape path rejection, a
  normalization fuzz target asserting the canonical form re-normalizes to
  itself, and SDK exporter lifecycle assertions.
- [`envelope_test.go`](https://github.com/fullsend-ai/fullsend/blob/main/internal/telemetry/tracecontext/envelope_test.go)
  contains independent signed-wire vectors for both domains and all provenance
  kinds plus a bound conversation envelope, conversation admission and
  fallback, binding checks, empty/default/padded-port convergence, zero-value fallback,
  pending-scope admission (container-scoped markers, stale markers beside bound
  envelopes), unresolved identities suppressing every carrier kind,
  malformed-input cases, and parser fuzzing.

These foundation tests do not establish production runtime integration or
live-adapter trace continuity.
