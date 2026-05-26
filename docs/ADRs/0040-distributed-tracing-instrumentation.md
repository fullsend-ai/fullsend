---
title: "40. Framework-native distributed tracing with OpenTelemetry"
status: Accepted
relates_to:
  - operational-observability
topics:
  - observability
  - telemetry
  - opentelemetry
---

# 40. Framework-native distributed tracing with OpenTelemetry

Date: 2026-05-23

## Status

Accepted

## Context

Fullsend agent runs are opaque. When a multi-agent pipeline dispatches a
triage agent, then a code agent, then a review agent, operators have no
structured way to understand what happened, how long each step took, or
where a failure occurred. The
[operational observability](../problems/operational-observability.md) problem
doc identifies this as a first-order concern.

Several prior decisions set the stage for this one:

- [ADR 0021](0021-jsonl-reasoning-trace-exposure.md) decided that JSONL
  reasoning traces are exposed from sandboxes with owner-scoped storage and
  credential scanning as defense-in-depth. That ADR addresses *what* traces
  contain and *who* can access them; this ADR addresses *how* structured
  telemetry is produced at the framework level.
- [ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md)
  established the scripted multi-agent pipeline (triage → code → review)
  whose cross-run correlation this ADR enables.
- [ADR 0022](0022-harness-level-output-schema-enforcement.md) established
  structured output schemas that `run-summary.json` complements with
  execution-level metadata.

The [operational observability](../problems/operational-observability.md)
problem doc identifies several open questions that this decision partially
addresses: the bootstrapping problem (how to get observability without
deploying infrastructure first) and the need for structured traces that
support both individual-run debugging and cross-run correlation.

### Approaches evaluated

Four approaches were considered along two dimensions: where telemetry
originates and what infrastructure is required.

```
  WHERE telemetry       WHAT backend is needed
  is produced
                    ┌────────────┐   ┌───────────────┐   ┌──────────────────┐
                    │  No backend │   │ General OTEL  │   │  LLM-aware OTEL  │
                    │  (files     │   │ (Jaeger,      │   │  (Phoenix, MLflow│
                    │   only)     │   │  Tempo, etc.) │   │   Langfuse)      │
  ──────────────────┼────────────┼───┼───────────────┼───┼──────────────────┤
  CLI produces      │            │   │               │   │                  │
  spans at source   │  A         │   │  B            │   │  B+             │
  (framework-native)│  Local     │   │  OTLP export  │   │  OTLP + GenAI   │
                    │  baseline  │   │               │   │  dashboards     │
  ──────────────────┼────────────┼───┼───────────────┼───┼──────────────────┤
  External tool     │            │   │               │   │                  │
  parses stdout     │  —         │   │  C            │   │  D              │
  after the run     │            │   │  Post-hoc     │   │  Post-hoc +     │
  (adopter-side)    │            │   │  span builder │   │  LLM platform   │
  ──────────────────┴────────────┴───┴───────────────┴───┴──────────────────┘
```

**A. Local baseline** — Every run writes `run-events.jsonl` (NDJSON) and
`run-summary.json` to the output directory. Zero infrastructure. Operators
`grep`, `jq`, or script against these files.

**B. Framework-native OTLP** — Everything in A, plus spans exported via
OTLP/HTTP when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. One env var turns
any general-purpose backend on.

**B+. Framework-native + LLM backend** — Same OTLP export pointed at a
backend that understands GenAI semantic conventions. The CLI's `gen_ai.*`
span attributes light up token cost rollups, prompt/completion inspection,
and agent-specific dashboards without any CLI-side config change.

**C. Post-hoc span builder** *(rejected)* — External tooling parses CLI
stdout after each run to construct spans. Fragile: stdout is not a stable
contract, timing is approximate, and intermediate state is lost.

**D. Post-hoc + LLM platform** *(rejected)* — Same as C, feeding an
LLM-aware backend. The early Arize Phoenix experiment used this approach.
It proved that GenAI dashboards are valuable, but confirmed that post-hoc
parsing is the wrong instrumentation point.

### Comparison

|                              | A. Local | B / B+. Framework OTLP | C. Post-hoc | D. Post-hoc + LLM |
|------------------------------|:--------:|:----------------------:|:-----------:|:------------------:|
| Infra needed                 | None     | OTEL backend           | OTEL backend| LLM platform       |
| Timing accuracy              | Exact    | Exact                  | ~Approx     | ~Approx            |
| Cross-run correlation        | Manual   | Automatic (W3C)        | Manual      | Manual             |
| Captures intermediate state  | Yes      | Yes                    | No          | No                 |
| Stable contract              | Yes      | Yes                    | No          | No                 |
| GenAI dashboards             | —        | Yes (B+ backend)       | —           | Yes                |
| Token/cost attribution       | —        | Yes (B+ backend)       | —           | Yes                |
| Survives CLI output changes  | Yes      | Yes                    | No          | No                 |
| Bootstrapping cost           | Zero     | 1 env var              | Custom glue | Custom glue        |

### Recommendation

**A + B combined** — the approach this ADR accepts. Every run always
produces local files (A). One env var enables OTLP export (B). Choosing an
LLM-aware backend (B+) activates GenAI dashboards with zero CLI changes.
This creates a zero-to-production gradient:

```
  Day 1              Day N               Day N+M
  ─────────────────────────────────────────────────
  run-events.jsonl    + OTLP to Jaeger    + MLflow/Phoenix
  run-summary.json      or Tempo            GenAI dashboards
  (grep, jq)            (trace UI)          (token costs, prompts)
       A ──────────────► B ──────────────► B+
```

