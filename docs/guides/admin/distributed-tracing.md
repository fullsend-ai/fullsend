# Distributed Tracing

Fullsend produces structured telemetry for every agent run. This guide covers
how to configure, consume, and extend the tracing system.

Decided in [ADR 0040](../../ADRs/0040-distributed-tracing-instrumentation.md).

## Zero-configuration baseline

Every `fullsend run` produces two files in the run output directory with no
configuration required:

- **`run-events.jsonl`** — NDJSON stream of lifecycle events (step starts,
  completions, failures, warnings) with timestamps, durations, and trace IDs.
- **`run-summary.json`** — Aggregated run summary including agent name, exit
  code, step timings, total duration, and a W3C `traceparent` value for
  downstream correlation.

These files are always written, even when no OTLP backend is configured.

## Enabling OTLP export

To send spans to an OpenTelemetry-compatible backend, set one of:

```bash
# Option 1: Set the standard OTEL endpoint (also enables telemetry implicitly)
export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4318"

# Option 2: Explicit enable (uses OTEL_EXPORTER_OTLP_ENDPOINT from env)
export FULLSEND_TELEMETRY=1
```

When enabled, spans are exported via OTLP/HTTP. Any backend that speaks OTLP
works: Jaeger, Grafana Tempo, Arize Phoenix, Langfuse, SigNoz, Honeycomb, etc.

If the endpoint is unreachable, the CLI continues normally — local files are
still produced and the run is not affected.

## Cross-run trace correlation

Multi-agent pipelines (triage → code → review) propagate trace context
automatically via the `TRACEPARENT` environment variable (W3C Trace Context).

When a workflow dispatches a run:

```yaml
env:
  TRACEPARENT: ${{ steps.parent.outputs.traceparent }}
```

The child run's root span becomes a child of the parent trace, creating a
unified view of the entire pipeline in your tracing backend.

The `run-summary.json` includes the `traceparent` value so downstream
consumers (scripts, other agents) can continue the trace chain.

## Span structure

A typical agent run produces this span hierarchy:

```
fullsend-run (root, SpanKind=Consumer if dispatched)
├── load-harness
├── setup-sandbox
│   └── create-sandbox (gen_ai.operation.name=create_agent)
├── agent-execution.iteration-0
│   └── (gen_ai.operation.name=invoke_agent)
├── agent-execution.iteration-1
├── collect-artifacts
├── security-scan
└── validation
```

### GenAI semantic conventions

Root and iteration spans carry [OTEL GenAI semantic convention](https://opentelemetry.io/docs/specs/semconv/gen-ai/) attributes:

| Attribute | Example | Description |
|-----------|---------|-------------|
| `gen_ai.operation.name` | `invoke_agent` | The GenAI operation type |
| `gen_ai.agent.name` | `triage` | The agent being executed |
| `gen_ai.request.model` | `claude-sonnet-4-20250514` | The model configured in the harness |
| `gen_ai.system` | `anthropic` | The LLM provider |

These attributes enable LLM-aware backends to recognize fullsend spans as
agent operations and surface them in GenAI-specific dashboards.

### SpanKind

- **Consumer**: The root span when `TRACEPARENT` is set (the run was
  dispatched by an external system).
- **Internal**: The root span for local/manual invocations.

## Custom attributes

Every span also carries fullsend-specific attributes:

| Attribute | Description |
|-----------|-------------|
| `fullsend.agent` | Agent name from the harness |
| `fullsend.harness` | Path to the harness YAML |
| `fullsend.model` | Model identifier |
| `fullsend.image` | Container image used |
| `fullsend.work_item_id` | Issue/PR number being addressed |

## Architecture

The tracing system uses an `InstrumentedPrinter` that unifies terminal output
and telemetry recording:

```
┌─────────────────────────────────────────┐
│          InstrumentedPrinter            │
│                                         │
│  ip.StepStart("name", "message")        │
│         │                    │          │
│         ▼                    ▼          │
│  ┌─────────────┐    ┌──────────────┐   │
│  │ ui.Printer  │    │  Recorder    │   │
│  │ (terminal)  │    │ (OTEL+JSONL) │   │
│  └─────────────┘    └──────────────┘   │
└─────────────────────────────────────────┘
```

This design ensures every step visible in the terminal is also captured in
telemetry — it is structurally impossible to have a UI step without a
corresponding trace span.

Early lifecycle steps (before the recorder is initialized) are buffered and
replayed once the recorder attaches.

## Extending instrumentation

When adding new lifecycle steps to the CLI:

```go
// Use ip.StepStart/StepDone — never call printer.StepStart directly
ip.StepStart("my-new-step", "Doing something useful",
    telemetry.StringAttr("key", "value"),
)
// ... do the work ...
ip.StepDone("my-new-step", "Done",
    telemetry.StringAttr("result", "success"),
)
```

A CI regression gate (`telemetry_lint_test.go`) ensures that raw
`printer.StepStart` calls cannot be introduced in `runAgent` — the test
will fail if someone bypasses the unified path.

## Recommended backends for evaluation

| Backend | Strengths | Setup |
|---------|-----------|-------|
| [Arize Phoenix](https://phoenix.arize.com/) | LLM-native, GenAI dashboard, free OSS | `docker run -p 6006:6006 -p 4318:4318 arizephoenix/phoenix` |
| [Jaeger](https://www.jaegertracing.io/) | Mature, trace-focused UI | `docker run -p 16686:16686 -p 4318:4318 jaegertracing/jaeger` |
| [Grafana Tempo](https://grafana.com/oss/tempo/) | Integrates with Grafana dashboards | docker-compose with Tempo + Grafana |
