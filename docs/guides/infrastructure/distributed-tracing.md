# Distributed Tracing

Fullsend produces structured telemetry for every agent run. This guide covers
how to configure, consume, and extend the tracing system.

Decided in [ADR 0050](../../ADRs/0050-distributed-tracing-instrumentation.md).

## Zero-configuration baseline (Level 1)

Every `fullsend run` produces one file in the run output directory with no
configuration required:

- **`run-telemetry.jsonl`** — OTLP JSON spans covering the run lifecycle
  (sandbox creation, agent iterations, validation) with timestamps, durations,
  trace IDs, and token/cost attributes.

This file is written on every run unless `OTEL_SDK_DISABLED=true`, which
suppresses all telemetry output including the local file. It contains
metadata only — no prompts, completions, or source code content.

## Prerequisites

Level 1 requires nothing. To enable OTLP export (Level 2 and Level 3) you need:

- An **OTLP/HTTP-capable backend** and its endpoint URL — e.g. Jaeger, Tempo,
  Grafana, MLflow ≥ 3.6, or any OpenTelemetry Collector.
- Any **backend authentication** (bearer token or basic auth) for the
  `OTEL_EXPORTER_OTLP_TRACES_HEADERS` variable.
- **Network reachability** from where runs execute (your machine or CI runners)
  to the backend endpoint.
- For a backend behind a **private CA** (e.g. an internal MLflow): the CA
  certificate bundle, pointed to by `OTEL_EXPORTER_OTLP_CERTIFICATE`. Local
  and bring-your-own-workflow runs only — the managed workflows do not yet
  pass a CA bundle through.

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

The local file (`run-telemetry.jsonl`) is produced with no configuration
needed (Level 1), unless `OTEL_SDK_DISABLED=true`.

When an endpoint is configured, spans are exported via OTLP/HTTP. Any backend
that speaks OTLP works: Jaeger, Grafana Tempo, MLflow, Arize Phoenix,
Langfuse, SigNoz, Honeycomb, Datadog, etc.

If the endpoint is unreachable, the CLI continues normally — local files are
still produced and the run is not affected.

Operational details:

- **Export timing:** spans are exported live via the OTel SDK's batch
  processor as they complete. On shutdown, the provider flushes remaining
  spans within a 5-second budget. A dead endpoint does not block the run.
- **Crashed runs:** completed spans that were already flushed mid-run reach
  the backend; spans still in the batch buffer are lost. The local
  `run-telemetry.jsonl` (written synchronously per span) remains the
  forensic record.
- **Sampling:** when the run continues an inbound `TRACEPARENT` whose W3C
  sampled flag is unset (`-00`), the upstream sampling decision is respected:
  nothing is exported. The local file is still written.
- **Protocol:** OTLP over `http/protobuf` only. Setting
  `OTEL_EXPORTER_OTLP_PROTOCOL` (or the traces-specific variant) to anything
  else — e.g. `grpc` — skips export with a warning rather than posting
  protobuf at a gRPC endpoint.
- **Validation:** a malformed endpoint value skips export with a warning; it
  is never silently replaced with the SDK's `localhost:4318` default.
- **Kill switches:** `OTEL_SDK_DISABLED=true` disables all telemetry output
  (OTLP export *and* the local file). `OTEL_TRACES_EXPORTER=none` disables
  only the OTLP export; the local file is still written.
- **Private CAs:** point `OTEL_EXPORTER_OTLP_CERTIFICATE` at a PEM bundle for
  backends with certificates outside the system trust store. There is no
  skip-verify option.

### MLflow example

MLflow ≥ 3.6 ingests OTLP/HTTP natively at `{server}/v1/traces` and routes
traces to an experiment via a required header:

```bash
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="https://mlflow.example.com/v1/traces"
export OTEL_EXPORTER_OTLP_TRACES_HEADERS="x-mlflow-experiment-id=42"
```

Header values are URL-decoded, so spaces are percent-encoded — for a
Basic-auth-fronted instance:

```bash
export OTEL_EXPORTER_OTLP_TRACES_HEADERS="authorization=Basic%20${CREDS_B64},x-mlflow-experiment-id=42"
```

> **Cost columns:** MLflow's per-trace cost is its own estimate — extracted
> input/output token counts priced against MLflow's internal model table. It
> excludes cache-creation/cache-read tokens, which dominate agent-run cost.
> The authoritative figure is the runtime-reported `fullsend.cost_usd` on
> `agent` spans (also in `run-telemetry.jsonl`).

## Enabling content capture (Level 3)

> **Planned:** Level 3 content capture is not yet implemented. This section
> documents the contract decided in
> [ADR 0050](../../ADRs/0050-distributed-tracing-instrumentation.md).

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

## Span structure

A run produces this span hierarchy (span names match the `name` field in
`run-telemetry.jsonl` — the exported spans and the local file are two views
of the same trace, with identical span ids):

```
run (root; Consumer when dispatched with TRACEPARENT, else Internal)
├── sandbox_create (gen_ai.operation.name=create_agent)
└── agent           (one per iteration; gen_ai.operation.name=invoke_agent)
```

### GenAI semantic conventions

