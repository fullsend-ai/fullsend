# Tracing with MLflow

MLflow 3.6+ ingests OTLP/HTTP traces natively and provides experiment-based
routing, a traces UI, and, from 3.10, per-trace cost estimates. This guide covers
MLflow-specific configuration on top of the general
[How To Emit Traces](how-to-emit-traces.md) setup.

## Before you begin

- MLflow **3.6 or later** running with a traces endpoint at
  `{server}/v1/traces`.
- A completed [How To Emit Traces](how-to-emit-traces.md) setup (endpoint
  variable and header secret configured).

## Endpoint and experiment routing

MLflow routes traces to an experiment via the `x-mlflow-experiment-id`
header. Append it to the authentication header in the same secret:

```bash
gh secret set OTEL_EXPORTER_OTLP_TRACES_HEADERS \
  --body "<authentication-header>,x-mlflow-experiment-id=<id>" \
  --repo <owner/repo>
```

Use `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` (signal-specific) rather than the
base endpoint, because MLflow's traces path is `{server}/v1/traces` and the
SDK would otherwise append `/v1/traces` to the base URL:

```bash
gh variable set OTEL_EXPORTER_OTLP_TRACES_ENDPOINT \
  --body "https://mlflow.example.com/v1/traces" \
  --repo <owner/repo>
```

## Basic auth encoding

Header values are URL-decoded by the OTel SDK. Encode spaces as `%20`:

```bash
# Base64-encode the credentials
CREDS_B64=$(echo -n "user:password" | base64)

# Set the header with URL-encoded space
gh secret set OTEL_EXPORTER_OTLP_TRACES_HEADERS \
  --body "Authorization=Basic%20${CREDS_B64},x-mlflow-experiment-id=<id>" \
  --repo <owner/repo>
```

## Organizing traces for an org

Two conventions keep a shared MLflow instance navigable as repos onboard:

1. **One experiment per org.** Create an experiment per org (e.g.
   `fullsend-<org>`) and point the org's header secret at its ID. MLflow's
   per-experiment access controls then align with org boundaries.

2. **Slice inside the experiment with resource attributes.** The standard
   `OTEL_RESOURCE_ATTRIBUTES` variable tags every trace:

   ```yaml
   # In workflow YAML (where ${{ github.* }} evaluates):
   env:
     OTEL_RESOURCE_ATTRIBUTES: "fullsend.repo=${{ github.repository }},fullsend.agent=triage,deployment.environment=prod"
   ```

   On the managed path, set the `OTEL_RESOURCE_ATTRIBUTES` Actions variable
   to a static value instead; variables are not expression-expanded.

   Enable these as columns in MLflow's Traces table.
   `fullsend.work_item_id` on the root `run` span correlates runs for the
   same issue.

## Cost column caveat

MLflow's per-trace cost column is its own estimate. MLflow computes it
from 3.10 on: 3.10 only when litellm is installed on the server, and 3.11
and later through MLflow's own model table when it is not. From 3.12 the
estimate covers the span's input, output, cache-creation, and cache-read
token counts. It prices all of a span's usage at the rates of the single
model the span names, so runs that use fallback or sub-agent models are
priced approximately.

The estimate also depends on the versions on the MLflow server:

- **MLflow 3.10 and 3.11** read only the input and output counts from the
  span. Because `gen_ai.usage.input_tokens` includes cached tokens, they
  price every cached token at the plain input rate instead of the cache
  rates. The estimate is too high when cache reads outweigh cache writes.
- **litellm before 1.77.3** (checked back to 1.75.8) subtracts cache-read
  tokens from input but not cache-creation tokens, so it charges
  cache-creation tokens twice.
- **litellm before 1.83.10** does not floor that subtraction at zero. A
  span written before fullsend included the cache counters in
  `gen_ai.usage.input_tokens` has no
  `fullsend.usage.input_includes_cache` attribute. On MLflow 3.12 or later
  with such a litellm, it can show a negative cost.
- **litellm long-context rates.** Where a model's price table has higher
  rates above a prompt-size threshold, litellm applies them to the whole
  span once the span's input count passes the threshold. A span covers a
  whole iteration, usually many requests, so its input count can pass the
  threshold when no single request did. The estimate is then too high.

MLflow computes the cost when it stores a span. Upgrading MLflow or
litellm changes the estimate for new traces only; traces already stored
keep the cost they were given.

The authoritative cost figure is the runtime-reported `fullsend.cost_usd`
attribute on `agent` spans (also in `run-telemetry.jsonl`). When that value
is zero, or when you need a list-price figure rather than the
runtime's reported estimate, compute it from the persisted token fields
— see [Computing dollar cost from token telemetry](../infrastructure/distributed-tracing.md#computing-dollar-cost-from-token-telemetry).

## Level 3 content

With content capture enabled (see
[How To Emit Traces](how-to-emit-traces.md#capture-conversation-content)),
the conversation lives on each `agent` span as `gen_ai.output.messages`
(and, on a retry that carried validation feedback, the runner's prompt as
`gen_ai.input.messages`):
open the trace and select the span to read it. The trace list's
Request/Response preview columns derive from the root span only (capped
at 1000 characters), so they stay empty for fullsend traces — content is
deliberately not duplicated onto the root span.

## Local development

Start a local MLflow instance and point the exporter at it:

```bash
uvx mlflow server
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="http://localhost:5000/v1/traces"
export OTEL_EXPORTER_OTLP_TRACES_HEADERS="x-mlflow-experiment-id=0"
fullsend run triage ...
```

See [Running Agents Locally](../user/running-agents-locally.md) for full
flags. View traces at `http://localhost:5000`.

## See also

- [How To Emit Traces](how-to-emit-traces.md): general setup for any OTLP backend
- [Tracing reference](../infrastructure/distributed-tracing.md): span
  schemas, attribute tables, and environment variable reference
