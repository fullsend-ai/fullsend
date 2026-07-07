# Distributed Tracing

Fullsend produces structured telemetry for every agent run. This guide covers
how to configure, consume, and extend the tracing system.

Decided in [ADR 0050](../../ADRs/0050-distributed-tracing-instrumentation.md).

## Zero-configuration baseline (Level 1)

Every `fullsend run` produces two files in the run output directory with no
configuration required:

- **`run-telemetry.jsonl`** — NDJSON stream of lifecycle events (step starts,
  completions, failures, warnings) with timestamps, durations, and trace IDs.
- **`run-summary.json`** — Aggregated run summary including agent name, exit
  code, step timings, total duration, and a W3C `traceparent` value for
  downstream correlation.

These files are always written, even when no OTLP backend is configured. They
contain metadata only — no prompts, completions, or source code content.

## Enabling OTLP export (Level 2)

To send metadata spans to an OpenTelemetry-compatible backend, set one of the
standard OTEL environment variables:

```bash
# Signal-specific (takes precedence, used as-is — no /v1/traces appended)
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="https://your-backend:4318/v1/traces"

# Base URL (SDK appends /v1/traces automatically)
export OTEL_EXPORTER_OTLP_ENDPOINT="https://your-backend:4318"
```

**Precedence:** `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` > `OTEL_EXPORTER_OTLP_ENDPOINT`.
Headers follow the same pattern: `OTEL_EXPORTER_OTLP_TRACES_HEADERS` > `OTEL_EXPORTER_OTLP_HEADERS`.

Local files (`run-telemetry.jsonl`, `run-summary.json`) are always produced
with no configuration needed (Level 1).

When an endpoint is configured, spans are exported via OTLP/HTTP. Any backend
that speaks OTLP works: Jaeger, Grafana Tempo, MLflow, Arize Phoenix,
Langfuse, SigNoz, Honeycomb, Datadog, etc.

If the endpoint is unreachable, the CLI continues normally — local files are
still produced and the run is not affected.

## Enabling content capture (Level 3)

By default, spans contain metadata only (timing, token counts, tool names,
errors). To include full prompt/completion content in spans:

```bash
export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true
```

This follows the [OTEL GenAI semantic conventions](https://github.com/open-telemetry/semantic-conventions/blob/v1.37.0/docs/gen-ai/gen-ai-spans.md)
which mandate that content capture is opt-in. When enabled, spans include:

- System prompts and user messages
- Tool arguments and results (file contents, command output)
- Agent reasoning/thinking text
- Completion text

**Warning:** Only enable content capture when your backend's access controls
are appropriate for the sensitivity of the data. Content may include
proprietary source code, issue descriptions with PII, or credentials visible
in tool outputs.

## Cross-run trace correlation

Multi-agent pipelines (triage → code → review) propagate trace context via
the `TRACEPARENT` environment variable (W3C Trace Context).

When a workflow dispatches a child run:

```yaml
env:
  TRACEPARENT: ${{ steps.parent.outputs.traceparent }}
```

The child run's root span becomes part of the parent trace, creating a
unified view of the entire pipeline.

For separate workflow runs on the same work item (triage → code → review as
independent GHA workflows), `TRACEPARENT` must be propagated manually — for
example, via hidden issue/PR comments. GitHub webhooks do not support custom
trace headers natively.

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

## GHA workflow configuration example

Add these environment variables to workflow jobs that run `fullsend run`:

```yaml
env:
  OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: "${{ secrets.OTLP_ENDPOINT }}"
  OTEL_EXPORTER_OTLP_TRACES_HEADERS: "Authorization=Bearer ${{ secrets.OTLP_TOKEN }}"
```

The secret names and values depend on your chosen backend. Consult your
backend's documentation for the endpoint URL and authentication mechanism.

## Local development

Run an agent locally with traces going to a local backend:

```bash
# Start a local Jaeger instance (OTLP-compatible)
podman run -d --name jaeger \
  -p 16686:16686 \
  -p 4318:4318 \
  jaegertracing/jaeger

# Run an agent with tracing enabled
export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4318"
fullsend run triage --issue 42

# View traces at http://localhost:16686
```

Other lightweight local backends:

| Backend | Command | UI |
|---------|---------|-----|
| Jaeger | `podman run -p 16686:16686 -p 4318:4318 jaegertracing/jaeger` | `localhost:16686` |
| Arize Phoenix | `podman run -p 6006:6006 -p 4318:4318 arizephoenix/phoenix` | `localhost:6006` |
| MLflow | `uvx mlflow server` (with OTLP plugin) | `localhost:5000` |

## Other backends

Any OTLP-compatible backend works. Choosing an LLM-aware backend (MLflow,
Phoenix, Langfuse) activates GenAI dashboards — token cost rollups,
prompt/completion inspection, agent-specific views — without any CLI-side
configuration change. The `gen_ai.*` span attributes are recognized
automatically.

For production deployments, consult your backend's documentation for:
- High-availability configuration
- Authentication and access control
- Data retention policies
- Cost considerations for high-volume trace ingestion
