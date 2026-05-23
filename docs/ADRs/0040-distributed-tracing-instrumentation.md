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

Two approaches were evaluated:

1. **Framework-native (this approach):** Instrument the CLI itself so every
   run produces structured telemetry as a side effect of execution.
2. **Adopter-side post-hoc:** Parse CLI stdout and artifact files after the
   run completes and construct spans externally.

The adopter-side approach demonstrated the value of LLM-aware backends (Arize
Phoenix, MLFlow) but suffers from fragile stdout parsing and cannot capture timing or
intermediate state reliably. The framework-native approach produces telemetry
at the source, with correct timing, parent-child relationships, and W3C trace
context propagation across dispatched runs.

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
  from the parent workflow, creating cross-run trace correlation.
- **Unified InstrumentedPrinter:** A single component that atomically
  produces both terminal UI output and telemetry events, making it
  structurally impossible to have a UI step without a corresponding span.
- **OTEL GenAI semantic conventions:** Root and iteration spans carry
  `gen_ai.operation.name`, `gen_ai.agent.name`, `gen_ai.request.model`,
  and `gen_ai.system` so LLM-aware backends recognize them as agent runs.
- **SpanKind signaling:** Root span is `Consumer` when `TRACEPARENT` is
  present (dispatched run), `Internal` otherwise.
- **Regression gates:** CI tests enforce that all lifecycle steps use the
  unified instrumentation path.

## Consequences

- Operators get structured observability for free — no configuration needed
  for the local baseline (`run-events.jsonl` + `run-summary.json`).
- Any OTLP-compatible backend (Jaeger, Tempo, Phoenix, MLFow, Langfuse, SigNoz)
  works with a single environment variable.
- Cross-run correlation works out of the box for dispatched pipelines via
  W3C `TRACEPARENT` propagation.
- The `InstrumentedPrinter` pattern means new lifecycle steps added to the
  CLI automatically appear in traces — contributors cannot accidentally
  skip telemetry.
- The `gen_ai.*` attributes are experimental (OTEL semconv status); they
  may change in future OTEL releases and will need updating.
