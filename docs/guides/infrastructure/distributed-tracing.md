# Tracing Reference

Structured reference for fullsend's distributed tracing system: environment
variables, span hierarchy, attributes, and operational behavior. For
step-by-step setup, see [How To Emit Traces](../user/how-to-emit-traces.md).
For implementation details, see the
[Tracing Development Guide](../dev/tracing.md).

## Telemetry levels

| Level | What it produces | Configuration required |
|-------|-----------------|----------------------|
| 1 | `run-telemetry.jsonl` file in the run output directory | None |
| 2 | OTLP/HTTP export to a remote backend (metadata only) | `OTEL_EXPORTER_OTLP_*ENDPOINT` |
| 3 | Conversation content (assistant text, reasoning, tool calls) on `agent` spans | `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true` |

All levels produce metadata (timing, token counts, tool names, errors).
Level 3 adds the agent's conversation content to spans — enabled by one
environment variable, exactly like Level 2's endpoint.

## Environment variables

### Endpoint configuration

| Variable | Purpose | Notes |
|----------|---------|-------|
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | Signal-specific endpoint URL | Takes precedence; used as-is, no `/v1/traces` appended |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Base endpoint URL | SDK appends `/v1/traces` automatically |

### Authentication

| Variable | Purpose | Notes |
|----------|---------|-------|
| `OTEL_EXPORTER_OTLP_TRACES_HEADERS` | Signal-specific headers | Takes precedence; `key=value` pairs separated by commas; values are URL-decoded |
| `OTEL_EXPORTER_OTLP_HEADERS` | Base headers | Same format as above |

### Private CA

| Variable | Purpose | Notes |
|----------|---------|-------|
| `OTEL_EXPORTER_OTLP_CERTIFICATE` | PEM file path for TLS root certificates | Points at a CA bundle for verifying the OTLP backend's certificate; no skip-verify option exists. In managed workflows the PEM file must be committed into the repository checkout (e.g. `.fullsend/otel-ca.pem`) because the runner has no other persistent filesystem; bring-your-own-workflow runs can use any local path |

### Resource attributes

| Variable | Purpose | Notes |
|----------|---------|-------|
| `OTEL_RESOURCE_ATTRIBUTES` | Static `k=v,k=v` trace tags | Merged into the OTel resource; `${{ github.* }}` expressions only evaluate in workflow YAML, not in Actions variables |

### Kill switches

| Variable | Value | Effect |
|----------|-------|--------|
| `OTEL_SDK_DISABLED` | `true` (case-insensitive) | Disables all telemetry output: OTLP export and the local file |

To disable only OTLP export without affecting the local file, unset the
endpoint variables:

```bash
unset OTEL_EXPORTER_OTLP_ENDPOINT
unset OTEL_EXPORTER_OTLP_TRACES_ENDPOINT
```

### Trace propagation

| Variable | Purpose | Notes |
|----------|---------|-------|
| `TRACEPARENT` | W3C Trace Context parent | When present, the root span becomes `SpanKindConsumer`; when the sampled flag is unset (`-00`), OTLP export is suppressed but the local file is still written |
| `TRACESTATE` | W3C Trace Context state | Propagated alongside `TRACEPARENT` |

### Content capture (Level 3)

Fullsend assembles Level 3 content from the normalized event stream the
console renders, redacts it through the security output pipeline, and
attaches it to the per-iteration `agent` span. The agent runtime's own
content-logging variables (`OTEL_LOG_USER_PROMPTS`,
`OTEL_LOG_ASSISTANT_RESPONSES`, etc.) are never set.

| Variable | Values that enable capture | Values that keep it off |
|----------|---------------------------|-------------------------|
| `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` | `true`, `span_only`, `span_and_event` (case-insensitive) | unset, `false`, `NO_CONTENT`, `event_only`, anything unrecognized |

