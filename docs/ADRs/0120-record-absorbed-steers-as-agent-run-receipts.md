---
title: "120. Record absorbed steers as agent-run receipts"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
  - flapping-convergence
topics:
  - concurrency
  - entities
  - authorization
  - entity-context-snapshot
---

# 120. Record absorbed steers as agent-run receipts

Date: 2026-11-02

## Status

Accepted

Companion decisions:

- ADR 0117 (proposed): what a steer is. Steering is correct without this ADR.

## Context

Under ADR 0117 (proposed), a running cycle can take in admitted entity updates
as steers. When the agent acknowledges a steer, the cycle has absorbed those
updates. The notification that prompted the steer still schedules a follow-up
cycle, and unless that cycle knows what was absorbed, it redoes the work.

[ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md) adds no
Fullsend handled-state gate (no Fullsend check that skips work already
done); deciding what is handled belongs to the harness.
[ADR 0098](0098-entity-first-harness-evaluation.md) allows "an explicit
per-harness receipt", a record that an update was already handled, for that
purpose. If the agent or anyone else could forge a receipt, the harness would
skip work it still owes. Where the receipt lives is therefore the core of this
decision.

ADR 0144 already defines host-written final metadata for a bound `fullsend
run`: the run-level result that names the canonical entity key, the
platform-verified result revision, and the selected cycle. The host writes it
outside the sandbox.

This ADR aims to let the next cycle's harness recognize absorbed updates
without adding a runner skip and without trusting content the sandbox can
write.

## Options

| Option | Why not chosen |
|---|---|
| Runner-level skip of absorbed records | ADR 0144 rules out a second handled-state gate |
| A receipt comment posted under the agent's App identity | The agent's own output is posted under that identity, and a `bot_role` "does not make the bot's content trustworthy" ([ADR 0107](0107-bot-identity-resolution-for-dispatch-authorization.md)) |
| A receipt comment the host posts after the run with the workflow job token | The host writes the comment, it needs no new read credential, and the receipt is visible on the entity. However, every workflow job in the repository with a write token posts under the same `github-actions` login, so comment authorship cannot tie a receipt to this harness's run |
| **A field of the host-written ADR 0144 final run metadata** (chosen) | — |

## Decision

We adopt an absorbed-steer receipt, written as a field of the host-written
ADR 0144 final run metadata, to let the next cycle's pre-script recognize the
updates a steered run absorbed.

Fullsend adds no runner skip.

### What is written

When a run with at least one acknowledged steer publishes its final metadata,
the host adds a receipt field containing:

- the issuing run's identity, harness, repository, and canonical entity key;
- for each steered cycle: the cycle, its outcome and eligibility, the steer
  count, and the `offered_heads` the steers delivered, for audit only;
- for each record key, one entry: the SHA-256 of the filtered record file as
  last delivered (steer envelope v1 `records[].sha256`), and the cycle and
  steer that delivered it.

**Eligibility.** A steered cycle counts as evidence of handled work only if it
completed successfully and its output was published: validation passed and
the post-script reported that it published the result rather than discarding
it (for example, as a stale head). If a cycle fails, is cancelled, times out,
cannot be verified, or has its output discarded, its acknowledgements are kept
for audit but marked ineligible, and its records and head moves stay open for
follow-up work.

**No head receipts in v1.** A receipt never lists a head as absorbed. A
review's report of the head it worked on is the agent's claim, and a fix that
built on a pushed head publishes a newer head, so neither can match the
entity's current head. The follow-up cycle always runs for a pushed head.

**One digest per record key.** A later steer of the same record key, in the
same cycle or a later cycle, replaces the earlier entry. Because only the
latest digest is kept, an edit that reverts a record to an earlier text never
matches a superseded digest. If that latest steer was not acknowledged, or its
cycle is ineligible, the record key is not absorbed, whatever an earlier steer
delivered.

**Carry forward.** A run that reads a verified receipt and runs no cycle for
those updates, because its pre-script skipped them, copies the still-verified
entries into its own final metadata, keeping the issuing run's identity. The
next run then finds them even though the skipping run steered nothing.

### Why it cannot be forged

**The metadata is host-only.** The host writes it outside the sandbox. On
GitHub Actions the `actions/upload-artifact` step publishes it after `fullsend
run` exits, with the Actions runtime credential that the runner gives that
step. Fullsend never passes that credential into the sandbox.

**A host-enforced path separates it from agent output.** The artifact carries
both the metadata and the agent's output, so what keeps them apart is a path
the host enforces, not the artifact itself:

- The receipt is valid only at the host's metadata path. A reader ignores any
  receipt-shaped file elsewhere in the artifact, including the agent's output
  tree.
- The host copies agent output only into a separate output subtree and never
  lets it reach the metadata path.
- The post-script runs before the host writes the metadata. The host writes
  the metadata last and replaces any file already at that path, so neither the
  agent nor the post-script can plant one. Only Fullsend's own host steps run
  between that write and the upload.

**Provenance comes from the platform.** The reader checks the producing run
against the platform's metadata for that run, never against fields inside the
receipt:

