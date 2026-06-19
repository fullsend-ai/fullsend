---
title: "50. Framework-native distributed tracing with OpenTelemetry"
status: Accepted
relates_to:
  - operational-observability
topics:
  - observability
  - telemetry
  - opentelemetry
---

# 50. Framework-native distributed tracing with OpenTelemetry

Date: 2026-05-23

## Status

Accepted

## Context

Fullsend agent runs are opaque. When a multi-agent pipeline dispatches
triage → code → review, operators have no structured way to understand what
happened, how long each step took, or where a failure occurred. The
[operational observability](../problems/operational-observability.md) problem
doc identifies this as a first-order concern.

Fullsend is distributed to many organizations — not just our team. The
tracing design must be safe by default without requiring any configuration
from adopters. Setting an OTLP endpoint must never accidentally expose
sensitive content (prompts, source code, PII) to shared or SaaS backends.

Prior decisions that inform this one:

- [ADR 0021](0021-jsonl-reasoning-trace-exposure.md) — JSONL reasoning trace
  exposure (what traces contain, who can access them)
- [ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md) —
  scripted multi-agent pipeline whose cross-run correlation this enables
- [ADR 0022](0022-harness-level-output-schema-enforcement.md) — structured
  output schemas that `run-summary.json` complements

## Options

### A. Post-hoc parsing (rejected)

External tooling parses CLI stdout after runs to construct spans. Fragile:
stdout is not a stable contract, timing is approximate, and intermediate
state is lost. The early Arize Phoenix experiment confirmed this.

### B. Framework-native OpenTelemetry (accepted)

CLI emits OTEL spans at source. Zero-infrastructure baseline (local files),
one env var enables OTLP export. Backend-agnostic. Content capture requires
explicit opt-in per OTEL GenAI semantic conventions.

### C. Vendor-specific trace format (rejected)

A runtime-locked trace builder (e.g., Claude-specific). Breaks when fullsend
adds support for other runtimes (OpenCode, Gemini CLI). Not portable.

## Decision

Fullsend instruments the CLI natively using OpenTelemetry with a three-level
opt-in model:

**Level 1 — Local baseline (every install, zero config):**
- Every run produces `run-telemetry.jsonl` and `run-summary.json` in the output
  directory (uploaded as GHA artifacts alongside transcripts)
- Metadata only: span hierarchy, timing, token counts, tool names, errors
- No data leaves the runner. No backend required.

**Level 2 — OTLP export (org opts in by setting endpoint):**
- When `OTEL_EXPORTER_OTLP_ENDPOINT` is set, metadata spans export via
  OTLP/HTTP to the org's chosen backend
- Still metadata only — safe for any backend including shared/SaaS platforms
- Spans follow [OTEL GenAI Semantic Conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/)
  (`gen_ai.operation.name`, `gen_ai.agent.name`, `gen_ai.request.model`,
  `gen_ai.system`)
- W3C `TRACEPARENT` propagation enables cross-run correlation for dispatched
  pipelines; separate workflow runs require manual propagation

**Level 3 — Content capture (org explicitly opts in):**
- When `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` is set, full
  prompt/completion content is included in spans
- Org is responsible for ensuring their backend's access controls are
  appropriate for the content sensitivity
- Enables LLM-judge evaluation scorers that need to read agent reasoning

**Additional design properties:**
- Runtime-agnostic: any runtime satisfying a transcript contract (turns,
  tools, tokens, model, stop reason) gets span promotion
- If the OTLP endpoint is unreachable, the CLI continues normally — local
  files still produced, run is not affected
- Simultaneous export to multiple backends is achieved by deploying an
  [OTEL Collector](https://opentelemetry.io/docs/collector/) as the endpoint;
  the CLI exports to one OTLP endpoint, the Collector fans out

**Scope boundary:** This ADR decides how traces are *generated* and how
content sensitivity is handled. Agent quality evaluation (scoring, regression
detection, baselines) *consumes* trace data but is a separate architectural
concern. Choice of backend is an adopter decision, not a platform decision.

## Consequences

- Every org gets structured observability with zero configuration (local files)
- OTLP export is always safe to enable (metadata only by default)
- Content capture is an explicit second opt-in — prevents accidental exposure
  of proprietary code or PII to shared/SaaS backends
- Any OTLP-compatible backend works (Jaeger, Tempo, MLflow, Phoenix,
  Langfuse, SigNoz, Honeycomb, Datadog)
- Cross-run correlation via `TRACEPARENT` for dispatched pipelines
- GenAI-aware backends get agent dashboards without CLI changes
- Runtime-agnostic: adding new runtimes doesn't require new trace formats
- The `gen_ai.*` attributes follow experimental OTEL semantic conventions
  and may change in future OTEL releases

## Deferred to implementation

These items are in scope for the implementation phase, not this architectural
decision:

1. **Sub-agent recursive span expansion** — When an agent dispatches sub-agents
   via `tool:Agent` (e.g., review agent's 6 sub-agents), their turns should
   become nested span subtrees, not flat spans. The transcript contract must
   handle recursive agent invocations.

2. **Pre/post script span instrumentation** — Pre-scripts, post-scripts, and
   validation scripts do significant work but aren't addressed in span
   structure. Define whether the framework instruments their execution
   automatically or provides a contract for scripts to emit spans.

## Related issues

- [#294](https://github.com/fullsend-ai/fullsend/issues/294) — Define trace
  granularity and retention policy
- [#295](https://github.com/fullsend-ai/fullsend/issues/295) — Define
  quality metrics for autonomous software factory
- [#296](https://github.com/fullsend-ai/fullsend/issues/296) — Evaluate
  Langfuse deployment threshold vs structured logging
- [#2367](https://github.com/fullsend-ai/fullsend/issues/2367) — Add
  `fullsend.runtime` trace attribute for multi-runtime observability
- [#2368](https://github.com/fullsend-ai/fullsend/issues/2368) — Add
  `fullsend.harness.content_sha` trace attribute for config change correlation
