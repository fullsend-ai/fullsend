# Trace Context Reference

The [trace-context v1 specification](../normative/trace-context/v1/README.md)
defines the identity, fallback, wire format, admission, and compatibility
contract for [ADR 0154](../ADRs/0154-correlate-work-item-and-behaviour-scenario-traces.md).
This page covers the Go API and adapter integration.

## Implementation scope

At implementation revision
[`1322cdf7d595673d7db4a812a9d74402aa90c849`](https://github.com/fullsend-ai/fullsend/tree/1322cdf7d595673d7db4a812a9d74402aa90c849/internal/telemetry/tracecontext),
[`identity.go`](https://github.com/fullsend-ai/fullsend/blob/1322cdf7d595673d7db4a812a9d74402aa90c849/internal/telemetry/tracecontext/identity.go)
implements identity resolution and fallback derivation;
[`envelope.go`](https://github.com/fullsend-ai/fullsend/blob/1322cdf7d595673d7db4a812a9d74402aa90c849/internal/telemetry/tracecontext/envelope.go)
implements signing, strict parsing, and admission. The package performs no
network I/O or configuration reads and never grants dispatch permission.
Carrier publication, key provisioning, production creation activation,
runtime admission wiring, and scenario collection remain later delivery
slices.

The package must stay importable from public behaviour-test code: it must not
depend on `internal/cli`, `internal/repos`, `internal/layers`, or the nested
mintcore module.

## Adapter integration

[`JiraClient.GetIssue`](https://github.com/fullsend-ai/fullsend/blob/1322cdf7d595673d7db4a812a9d74402aa90c849/internal/tracker/jira_client.go)
exposes the project ID as `tracker.Issue.ContainerID`; Jira
[search requests](https://github.com/fullsend-ai/fullsend/blob/1322cdf7d595673d7db4a812a9d74402aa90c849/internal/forge/jira/client.go)
include `project`. Missing or invalid project IDs stay zero (unresolved).
Jira's create response contains no project ID, so a subsequent trusted read
must resolve the container before trace admission. Forge-backed tracker
callers must resolve their repository separately through `forge.Client`.

[`CreateCrossRepoChangeProposal`](https://github.com/fullsend-ai/fullsend/blob/1322cdf7d595673d7db4a812a9d74402aa90c849/internal/forge/github/github.go)
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

## Go API

| Symbol | Purpose |
|--------|---------|
| `Identity` | The immutable binding tuple |
| `Identity.Resolved` | Whether forge-side identity components are present |
| `DeriveFallback` | The v1 deterministic fallback context |
| `NormalizeInstance` | Canonical deployment base URL, including its path prefix |
| `Envelope` | A decoded v1 envelope or pending marker |
| `Envelope.Sign` | Validate and sign with an Ed25519 private key (host-side only) |
| `Envelope.Marshal` | Canonical wire bytes |
| `Parse` | Strict bounded decode |
| `DecodeCarrier` | Strict base64 carrier decode |
| `KeyID` | The 8-byte key selector for a public key |
| `Admit` | The pure admission decision |
| `Result`, `Outcome` | Admission outcome and admitted IDs; the zero value selects fallback |

## Conformance evidence

- [`identity_test.go`](https://github.com/fullsend-ai/fullsend/blob/1322cdf7d595673d7db4a812a9d74402aa90c849/internal/telemetry/tracecontext/identity_test.go)
  contains independent fallback vectors, deployment-prefix separation checks,
  cross-process convergence, and SDK exporter lifecycle assertions.
- [`envelope_test.go`](https://github.com/fullsend-ai/fullsend/blob/1322cdf7d595673d7db4a812a9d74402aa90c849/internal/telemetry/tracecontext/envelope_test.go)
  contains independent signed-wire vectors for both domains and all provenance
  kinds, binding checks, empty/default/padded-port convergence, zero-value fallback,
  malformed-input cases, and parser fuzzing.

These foundation tests do not establish production runtime integration or
live-adapter trace continuity.
