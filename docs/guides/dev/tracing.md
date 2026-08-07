# Tracing Internals

Fullsend's distributed tracing system records structured telemetry for every
agent run. This document explains how the tracing implementation works and
how to extend it. It is aimed at contributors modifying the telemetry
package or the span instrumentation in the run command.

For enabling tracing on an installation, see
[How To Emit Traces](../user/how-to-emit-traces.md). For span schemas and
attribute definitions, see the
[Tracing Reference](../infrastructure/distributed-tracing.md). The design
is specified in
[ADR 0050](../../ADRs/0050-distributed-tracing-instrumentation.md).

## How the tracing system is structured

The tracing code is split between two locations:

- **`internal/telemetry/`** - the TracerProvider, exporters, and W3C
  traceparent helpers.
- **`internal/cli/run.go`** - span creation, attribute assignment, and
  trace context propagation to child scripts.

The telemetry package owns the provider and exporters. `run.go` owns the
span lifecycle: it decides when spans start and end, and which attributes
they carry.

### Package layout

```
internal/telemetry/
├── telemetry.go          Setup, TracerProvider wiring, env parsing
├── fileexporter.go       Synchronous OTLP JSON file exporter
├── trace.go              W3C traceparent formatting helpers
├── main_test.go
├── telemetry_test.go
├── fileexporter_test.go
├── otlpsink_test.go
└── trace_test.go
```

## TracerProvider setup

`telemetry.Setup(dir, version)` returns a `trace.Tracer` and a cleanup
function. It creates a `TracerProvider` with two span processors:

1. **fileExporter** via `SimpleSpanProcessor`: writes every completed span
   as one OTLP JSON line to `run-telemetry.jsonl`. Calls `f.Sync()` after
   each write so spans survive a process crash.

2. **otlptracehttp** via `BatchSpanProcessor` wrapped in
   `parentSampledProcessor`: exports to a remote backend when an
   `OTEL_EXPORTER_OTLP_*ENDPOINT` env var is set.

If neither exporter can be created (bad directory, SDK disabled), `Setup`
returns a noop tracer. Telemetry failures never affect the run.

```
telemetry.Setup()
│
├── OTEL_SDK_DISABLED check   → noop tracer if "true"
├── os.OpenFile(jsonl)        → noop tracer on error
│
├── SimpleSpanProcessor(fileExporter)           ← always present
│
└── OTEL_EXPORTER_OTLP_*ENDPOINT check
    ├── validateEndpoints()   → stderr warning, skip
    ├── newOTLPExporter()     → stderr warning, skip
    └── parentSampledProcessor(BatchSpanProcessor(otlpExporter))
```

### Why parentSampledProcessor exists

The OTel SDK's `AlwaysSample` sampler records all spans locally, which is
needed for the file exporter. But `AlwaysSample` ignores an upstream
unsampled decision. The `parentSampledProcessor` wraps the OTLP batch
processor and suppresses the entire trace from export when the root span's
remote parent has the W3C sampled flag unset (`-00`). It tracks suppressed
trace IDs in a `sync.Map` so child spans under the same trace are also
dropped.

Result: the file exporter always writes all spans; the OTLP exporter
respects upstream sampling.

### Endpoint validation

`validateEndpoints` rejects non-http(s) URLs and unsupported protocols
before creating the exporter. A malformed endpoint produces a stderr
warning; the SDK's default `localhost:4318` fallback is never used
silently.

## Span lifecycle in run.go

`run.go` creates three span types arranged in a parent-child hierarchy:

```
run (root)
├── sandbox_create    (gen_ai.operation.name=create_agent)
└── agent             (one per iteration; gen_ai.operation.name=invoke_agent)
```

### Root span

Created by `resolveTraceIdentity()`. When an inbound `TRACEPARENT` env var
is present, the W3C propagator extracts a remote span context and the root
span is `SpanKindConsumer`. Otherwise it is `SpanKindInternal`.

Start attributes: `fullsend.agent`, `fullsend.work_item_id`,
`gen_ai.operation.name`, `gen_ai.agent.name`.

End attributes (set in a deferred cleanup): `exit_code`, aggregated token
usage (`gen_ai.usage.*`), `fullsend.cost_usd`, `fullsend.num_turns`,
`fullsend.tool_calls`, `fullsend.iterations`, `gen_ai.request.model`.