Post-hoc approaches (C, D) are superseded. The early Phoenix experiment
(D) validated the value of GenAI backends, which informed the decision to
include `gen_ai.*` semantic conventions in the framework-native approach.

See the
[Distributed Tracing admin guide](../guides/admin/distributed-tracing.md#live-deployment-example)
for a worked example with live GitHub Actions runs.

## Decision

**Fullsend instruments the CLI natively using OpenTelemetry with a
zero-infrastructure baseline.**

The `internal/telemetry` package provides:

- **Always-on local output:** Every run produces `run-events.jsonl` (NDJSON
  structured events) and `run-summary.json` regardless of configuration.
  No collector or backend is required.
- **Optional OTLP export:** When `OTEL_EXPORTER_OTLP_ENDPOINT` or
  `FULLSEND_TELEMETRY=1` is set, spans are additionally exported via
  OTLP/HTTP to any compatible backend.
- **W3C trace context propagation:** Dispatched runs inherit `TRACEPARENT`
  from the parent workflow, creating cross-run trace correlation. The
  `work_item_id` attribute (`owner/repo#N`) enables querying all traces
  related to a single issue or PR across the full triage → code → review
  pipeline.
- **Unified InstrumentedPrinter:** A single component that atomically
  produces both terminal UI output and telemetry events, making it
  structurally impossible to have a UI step without a corresponding span.
  Early lifecycle steps (before the run directory exists) are buffered and
  replayed once the recorder attaches.
- **OTEL GenAI semantic conventions:** Root and iteration spans carry
  `gen_ai.operation.name`, `gen_ai.agent.name`, `gen_ai.request.model`,
  and `gen_ai.system` so LLM-aware backends recognize them as agent runs.
- **Transcript-to-span promotion:** Claude Code JSONL transcripts are
  parsed post-execution, and individual LLM turns are emitted as child
  spans with `gen_ai.content.prompt`, `gen_ai.content.completion`,
  `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, tool call
  metadata, and stop reason. This bridges the gap between the JSONL
  reasoning traces ([ADR 0021](0021-jsonl-reasoning-trace-exposure.md)) and
  structured OTEL spans.
- **SpanKind signaling:** Root span is `Consumer` when `TRACEPARENT` is
  present (dispatched run), `Internal` otherwise.
- **Regression gates:** CI tests (`telemetry_lint_test.go`) enforce that
  all lifecycle steps in `runAgent` use the `InstrumentedPrinter` path.
  Raw `printer.StepStart/StepDone/StepFail/StepWarn` calls and the legacy
  `recStep/recDone/recFail/recWarn` closures are both caught.

### Production backend

Traces are exported to an MLflow instance (`https://mlflow-35-212-57-52.nip.io`)
running on a GCP VM. MLflow ingests OTLP/HTTP traces and provides GenAI-aware
dashboards with token usage rollups. See
[Distributed Tracing admin guide](../guides/admin/distributed-tracing.md)
for configuration details and alternative backends.

## Consequences

- Operators get structured observability for free — no configuration needed
  for the local baseline (`run-events.jsonl` + `run-summary.json`). This
  addresses the
  [bootstrapping problem](../problems/operational-observability.md) identified
  in the observability problem doc: the factory gets observability before any
  infrastructure is deployed.
- Any OTLP-compatible backend (Jaeger, Tempo, Phoenix, MLflow, Langfuse,
  SigNoz, Honeycomb) works with a single environment variable.
- Cross-run correlation works out of the box for dispatched pipelines via
  W3C `TRACEPARENT` propagation and the `work_item_id` span attribute.
- The `InstrumentedPrinter` pattern means new lifecycle steps added to the
  CLI automatically appear in traces — contributors cannot accidentally
  skip telemetry.
- The `gen_ai.*` attributes follow the
  [OTEL GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/)
  which are experimental; they may change in future OTEL releases and will
  need updating.
- `run-summary.json` provides a machine-stable contract (versioned via
  `schema_version`) for downstream consumers — scripts, retro agents, and
  dashboards can ingest it without parsing CLI stdout.

## Related issues

- [#294](https://github.com/fullsend-ai/fullsend/issues/294) — Define
  trace granularity and retention policy (open; this ADR provides the
  local-first baseline but defers retention decisions)
- [#295](https://github.com/fullsend-ai/fullsend/issues/295) — Define
  quality metrics for autonomous software factory (open; traces provide
  the raw data these metrics will be computed from)
- [#296](https://github.com/fullsend-ai/fullsend/issues/296) — Evaluate
  Langfuse deployment threshold vs structured logging (open; this ADR's
  zero-config baseline is the "structured logging" phase, with OTLP export
  as the graduation path)
- [#637](https://github.com/fullsend-ai/fullsend/issues/637) — UI
  monitoring/status dashboard (open; can consume `run-summary.json` and
  OTLP data for agent-centric dashboards)
- [#896](https://github.com/fullsend-ai/fullsend/issues/896) — Emit
  source/destination annotations for agent workflow runs (open;
  complements tracing with GitHub-native resource correlation)
- [#1043](https://github.com/fullsend-ai/fullsend/issues/1043) — Add
  observability for review agent re-trigger failures (open; cross-run
  tracing via `TRACEPARENT` helps correlate the re-trigger chain)
