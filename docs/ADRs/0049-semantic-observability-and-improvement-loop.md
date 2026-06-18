---
title: "49. Semantic observability and improvement loop"
status: Accepted
relates_to:
  - operational-observability
  - testing-agents
topics:
  - observability
  - harness
  - retro
  - evaluation
---

# 49. Semantic observability and improvement loop

Date: 2026-06-16

## Status

Accepted

## Context

[ADR 0021](0021-jsonl-reasoning-trace-exposure.md) gives fullsend per-run JSONL
transcripts — the full prompt/completion/tool record. That is necessary for
debugging and replay, but expensive to scan at factory scale and weak for
trend questions: stuck agents, tool loops, phase drift, recurring failure modes.

[operational-observability.md](../problems/operational-observability.md) calls
for structured traces, human feedback, and replay; [testing-agents.md](../problems/testing-agents.md)
calls for golden-set regression and behavioral monitoring. The retro agent
([#131](https://github.com/fullsend-ai/fullsend/issues/131)) closes the loop
after a workflow ends, but its input today is mostly raw JSONL.

We took inspiration from an external long-running agent platform ([The Darwin
Project](https://github.com/The-Darwin-Project/Blackboard)) that separates
**raw LLM traces** from **semantic run signals**,
**read-only observation**, **shadowed interventions**, and **structured lesson
extraction**. This ADR adapts those ideas to fullsend's batch, sandbox-scoped
execution model.

OTel-compatible LLM observability platforms can already store tool calls,
trace metadata, sessions, and custom scores. The gap is not another raw trace
format — it is **derived signals**: fullsend-specific heuristics computed from
stream-json that no generic backend infers on its own.

## Options

### Option A: Artifact-only enrichment

Compute derived signals post-run and store them only in GHA artifacts alongside
JSONL. No external observability backend.

**Trade-offs:** Works offline and matches ADR 0021's artifact model. Poor for
fleet-wide trends, cross-run queries, and cost dashboards at org scale.

### Option B: External trace backend only

Export JSONL as traces to an OTel-compatible LLM observability platform. Rely on
that platform for tool spans, metadata, and scores. No local signal artifact.

**Trade-offs:** Strong query and dashboard surface. Retro and offline debugging
depend on backend availability and org credentials.

### Option C: Hybrid (recommended)

Export traces to an OTel-compatible backend as the primary query surface.
Optionally mirror derived signals to run artifacts when the backend is
unavailable. Same signal schema in both sinks — not two data models.

**Trade-offs:** Adds integration and credential management. Preserves ADR 0021
forensic fidelity and offline retro fallback.

Backend vendor selection (e.g. Langfuse, Phoenix, self-hosted OTel collector) is
deferred to a follow-on decision. See
[operational-observability.md](../problems/operational-observability.md).

## Decision

Adopt option C. Introduce four layered capabilities on top of JSONL extraction:

### 1. Derived signals

A host-side enricher parses stream-json during and after each run and emits
**derived signals** — compact, machine-readable indicators beyond raw
prompt/completion pairs. Examples:

- Tool and phase markers (largely redundant with trace spans; useful for
  artifact-only consumers)
- Fullsend-specific patterns: repeated tool use, validation retry loops,
  defer/monitoring velocity, stuck-run heuristics

Signals are attached to the run's trace record (metadata, child spans, or
named scores) on the observability backend. An optional artifact mirror uses
the same schema for offline retro when no backend is configured.

JSONL remains the forensic source ([ADR 0021](0021-jsonl-reasoning-trace-exposure.md)).
Derived signals are enrichments, not a replacement.

### 2. Observer (read-first)

A host-side **observer** analyzes a run or workflow using trace backend APIs
and/or local JSONL and signals:

- **v1:** Post-run, read-only analysis producing a human-readable report. No
  write access to the forge.
- **v2 (optional):** Harness stage between pipeline steps that emits a
  scorecard only (e.g. escalate-to-human), still read-only.

The observer is an analyst, not a fixer — consistent with the retro agent
design ([#131](https://github.com/fullsend-ai/fullsend/issues/131)).

CLI naming, harness wiring, and report format are implementation details
deferred to [ADR 0024](0024-harness-definitions.md) or a follow-on ADR.

### 3. Shadow mode for observer actions

When the observer gains write capabilities (forge comments, labels, issues),
they are **shadowed by default**:

- Shadow on: proposed actions are recorded with `delivered: false`; nothing
  reaches the forge.
- Shadow off: explicit per-harness allowlist of permitted actions.

Configuration mechanism (env var, harness field, or org policy) is deferred.
This de-risks rollout before any automated intervention ships.

### 4. Lesson extraction

After retro or observe, a step extracts **structured lessons** — title, pattern,
anti-pattern, keywords, and references to the originating run or PR.

Lessons are proposed into the config repo via PR for human review, then
consumed by the eval harness ([testing-agents.md](../problems/testing-agents.md))
as golden-set cases. Narrative retro issues remain; lessons are searchable,
testable memory. Schema and storage location are deferred; git is the source
of truth for reviewed lessons.

### Rollout order

1. Derived-signal enricher + trace export
2. Observer read-only report
3. Shadow action log
4. Lesson extraction wired to retro output

### Non-goals

- Replacing JSONL traces or ADR 0021 security model
- A bespoke trace store parallel to OTel-compatible backends
- Real-time long-lived observer sessions inside sandboxes
- Mandating a specific observability vendor or eval platform in this ADR

## Consequences

**Easier:**

- Operators and retro agent reason over signals, not full transcripts
- One query surface for tools, costs, patterns, and scores when a backend is
  configured
- Safe path to automated suggestions before live forge actions
- Closed loop from production runs → lessons → regression tests
- Correlation across multi-agent workflows via shared run and workflow
  identifiers on traces

**Harder:**

- Observability backend deployment and credential management per org
- Signal heuristics need tuning to avoid noisy or misleading scores
- Hybrid mode requires keeping artifact mirror and backend schema aligned
- Lesson quality depends on human review discipline in the config repo

**Follow-ups:**

- ADR or design doc for trace-backend selection and export format
- Harness layout for signal artifacts, shadow logs, and lesson files
  ([ADR 0024](0024-harness-definitions.md))
- Per-SIG dashboards and aggregation on trace scores and tags
- Observer write tools and action allowlist policy
