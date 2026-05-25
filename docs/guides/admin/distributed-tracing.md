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

## MLflow tracing backend (production)

Fullsend traces are exported to an MLflow instance at
`https://mlflow-35-212-57-52.nip.io`. MLflow ingests OTLP/HTTP traces and
provides GenAI-aware dashboards with automatic token usage rollups.

### GHA workflow configuration

Add these environment variables to workflow jobs that run `fullsend run`:

```yaml
env:
  OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: "https://mlflow-35-212-57-52.nip.io/v1/traces"
  OTEL_EXPORTER_OTLP_TRACES_HEADERS: "Authorization=Bearer ${{ secrets.MLFLOW_OTLP_TOKEN }},x-mlflow-experiment-id=0"
```

The `MLFLOW_OTLP_TOKEN` GitHub Actions secret must be set at the org or repo
level. The token value is stored in GCP Secret Manager
(`mlflow-otlp-token` in `it-gcp-konflux-dev-fullsend`).

### MLflow UI access

The MLflow UI is at `https://mlflow-35-212-57-52.nip.io` behind basic auth
(user: `admin`, password: same as the OTLP token). Navigate to the **Traces**
tab to view agent run traces with span hierarchies, GenAI attributes, and
token usage.

### Infrastructure

The MLflow instance runs on a GCP VM (`mlflow`, zone `us-east4-c`) in the
`it-gcp-konflux-dev-fullsend` project:

- **VM**: e2-medium (2 vCPU, 4GB), Ubuntu 24.04 LTS
- **Static IP**: 35.212.57.52
- **Stack**: MLflow + PostgreSQL + RustFS (S3-compatible artifacts) + Caddy
  (TLS, auth, reverse proxy)
- **Service account**: `mlflow-vm@it-gcp-konflux-dev-fullsend.iam.gserviceaccount.com`
  (logging + monitoring write only)
- **Network**: HTTPS (443) open, SSH via IAP only (35.235.240.0/20), iptables
  rate limiting (30 new connections/min per source IP)
- **Auth**: Bearer token for OTLP, basic auth for UI, both enforced by Caddy

Admin access:

```bash
gcloud compute ssh mlflow --zone=us-east4-c --tunnel-through-iap \
  --project=it-gcp-konflux-dev-fullsend
```

## Other backends for evaluation

| Backend | Strengths | Setup |
|---------|-----------|-------|
| [Arize Phoenix](https://phoenix.arize.com/) | LLM-native, GenAI dashboard, free OSS | `docker run -p 6006:6006 -p 4318:4318 arizephoenix/phoenix` |
| [Jaeger](https://www.jaegertracing.io/) | Mature, trace-focused UI | `docker run -p 16686:16686 -p 4318:4318 jaegertracing/jaeger` |
| [Grafana Tempo](https://grafana.com/oss/tempo/) | Integrates with Grafana dashboards | docker-compose with Tempo + Grafana |
