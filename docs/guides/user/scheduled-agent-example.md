# Scheduled Agent Example

This guide shows how to run a fullsend agent on a recurring schedule (e.g., nightly) using a GitHub Actions cron trigger.

## Use case

Scheduled agents are useful when you want to:
- Run a custom agent every night (code scanning, dependency audits, repo hygiene, etc.)
- Trigger an agent on a fixed cadence without a GitHub event (issue, label, or comment)
- Manually dispatch an agent on demand via `workflow_dispatch`

For agents triggered by external systems like Jira, see [Custom Poller Example](custom-poller-example.md).

## Prerequisites

Before creating a scheduled workflow, you need:

1. **A registered agent** in your `.fullsend/` directory. See [Bring Your Own Agent](bring-your-own-agent.md) for the full walkthrough.
2. **Repository variables and secrets** configured for fullsend (see [Variables and secrets](#variables-and-secrets) below).
3. **GCP Workload Identity Federation** provisioned for your repo — run [`fullsend inference provision`](../../cli/inference.md) first.
4. **A tracking issue** in your repository. The harness requires an `issue.html_url` in the event payload — create a dedicated issue (e.g., "Nightly agent tracking") and note its number.

## Example workflow

Create `.github/workflows/fullsend-nightly.yaml` in your repository:

```yaml
name: Fullsend Nightly

on:
  schedule:
    # Nightly at 2:00 AM UTC — adjust to your timezone
    - cron: "0 2 * * *"
  workflow_dispatch:

permissions:
  actions: write
  contents: write
  id-token: write
  issues: write
  packages: read
  pull-requests: write

jobs:
  harness:
    name: Harness
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v0
    with:
      # Matrix literal — replace "my-nightly-agent" and "42" (see Prerequisites).
      matrix: '{"include":[{"agent":"my-nightly-agent","source_repo":"${{ github.repository }}","role":"harness","event_payload":"{\"issue\":{\"html_url\":\"https://github.com/${{ github.repository }}/issues/42\",\"number\":42}}","status_repo":"","status_number":""}]}'
      mint_url: ${{ vars.FULLSEND_MINT_URL }}
      gcp_region: ${{ vars.FULLSEND_GCP_REGION }}
    secrets:
      FULLSEND_GCP_WIF_PROVIDER: ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}
      FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}
      OTEL_EXPORTER_OTLP_TRACES_HEADERS: ${{ secrets.OTEL_EXPORTER_OTLP_TRACES_HEADERS }}
      OTEL_EXPORTER_OTLP_HEADERS: ${{ secrets.OTEL_EXPORTER_OTLP_HEADERS }}
```

Replace `my-nightly-agent` with the name of the agent you registered via `fullsend agent add --name <name>`, and `42` with the number of your tracking issue.

## How it works

1. The workflow passes a pre-computed matrix literal directly to `reusable-dispatch.yml`. When a matrix is provided, the reusable workflow skips routing and dispatch and goes directly to `harness-run`.
2. `harness-run` resolves and executes your agent inside a sandbox, just as it would for an event-triggered agent.

```
schedule / workflow_dispatch
        |
        v
reusable-dispatch.yml  (matrix literal -> harness-run)
        |
        v
harness-run  (sandbox execution of your agent)
```

## Running multiple agents

To run several agents on the same schedule, add entries to the matrix `include` array — one object per agent. Each agent runs as a separate matrix job in parallel. See [Custom Poller Example — Matrix Format](custom-poller-example.md#matrix-format) for the full schema.

## Variables and secrets

Your repository needs these variables and secrets configured (same as event-triggered agents):

**Variables:**
- `FULLSEND_MINT_URL` - Token mint service URL
- `FULLSEND_GCP_REGION` - GCP region for Vertex AI
- `OTEL_EXPORTER_OTLP_ENDPOINT` - [OpenTelemetry](../../glossary.md#otel-primary-facts) endpoint (optional)

**Secrets:**
- `FULLSEND_GCP_WIF_PROVIDER` - GCP Workload Identity Federation (WIF) provider
- `FULLSEND_GCP_PROJECT_ID` - GCP project ID
- `OTEL_EXPORTER_OTLP_TRACES_HEADERS` - [OTEL](../../glossary.md#otel-primary-facts) auth headers (optional)
- `OTEL_EXPORTER_OTLP_HEADERS` - OTEL headers (optional)

## Permissions

The top-level `permissions:` block must grant the maximum permissions required by `reusable-dispatch.yml`. Even though a scheduled run may not create PRs, GitHub validates that the caller grants sufficient permissions for all jobs in the reusable workflow. See [Custom Poller Example — Permissions](custom-poller-example.md#permissions) for details.

## Tips

- **Test first with `workflow_dispatch`:** use the manual trigger to verify your agent runs before relying on the cron schedule.
- **Cron syntax:** GitHub Actions uses UTC. `"0 2 * * *"` means 2:00 AM UTC daily. See [GitHub's cron docs](https://docs.github.com/en/actions/writing-workflows/choosing-when-your-workflow-runs/events-that-trigger-workflows#schedule) for syntax.
- **Concurrency:** if your nightly runs overlap, add a [`concurrency`](https://docs.github.com/en/actions/writing-workflows/choosing-what-your-workflow-does/control-the-concurrency-of-your-workflows) group to prevent duplicates.

## See also

- [Bring Your Own Agent](bring-your-own-agent.md) — create and register a custom agent
- [Custom Poller Example](custom-poller-example.md) — schedule agents that poll external systems (Jira, Linear, etc.)
- [Configuring agent behavior](customizing-agents.md) — harness YAML structure and `base:` composition
