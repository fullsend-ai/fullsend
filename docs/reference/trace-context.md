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
grants dispatch permission. `fullsend repos install` provisions each
repository's keys (see [Key lifecycle](#key-lifecycle)). Carrier publication,
the flags through which the behaviour-test suite supplies its own keys,
delivery of the signing key to runs, production creation activation, runtime
admission wiring, and scenario collection are not implemented yet.

The package must stay importable from public behaviour-test code: it must not
depend on `internal/cli`, `internal/repos`, `internal/layers`, or the nested
mintcore module.

## Key lifecycle

[`fullsend repos install`](../cli/repos.md#trace-signing-keys) creates and
maintains each repository's signing keys. No fullsend code reads the signing
key yet, and nothing signs or publishes trace-context carriers. On GitHub, no
workflow passes the secret to a job. On GitLab, the CI/CD variable reaches
every job that runs on a protected branch or tag. Fullsend's own GitLab jobs
unset it on the first line of their `before_script`, so `fullsend run` and the
scripts it starts do not inherit it. Install delivers the jobs of the
repository's `fullsend_ref`, and it writes a key only when that ref names a
release whose jobs unset the variable (see
[GitLab pinned releases](../cli/repos.md#trace-signing-keys)). If the
variable is still set, `fullsend run` keeps it out of harness `${VAR}`
expansion, host-side scripts and the sandbox. While a fullsend workflow is
on a GitLab repository's default branch, install writes a key only once the
fullsend jobs there unset the variable (see
[Installation ID and key pair](#installation-id-and-key-pair)). Other jobs on
protected branches and tags, including the project's own jobs, still receive it,
and so does an `after_script` or `hooks:pre_get_sources_script` that a fullsend
job inherits from the project's `default:` configuration or a deprecated
top-level `after_script:`, because GitLab runs it in another shell. On the
Docker executor, the value may remain in the job's initial environment, so the
unset only keeps the job's later `before_script` and `script` commands from
inheriting the value. Delivery of the key to runs comes later. Until then,
`fullsend run` only validates the
[`trace_context`](config-reference.md#trace_context) block.

### Installation ID and key pair

Each repository with keys has its own installation ID: 16 random bytes,
written as 32 lowercase hex characters. Install generates it with the
repository's first key pair. Reinstall, recovery and rotation keep it.
Install starts a new installation only when the repository's `repos.yaml`
entry has no `trace_context` block, and either its committed
`.fullsend/config.yaml` has none or the repository was fully uninstalled (see
[Uninstall](#uninstall)). Later runs keep the new installation, even before
that install's commit replaces the ended installation's block.

The key pair is an Ed25519 pair. Install keeps three records of it:

- **The private key**, as its 32-byte seed in standard base64, in the
  `FULLSEND_TRACE_SIGNING_KEY` secret. On GitHub this is a repository secret.
  On GitLab it is a protected, masked CI/CD variable available to all
  environments, and install fails rather than store it unmasked. GitLab
  passes such a variable to every job that runs on a protected branch or tag,
  including the project's own jobs.
- **The public key and the installation ID**, in the `trace_context` block of
  the repository's `repos.yaml` entry. Install renders the block into the
  managed `.fullsend/config.yaml`.
- **The key ID of the seed last written**, in the
  `FULLSEND_TRACE_SIGNING_KEY_ID` variable, called the ledger. A key ID is the
  first 8 bytes of the SHA-256 hash of the public key, written as 16 lowercase
  hex characters. The ledger is not secret. It exists because install cannot
  read a GitHub secret back.

A fresh install gets keys. An existing install gets keys only when you pass
`--rotate-trace-signing-key`: its first rotation provisions them. A fresh
install from a manifest that install cannot write, such as an HTTPS URL, gets no
keys. Install prints that they were not provisioned, and a later run with
`--rotate-trace-signing-key` and a writable manifest provisions them. With
`--rotate-trace-signing-key`, such a fresh install stops before it writes any
key, unless the repository's GitLab `fullsend_ref` names a release whose jobs do
not unset the variable: that install goes ahead without keys too
([GitLab pinned releases](../cli/repos.md#trace-signing-keys)).

On GitLab, while a fullsend workflow is on the default branch, install
writes a key only once the poll, dispatcher and agent jobs there unset
`FULLSEND_TRACE_SIGNING_KEY` on the first line of their `before_script`.
This holds for the first rotation of an existing install and for a later
rotation, recovery or exposed-seed replacement (see
[Writes and recovery](#writes-and-recovery)). Until then, install writes no
key and records none in `repos.yaml`, even when the manifest is read-only,
and prints that the key was not written or the keys were not provisioned. It
still commits its scaffold change, which adds the line. Run
`fullsend repos install` again for that repository, with a writable manifest,
once that change is merged, adding `--rotate-trace-signing-key` when the key
was for a rotation. Fullsend pipelines created before the line reached the
default branch keep jobs without it, whichever install committed it, and
GitLab passes the key to each job when it starts or is retried: let those
pipelines finish before install writes a key, and do not retry their jobs
afterwards. When the repository's `fullsend_ref` names a release whose
jobs do not unset the variable, install says to move `gitlab.fullsend_ref`, or
the `fullsend_ref` of the repository's entry, instead. A fresh install with
such a `fullsend_ref` goes ahead without keys. After the move,
`--rotate-trace-signing-key` provisions them.

### Writes and recovery

Install writes a new key in this order:

1. It records the new public key as `active` in `repos.yaml`, before any
   forge write. The previous active key moves to `retained`.
2. It reads the ledger again. If the ledger changed since the run first read
   it, as when another install or uninstall of the repository ran at the same
   time, it writes no key, skips the commit and fails that repository. Run
   install again once the other run has finished; after another install, use the
   `repos.yaml` that install wrote.
3. It writes the seed to `FULLSEND_TRACE_SIGNING_KEY`.
4. Only after that write succeeds, it writes the new key ID to the ledger.
5. It commits the managed `.fullsend/config.yaml`, directly or in the install
   pull request.

On GitLab, steps 2 to 4 hold the `FULLSEND_GITLAB_INSTALL_LEASE` project lease
that serializes installers (see
[Upgrade compatibility](../cli/repos.md#upgrade-compatibility)), so two
installs of one project cannot interleave these writes. Install waits up to
two minutes for the lease. If it cannot take the lease in that time, it fails
that repository before step 2. If it cannot release the lease, it fails the
repository before step 5. When no installer of the project is running, delete
`FULLSEND_GITLAB_INSTALL_LEASE` if it remains, then run install again. GitHub
has no such lease, so run one install of a GitHub repository at a time. When two
runs overlap there, the secret can end up holding a key other than the one that
the ledger, `repos.yaml` and the committed configuration name. Install can see
only that the secret exists, not which key it holds, so the mismatch stays
hidden. `fullsend repos install <repo> --rotate-trace-signing-key` writes a
matching key.

An install is healthy when the secret exists and the ledger names the active
key. Install then writes no key unless you pass `--rotate-trace-signing-key`.
When the secret is missing, or the ledger is missing or names a retained key,
the next plain `repos install` generates a new key through the same steps.
Unless another install of the repository wrote a key after the run started (see
step 2), a run that stopped at any step therefore converges on a plain retry.
That retry keeps every trusted key: the key that the stopped run recorded stays
in `retained`. The same applies to a run that enrolled a fully uninstalled
repository again, before its commit lands (see [Uninstall](#uninstall)).

On GitLab, install also checks how the stored seed is protected. It treats a
seed that is unmasked, unprotected or a file-type variable as exposed. It
replaces the seed with a new key, keeps the old public key in `retained`, and
prints the old key's ID so that you can remove it. Install reads only the
variable that applies to all environments, so it treats a seed that exists
only for a specific environment as missing. It writes a new seed for all
environments and keeps the old key in `retained`. It does not print that key's
ID or delete the environment's variable. Delete that variable yourself, then
remove the old key in a later run with `--remove-trace-verification-key`.

Commit the changes install makes to `repos.yaml`: it is the only record of the
key set.

### Rotation and removal

`fullsend repos install --rotate-trace-signing-key` generates a new key pair.
The previous active key moves to `retained`, so envelopes it signed still
verify. Envelopes carry no timestamp, so a retained key stays trusted until
you remove it. Install never removes a retained key on its own.

`fullsend repos install --remove-trace-verification-key <key-id>` removes a
retained key from every selected repository that retains it. Removal revokes
the key: envelopes it signed fall back with reason `unknown-key`. You cannot
remove the active key, and removal applies only to keys that were retained
before the run. To retire the active key, rotate in one run, then remove the
old key in a later run, once the rotation is on the default branch. Until
then, the committed configuration still has the old key active. You also
cannot remove the key that the ledger names, which is a retained key only
after an interrupted run. Run `fullsend repos install` without removing that
key first, and remove it once a run has written a new key ID to the ledger.
To find a retained key's ID, hash its public key:

```bash
printf '%s' '<retained public key>' | base64 -d | sha256sum | cut -c1-16
```

A removal delivered by pull or merge request takes effect when it merges.
Until then, the committed `.fullsend/config.yaml` still lists the key, and
install refuses the entry as a stale manifest (see
[Stale manifest](#stale-manifest)). Merge the removal before the next install
of that repository, or name the key again with
`--remove-trace-verification-key` on each run until it merges.

### Stale manifest

Install replaces the managed `.fullsend/config.yaml` with what `repos.yaml`
declares, so an older copy of `repos.yaml` could drop keys that a later run
added. Before it writes `repos.yaml` or any key, install compares each selected
repository's entry with the committed `.fullsend/config.yaml` and the ledger. It
stops with a "manifest is stale" error when:

- the committed installation ID differs from the entry's, and a fullsend
  workflow is on the default branch;
- a committed active or retained key is missing from the entry, unless
  `--remove-trace-verification-key` names it or the key belongs to an ended
  installation. A retained key is missing, for example, while the pull or
  merge request that removes it is open (see
  [Rotation and removal](#rotation-and-removal));
- the committed configuration has a `trace_context` block and the entry has
  none, unless the repository was fully uninstalled (see
  [Uninstall](#uninstall));
- the ledger names a key that the entry does not list;
- another `repos.yaml` entry uses the same installation ID.

A committed block belongs to an ended installation when its installation ID
differs from the entry's and the default branch has no fullsend workflow, for
example before the commit of a re-enrollment lands (see
[Uninstall](#uninstall)). Install then skips the checks of that block's
installation ID and keys. It still checks the ledger and the other entries'
installation IDs.

The error names the repository and, for a key missing from the entry, its key
ID. It never shows a key or an installation ID. Run install from the current
`repos.yaml`.

A merged change to the committed `.fullsend/config.yaml` can also add a key
that no install recorded. If you did not add the key that the error names,
revert that change, and do not add the key to `repos.yaml`: that would make it
trusted.

Install also stops the run, before its key writes, when it needs to record a key
or a removal and the manifest is an HTTPS URL or cannot be written, because it
could not record the change. Run install from a writable local copy of the
manifest instead. A fresh install without `--rotate-trace-signing-key` is the
exception, including the re-enrollment of a fully uninstalled repository: it
goes ahead without keys (see
[Installation ID and key pair](#installation-id-and-key-pair) and
[Uninstall](#uninstall)).

### Uninstall

`fullsend repos uninstall` deletes `FULLSEND_TRACE_SIGNING_KEY` and the
ledger. On GitLab it also deletes `.fullsend/config.yaml`, which takes the
public keys out of the repository. On GitHub it leaves `.fullsend/config.yaml`
in place, `trace_context` block included. With `--uninstall-only`, the
`repos.yaml` entry keeps its `trace_context` block. The next install then
keeps the installation ID, moves the old active key to `retained`, and writes
a new key pair.

A full uninstall also removes the repository's `repos.yaml` entry. A fully
uninstalled repository that you declare and install again is enrolled again
automatically as a new installation. Install generates a new installation ID
and key pair, and replaces the `trace_context` block that GitHub leaves in the
committed `.fullsend/config.yaml`. It prints the ended installation's ID and
the key IDs that it stops trusting. The repository counts as fully uninstalled
once it has no fullsend workflow, `FULLSEND_TRACE_SIGNING_KEY` or ledger left.
Until then, for example while the uninstall pull request is open, install
refuses the entry as a stale manifest. To keep the installation instead,
restore the entry's `trace_context` from the `repos.yaml` the last install
wrote.

Install replaces the ended block in the commit that delivers its scaffold, by
default through the install pull request. Until that change lands, the
default branch has no fullsend workflow and keeps the ended block. Later runs
treat that block as the ended installation's: they print the ended
installation again, keep the entry's installation, and recover a missing seed
or ledger as usual. A plain retry after an interrupted run therefore
converges, and the other repositories in the run install as usual.

From a manifest that install cannot write, such as an HTTPS URL, install
enrolls a fully uninstalled repository again without keys, as for any fresh
install (see [Installation ID and key pair](#installation-id-and-key-pair)).
It still prints the ended installation, and its commit removes the ended
block instead of replacing it.

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

## Go API

| Symbol | Purpose |
|--------|---------|
| `Identity` | The immutable binding tuple |
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
| `SigningKey` | A private signing key expanded from a 32-byte seed; `SignEnvelope` signs with it, and every formatter and encoder, `slog` included, yields `[redacted]` |
| `ParseSigningSeed`, `NewSigningKey` | A signing key from a standard base64 seed, or a new random one |
| `SigningKey.EncodeSeed` | The seed as standard base64, the only way to read it out; install uses it to write the secret |
| `KeySet` | The admission key map of the active and retained keys, keyed by `KeyID`; rejects keys that are not 32 bytes and duplicate key IDs |

## Conformance evidence

- [`identity_test.go`](https://github.com/fullsend-ai/fullsend/blob/main/internal/telemetry/tracecontext/identity_test.go)
  contains independent fallback vectors, deployment-prefix separation checks,
  cross-process convergence, root-dot, port, and IPv6 spelling
  canonicalization and zone rejection, mixed-escape path rejection, a
  normalization fuzz target asserting the canonical form re-normalizes to
  itself, and SDK exporter lifecycle assertions.
- [`envelope_test.go`](https://github.com/fullsend-ai/fullsend/blob/main/internal/telemetry/tracecontext/envelope_test.go)
  contains independent signed-wire vectors for both domains and all provenance
  kinds, binding checks, empty/default/padded-port convergence, zero-value fallback,
  pending-scope admission (container-scoped markers, stale markers beside bound
  envelopes), unresolved identities suppressing every carrier kind,
  malformed-input cases, and parser fuzzing.

These foundation tests do not establish production runtime integration or
live-adapter trace continuity.