Spans carry [OTEL GenAI semantic convention](https://opentelemetry.io/docs/specs/semconv/gen-ai/) attributes:

| Attribute | Example | On |
|-----------|---------|-----|
| `gen_ai.operation.name` | `invoke_agent` | `run` and `agent` spans (`create_agent` on `sandbox_create`) |
| `gen_ai.agent.name` | `triage` | `run` and `agent` spans |
| `gen_ai.request.model` | `claude-opus-4-6` | `agent` spans (resolved model) |
| `gen_ai.system` | `anthropic` | `agent` spans (the model vendor, from the runtime) |
| `gen_ai.usage.input_tokens` / `output_tokens` / `cache_*_input_tokens` | `109938` | `agent` spans |

These attributes enable LLM-aware backends to recognize fullsend spans as
agent operations and surface them in GenAI-specific dashboards.

### SpanKind

- **Consumer**: The root span when a valid inbound `TRACEPARENT` was adopted
  (the run was dispatched by an instrumented system).
- **Internal**: The root span for local/manual invocations, and all child
  spans.

## Custom attributes

Fullsend-specific attributes:

| Attribute | On | Description |
|-----------|-----|-------------|
| `fullsend.work_item_id` | `run` span | Work item identity (e.g. `owner/repo#123`) — the primary cross-run correlation key |
| `fullsend.cost_usd` | `agent` spans | Iteration cost in USD, rounded to cents |
| `fullsend.tool_calls` | `agent` spans | Tool invocations in the iteration |
| `fullsend.agent` | `run` span | Agent name (renamed from bare `agent` in the OTel SDK migration) |

## GHA workflow configuration

### Managed workflows

Only the **triage** stage forwards OTEL configuration in this release; the
other agents (code, fix, review, retro, prioritize) do not export yet.

To enable export for triage runs, set on the org (or repo) that hosts the
fullsend caller workflows:

1. Actions **variable** `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` — the backend's
   full traces URL (e.g. `https://mlflow.example.com/v1/traces`).
2. Actions **secret** `OTEL_EXPORTER_OTLP_TRACES_HEADERS` — the complete
   header string, auth and routing included (e.g.
   `Authorization=Bearer%20<token>,x-mlflow-experiment-id=42`).
3. Optional: Actions **variable** `OTEL_RESOURCE_ATTRIBUTES` — static
   `k=v,k=v` trace tags. The value is used verbatim: `${{ github.* }}`
   expressions evaluate only in workflow YAML, not in variables.

Installations scaffolded before this release must also forward the secret
(add `OTEL_EXPORTER_OTLP_TRACES_HEADERS` under `secrets:`) until the scaffold
is re-synced: in the `.fullsend` repo's `triage.yml` (per-org), or in the
fullsend shim workflow's dispatch job (per-repo).

### Bring your own workflow

Add the environment variables to any job that runs `fullsend run`:

```yaml
env:
  OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: "${{ vars.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT }}"
  OTEL_EXPORTER_OTLP_TRACES_HEADERS: "${{ secrets.OTEL_EXPORTER_OTLP_TRACES_HEADERS }}"
```

Any variable and secret names work here — the values reach the exporter
as-is. Consult your backend's documentation for the endpoint URL and
authentication mechanism.

### Organizing traces for an org

Two conventions keep a shared backend navigable as repos onboard:

1. **One backend bucket per org.** On MLflow, create one experiment per org
   (e.g. `fullsend-<org>`) and point the org's header secret at its id. The
   backend's per-bucket access controls then align with org boundaries.
2. **Slice inside the bucket with resource attributes.** Standard OTel
   resource env is honored, so workflows can tag every trace with repo,
   agent, and environment:

   ```yaml
   env:
     OTEL_RESOURCE_ATTRIBUTES: "fullsend.repo=${{ github.repository }},fullsend.agent=triage,deployment.environment=prod"
   ```

   The example is inline workflow `env:`, where `${{ github.* }}` evaluates.
   On the managed path, set the `OTEL_RESOURCE_ATTRIBUTES` Actions variable
   to a static value instead — variables are not expression-expanded.

   These become filterable trace tags (enable them as columns in MLflow's
   Traces table). `fullsend.work_item_id` is on the root `run` span, so runs
   for the same issue correlate by filtering on the root span.

## Local development

Run an agent locally with traces going to a local backend:

1. Start a local Jaeger instance (OTLP-compatible):

   ```bash
   podman run -d --name jaeger \
     -p 16686:16686 \
     -p 4318:4318 \
     jaegertracing/jaeger
   ```

2. Point the exporter at it and run an agent:

   ```bash
   export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4318"
   fullsend run triage --issue 42
   ```

3. View the traces at <http://localhost:16686>.

Other lightweight local backends:

| Backend | Command | UI |
|---------|---------|-----|
| Jaeger | `podman run -p 16686:16686 -p 4318:4318 jaegertracing/jaeger` | `localhost:16686` |
| Arize Phoenix | `podman run -p 6006:6006 -p 4318:4318 arizephoenix/phoenix` | `localhost:6006` |
| MLflow ≥ 3.6 | `uvx "mlflow>=3.6" server --backend-store-uri sqlite:///mlflow.db` (native OTLP at `/v1/traces`; requires the `x-mlflow-experiment-id` header — see the MLflow example above) | `localhost:5000` |

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
