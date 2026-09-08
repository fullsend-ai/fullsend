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
they are handling, including comments, reviews, diffs, checks, and logs. Those
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

The pre-script may inspect the host snapshot and skip the run. It cannot mutate
the agent's view: Fullsend verifies the manifest digests before upload and
restores or rejects changed files. The sandbox copy is read-only to the agent.
Agent prompts should point to `summary.md` and instruct the agent to open only
the files needed for its task; runtime forge reads remain an explicit fallback
for data outside the entity snapshot, not the default way to obtain it.

The host snapshot uses a mode-`0700` directory and mode-`0600` files. Fullsend
removes the sandbox copy after its last sandbox consumer and the host copy after
the post-script, on success, failure, skip, or handled cancellation; startup
also scavenges orphaned context directories after abnormal termination. Context
is excluded from retained run artifacts by construction. Diagnostics may retain
only bounded counts, digests, and filtering findings, never bodies, diffs, or
logs.

## Consequences

- Agents start with a consistent, filtered view of entity content and need fewer forge tool calls and prompt tokens.
- Pre-scripts, agents, validation, and post-scripts share one versioned relative-path contract without putting generated input in Git.
- Snapshot assembly adds startup latency and ephemeral storage, bounded by per-entry and total-size limits.
- A snapshot can become stale during a run, so outputs that mutate forge state must still validate relevant revisions in deterministic post-processing.
- Forge adapters must expose the snapshot inputs through `forge.Client`; platform-specific gaps are explicit manifest errors rather than silent omissions.
