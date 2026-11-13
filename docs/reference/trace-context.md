# Trace Context Reference

The [trace-context v1 specification](../normative/trace-context/v1/README.md)
defines the identity, fallback, wire format, admission, and compatibility
contract for [ADR 0154](../ADRs/0154-correlate-work-item-and-behaviour-scenario-traces.md).
This page covers the Go API, adapter integration, and the signing-key
lifecycle.

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
`fullsend repos install` provisions the signing keys of GitHub repositories
(see [Key lifecycle](#key-lifecycle) for when). Key provisioning on GitLab, the
install flags through which the behaviour-test suite supplies its own keys,
delivery of the signing key to runs, carrier publication, production creation
activation, runtime admission wiring, and scenario collection are not
implemented yet.

The package must stay importable from public behaviour-test code: it must not
depend on `internal/cli`, `internal/repos`, `internal/layers`, or the nested
mintcore module.

## Key lifecycle

[`fullsend repos install`](../cli/repos.md#trace-signing-keys) creates and
maintains the signing keys of GitHub repositories. No fullsend code reads
the signing key yet, nothing signs or publishes trace-context carriers, and no
workflow passes the secret to a job. Until then, `fullsend run` only validates
the [`trace_context`](config-reference.md#trace_context) block. On GitLab,
install writes no key and no `FULLSEND_TRACE_SIGNING_KEY_ID`, even with
`--rotate-trace-signing-key`, and prints that it does not provision them on
GitLab yet.

> **Planned:** keys for GitLab repositories, removing a retained key, keys that
> the behaviour-test suite supplies through install flags, and delivery of the
> signing key to runs
> ([#9060](https://github.com/fullsend-ai/fullsend/issues/9060)).

### Installation ID and key pair

Each repository with keys has its own installation ID: 16 random bytes,
written as 32 lowercase hex characters. Install generates it with the
repository's first key pair. Recovery, rotation and a reinstall after
`--uninstall-only` keep it. Install starts a new installation only when the
repository's `repos.yaml` entry has no `trace_context` block, and either its
committed `.fullsend/config.yaml` has none or the repository was fully
uninstalled (see [Uninstall](#uninstall)).

The key pair is an Ed25519 pair. Install keeps three records of it:

- **The private key**, as its 32-byte seed in standard base64, in the
  `FULLSEND_TRACE_SIGNING_KEY` repository secret.
- **The public key and the installation ID**, in the `trace_context` block of
  the repository's `repos.yaml` entry. Install renders the block into the
  managed `.fullsend/config.yaml`.
- **The key ID of the last seed that install confirmed writing**, in the
  `FULLSEND_TRACE_SIGNING_KEY_ID` repository variable. A key ID is the first
  8 bytes of the SHA-256 hash of the public key, written as 16 lowercase hex
  characters. Envelopes carry the same bytes, so the value is not secret.
  GitHub never returns a secret's value, so the variable records which key
  the secret holds. Nothing at run time uses it.

A fresh install, one with no fullsend workflow on the default branch yet, gets
keys. An existing install whose entry has no `trace_context` gets none from a
plain run: its first run with `--rotate-trace-signing-key` provisions them. A
fresh install from a manifest that install cannot write, such as an HTTPS URL,
gets no keys. Install prints that they were not provisioned, and a later run
with `--rotate-trace-signing-key` and a writable manifest provisions them. With
`--rotate-trace-signing-key`, such a fresh install stops the run before it
writes any key.

### Writes and recovery

Install writes a new key in this order:

1. It records the new public key as `active` in `repos.yaml` and moves the
   previous active key, if any, to `retained`. When another install that uses
   the same file changed it after this run loaded it, install stops before
   writing it.
2. It reads `FULLSEND_TRACE_SIGNING_KEY_ID` again. If the read fails, or the
   variable appeared, disappeared or changed since the run first read it, as
   when another install or uninstall of the repository ran at the same time,
   install writes no key, skips the commit and fails that repository. Run
   install again once the other run has finished; after another install, use
   the `repos.yaml` that install wrote.
3. It deletes `FULLSEND_TRACE_SIGNING_KEY_ID`.
4. It writes the seed to `FULLSEND_TRACE_SIGNING_KEY`.
5. It sets `FULLSEND_TRACE_SIGNING_KEY_ID` to the new key ID.
6. It commits the managed `.fullsend/config.yaml`, directly or in the install
   pull request.

Each step runs only after the one before it succeeds, and a failure in steps
2 to 5 stops the repository before step 6. So the variable never names a key
whose seed the secret no longer holds, apart from overlapping installs (see
below). A failure in step 2 or 3 leaves it as it was or absent, and a failure
in step 4 or 5 leaves it absent unless the failed step 5 write still reached
GitHub.

An install is healthy when the secret exists and the variable names the
active key. Install then writes no key unless you pass
`--rotate-trace-signing-key`. When the variable is missing or names a retained
key, the secret may not hold the active key. In that case, and when the secret
is missing, the next plain `repos install` generates a new key through the
same steps and keeps the installation ID. From a read-only manifest, that run
stops before it writes any key.

Unless another install of the repository wrote a key after the run started
(see step 2), a run that stopped at any step converges on a plain retry with
the `repos.yaml` it wrote. That retry keeps every trusted key: a key that the
stopped run recorded but did not confirm writing moves to `retained`. Keep and
commit that `repos.yaml` even though the run failed: after a run that stopped
at step 4 or 5, a retry from an older copy writes a new key and drops the one
the stopped run may have written, and install cannot tell (see
[When install refuses an entry](#when-install-refuses-an-entry)). A seed write
that failed may still have landed, for example after a timeout.

GitHub installs take no lease, so step 2 misses an install or uninstall that
writes after it. Two overlapping installs can leave the secret holding a key
that the variable does not name, and install cannot see that. Run one install
of a repository at a time. From a writable manifest, this command writes a
matching key:

```bash
fullsend repos install <repo> --rotate-trace-signing-key
```

Commit the changes install makes to `repos.yaml`: install renders
`.fullsend/config.yaml` from it, and the committed copy changes only when the
install commit lands.

### Rotation

`fullsend repos install --rotate-trace-signing-key` generates a new key pair.
The previous active key moves to `retained`, so envelopes it signed still
verify. Envelopes carry no timestamp, so a retained key stays trusted. Within
an installation, install never removes a retained key. Enrolling a fully
uninstalled repository again replaces the ended installation's block, keys
included (see [Uninstall](#uninstall)).

### When install refuses an entry

Install replaces the managed `.fullsend/config.yaml` with what `repos.yaml`
declares, so an older copy of `repos.yaml` could drop keys that a later run
added. Before it writes `repos.yaml` or any key, install compares each
selected repository's entry with the committed `.fullsend/config.yaml` and
with `FULLSEND_TRACE_SIGNING_KEY_ID`. It stops the run when:

- the committed installation ID differs from the entry's, and a fullsend
  workflow is on the default branch;
- a committed active or retained key is missing from the entry, unless the
  committed block belongs to an ended installation;
- the committed configuration has a `trace_context` block and the entry has
  none, unless the repository was fully uninstalled (see
  [Uninstall](#uninstall));
- `FULLSEND_TRACE_SIGNING_KEY_ID` exists while the entry has no
  `trace_context`, or names a key that the entry does not list;
- another `repos.yaml` entry uses the same installation ID.

These usually mean that the last install wrote a different `repos.yaml`. Run
install with the `repos.yaml` the last install wrote, or restore the entry's
`trace_context` from it.

Install cannot detect an older copy after a run that failed after deleting
`FULLSEND_TRACE_SIGNING_KEY_ID` (step 3), at the seed write (step 4) or the
variable write (step 5). A seed write that failed may still have landed, for
example after a timeout. The variable is absent and the committed block is
unchanged, so none of these checks fires, and a retry from the older copy
replaces a seed that landed without retaining its key. That run's error says
to keep and commit the `repos.yaml` it wrote; retry with it.

A committed block belongs to an ended installation when its installation ID
differs from the entry's and the default branch has no fullsend workflow, for
example before the commit of a re-enrollment lands (see
[Uninstall](#uninstall)). Install then skips the checks of that block's
installation ID and keys. It still checks `FULLSEND_TRACE_SIGNING_KEY_ID` and
the other entries' installation IDs.

The error names the repository and, for a key missing from the entry, its key
ID. It never shows a key, an installation ID or the value of
`FULLSEND_TRACE_SIGNING_KEY_ID`.

A merged change to the committed `.fullsend/config.yaml` can also add a key
that no install recorded. If you did not add the key that the error names,
revert that change, and do not add the key to `repos.yaml`: that would make it
trusted.

A `FULLSEND_TRACE_SIGNING_KEY_ID` value that is not 16 lowercase hex
characters also stops the run before it writes `repos.yaml` or any key. The
error names the repository and the variable and says to delete the variable.
It never shows the value. Delete the variable, then run install again:

```bash
gh variable delete FULLSEND_TRACE_SIGNING_KEY_ID --repo OWNER/REPO
fullsend repos install OWNER/REPO
```

Install also stops the run, before it writes any key, when it needs to record
a key and the manifest is read-only: an HTTPS URL, a pipe or FIFO, or a local
file without write permission. Run install from a writable local copy of the
manifest instead. A fresh install without `--rotate-trace-signing-key` is the
exception, including the re-enrollment of a fully uninstalled repository: it
goes ahead without keys (see
[Installation ID and key pair](#installation-id-and-key-pair) and
[Uninstall](#uninstall)).

### Uninstall

On GitHub, uninstall deletes `FULLSEND_TRACE_SIGNING_KEY` and
`FULLSEND_TRACE_SIGNING_KEY_ID` and leaves `.fullsend/config.yaml`,
`trace_context` block included, in place. With `--uninstall-only`, the
`repos.yaml` entry keeps its `trace_context` block. The next install then
keeps the installation ID, moves the old active key to `retained`, and writes
a new key pair. `--manifest-only` removes the entry and leaves the variable,
the secret, the workflow and the committed block in place. When you declare
the repository again without keys, install refuses the entry until you
restore its `trace_context` or a full uninstall finishes.

A full uninstall also removes the repository's `repos.yaml` entry. A fully
uninstalled repository that you declare and install again is enrolled again
as a new installation. Install generates a new installation ID and key pair,
and replaces the `trace_context` block that GitHub leaves in the committed
`.fullsend/config.yaml`. It prints the ended installation's ID and the key IDs
that it stops trusting. The repository counts as fully uninstalled once it has
no fullsend workflow, `FULLSEND_TRACE_SIGNING_KEY` or
`FULLSEND_TRACE_SIGNING_KEY_ID` left. Until then, for example while the
uninstall pull request is open, install refuses the entry. To keep the
installation instead, restore the entry's `trace_context` from the
`repos.yaml` the last install wrote.

A repository that a glob covers stays selected after uninstall removes its own
entry, so these refusals stop any install that selects it, such as an
unfiltered run or the glob, and once a full uninstall finishes, the next such
install enrolls it again. Select other repositories by name, or narrow the
glob, to keep it out.

Install replaces the ended block in the commit that delivers its scaffold, by
default through the install pull request. Until that change lands, the
default branch has no fullsend workflow and keeps the ended block. Later runs
treat that block as the ended installation's: they print the ended
installation again, keep the entry's installation, and recover a missing seed
or `FULLSEND_TRACE_SIGNING_KEY_ID` as usual.

From a read-only manifest, install enrolls a fully uninstalled repository
again without keys, as for any fresh install (see
[Installation ID and key pair](#installation-id-and-key-pair)). It still
prints the ended installation, and its commit removes the ended block instead
of replacing it.

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
