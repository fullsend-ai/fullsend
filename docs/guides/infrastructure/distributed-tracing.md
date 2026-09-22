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
| 3 | Conversation content (assistant text, reasoning, tool calls, and — on Claude runs — tool results) on `agent` spans | `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true` |

All levels produce metadata (timing, token counts, tool names, errors),
including up to one `execute_tool` span per id-bearing tool call under each
`agent` span (Claude Code today; pi and codex emit no call ids, [#7414](https://github.com/fullsend-ai/fullsend/issues/7414)),
capped at 1,024 per iteration.
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

**Captured:** assistant text, reasoning, tool calls (name plus short
summary), and tool results — including any sub-agent activity,
unattributed — as the `gen_ai.output.messages` span attribute: a JSON
string following the
[GenAI output-messages schema](https://github.com/open-telemetry/semantic-conventions/blob/v1.37.0/docs/gen-ai/gen-ai-output-messages.json)
with a `finish_reason` of `stop` or `error`. Tool results, and the `id` that correlates each with its call, are captured
only when the runtime's stream provides them: Claude runs do; the pi and
codex parsers emit neither yet
([#7414](https://github.com/fullsend-ai/fullsend/issues/7414)).

**Not captured:** model input (`gen_ai.input.messages`) and
pre/post-script content. First-iteration runs have no meaningful
runner-side input; retry iterations carry the injected validation
feedback, a natural input-capture follow-up.

**Redaction and size:** every part passes through security redaction
(Unicode normalization, then secret masking) before reaching the span.
Content is bounded at 256 KiB per iteration — each tool result at 8 KiB —
kept as an ordered suffix; overflow drops the oldest content first. Those
bounds count raw bytes; the exported JSON string is bounded as well, at
255,000 bytes, because encoding adds 9–11% at these bounds on real runs
and up to six times on escape-dense content — a record over that is trimmed again,
oldest first.
Truncation is marked via `fullsend.content.truncated` on the span and
`fullsend.truncated` on each cut part. A tool result whose stream line
exceeds the parser's 1 MiB bound is kept as an empty, marked
`tool_call_response` part — the call was answered, its content is lost and
its `is_error` unknown — provided the line shows the call id within its
first 1 MiB, where Claude Code normally writes it (ahead of the content; an
id serialized after the content is not recovered, and that call closes
`unanswered`). Two cases are absent rather than truncated: any other stream
line beyond 1 MiB is skipped whole by the parser, and results whose content is entirely non-text (for example
images) produce no part. A result that mixed text with non-text blocks keeps
its text and is marked `fullsend.truncated`; a failed call with empty
output survives as a `tool_call_response` part carrying `is_error`. The SDK's span attribute length cap is
lifted while capture is on; an explicit
`OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT` still wins and will cut content
mid-JSON — fullsend warns on stderr at startup.

**Size limits:** none of these bounds is a measured backend limit. The
only acceptance proof is one 255,082-byte attribute, read back whole from
the pilot MLflow backend on 2026-08-20; nothing larger was ever sent, so
the backend's ceiling is unknown. The runner imposes nothing lower: the
SDK cap is lifted, the exporter is OTLP over HTTP with no message limit of
its own, and the file sink has none. The bounds stay because guessing
wrong is costly — a backend that refuses an oversized request refuses the
whole batch, up to 512 spans with their Level 1 metadata, and the exporter
does not retry a refusal. On three captured review runs (117–255 tool
results per iteration, sub-agents included) these bounds evict 28–56% of
tool results. A 1 MiB total with the same per-result bound evicts none
(records of 412–938 KB); raising only the per-result bound to 32 KiB
evicts 47–91%; keeping every result whole takes 1.1–2.2 MB. So the total
is the bound to raise first, once the size is proven on the target
backend: send attributes of increasing size and read each back whole
([#7415](https://github.com/fullsend-ai/fullsend/issues/7415)).

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
    └── execute_tool (one per id-bearing tool call; gen_ai.operation.name=execute_tool)
```

`execute_tool` spans are named `execute_tool <tool name>`. One starts when
the runtime reports a tool call (its arguments complete) and ends when it
reports the result — both are runner-side receipt times, which trail the
sandbox by the pipe latency and the parser's decode of the line, stamped after the
console renderer's output for the call (it prints nothing for a result) and
before that event's Level 3 content processing. Events are handled one at a
time, so with several calls open a time can still trail the processing of
an earlier event, such as another call's large result. The span approximates
execution rather than measuring it: both times trail the sandbox, and when
the start trails by more than the end (an earlier event was still being
processed when the call arrived) the span is shorter than the execution it
covers. A call with no result
by the end of the iteration is closed with `error.type=unanswered`; a
result whose stream line exceeded the parser's 1 MiB bound ends its span on
receipt, marked `fullsend.tool.result_oversized` with no status and no
`error.type` — the tool answered, and whether it failed was never decoded; a
result whose call was never reported (its stream line was skipped) is a
near-zero-duration span marked `fullsend.tool.unmatched`. Runtimes whose parsers
emit no call ids (pi, codex) produce no `execute_tool` spans, and neither
do server-side tools, whose result never arrives as a `tool_result`. Tool
names and call ids pass through the same sanitizer as span content (Unicode
normalization, then secret redaction): a name is redacted in place and
bounded to 256 bytes for the attribute and 128 for the span name; an id with
any finding is dropped from the span. At most 1,024 `execute_tool`
spans are recorded per iteration; calls past that are counted in
`fullsend.tool_spans.dropped` on the `agent` span. Tool content never rides
these spans — see Content capture.

### SpanKind

| Span | Kind | Condition |
|------|------|-----------|
| `run` | Consumer | Valid inbound `TRACEPARENT` (dispatched by an instrumented system) |
| `run` | Internal | No inbound `TRACEPARENT` (local/manual invocation) |
| `sandbox_create` | Internal | Always |
| `agent` | Internal | Always |
| `execute_tool` | Internal | Always |

## Span attributes

### GenAI semantic convention attributes

These follow the [OTel GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/)
and are recognized by LLM-aware backends for GenAI dashboards.

| Attribute | Example | Present on |
|-----------|---------|------------|
| `gen_ai.operation.name` | `invoke_agent` | `run`, `agent` (`create_agent` on `sandbox_create`; `execute_tool` on `execute_tool`) |
| `gen_ai.agent.name` | `triage` | `run`, `agent` |
| `gen_ai.tool.name` | `Bash` | `execute_tool` (the runtime's tool name; absent when the call was never reported) |
| `gen_ai.tool.call.id` | `toolu_01…` | `execute_tool` (absent when the id carried a security finding — a tainted id is dropped, never substituted, so it cannot collide with another call's) |
| `gen_ai.system` / `gen_ai.provider.name` | `anthropic` / `anthropic-vertex` | `agent` (serving endpoint of the model used on this span — varies by runtime; `system` is the pre-v1.37 name — both keys are emitted with the same value so EM-001 and modern backends agree. Not the runtime name: `fullsend.runtime` is the harness.) |
| `gen_ai.request.model` | `claude-opus-4-6` | `agent` (resolved model) |
| `gen_ai.usage.input_tokens` / `output_tokens` / `cache_*_input_tokens` | `109938` | `agent` |

Provider identity is the **serving endpoint**, not the model publisher and not the agent runtime. Claude Code reports `anthropic`. Pi reports the prefix of the resolved `provider/id` spec (`anthropic-vertex`, `xai-vertex`, `google-vertex`, `openai`, `anthropic`): a Claude model on Vertex is `anthropic-vertex` even though the publisher is Anthropic, because that is the catalog and credential path the run used. `fullsend.runtime` (`claude`, `pi`, …) stays a separate Fullsend attribute. Fullsend does not emit `mlflow.*` attributes; backends that derive native cost fields do so from these portable GenAI keys.

The `agent` span's provider identity reflects only the parent run's serving endpoint. When a Pi run dispatches subagents on different vendors, their usage is folded into the same span's token/cost totals without its own provider attribution — a mixed-vendor Pi run can attach multi-provider usage to a span identified by a single provider.

> **Breaking change — Pi runtime:** `agent` spans from the Pi runtime used to
> report the literal string `pi` under `gen_ai.system`. They now report the
> resolved serving-endpoint provider under both `gen_ai.system` and the
> newly emitted `gen_ai.provider.name`
> (`anthropic-vertex`, `xai-vertex`, `google-vertex`, `openai`, `anthropic`).
> Downstream consumers that filtered or classified on
> `gen_ai.system == "pi"` will silently stop matching Pi-runtime spans and
> must switch to `fullsend.runtime == "pi"` to identify Pi-originated spans,
> or update their provider allowlist to include the resolved values above.

### Fullsend-specific attributes

| Attribute | Present on | Description |
|-----------|------------|-------------|
| `fullsend.runtime` | `agent` | Harness identity (`claude`, `pi`, …), distinct from `gen_ai.system` (the serving endpoint) |
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
| `fullsend.content.truncated` | `agent` | Level 3 only: present (`true`) when the size budget cut or dropped content, or a kept tool result is a parser-side fragment or an oversized line's empty stand-in (`fullsend.truncated` on the part; no byte count) |
| `fullsend.content.dropped_bytes` | `agent` | Level 3 only: exact part bytes removed by the size budget — content, ids, and the fixed footprint of an errored-empty or oversized stand-in part — in raw bytes whichever bound made the cut; the bytes of a skipped oversized line were never decoded and are not counted |
| `fullsend.content.redactions` | `agent` | Level 3 only: number of security findings raised while redacting content at assembly (including findings from parts the size budget later dropped) |
| `fullsend.tool.unmatched` | `execute_tool` | Present (`true`) when a result arrived for a call the stream never reported; the span has near-zero duration |
| `fullsend.tool.result_oversized` | `execute_tool` | Present (`true`) when the result's stream line exceeded the parser's 1 MiB bound: the call was answered but nothing of the result was decoded, so the span has no status and no `error.type` |
| `fullsend.tool_spans.dropped` | `agent` | Present when the iteration hit the 1,024-span cap and at least one id-bearing call was refused a span: the number of `tool_use` events with a usable id that arrived past the cap, each counted once whatever its result later does; a result with no open span past the cap is not counted |

### Common attributes

| Attribute | Present on | Description |
|-----------|------------|-------------|
| `exit_code` | `run`, `agent` | Process exit code |
| `iteration` | `agent` | 1-based iteration index |
| `error.type` | `execute_tool` | `tool_error` when the runtime flagged the result `is_error`; `unanswered` when the call had no result by the end of the iteration (the runtime was stopped, or an over-long result line showed no call id within its first 1 MiB) or the runtime reported the same call id again (the earlier open call is superseded); absent on success |

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
and display behavior of that value across every output surface.

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
counts. A missing runtime cost propagates as `$0.00` on all surfaces.

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

## Output file format

`run-telemetry.jsonl` contains one JSON object per line. Each object is a
complete OTLP `TracesData` message with hex-encoded trace/span IDs (per
the OTLP JSON spec, not base64).

Non-finite float values (NaN, Infinity) are encoded as proto3 JSON strings
(`"NaN"`, `"Infinity"`, `"-Infinity"`).

The file is written synchronously per span. Spans are flushed to disk as
they complete; the file is the forensic record for crashed runs. Every
`execute_tool` span is one such line, so a tool-heavy iteration (a hundred
or more id-bearing calls — Claude Code today) adds up to that many, capped
at 1,024.

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
