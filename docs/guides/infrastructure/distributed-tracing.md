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
| 3 | Conversation content (assistant text, reasoning, tool calls and — on Claude runs — their arguments and results; on a retry that carries validation feedback, the prompt the runner composed) on `agent` spans | `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true` |

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
| `OTEL_EXPORTER_OTLP_TRACES_HEADERS` | Signal-specific headers | Takes precedence; `key=value` pairs separated by commas; values are URL-decoded. Must use `=` as the delimiter, not `:` |
| `OTEL_EXPORTER_OTLP_HEADERS` | Base headers | Same format as above |

::: warning Header syntax: `key=value`, not `key: value`
OpenTelemetry headers use comma-separated `key=value` pairs (e.g. `Authorization=Bearer%20<token>`), **not** HTTP header-line syntax (`Authorization: Bearer <token>`). Fullsend validates configured headers before creating the exporter. If an entry uses `:` instead of `=`, validation warns on stderr and skips remote OTLP export to prevent header corruption and credential leakage.
:::

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
attaches it to the per-iteration `agent` span. The one input it records,
a retry's validation-feedback prompt, is composed by the runner — fixed
framing around the previous iteration's validation output — rather than
read from the stream, and goes through the same redaction. On both sides
of the pipeline — on the text as written, and again on what the pipeline
returns, since its normalization can join a value the stream split or
spelled in compatibility characters — the value of each sensitive runner
environment key (`env.runner`, or the deprecated `runner_env`), and of each
provider-only key the runner keeps out of it (`GH_WORKFLOW_TOKEN`), 8 bytes or
longer, is replaced with `[REDACTED:<key>]`, the marker validation
feedback uses. Each value replaced in a string counts one redaction per
pass, however often it occurs there; in an
assignment, an authorization header or a secret-named field the patterns
then mask the marker as well and count again. Not covered: a value split
or spelled that way which a pattern also recognises — in one of those
contexts, in a connection string, or by its own prefix — is masked by
that pattern, and shows what the pattern's mask shows. A tool call loses
its `summary` when redaction found a secret in its arguments, the dropped
members included, masked a URL's userinfo, query or fragment in them,
stripped an escape sequence or tag characters from them, or could not
judge them at all (invalid UTF-8 or the replacement character): the parser cut the summary out of them before anything scanned it,
so the secret could be there as a beginning. Where a runtime reports a summary and no
arguments (pi, codex, OpenCode), its parser has already run the patterns
over the summary and, for pi and codex, cut it: a value that straddles a
cut stays in part, and one a pattern recognised stays as that pattern's
mask. The agent runtime's own
content-logging variables (`OTEL_LOG_USER_PROMPTS`,
`OTEL_LOG_ASSISTANT_RESPONSES`, etc.) are never set.

| Variable | Values that enable capture | Values that keep it off |
|----------|---------------------------|-------------------------|
| `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` | `true`, `span_only`, `span_and_event` (case-insensitive) | unset, `false`, `NO_CONTENT`, `event_only`, anything unrecognized |