The variable name and accepted values follow the
[OpenTelemetry GenAI instrumentation convention](https://github.com/open-telemetry/opentelemetry-python-contrib/tree/main/instrumentation-genai).
Fullsend records content on span attributes only, so `event_only` stays
off. An unrecognized value disables capture; telemetry never fails a run.

**Captured:** assistant text, reasoning, and tool calls (name plus short
summary) — including any sub-agent activity, unattributed — as the
`gen_ai.output.messages` span attribute: a JSON string following the
[GenAI output-messages schema](https://github.com/open-telemetry/semantic-conventions/blob/v1.37.0/docs/gen-ai/gen-ai-output-messages.json)
with a `finish_reason` of `stop` or `error`.

**Not captured:** model input (`gen_ai.input.messages`) and
pre/post-script content. First-iteration runs have no meaningful
runner-side input; retry iterations carry the injected validation
feedback, a natural input-capture follow-up.

> **Planned:** Tool results, once a parser extension adds them to the
> normalized event stream — the next change after
> [#6429](https://github.com/fullsend-ai/fullsend/pull/6429).

**Redaction and size:** every part passes through security redaction
(Unicode normalization, then secret masking) before reaching the span.
Content is bounded at 256 KiB per iteration, kept as an ordered suffix;
overflow drops the oldest content first. Truncation is marked via
`fullsend.content.truncated`. The SDK's span attribute length cap is
lifted while capture is on; an explicit
`OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT` still wins and will cut content
mid-JSON — fullsend warns on stderr at startup.

**Sinks:** content rides the span to both `run-telemetry.jsonl` and the
OTLP endpoint (when configured). Spans may contain proprietary source
code, PII, or credentials; the organization enabling capture is
responsible for its backend's access controls. For how MLflow displays
the content, see [Tracing with MLflow](../user/tracing-with-mlflow.md).

## Span hierarchy

A run produces this span tree. Span names match the `name` field in
`run-telemetry.jsonl`; exported spans and the local file are two views
of the same trace with identical span IDs.

```
run (root; Consumer when dispatched with TRACEPARENT, else Internal)
├── sandbox_create (gen_ai.operation.name=create_agent)
└── agent           (one per iteration; gen_ai.operation.name=invoke_agent)
```

### SpanKind

| Span | Kind | Condition |
|------|------|-----------|
| `run` | Consumer | Valid inbound `TRACEPARENT` (dispatched by an instrumented system) |
| `run` | Internal | No inbound `TRACEPARENT` (local/manual invocation) |
| `sandbox_create` | Internal | Always |
| `agent` | Internal | Always |

## Span attributes

### GenAI semantic convention attributes

These follow the [OTel GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/)
and are recognized by LLM-aware backends for GenAI dashboards.

| Attribute | Example | Present on |
|-----------|---------|------------|
| `gen_ai.operation.name` | `invoke_agent` | `run`, `agent` (`create_agent` on `sandbox_create`) |
| `gen_ai.agent.name` | `triage` | `run`, `agent` |
| `gen_ai.system` / `gen_ai.provider.name` | `anthropic` | `agent` (model vendor; `system` deprecated in OTel GenAI semconv v1.37 — EM-001 accepts either) |
| `gen_ai.request.model` | `claude-opus-4-6` | `agent` (resolved model) |
| `gen_ai.usage.input_tokens` / `output_tokens` / `cache_*_input_tokens` | `109938` | `agent` |

### Fullsend-specific attributes

| Attribute | Present on | Description |
|-----------|------------|-------------|
| `fullsend.work_item_id` | `run` | Work item identity (e.g. `owner/repo#123`); primary cross-run correlation key |
| `fullsend.agent` | `run` | Agent name |
| `fullsend.cost_usd` | `run` (aggregated), `agent` | Cost in USD, rounded to cents (see [Cost data contract](#cost-data-contract)) |
| `fullsend.tool_calls` | `run` (aggregated), `agent` | Number of tool invocations |
| `fullsend.num_turns` | `run` | Total conversation turns across all iterations |
| `fullsend.iterations` | `run` | Number of agent iterations (validation loop included) |
| `fullsend.security_trace_id` | `run` | Security scanner trace correlation ID |
| `fullsend.harness.url` | `run` | Source URL the harness was fetched from; omitted for local-path harnesses |
| `fullsend.harness.path` | `run` | Local path of the resolved harness file; omitted when empty |
| `fullsend.harness.content_sha` | `run` | SHA-256 of the resolved harness file; absent when the file cannot be read |
| `fullsend.prescript.skipped` | `run` | Whether the pre-script signaled a skip |
| `fullsend.prescript.skip_reason` | `run` | Human-readable skip reason from the pre-script |
| `fullsend.transcript_error` | `agent` | Present (`true`) when the agent exited 0 but its transcript reported an error — the span's status is Error while `exit_code` keeps the raw process exit |
| `gen_ai.output.messages` | `agent` | Level 3 only: the iteration's conversation content as a JSON string (see Content capture) |
| `fullsend.content.truncated` | `agent` | Level 3 only: present (`true`) when the size budget cut or dropped content |
| `fullsend.content.dropped_bytes` | `agent` | Level 3 only: exact content bytes removed by the size budget |
| `fullsend.content.redactions` | `agent` | Level 3 only: number of security findings raised while redacting content at assembly (including findings from parts the size budget later dropped) |

### Common attributes

| Attribute | Present on | Description |
|-----------|------------|-------------|
| `exit_code` | `run`, `agent` | Process exit code |
| `iteration` | `agent` | 1-based iteration index |

### Resource attributes

Set on every span via the OTel resource:

| Attribute | Value |
|-----------|-------|
| `service.name` | `fullsend` |
| `service.version` | CLI version string |

Additional resource attributes from `OTEL_RESOURCE_ATTRIBUTES` are merged in.

## Cost data contract

Fullsend does not calculate inference cost from token counts or maintain a
model-price table. Each runtime reports a USD cost value and fullsend
records it as-is. This section defines the source, aggregation, rounding,
and display behavior of that value across every output surface. To compute
a dollar figure from persisted token counts using your own contracted
rates, see [Computing dollar cost from token telemetry](#computing-dollar-cost-from-token-telemetry).

### Runtime cost extraction

Each runtime extracts cost differently from its agent process:

| Runtime | Source | Extraction |
|---------|--------|------------|
| `claude` | Claude Code stream JSON | Reads `result.total_cost_usd` from the final result event — a single value covering the entire iteration |
| `pi` | Pi assistant message stream | Sums `usage.cost.total` across all assistant messages in the iteration |
| `opencode` | OpenCode step stream | Sums `step_finish.part.cost` across all step-finish events in the iteration |

The runtime-reported value includes whatever the provider prices — input
tokens, output tokens, cache-creation tokens, cache-read tokens, and
reasoning tokens. Fullsend has no visibility into the provider's pricing
breakdown; it accepts the reported total.

Token counts (including `cache_creation_input_tokens` and
`cache_read_input_tokens`) are recorded as separate telemetry attributes.
They are not inputs to any fullsend-side cost calculation.

### Cross-iteration aggregation

When a run has multiple iterations (validation loop retries), the runner
sums raw costs:

```
run_total_cost_usd = sum(iteration.total_cost_usd for each completed iteration)
```

This sum is the single aggregate cost for the run, used by every
downstream surface.

### Rounding and precision by surface

The raw aggregate is a floating-point sum. Different output surfaces
apply different precision:

| Surface | Value | Precision | Example |
|---------|-------|-----------|---------|
| `metrics.json` `total_cost_usd` | Raw aggregate | Full float64 | `0.8234567` |
| `fullsend.cost_usd` on `agent` spans | Per-iteration | Rounded to cents: `round(value × 100) / 100` | `0.41` |
| `fullsend.cost_usd` on the root `run` span | Aggregate | Rounded to cents: `round(value × 100) / 100` | `0.82` |
| Console (per-iteration) | Per-iteration | Four decimal places (`$%.4f`) | `$0.4117` |
| Status comment footer | Aggregate | Two decimal places (`$%.2f`) | `$0.82` |

The root span's rounded cost is computed by rounding the raw aggregate
sum. Because rounding happens after summing, the root span value can
differ from the sum of individually rounded agent-span values. For
example, two iterations at `$0.414` and `$0.415` round individually to
`$0.41` and `$0.42` (sum `$0.83`), but the aggregate `$0.829` rounds to
`$0.83` — or, with different fractional values, the aggregate may round
differently than the sum of parts.

### No pricing-table fallback

If a runtime does not report cost (returns zero or the field is absent),
fullsend records zero. There is no fallback cost calculation from token
counts. A missing runtime cost propagates as `$0.00` on all surfaces. See
[Computing dollar cost from token telemetry](#computing-dollar-cost-from-token-telemetry)
for how to apply your own contracted rates to the persisted token fields.

### Distinction from backend-derived cost estimates

Tracing backends may display their own cost estimates alongside
`fullsend.cost_usd`. These are independent calculations:

- **MLflow** estimates cost from token counts against its internal model
  table. This estimate excludes cache-creation and cache-read token
  pricing, which can dominate agent-run cost. See
  [Tracing with MLflow — Cost column caveat](../user/tracing-with-mlflow.md#cost-column-caveat).
- Other LLM-aware backends may apply similar token-based estimates.

The authoritative cost for a fullsend run is always `fullsend.cost_usd`
(on spans) or `total_cost_usd` (in `metrics.json`). Backend-derived
estimates are informational and may diverge.

## Computing dollar cost from token telemetry

Fullsend never converts token counts into dollars. When a runtime reports
`total_cost_usd`, that value is recorded as-is — typically a list-price
estimate, not your contracted rate. When the runtime reports nothing
(cancelled runs, or Codex, which sends no cost), the field stays `0`. In
both cases you can compute a dollar figure from the persisted token counts
using rates from your own contract.

The token fields below are the counters
[PR #6938](https://github.com/fullsend-ai/fullsend/pull/6938) persists on
cancelled runs (and records on completed runs too), plus `reasoning`,
which is recorded only on completed runs. They are disjoint: `input` is
uncached input only, cache tokens are not included in `input` or `output`,
and — for Codex specifically — `reasoning` is not included in `output`
either.

### Persisted token fields

| Kind | `metrics.json` `token_usage` | `agent` span attribute |
|------|------------------------------|------------------------|
| Uncached input | `input` | `gen_ai.usage.input_tokens` |
| Output | `output` | `gen_ai.usage.output_tokens` |
| Cache creation | `cache_creation` | `gen_ai.usage.cache_creation.input_tokens` |
| Cache read | `cache_read` | `gen_ai.usage.cache_read.input_tokens` |
| Reasoning | `reasoning` | `gen_ai.usage.reasoning_tokens` |

`metrics.json` always includes `reasoning` (`0` when there is none). The
`gen_ai.usage.reasoning_tokens` span attribute, unlike the other four
`gen_ai.usage.*` attributes above, is omitted entirely when reasoning is
zero rather than being attached as `0` — do not rely on its presence or
absence when filtering OTel spans.

For a cancelled GitHub Actions run, use `metrics.json` — it is the
run-level aggregate and is uploaded even when the job is cancelled. Span
attributes are per-iteration.

`token_usage.reasoning` (span: `gen_ai.usage.reasoning_tokens`) is also
recorded on completed runs. On cancelled Claude runs it is typically zero,
because reasoning is taken from the terminal result event that cancellation
never emits. Whether a completed run needs `reasoning` added to the cost
formula depends on the runtime: for Codex, reasoning tokens are counted
separately from `output` (`output` excludes them), so omitting `reasoning`
undercounts. For Claude and pi, `output` already includes reasoning
tokens, so adding `reasoning` on top of `output` double-counts and
overbills.

### Worked example

A cancelled run's `metrics.json` might look like (`reasoning` is omitted
here because it is typically zero on cancelled runs; see the caveat above
for completed and Codex runs):

```json
{
  "total_cost_usd": 0,
  "token_usage": {
    "input": 1000,
    "output": 500,
    "cache_creation": 150000,
    "cache_read": 600000
  }
}
```

Using hypothetical contracted rates of $2.00 / $10.00 / $2.50 / $0.20 per
million tokens (uncached input / output / cache-creation / cache-read):

```
cost = 1000/1e6 * 2.00
     + 500/1e6 * 10.00
     + 150000/1e6 * 2.50
     + 600000/1e6 * 0.20
     = 0.002 + 0.005 + 0.375 + 0.120
     = $0.502
```

Pricing only uncached input and output would give $0.007 and miss the cache
tokens that dominate this run. Substitute your contracted rates; do not
copy these numbers into a billing pipeline.

For a completed Codex run, add a fifth term, `reasoning/1e6 * <reasoning
rate>`, to the formula above: Codex's `reasoning` counter is disjoint from
`output`, so the four-field formula alone will undercount whenever
reasoning tokens are non-trivial. Don't add this term for Claude or pi
runs — both runtimes already fold reasoning tokens into `output`, so
adding `reasoning` on top would double-count it.

## Output file format

`run-telemetry.jsonl` contains one JSON object per line. Each object is a
complete OTLP `TracesData` message with hex-encoded trace/span IDs (per
the OTLP JSON spec, not base64).

Non-finite float values (NaN, Infinity) are encoded as proto3 JSON strings
(`"NaN"`, `"Infinity"`, `"-Infinity"`).

The file is written synchronously per span. Spans are flushed to disk as
they complete; the file is the forensic record for crashed runs.

## Cross-run trace correlation

Multi-agent pipelines (triage, code, review) propagate trace context via
the `TRACEPARENT` environment variable (W3C Trace Context).

When a workflow dispatches a child run:

```yaml
env:
  TRACEPARENT: ${{ steps.parent.outputs.traceparent }}
```

The child run's root span becomes part of the parent trace.

For separate workflow runs on the same work item (e.g. triage, code, review
as independent GHA workflows), `TRACEPARENT` must be propagated manually.
GitHub webhooks do not support custom trace headers.

Within a single work item, `fullsend.work_item_id` on the root `run` span
is the correlation key for filtering related traces in a backend.

## Operational behavior

- **Export timing:** spans are exported live via the batch processor. On
  shutdown, the provider flushes remaining spans within a 5-second budget.
  A dead endpoint does not block the run.
- **Retry:** the exporter retries on transient failures (HTTP 503, etc.)
  with an initial interval of 250 ms and a max interval of 2 s. The
  5-second context deadline passed to `tp.Shutdown` bounds both retries
  and in-flight requests, so a persistently failing or hanging endpoint
  does not extend shutdown.
- **Crashed runs:** completed spans already flushed mid-run reach the
  backend; spans in the batch buffer are lost. The local file remains.
- **Sampling:** when `TRACEPARENT` has the sampled flag unset (`-00`),
  OTLP export is suppressed. The local file is still written.
- **Endpoint validation:** the CLI validates the endpoint before creating
  the OTLP exporter. An endpoint is invalid if it cannot be parsed as a
  URL, has no scheme, uses a scheme other than `http` or `https`, or has no
  host (e.g. `localhost:4318` instead of `http://localhost:4318`). When
  invalid, the CLI prints a warning to stderr and skips OTLP export; the
  local file exporter is unaffected. A valid signal-specific endpoint is not
  blocked by an invalid generic endpoint.
- **Private CAs:** `OTEL_EXPORTER_OTLP_CERTIFICATE` points at a PEM
  bundle. No skip-verify option exists.

## GHA workflow configuration

### Managed workflows

All agent stages (triage, code, review, fix, retro, prioritize, harness)
forward OTEL configuration. To enable export, set on the org (or repo)
that hosts the fullsend caller workflows:

| Name | Type | Required | Purpose |
|------|------|----------|---------|
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | Variable | Yes | Backend's full traces URL (e.g. `https://mlflow.example.com/v1/traces`). Alternatively, set `OTEL_EXPORTER_OTLP_ENDPOINT` (the base URL without a signal path); managed workflows forward both variants. |
| `OTEL_EXPORTER_OTLP_TRACES_HEADERS` | Secret | Yes | Complete header string, auth and routing included (e.g. `Authorization=Bearer%20<token>,x-mlflow-experiment-id=42`). |
| `OTEL_EXPORTER_OTLP_HEADERS` | Secret | No | Generic (non-signal-specific) OTLP headers. Same format as the traces variant. Useful when a single header set covers all signals. |
| `OTEL_EXPORTER_OTLP_CERTIFICATE` | Variable | No | Path to a PEM CA bundle for backends behind a private CA. Commit the bundle into the config repo (e.g. `.fullsend/otel-ca.pem`) and set the variable to that checkout-relative path. |
| `OTEL_RESOURCE_ATTRIBUTES` | Variable | No | Static `k=v,k=v` trace tags. The value is used verbatim; `${{ github.* }}` expressions evaluate only in workflow YAML, not in variables. |
| `OTEL_SDK_DISABLED` | Variable | No | Set to `true` to disable all telemetry, including the local file exporter. |
| `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` | Variable | No | Set to `true` to attach conversation content to `agent` spans (Level 3; see Content capture). |

Installations scaffolded before OTEL support was added must also forward the
secrets (add `OTEL_EXPORTER_OTLP_TRACES_HEADERS` and
`OTEL_EXPORTER_OTLP_HEADERS` under `secrets:`) until the scaffold is
re-synced: in the `.fullsend` repo's stage workflows (per-org), or in the
fullsend shim workflow's dispatch job (per-repo).

### Bring your own workflow

Add the environment variables to any job that runs `fullsend run`:

```yaml
env:
  OTEL_EXPORTER_OTLP_ENDPOINT: "${{ vars.OTEL_EXPORTER_OTLP_ENDPOINT }}"
  OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: "${{ vars.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT }}"
  OTEL_EXPORTER_OTLP_TRACES_HEADERS: "${{ secrets.OTEL_EXPORTER_OTLP_TRACES_HEADERS }}"
  OTEL_EXPORTER_OTLP_HEADERS: "${{ secrets.OTEL_EXPORTER_OTLP_HEADERS }}"
  OTEL_RESOURCE_ATTRIBUTES: "${{ vars.OTEL_RESOURCE_ATTRIBUTES }}"
  OTEL_SDK_DISABLED: "${{ vars.OTEL_SDK_DISABLED }}"
  OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT: "${{ vars.OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT }}"
  OTEL_EXPORTER_OTLP_CERTIFICATE: "${{ vars.OTEL_EXPORTER_OTLP_CERTIFICATE }}"
```

Any variable and secret names work here; the values reach the exporter
as-is. Consult your backend's documentation for the endpoint URL and
authentication mechanism.

## Eval measurements

After each managed agent run, `fullsend eval-measure` scores
`run-telemetry.jsonl` in the same job (fail-open). Scores land in
`eval-measurements.jsonl` beside telemetry when at least one new score is
produced (tool-agnostic artifact). When `OTEL_EXPORTER_OTLP_*` is set, those
scores also export as `gen_ai.evaluation.result` span events on the same
TraceID (fail-open; does not rewrite `run-telemetry.jsonl`).

Today's scorers (starting with EM-001) read the Level 1/2 **metadata**
contract of `run-telemetry.jsonl` — span tree and attributes, not prompt or
completion bodies. That foundation is intentional: fitness scores must trust
the trace before quality scores can. **Planned:** content-aware scorers that
consume Level 3 prompt/completion capture once Level 3 is implemented —
that is where the real quality signal lives. See
[Eval Measurements](./eval-measurements.md) and
[ADR 0087](../../ADRs/0087-eval-measurements-online-trace-scoring.md).

## See also

- [How To Emit Traces](../user/how-to-emit-traces.md): step-by-step setup guide
- [Tracing Development Guide](../dev/tracing.md): implementation details for contributors
- [Eval Measurements](./eval-measurements.md): online scoring of wild-run traces
- [fullsend run — metrics.json](../../cli/run.md#metricsjson-fields): CLI reference for `total_cost_usd` and other output fields