For the full attribute list (including `fullsend.security_trace_id` and
`fullsend.prescript.*`), see the
[Tracing reference attribute table](../infrastructure/distributed-tracing.md#span-attributes).

### sandbox_create span

Started before sandbox creation, ended after bootstrap completes. Carries
`gen_ai.operation.name=create_agent`.

### agent spans

One per iteration (validation loop iterations included). Started before
`Exec()`, ended after output extraction.

The helper functions `agentSpanStartAttrs()` and `agentSpanEndAttrs()`
build the attribute slices. Start attributes: `iteration`,
`gen_ai.operation.name`, `gen_ai.agent.name`. End attributes: `iteration`,
`exit_code`, `gen_ai.system`, model, token counts, `fullsend.cost_usd`,
`fullsend.tool_calls`.

## Trace identity and TRACEPARENT propagation

`resolveTraceIdentity()` handles W3C trace context propagation in three
steps:

1. Extracts `TRACEPARENT` and `TRACESTATE` from env via the W3C propagator.
2. Starts the root span (Consumer if remote parent, Internal otherwise).
3. Computes the propagated traceparent with flag preservation: if the
   inbound parent was valid, remote, and unsampled, the outbound
   traceparent keeps the unsampled flag instead of the local
   `AlwaysSample` flag. This prevents child runs from re-advertising as
   sampled when the parent trace opted out.

The resulting `TRACEPARENT` string is passed to pre-scripts, post-scripts,
and the sandbox environment via `childScriptEnv()`. That function strips
any inherited `TRACEPARENT` from `os.Environ()` and `runner_env` before
appending fullsend's own value (issue #2779).

## File exporter output format

`fileExporter` writes OTLP JSON with hex-encoded trace/span IDs (per the
OTLP JSON spec, not base64). Each line is a complete `TracesData` message.
Non-finite floats (NaN, Infinity) are encoded as proto3 JSON strings per
the protobuf spec.

`buildResourceSpans()` groups SDK spans by resource and instrumentation
scope, preserving insertion order. This matches the structure a backend
receives via OTLP/HTTP.

## Prerequisites

- A local clone of the fullsend repo
- Go toolchain (see `go.mod` for the minimum version)
- Podman or Docker, if testing with a local tracing backend

## How to add a new span attribute

1. Add the `attribute.String` / `attribute.Int` / `attribute.Float64` call
   in `run.go` at the appropriate point. Use start attributes for values
   known when the span opens; use end attributes (set before `span.End()`)
   for values computed during the span.

2. Choose the attribute key namespace:
   - `gen_ai.*` for attributes defined by the
     [OTel GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/).
   - `fullsend.*` for project-specific attributes.

3. Update the attribute tables in the
   [Tracing Reference](../infrastructure/distributed-tracing.md).

4. No exporter changes are needed; both exporters pick up new attributes
   automatically.

## How to test

### Unit tests

```bash
go test ./internal/telemetry/...
```

Tests use `t.Setenv` for OTEL env vars and `t.TempDir` for the file
exporter. No external backend is required.

### Test seam for the OTLP exporter

`newOTLPExporter` is a package-level `var` that tests override to spy on
or stub out exporter creation:

```go
orig := newOTLPExporter
defer func() { newOTLPExporter = orig }()
newOTLPExporter = func(_ context.Context) (sdktrace.SpanExporter, error) {
    // spy or stub
}
```

### Testing with a local backend

Start a Jaeger instance and point the exporter at it:

```bash
podman run -d --name jaeger \
  -p 16686:16686 \
  -p 4318:4318 \
  jaegertracing/jaeger

export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4318"
go run ./cmd/fullsend run triage ...
```

See [Running Agents Locally](../user/running-agents-locally.md) for full
flags. View traces at `http://localhost:16686`.

## Replaying collected traces

`hack/upload-traces.sh` replays `run-telemetry.jsonl` files into any OTLP
backend using `otelcol-contrib`. This is useful for inspecting traces from
CI runs or from another machine without re-running the agent.

```bash
# Replay a single file
hack/upload-traces.sh run/agent-run-123/run-telemetry.jsonl \
  --endpoint http://localhost:4318

# Replay all .jsonl files under a directory
hack/upload-traces.sh run/ --endpoint http://localhost:4318
```

The collector runs continuously and watches for new data; press Ctrl+C
when done.

**Prerequisites:** `otelcol-contrib` >= 0.120.0 on `PATH`
([releases](https://github.com/open-telemetry/opentelemetry-collector-releases/releases)).

**Authentication headers:** `otelcol-contrib` ignores the standard
`OTEL_EXPORTER_OTLP_HEADERS` env var. Edit
`hack/upload-traces-otelcol-config.yaml` and uncomment the `headers:`
block to set auth tokens or routing headers (e.g.
`x-mlflow-experiment-id`).

## Compatible backends

Any OTLP/HTTP-capable backend works. LLM-aware backends recognize the
`gen_ai.*` attributes and surface GenAI dashboards (token cost rollups,
prompt/completion inspection, agent-specific views) without CLI-side
changes.

| Backend | Local quickstart | UI |
|---------|-----------------|-----|
| Jaeger | `podman run -p 16686:16686 -p 4318:4318 jaegertracing/jaeger` | `localhost:16686` |
| Arize Phoenix | `podman run -p 6006:6006 -p 4318:4318 arizephoenix/phoenix` | `localhost:6006` |
| MLflow >= 3.6 | `uvx mlflow server` | `localhost:5000` |
| otel-gui | `podman run -p 4318:4318 ghcr.io/metafab/otel-gui:latest` | `localhost:4318` |

## See also

- [How To Emit Traces](../user/how-to-emit-traces.md): user guide for enabling tracing
- [Tracing Reference](../infrastructure/distributed-tracing.md): span schemas and attribute tables
- [ADR 0050](../../ADRs/0050-distributed-tracing-instrumentation.md): design decision