The variable name and accepted values follow the
[OpenTelemetry GenAI instrumentation convention](https://github.com/open-telemetry/opentelemetry-python-contrib/tree/main/instrumentation-genai).
Fullsend records content on span attributes only, so `event_only` stays
off. An unrecognized value disables capture; telemetry never fails a run.

**Captured:** assistant text, reasoning, tool calls (name, short summary
and `arguments`), and tool results — including any sub-agent activity,
unattributed — as the `gen_ai.output.messages` span attribute: a JSON
string following the
[GenAI output-messages schema](https://github.com/open-telemetry/semantic-conventions/blob/v1.37.0/docs/gen-ai/gen-ai-output-messages.json)
with a `finish_reason` of `stop` or `error`. Tool arguments, tool results, and the `id` that correlates a result with its call, are captured
only when the runtime's stream provides them: Claude runs do; the pi and
codex parsers emit none of the three yet
([#7414](https://github.com/fullsend-ai/fullsend/issues/7414)).
`arguments` holds the members of the call's input that name what was
called, listed by tool, each redacted as text: `Bash` — `command`,
`description`, `timeout`; `Read` — `file_path`, `limit`, `offset`;
`Write` — `file_path`; `Edit` — `file_path`, `replace_all`;
`NotebookEdit` — `notebook_path`, `cell_id`, `cell_type`, `edit_mode`;
`Grep` — `pattern`, `path`, `glob`, `type`, `output_mode`, `context`,
`head_limit`, `multiline`, `-A`, `-B`, `-C`, `-i`, `-n`; `Glob` —
`pattern`, `path`; `WebFetch` — `url`; `Agent` — `description`, `model`,
`subagent_type`; `Skill` — `skill`. Nothing else of the input is
recorded: a file body, an edit, a prompt, a notebook source, a todo list,
any member whose value is an object or an array, and the whole input of
a tool not listed here (every `mcp__` tool among them) are dropped —
scanned first, so a secret in them still counts, but neither charged nor
marked, since the list is the policy and the markers mean loss. A Claude
call's `summary` is cut from its input separately — a `Bash` command or an
`Agent` prompt collapsed to one line and cut at 120 characters, a `Read`,
`Write` or `Edit` path at 200, a `Grep` or `Glob` pattern at 50 — so the
start of a prompt `arguments` drops can still appear there. A `Bash`
`command` is recorded whole, up to 8 KiB, so a heredoc or an inline
script records what it writes. A URL anywhere in the record — a
member, text, a result, a summary, the input message, a name — has
everything from its `://` (or the same with its colon or slashes escaped for a shell or JSON) to its last `@`
(userinfo — a password holding a `/` or a URL nested in another's path
included; an `@` in a path is masked with it, the host too) and
everything after its first `?` or `#` masked `***`, after the patterns
ran, and counted in `fullsend.content.sanitizations`; when the first `?`
or `#` comes before the last `@` — a password holding one, an `@` in the
query — everything after the separator is masked, and so it is after a
separator with any part percent-encoded, anywhere in the URL
(`https%3A%2F%2F…`, `https%3A//…`, `https:%2F%2F…`, encoded once or more),
since nothing decodes it, and when the last `@` follows one — a URL with
userinfo nested in another's path; behind a plain `://`, a percent-encoded
`@`, `?` or `#` (`%40`, `%3F`, `%23`, encoded once or more) reads as the
character it encodes. The URL runs to white space or to a
double quote that could close a JSON string — one followed by white
space, `,`, `:`, `}`, `]` or the end — so one inside JSON ends with its
string and the fields after it are kept; inside JSON escaped in another
string (`\"`) it runs on. Kept as well, under the named limit for a
secret no pattern matches: a token carried in the path itself, as a
webhook URL's is; in a URL holding a quote that ends it, what follows the
quote and userinfo whose `@` follows it. The kept
members are decoded, redacted one by one and encoded again, so key
order, spacing and escapes are not the stream's; a string a terminal
escape sequence or tag characters were
stripped from, or that holds invalid UTF-8 or the replacement character
a lossy decode leaves (U+FFFD), is masked whole, since the
stripping can leave most of a token, a later write would join the
halves of a value an invalid byte splits, and no pattern spans the
replacement character.

On a retry iteration under `validation_loop.feedback_mode: append`, the
prompt the runner composed — its fixed framing around the previous
iteration's validation output — is recorded as `gen_ai.input.messages`: one
`user` message with one text part, following the
[GenAI input-messages schema](https://github.com/open-telemetry/semantic-conventions/blob/v1.37.0/docs/gen-ai/gen-ai-input-messages.json).
The attribute is absent on the first iteration and on a retry without
feedback, which send the runtime's fixed default prompt.

**Not captured:** the rest of the model's input, and pre/post-script
content. The fixed default prompt is a constant that carries no task and
is left out by choice; the system prompt and per-request context the
runtime builds from the agent definition are not visible to fullsend,
which sees the runtime's stream, not its API requests.

**Redaction and size:** every part passes through security redaction
(runner environment values, Unicode normalization, secret masking, then
runner environment values again; a string the normalizer stripped an
escape sequence or tag characters from, or that holds invalid UTF-8 or
the replacement character, is masked whole) before reaching the span. What
that guarantees, and its four named limits, is stated in the
[developer guide](../dev/tracing.md) beside the test that checks it.
Content is bounded at 256 KiB per iteration — each tool result at 8 KiB,
each call's arguments at 8 KiB —
kept as an ordered suffix; overflow drops the oldest content first. Those
bounds count raw bytes, except that arguments count as the
re-encoded JSON described above, against their own bound and against the
total (as redacted text when the arguments were not one JSON object). That encoding writes `<`, `>` and `&` as six bytes each, so
arguments dense in those characters reach their bound at about a sixth of
that size on the stream. The exported JSON string is bounded as well, at
255,000 bytes, because encoding adds 8–10% at these bounds on real runs
and up to six times on escape-dense content — a record over that is trimmed again,
oldest first.
Truncation is marked via `fullsend.content.truncated` on the span and
`fullsend.truncated` on each cut part. A tool result over its bound keeps
its tail; arguments over theirs, or not a complete JSON object, are
dropped whole — a cut object is not JSON — and the marked `tool_call`
part keeps its `id`, name and summary — not the summary when redaction
found a secret in the arguments, masked a URL's userinfo, query or
fragment in them, stripped an escape sequence or tag characters from
them, or could not judge them at all. A member outside the recorded list (above) is
dropped without a mark, its listed members kept. A name that redacts to
nothing never had arguments: they are kept only under a tool's exact
name, plain words no scan touches. So
`fullsend.truncated` means different things by part type: on a tool
result, part of the response is kept; on a tool call, the arguments were
dropped whole. Read it together with the part's `type`. The input message is not cut at this stage:
the validation output inside it is cut at 10 KiB when the prompt is
composed (the prompt then says `[truncated]`), and the two markers above
do not describe it. The recorded copy is the redacted one, so it can
differ from the prompt the agent received: compatibility characters are
folded, which can make it about eleven times longer, and its encoded size
counts toward the 255,000 bytes. A tool result whose stream line
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
calls per iteration and as many results, sub-agents included; 41–100 KB
of arguments, with the arguments of 0–3 calls over their bound) these
bounds evict 31–63% of tool results — 28–56% before arguments shared the
total. A 1 MiB total with the same per-part bounds evicts none
(records of 456–1,011 KB); raising only the per-result bound to 32 KiB
evicts 47–91%; keeping every result whole takes 1.1–2.3 MB. No run of an
agent that writes files was measured; a `Write` call carries the file
body as an argument. So the total
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
names and call ids get the redaction span content gets (runner environment
and provider-only values, Unicode normalization, secret redaction, then
those values again): a name is redacted in place and
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
| `gen_ai.input.messages` | `agent` | Level 3 only, on a retry iteration that carries validation feedback: the prompt the runner composed, as a JSON string (see Content capture) |
| `gen_ai.output.messages` | `agent` | Level 3 only: the iteration's conversation content as a JSON string (see Content capture) |
| `fullsend.content.truncated` | `agent` | Level 3 only: present (`true`) when the size budget cut or dropped content, a tool call's arguments were dropped whole, or a kept tool result is a parser-side fragment or an oversized line's empty stand-in (`fullsend.truncated` on the part; no byte count for the last two) |
| `fullsend.content.dropped_bytes` | `agent` | Level 3 only: exact part bytes removed by the size budget — content, ids, arguments dropped whole (counted as re-encoded JSON when the bound drops them, or as redacted text when the arguments were not one JSON object; a member the recorded list leaves out is not counted), and the fixed footprint of an errored-empty or oversized stand-in part — otherwise in raw bytes, whichever bound made the cut; the bytes of a skipped oversized line were never decoded and are not counted |
| `fullsend.content.redactions` | `agent` | Level 3 only: number of known secrets matched while redacting content at assembly — the secret redactor's patterns and the runner-env literal pass — the input message included (and findings from parts the size budget later dropped) |
| `fullsend.content.sanitizations` | `agent` | Level 3 only: number of strings masked or rewritten for their shape rather than for a secret they matched — compatibility characters folded, invisible characters removed, an escape sequence or tag characters stripped or invalid UTF-8 or U+FFFD present (each such string is `***` whole, since it can hide a secret), a retry prompt altered upstream (`***` whole), a URL's userinfo, query or fragment masked |
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

Inside a run, the runner also writes the per-iteration **agent** span as
`TRACEPARENT` into `.fullsend/iteration.env` so local runtimes (claude, pi,
codex) can attach their telemetry to that span. Host-side pre/post scripts
still receive the run-root value; the two parents share a trace ID but have
different span IDs. An inbound unsampled flag (`-00`) is preserved on both
paths.

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
re-synced, in the fullsend shim workflow's dispatch job.

`fullsend run` reads the OTLP header secrets for its own exporter but does not
pass them to host-side scripts unless the harness declares them under
[`env.runner`](../../reference/harness-reference.md#field-details).

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

## Mint tracing

The token mint takes part in the same trace model. It records one server
span per request and child spans for each stage, then exports them over
OTLP/HTTP (JSON encoding) when a traces endpoint is configured.

**Availability:** tracing is configurable for the
[standalone mint](standalone-mint.md#tracing), for GCP Cloud Function
mints, and for Cloudflare Worker mints, durable and preview, through
`fullsend mint deploy --otlp-traces-endpoint` and `--otlp-headers-file`
(see [`mint deploy` tracing for GCP](../../cli/mint.md#tracing-gcp) and
[for Cloudflare](../../cli/mint.md#tracing)). On GCP the endpoint is
stamped into the generated function source and collector headers are
stored in a per-deploy Secret Manager secret
(`fullsend-mint-otlp-headers-<random>`) whose ID is stamped next to the
endpoint; the function reads it at cold start, and rotating the headers
stores a new secret and redeploys the function. On Cloudflare the endpoint is
compiled into the Worker WASM binary; collector headers are stored as
a Worker secret (`MINT_OTLP_HEADERS`, scoped to the version, for previews; a
per-deploy `MINT_OTLP_HEADERS_<random>` name for durable deploys). The
instrumentation is shared by all backends (it lives in
`internal/mintcore`). Behaviour-test preview mints export to the suite's
[job-scoped collector](../dev/behaviour-testing.md#job-scoped-trace-collection).

**Propagation:** the mint client (`internal/mintclient`) starts a
`fullsend.mint.token` client span when the caller's context has an active
span. It sends W3C `traceparent` and `tracestate` headers to the mint. No
headers are sent when no trace context is active. The mint extracts the
incoming context, so its server span becomes a child of the client span.
It also keeps the upstream sampling decision: an unsampled parent (`-00`)
produces no export. The mint returns a W3C `traceresponse` header that
identifies its server span. Client retries reuse the client span, so
each attempt shows up as a sibling server span, and the client span
records `fullsend.mint.attempts`.

**Spans:**

| Span | Kind | Stage recorded on failure |
|------|------|---------------------------|
| `POST /v1/token`, `GET /v1/status`, … | Server | (aggregated, see below) |
| `mint.oidc_verify` | Internal | `oidc_verification`, or `authorization` when the token verifies but enrollment or workflow-ref policy rejects it |
| `mint.status_auth` | Internal | `authentication` |
| `mint.authorize` | Internal | `authorization` |
| `mint.foreign_grant_fallback` | Internal | — (records `fullsend.mint.fallback_result`) |
| `mint.foreign_grant_check` | Internal | `foreign_grant` (records `fullsend.mint.foreign_grant_scope`, `repo` or `org`) |
| `mint.pem_access` | Internal | `pem_access` |
| `mint.github_app_auth` | Internal | `github_app_auth` |
| `mint.installation_lookup` | Internal | `installation_lookup` |
| `mint.create_installation_token` | Internal | `token_issuance` |
| `mint.bot_role_config` | Internal | `bot_role_config` (`POST /v1/bot-role`: invalid role App ID registration) |
| `mint.bot_role_credentials` | Internal | `bot_role_config`, `pem_access` or `github_app_auth`, only when the credential is required; an unavailable credential for the `/apps` lookup records `fullsend.bot_role.skip_reason` instead |
| `mint.bot_role_apps_token` | Internal | `token_issuance`, never recorded as a failure: when no installation token can be obtained for the `/apps` lookup (the App is not installed on the repo, or the token request fails) the span records `fullsend.bot_role.failure_step` and `skip_reason` and the lookup is anonymous |
| `mint.github_apps_lookup` | Internal | `github_apps_lookup` |

`POST /v1/bot-role` also reuses `mint.installation_lookup`. The
`mint.github_apps_lookup` and `mint.installation_lookup` spans of a bot-role
request record `fullsend.bot_role.attempts`, `upstream_class` (`ok`,
`not_found`, `transport`, `4xx`, `5xx`, `malformed_response`, `local`),
`upstream_status`, `rate_limit` (`none`, `primary`, `secondary`, `other`),
`retry_decision` (`succeeded`, `not_retryable`, `attempts_exhausted`,
`wait_exceeds_bound`, `wait_exceeds_deadline`, `context_done`,
`sleep_interrupted`), `wait_ms`, `wait_bound_ms` (the 5-second retry wait
bound) and `result` (`ok`, `no_match`, `credential_rejected`, `error`), and
carry one `attempt` event per try with the same per-attempt values. The
`/apps` span adds `apps_auth` (`installation_token` or `anonymous`). None of these
carry the actor login, App slug, repository, credentials or response bodies.

`GET /health` is not traced. Unknown routes are recorded as `other`, so
paths supplied by callers never reach the collector.

**Server span attributes:** `http.request.method`, `url.path`,
`http.response.status_code`, `fullsend.mint.outcome` (`success`,
`denied`, `rejected`, `error`), `fullsend.mint.failure_stage`,
`fullsend.mint.backend`, `fullsend.mint.deployment`, `fullsend.mint.role`,
`fullsend.mint.level`, `fullsend.mint.repo_count`,
`fullsend.mint.caller_org`, `fullsend.mint.source_repo`,
`fullsend.mint.workflow_ref`, `fullsend.mint.target_org`,
`fullsend.mint.cross_org`, and on success `fullsend.mint.app_id`,
`fullsend.mint.installation_id`, and `fullsend.mint.repository_selection`.
The `mint.foreign_grant_check` span also records
`fullsend.mint.foreign_grant_scope` (`repo` or `org`). Server spans get error status only for 5xx responses. Stage spans get
error status plus an `exception` event when their stage fails.

**Resource attributes:** `service.name` (`fullsend-mint`),
`service.version`, `fullsend.mint.commit`, `fullsend.mint.backend`,
`fullsend.mint.deployment`.

**Security boundaries:** spans never carry OIDC JWTs, installation tokens,
PEM material, or collector authentication headers. Error text on spans is
capped at 256 bytes, and anything shaped like a JWT, GitHub token, bearer
credential, `Authorization` header value, URL userinfo, sensitive URL query
value, or PEM block is redacted. Failed GitHub installation-token
calls record only the HTTP status, never the response body. The mint
client records only the failure stage and a fixed error class (`timeout`,
`canceled`, `request_failed`, or `error`) on its span.

**Fail-open:** each request's spans are exported in one request bounded by
a 2-second timeout. Collector errors, non-2xx responses, and timeouts are
logged (without header values) and never change the mint response. The
standalone mint exports in the background after the response is returned;
serverless backends export before returning because they cannot run work
afterwards. OTLP export does not follow redirects, so collector
credentials and span payloads are never forwarded to another origin. On
the Cloudflare Worker the host fetch bridge uses `redirect: "manual"` for
export requests. A Worker export adds at most the 2-second export timeout
to the response. If the collector headers secret cannot be parsed, the
Worker logs a warning without header values and runs with tracing
disabled.

**Export budget:** an inbound `traceparent` sampled flag is honored only
within a local budget of 20 sampled requests per second; requests beyond it
are treated as unsampled. At most 4 exports run concurrently; additional
exports are dropped rather than queued.

The mint does not use the OpenTelemetry Go SDK. It uses a small built-in
exporter so the Cloudflare Worker WASM binary stays within its size
limit. See [Mintcore Architecture](../../contributing/mintcore.md#tracing).

## See also

- [How To Emit Traces](../user/how-to-emit-traces.md): step-by-step setup guide
- [Tracing Development Guide](../dev/tracing.md): implementation details for contributors
- [Eval Measurements](./eval-measurements.md): online scoring of wild-run traces
- [fullsend run — metrics.json](../../cli/run.md#metricsjson-fields): CLI reference for `total_cost_usd` and other output fields
