# How To Emit Traces

## Overview

Fullsend emits OpenTelemetry traces for every agent run. By default, traces
are written to the `run-telemetry.jsonl` file that gets uploaded into an artifact
in the workflow. Fullsend is able to send traces to a remote OpenTelemetry-compatible
endpoint.

Follow this guide to configure a GitHub repository or organization to send traces
to a backend like MLflow, Jaeger, Grafana Tempo, etc.

## Before you begin

- An **OTLP/HTTP-compatible endpoint** and its URL (e.g.
  `https://mlflow.example.com:4318/v1/traces`).
- **Authentication credentials** for the endpoint (bearer token, basic
  auth, or API key) if required.
- **Network reachability** from where runs execute (your machine or CI
  runners) to the backend endpoint.
- The **`gh` CLI** installed and authenticated, or access to the GitHub UI
  for setting variables and secrets.

## Send traces from a repository

1. Determine the endpoint URL where your backend receives traces.

   For most systems this is `<host>:4318/v1/traces`.

2. Set the endpoint as a GitHub Actions variable on the repository.

   ```bash
   gh variable set OTEL_EXPORTER_OTLP_ENDPOINT --body "https://example.com:4318" --repo <owner/repo>
   ```

   `OTEL_EXPORTER_OTLP_ENDPOINT` gets `/v1/traces` appended automatically
   by the SDK. If your backend uses a different path, use
   `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` and specify the full URL instead.

3. Build the authentication header for your endpoint.

   The header format depends on your backend. Common patterns include:

   - Bearer token: `Authorization=Bearer%20<token>`
   - Basic auth: `Authorization=Basic%20<base64 of user:password>`

    `%20` is the encoding for space, don't use spaces directly.

4. Set the authentication header as a GitHub Actions secret on the repository.

   ```bash
   gh secret set OTEL_EXPORTER_OTLP_TRACES_HEADERS --body "<authentication-header>" --repo <owner/repo>
   ```

5. *(Optional)* Add backend-specific routing headers to the same secret.

   Some backends require additional headers. For MLflow, append the
   experiment ID:

   ```bash
   gh secret set OTEL_EXPORTER_OTLP_TRACES_HEADERS \
     --body "<authentication-header>,x-mlflow-experiment-id=<id>" \
     --repo <owner/repo>
   ```

6. *(Optional)* Set resource attributes to tag traces with metadata.

   ```bash
   gh variable set OTEL_RESOURCE_ATTRIBUTES \
     --body "deployment.env=prod,category=a" \
     --repo <owner/repo>
   ```

   Resource attributes may become filterable trace tags depending on your backend.

## Send traces to an endpoint from a GitHub organization

1. Set the endpoint as an organization-level variable.

   To make it visible to all repositories:

   ```bash
   gh variable set OTEL_EXPORTER_OTLP_ENDPOINT \
     --body "https://example.com:4318" \
     --org <org> --visibility all
   ```

   To restrict visibility to specific repositories:

   ```bash
   gh variable set OTEL_EXPORTER_OTLP_ENDPOINT \
     --body "https://example.com:4318" \
     --org <org> --repos repo1,repo2,repo3
   ```

2. Set the authentication header as an organization-level secret with the
   same visibility.

   ```bash
   gh secret set OTEL_EXPORTER_OTLP_TRACES_HEADERS \
     --body "<authentication-header>" \
     --org <org> --visibility all
   ```

   To restrict visibility to specific repositories:

   ```bash
   gh secret set OTEL_EXPORTER_OTLP_TRACES_HEADERS \
     --body "<authentication-header>" \
     --org <org> --repos repo1,repo2,repo3
   ```

## Capture conversation content

Set `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` to add the agent's
text, reasoning, and tool calls to each `agent` span, in the local file and
at the endpoint. Content is redacted for secrets and bounded per iteration,
but may still contain proprietary code or PII — make sure your backend's
access controls fit before enabling it.

```bash
gh variable set OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT --body "true" --repo <owner/repo>
```

## Disable trace export

Remove the endpoint variable and header secret from the repository or
organization to stop sending traces to the endpoint. The local
`run-telemetry.jsonl` file continues to be produced.

```bash
gh variable delete OTEL_EXPORTER_OTLP_ENDPOINT --repo <owner/repo>
gh secret delete OTEL_EXPORTER_OTLP_TRACES_HEADERS --repo <owner/repo>
```

## Disable the local trace file

Set `OTEL_SDK_DISABLED` to suppress all telemetry output, including the
local file and any configured endpoint export.

```bash
gh variable set OTEL_SDK_DISABLED --body "true" --repo <owner/repo>
```

To disable at the organization level and then re-enable individual
repositories:

```bash
gh variable set OTEL_SDK_DISABLED --body "true" --org <org> --visibility all
gh variable set OTEL_SDK_DISABLED --body "false" --repo <owner/repo>
```

## See also

- [Tracing with MLflow](tracing-with-mlflow.md): experiment routing, Basic
  auth encoding, org-level organization, and cost column caveats
- [Tracing Reference](../infrastructure/distributed-tracing.md): span
  schemas, attribute tables, and environment variable reference
- [Tracing Development Guide](../dev/tracing.md): implementation details
  for contributors
