---
title: "82. Semantic observability and improvement loop"
status: Accepted
relates_to:
  - operational-observability
  - testing-agents
  - cross-run-memory
topics:
  - observability
  - harness
  - retro
  - evaluation
---

# 82. Semantic observability and improvement loop

Date: 2026-06-16

## Status

Accepted

## Context

[ADR 0021](0021-jsonl-reasoning-trace-exposure.md) gives fullsend per-run JSONL
transcripts — the full prompt/completion/tool record. That is necessary for
debugging and replay, but expensive to scan at factory scale and weak for
trend questions: stuck agents, tool loops, phase drift, recurring failure modes.

[ADR 0050](0050-distributed-tracing-instrumentation.md) decides how fullsend
produces structured traces — local run telemetry, optional OTLP export, and
W3C trace context across multi-agent pipelines. Derived signals and the
observer defined here consume that tracing layer; they do not replace it.

[operational-observability.md](../problems/operational-observability.md) calls
for structured traces, human feedback, and replay; [testing-agents.md](../problems/testing-agents.md)
calls for golden-set regression and behavioral monitoring. The retro agent
([#131](https://github.com/fullsend-ai/fullsend/issues/131),
[docs/agents/retro.md](../agents/retro.md)) closes the loop after a workflow
ends: it reconstructs the forge-facing workflow graph (issue/PR timelines,
reviews, run logs) and files structured improvement issues. That product is
deliberately narrative and triage-oriented. It is not planned to become a
fleet query tool over OTel backends, a mid-pipeline scorecard stage, or a
gated path for observer-style forge interventions (comments/labels) — those
gaps are why this ADR introduces a separate observer capability (see Decision
§2).

"Shadow mode" elsewhere means repo-level autonomy probation
([autonomy-spectrum.md](../problems/autonomy-spectrum.md)), shadowed
team-selection policies ([adaptive-agent-selection.md](../problems/adaptive-agent-selection.md)),
and open questions in [governance.md](../problems/governance.md) and
[production-feedback.md](../problems/production-feedback.md). **This ADR's
shadow mode is narrower:** gating **observer-initiated forge writes** before
they hit the forge. Same gradual-trust idea; not a shared global switch with
those other uses unless a later ADR unifies them.

We took inspiration from an external long-running agent platform ([The Darwin
Project](https://github.com/The-Darwin-Project/Blackboard)) that separates
**raw LLM traces** from **semantic run signals**,
**read-only observation**, **shadowed interventions**, and **structured lesson
extraction**. This ADR adapts those ideas to fullsend's batch, sandbox-scoped
execution model.

OTel-compatible LLM observability platforms can already store tool calls,
trace metadata, sessions, and backend-native annotations. The gap is not
another raw trace format — it is **derived signals**: fullsend-specific
heuristics that no generic backend infers on its own.

## Options

### Option A: Artifact-only enrichment

Compute derived signals post-run and store them only in GHA artifacts alongside
JSONL. No external observability backend.

**Trade-offs:** Works offline and matches ADR 0021's artifact model. Poor for
fleet-wide trends, cross-run queries, and cost dashboards at org scale.

### Option B: External trace backend only

Export JSONL as traces to an OTel-compatible LLM observability platform. Rely on
that platform for tool spans, metadata, and annotations. No local signal artifact.

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

Adopt option C. Introduce four layered capabilities on top of JSONL extraction
and the traces produced by [ADR 0050](0050-distributed-tracing-instrumentation.md):

### 1. Derived signals

A host-side enricher emits **derived signals** — compact, machine-readable
indicators beyond raw prompt/completion pairs — primarily from **ADR 0050 OTel
span/attribute output** (runtime-agnostic). Optionally, for the Claude runtime
only, it may also read that runtime's `stream-json` transcript (via the existing
`TranscriptHandler` pattern) for heuristics that need mid-run or
transcript-shaped cues. Other runtimes need their own transcript adapter or rely
on spans alone.

Examples:

- Tool and phase markers (largely redundant with trace spans; useful for
  artifact-only consumers)
- Fullsend-specific patterns: repeated tool use, validation retry loops,
  defer/monitoring velocity, stuck-run heuristics

Signals attach to the run's trace record via backend-native annotation
mechanisms (tags, metadata, span events, or vendor scoring primitives — exact
mapping deferred with backend selection). An optional artifact mirror uses the
same schema for offline consumers when no backend is configured.

By default, derived signals use **Level 1/2 metadata** only. If computation
needs Level 3 content (prompts/completions), ADR 0050's explicit content-capture
opt-in still applies; this ADR does not loosen it.

Derived-signal computation and **export to an external observability backend**
honor the same per-agent JSONL-suppression declaration as
[ADR 0021](0021-jsonl-reasoning-trace-exposure.md). When an agent opts into
suppression, derived signals for that agent are either omitted or restricted to
the **artifact-only mirror**; they are not sent to a third-party/SaaS backend.

JSONL remains the forensic source ([ADR 0021](0021-jsonl-reasoning-trace-exposure.md)).
Derived signals are enrichments, not a replacement.

### 2. Observer (read-first)

A host-side **observer** analyzes a run or workflow using trace backend APIs
and/or local JSONL and signals:

- **v1:** Post-run, read-only analysis producing a human-readable report. No
  write access to the forge.
- **v2 (optional):** Harness stage between pipeline steps that emits a
  scorecard only (e.g. escalate-to-human), still read-only.

**Relationship to retro — why a separate observer:** The observer is a **new
capability**, not a rename or replacement of the retro agent. Retro remains the
default post-workflow agent: forge-facing, issue-proposing, workflow-graph
oriented ([docs/agents/retro.md](../agents/retro.md)). Observer v1 is
**read-only analysis over traces/signals** (backend APIs and/or local JSONL +
derived signals). Value retro does not provide and is not planned to provide:

- **Observability-backend / fleet queries** — cross-run trends, cost and pattern
  aggregations, and stuck-run heuristics over OTel (or local signal mirrors),
  rather than reconstructing one workflow from forge timelines and run logs.
- **Trace/signal-first product** — compact reports for operators and dashboards;
  not structured GitHub issues that enter triage.
- **Optional mid-pipeline scorecard (v2)** — a harness stage between steps;
  retro stays post-workflow / on-demand (`/fs-retro`) only.
- **Later shadowed forge interventions** — comments/labels/etc. gated by §3;
  distinct from retro's existing post-script issue-creation path.

Retro may later *consume* observer reports or derived signals as better input;
that wiring is a follow-up, not decided here. Lesson extraction (§4) is a
**separate step** that may use retro output, observer output, or both — it is
not owned by the observer in v1.

CLI naming, harness wiring, and report format are implementation details
deferred to a follow-on ADR.

### 3. Shadow mode for observer actions

When the observer gains write capabilities (forge comments, labels, issues),
they are **shadowed by default**:

- Shadow on: proposed forge actions are **recorded but not delivered** (exact
  record schema deferred to implementation).
- Shadow off: delivery requires an **explicit allowlist** of permitted actions
  (mechanism — env, harness field, or org policy — deferred).

This de-risks rollout before any automated intervention ships.

### 4. Lesson extraction

After retro and/or an observe report, a step extracts **structured lessons** —
title, pattern, anti-pattern, keywords, and references to the originating run
or PR.

Lessons are proposed into the config repo via PR for human review, then
consumed by the eval harness ([testing-agents.md](../problems/testing-agents.md))
as golden-set cases. Narrative retro issues remain; lessons are searchable,
testable memory. Schema and storage location are deferred; git is the source
of truth for reviewed lessons.

### Rollout Order

1. Derived-signal enricher + trace export
2. Observer read-only report
3. Shadow action log
4. Lesson extraction wired to retro and/or observe output

### Non-Goals

- Replacing JSONL traces or ADR 0021 security model
- A bespoke trace store parallel to OTel-compatible backends
- Real-time long-lived observer sessions inside sandboxes
- Mandating a specific observability vendor or eval platform in this ADR
- Treating a Claude-only `stream-json` enricher as the portable multi-runtime
  contract (same reason ADR 0050 rejected vendor-specific trace formats)
- Replacing or subsuming the retro agent

## Consequences

**Easier:**

- Operators and retro agent reason over signals, not full transcripts
- One query surface for tools, costs, patterns, and annotations when a backend
  is configured
- Safe path to automated suggestions before live forge actions
- Closed loop from production runs → lessons → regression tests
- Correlation across multi-agent workflows via shared run and workflow
  identifiers on traces
- Clear split: retro owns forge-facing post-mortems; observer owns
  trace/signal analysis and future gated interventions

**Harder:**

- Observability backend deployment and credential management per org
- Signal heuristics need tuning to avoid noisy or misleading annotations
- Hybrid mode requires keeping artifact mirror and backend schema aligned
- Lesson quality depends on human review discipline in the config repo
- Lesson candidates are agent-authored interpretations of run content that may
  itself be adversarial (see
  [cross-run-memory.md](../problems/cross-run-memory.md)). Human review before
  merge into the config repo is the required mitigation; ungated auto-merge of
  lessons is out of scope

**Follow-ups:**

- ADR or design doc for trace-backend selection and export format
- Harness layout for signal artifacts, shadow logs, and lesson files
- Per-SIG dashboards and aggregation on trace annotations and tags
- Observer write tools and action allowlist policy
- Optional wiring for retro to consume observer reports / derived signals
