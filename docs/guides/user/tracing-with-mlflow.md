# Tracing with MLflow

MLflow 3.6+ ingests OTLP/HTTP traces natively and provides experiment-based
routing, a traces UI, and per-trace cost estimates. This guide covers
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

MLflow's per-trace cost column is its own estimate: it extracts input/output
token counts and prices them against MLflow's internal model table. This
excludes cache-creation and cache-read tokens, which dominate agent-run cost.

The authoritative cost figure is the runtime-reported `fullsend.cost_usd`
attribute on `agent` spans (also in `run-telemetry.jsonl`).

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