- The platform metadata gives the run's repository, workflow file, event type,
  and the ref and revision whose workflow code ran.
- The reader accepts only a run whose workflow code came from the default
  branch that the current run also uses. It ignores runs from forks, runs whose
  workflow code came from another branch or a pull request ref, and runs whose
  provenance it cannot verify. When it ignores a receipt, the cycle does the
  work.
- Triggers that run workflow code from a pull request ref, such as
  `pull_request_review` today, therefore never produce an accepted receipt.
  Fix runs started that way get receipts only once the shim moves those runs
  to default-branch workflow code.
- The producer must be this harness's own Fullsend workflow: the same
  workflow file the current run uses, and an artifact uploaded by this
  harness's job in that run. An artifact from any other workflow or job is
  ignored, even when its name, path, and receipt fields match.
- The receipt's harness, repository, and canonical entity must also match the
  cycle being checked.

Trust comes from this host-only write path and the producing run's platform
metadata, not from who authored a forge comment.

### Reading and admitting it

- **Which run.** Before the pre-script runs, the host first keeps only runs
  that pass the provenance rules above, then reads the receipt from the latest
  of them for the same canonical entity in which this harness's job completed
  and uploaded its `fullsend-<agent>` artifact. A branch or pull-request-ref
  run therefore never hides an earlier valid receipt. Runs for other
  entities, and runs in which this harness's job was skipped or cancelled,
  are passed over. Carry forward means a run that skipped its cycle does not
  hide an earlier receipt from later runs.
- **Within one run.** Within one resident `fullsend run` (ADR 0144), a later
  cycle reads the receipts of the earlier cycles from host-private run state,
  never from the sandbox or the output tree. The same eligibility, digest, and
  head checks apply, and the final artifact still carries the receipt for later
  runs.
- **Eligible cycles only.** Only records from an eligible cycle are listed as
  absorbed.
- **Digest check.** The host re-derives each absorbed record's current
  filtered file and checks its SHA-256. Any record that changed, is missing,
  or cannot be verified is not listed as absorbed, so the next cycle runs for
  it.
- **Pre-script only.** The host stages the verified receipt in the snapshot's
  `agent-runs` history, where the pre-script reads it as ADR 0098 allows.
  Harness CEL cannot see it, because CEL evaluates the normalized entity and
  event before the snapshot exists. Exposing receipts to CEL would need the
  same fields in the normalized-entity contract, which is not part of this
  decision.
- **Snapshot v2.** The closed
  [entity-context-snapshot v1](../normative/entity-context-snapshot/v1/README.md)
  `agent-runs` schema has no field for a receipt. Receipts therefore need
  entity-context-snapshot v2, which adds: absorbed record key and digest,
  cycle outcome and eligibility, steer count, `offered_heads`, and issuing run
  identity.
- **Fail open.** Metadata that is missing, unreadable, or mismatched is
  ignored, and the cycle does the work.

### What a receipt does not prove

An acknowledgement proves the message arrived, not that the agent acted on it.

- **Records.** A pre-script that skips work on a receipt relies on its agent
  acting on acknowledged steers. A harness whose agent does not act on the
  steer envelope must not skip on receipts.
- **Heads.** `offered_heads` is audit only, and an agent's report of the head
  it worked on is not proof. No head is ever listed as absorbed, so a steered
  head never lets a follow-up cycle skip that head.

## Forge scope

- **GitHub:** GitHub Actions, from the workflow run's `fullsend-<agent>`
  artifact.
- **GitLab:** not supported yet. The design needs host-written run metadata
  that a later job can read back (for example a job artifact); no equivalent
  read path is designed, so every steered cycle is followed by a full cycle.
- **Forgejo:** not supported yet, for the same reason as GitLab.
- **Jira:** not applicable. Entity-context-snapshot v1 does not stage Jira
  entities, so they do not steer (ADR 0117).

## Consequences

- A harness whose pre-script reads receipts skips redundant follow-up cycles
  after absorbed records; pushed heads always get a follow-up cycle. A harness
  that does not read receipts still runs them all, which stays correct.
- Receipt trust rests on the host-only write path of the run metadata, the
  producing run's platform metadata, and a per-record digest check, not on
  forge identity, agent reports, or content filtering.
- Reading the prior run's artifact needs a host-only credential with
  `actions: read`, which main does not provide today. The run-record signal in
  ADR 0119 (proposed) needs the same kind of credential. Neither credential
  reaches the sandbox.
- The receipt is not visible on the entity; it is read from the artifact, so a
  person checking why a cycle skipped work looks at the prior run.
- Receipts work only on GitHub Actions (see Forge scope), and only while the
  platform keeps the prior run's artifact. If the artifact has expired, the
  next cycle does the work.
- Receipts wait on the ADR 0144 final metadata and entity-context-snapshot v2;
  until both exist, every steered cycle is followed by a full cycle.
- Auto-merge and approval gates keep comparing the cycle's platform-verified
  result revision with live state; a receipt never advances that revision.
