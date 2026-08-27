---
title: "92. Resume agent sessions from JSONL transcripts"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
  - cross-run-memory
topics:
  - session
  - transcript
  - sandbox
  - dispatch
  - jsonl
---

# 92. Resume agent sessions from JSONL transcripts

Date: 2026-08-27

## Status

Accepted

Builds on JSONL exposure
([ADR 0021](0021-jsonl-reasoning-trace-exposure.md)), ephemeral sandboxes
([ADR 0016](0016-unidirectional-control-flow.md),
[ADR 0036](0036-agent-execution-sandbox.md)), and is distinct from the
conversation surface
([ADR 0086](0086-conversation-surface-for-agent-participation.md)).

Motivated by the 2026-08-26 contributors meeting and
[#459](https://github.com/fullsend-ai/fullsend/issues/459).

## Context

Every fullsend dispatch starts a new ephemeral sandbox and rebuilds agent
context from the work item. That is the right isolation default, but it is
expensive for small follow-ups: a `/fs-fix` that changes one line still pays
the full cold-start token cost.

Keeping the GitHub Action alive until a human replies is not viable (job
timeouts, idle compute). Cross-run memory is a different problem
([cross-run-memory.md](../problems/cross-run-memory.md)). Re-injecting forge
comments is also not a conversation tree.

JSONL transcripts are already extracted
([ADR 0021](0021-jsonl-reasoning-trace-exposure.md)). Runtimes already resume
from them (`claude --resume`, Pi's session tree). Loading a CI artifact into
a local OpenShell sandbox has been demonstrated.

## Options

### A. Keep the process alive

Hold the runner until the human replies. Rejected: timeouts, idle cost, and
it does not survive "an hour later."

### B. Cross-run memory / third-party session store

Persist lessons or session blobs in a new store. Rejected: poisoning,
staleness, and a second instruction channel. Resume is not memory.

### C. Re-inject forge comments only

The next run reads issue/PR comments as today. Loses tool-call history and
KV-cache continuity. Already the default; not a continuation.

### D. Restore the JSONL conversation tree into a new sandbox (chosen)

Start a new ephemeral run whose runtime session is the prior transcript
(optionally forked at a turn). Sandbox filesystem and process state are not
restored; the repo is cloned fresh.

## Decision

Adopt **Option D**.

Session continuation means: **replay a prior run's JSONL transcript as the
starting conversation of a new ephemeral sandbox**, scoped to the **same
agent** and **same work item**.

- The sandbox stays ephemeral. No process, volume, or third-party memory
  store survives the first run.
- The transcript is an **input**, like issue text: untrusted, access-controlled
  by [ADR 0021](0021-jsonl-reasoning-trace-exposure.md). Suppressed JSONL
  cannot be resumed.
- Provider prompt-cache hits are an optimization, not a requirement. Resume
  must work even when the cache is cold.
- Scratch remains the default for unlabeled dispatch. Compacting, starting
  from scratch, and resuming are distinct patterns; this ADR only adds
  resume.
- Cross-role consumption of another agent's JSONL is not resume; it stays
  under [cross-run-memory.md](../problems/cross-run-memory.md).
- Exact trigger UX (slash flag vs dedicated command) and local vs CI
  packaging are follow-on implementation. Both surfaces use this model.
  Local resume is tracked in
  [#459](https://github.com/fullsend-ai/fullsend/issues/459).

## Consequences

- Cheap follow-ups (`/fs-fix` "remove that line", interactive skills that
  wait on a human reply) can reuse the prior conversation instead of
  rebuilding context.
- Injection surface grows by one hop: a poisoned first run's transcript
  becomes context for the second. Same-agent, same-work-item, explicit
  trigger, and ADR 0021 access control bound that hop.
- Rebuilt sandboxes may differ (image, tools, HEAD). Agents must tolerate
  that; pinning the environment is a later choice.
- Long sessions may still need compaction — a later choice, not a
  requirement of resume.
