---
title: "107. Deterministic filtered entity-context staging"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - entity-context
  - security
  - harness
  - token-cost
---

# 107. Deterministic filtered entity-context staging

Date: 2026-09-07

## Status

Accepted

## Context

[Issue #6407](https://github.com/fullsend-ai/fullsend/issues/6407) identifies
that agents and harness scripts repeatedly fetch the issue or change proposal
they are handling, including comments, reviews, checks, and logs. Those
tool calls spend tokens, make runs depend on runtime network access, and give
each consumer a different view when the entity changes during a run.

Forge content is also untrusted input. Fetching it directly from inside the
sandbox bypasses the deterministic point where Fullsend can bound, normalize,
redact, and label content before it reaches an agent. This decision generalizes
the preferred prefetch model from
[ADR 0017](0017-credential-isolation-for-sandboxed-agents.md) to every issue and
change-proposal run.

## Decision

Before the harness pre-script, `fullsend run` uses `forge.Client` to assemble
one immutable snapshot of the handled entity. The runner applies a mandatory,
deterministic content pipeline: size limits, Unicode safety normalization,
secret and sensitive-data redaction, and injection scanning. It fails closed
when required data cannot be fetched or safely represented. Filtered content
is the only copy exposed to scripts and the agent; the manifest records every
truncation, replacement, finding, and fetch error without retaining rejected
content.

The snapshot is written outside the repository clone. Host-side pre- and
post-scripts receive `FULLSEND_CONTEXT_DIR` pointing to
an access-restricted temporary directory outside the retained run-output
tree; inside the sandbox the same variable points to
`/sandbox/workspace/context`. Fullsend uploads that directory after sandbox
creation and before repository/runtime execution. Consumers therefore use the
same variable and relative paths on both sides of the sandbox boundary, and
the context cannot be staged or committed accidentally with repository files.

The exact tree, schemas, canonical serialization, stable record-key derivation,
filter statuses, and compatibility rules are the versioned
[entity-context v1 specification](../normative/entity-context/v1/README.md).
Content records and mutable observation state are separate: resolving or
reordering a thread changes its state/index files, never an unchanged comment
or review body. No runner-clock timestamp enters the staged tree. Given the
same forge state and filter version, implementations produce the same paths
and bytes; breaking that guarantee requires a new major specification.

The snapshot contains forge state that cannot be reconstructed from the target
Git checkout. It does not copy diffs, commit history, changed-file manifests,
or repository revision metadata. Fullsend provisions sufficient Git objects and
refs separately; controllers derive and filter diffs or commit projections for
agents and sub-agents that need them. Git object IDs appear in entity context
only to relate reviews, threads, comments, and agent runs to repository state.

Each comment and review is a self-contained attributed record whose filename
sorts chronologically. The initial body uses the same record format and sorts
first. Order files define whole-conversation and review-focused projections and
refer to the same records rather than copying content. New replies append
without rewriting unchanged record files. Review-thread relationships, commit
references, and immutable Fullsend agent-run receipts preserve enough provenance
to relate a finding, the reviewed revision, a subsequent fix, and a re-review.

The pre-script may inspect the host snapshot and skip the run. It cannot mutate
the agent's view: Fullsend verifies the manifest digests before upload and
restores or rejects changed files. The sandbox copy is read-only to the agent.
When a runtime injects staged context into a model request, it emits each
ordered record as a distinct content block, followed by relationship and
mutable-state blocks and then run-specific instructions. It must not collapse
the records into one changing prompt block when cache reuse is intended.
Provider prompt caching remains an optimization, not a conformance guarantee;
agents that read records through tools still pay the corresponding tool-result
tokens. `summary.md` and deterministic projections remain navigation aids for
selective reads. Runtime forge reads are an explicit fallback for data outside
the snapshot, not the default way to obtain it.

The host snapshot uses a mode-`0700` directory and mode-`0600` files. Fullsend
removes the sandbox copy after its last sandbox consumer and the host copy after
the post-script, on success, failure, skip, or handled cancellation; startup
also scavenges orphaned context directories after abnormal termination. Context
is excluded from retained run artifacts by construction. Diagnostics may retain
only bounded counts, digests, and filtering findings, never bodies or logs.

## Consequences

- Agents start with one filtered, versioned view and avoid duplicate forge reads, but token and provider-cache savings are conditional on selective projections and segmented runtime injection rather than automatic consequences of staging files.
- Fullsend must provision the Git objects and refs required by each run, let controllers derive and filter repository projections on demand, and add segmented context input, cache-boundary support, and efficiency telemetry before claiming an improvement.
- Shipped and custom review, fix, and code agents must migrate from mutable sticky summaries and single-body hand-offs to discovered entity-context projections, publish immutable per-run result receipts while retaining human-facing summaries, and anchor decisions to record keys and revisions.
- Forge adapters must expose review/reply relationships, locations, reviewed-revision references, checks, and immutable agent-result references through `forge.Client`; unavailable or unrecoverable forge history is an explicit manifest gap, not a silent omission.
- Snapshot assembly adds bounded startup latency and storage and can become stale, so collection profiles avoid indiscriminate log/history fetching and deterministic post-processing still validates relevant revisions before any forge mutation.
