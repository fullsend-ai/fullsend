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
2. **Repository variables and secrets** configured for fullsend (see [Prerequisites](#variables-and-secrets) below).
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
    inputs:
      dry_run:
        description: "Preview mode — no writes"
        required: true
        default: "true"
        type: choice
        options:
          - "true"
          - "false"

permissions:
  actions: write
  contents: write
  id-token: write
  issues: write
  packages: read
  pull-requests: write

jobs:
  build-matrix:
    runs-on: ubuntu-24.04
    outputs:
      matrix: ${{ steps.matrix.outputs.matrix }}
    steps:
      - uses: actions/checkout@v4

      - name: Build dispatch matrix
        id: matrix
        env:
          REPO: ${{ github.repository }}
          # Create a tracking issue and set its number here (see Prerequisites).
          ISSUE_NUMBER: "42"
          DRY_RUN: ${{ inputs.dry_run || 'true' }}
        run: |
          # Replace "my-nightly-agent" with your agent's name
          # (the name field from your harness YAML, or the filename
          # used with `fullsend agent add --name <name>`).
          ISSUE_URL="https://github.com/${REPO}/issues/${ISSUE_NUMBER}"
          MATRIX=$(jq -n -c \
            --arg agent "my-nightly-agent" \
            --arg repo "$REPO" \
            --arg url "$ISSUE_URL" \
            --arg num "$ISSUE_NUMBER" \
            --arg dry "$DRY_RUN" \
            '{include: [{
                agent: $agent,
                source_repo: $repo,
                role: "harness",
                event_payload: ({issue: {html_url: $url, number: ($num | tonumber)}, dry_run: ($dry == "true")} | tojson),
                status_repo: "",
                status_number: ""
              }]}')
          DELIM="MATRIX_$(openssl rand -hex 8)"
          {
            echo "matrix<<${DELIM}"
            printf '%s' "${MATRIX}"
            echo
            echo "${DELIM}"
          } >> "${GITHUB_OUTPUT}"

  harness:
    name: Harness
    needs: build-matrix
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v0
    with:
      matrix: ${{ needs.build-matrix.outputs.matrix }}
      mint_url: ${{ vars.FULLSEND_MINT_URL }}
      gcp_region: ${{ vars.FULLSEND_GCP_REGION }}
    secrets:
      FULLSEND_GCP_WIF_PROVIDER: ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}
      FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}
      OTEL_EXPORTER_OTLP_TRACES_HEADERS: ${{ secrets.OTEL_EXPORTER_OTLP_TRACES_HEADERS }}
      OTEL_EXPORTER_OTLP_HEADERS: ${{ secrets.OTEL_EXPORTER_OTLP_HEADERS }}
```

Replace `my-nightly-agent` with the name of the agent you registered via `fullsend agent add --name <name>`, and `"42"` with the number of your tracking issue.

## How it works

1. **`build-matrix` job** constructs a pre-computed [harness](../../glossary.md#harness) matrix with your agent's name. The matrix format matches the output of `fullsend dispatch --output-driver gha-matrix`.
2. **`harness` job** calls `reusable-dispatch.yml` with the pre-computed matrix. When a matrix is provided, the reusable workflow skips routing and dispatch steps and goes directly to `harness-run`.
3. **`harness-run`** resolves and executes your agent inside a sandbox, just as it would for an event-triggered agent.

```
schedule / workflow_dispatch
        |
        v
build-matrix  (construct matrix JSON for your agent)
        |
        v
reusable-dispatch.yml  (pre-computed matrix -> harness-run)
        |
        v
harness-run  (sandbox execution of your agent)
```

## Posting status to an issue

By default, the example above sets `status_repo` and `status_number` to empty strings, so the agent runs without posting status comments. To have the agent report its status on a tracking issue, set those fields:

```yaml
      - name: Build dispatch matrix
        id: matrix
        env:
          REPO: ${{ github.repository }}
          ISSUE_NUMBER: "42"
          DRY_RUN: ${{ inputs.dry_run || 'true' }}
        run: |
          ISSUE_URL="https://github.com/${REPO}/issues/${ISSUE_NUMBER}"
          MATRIX=$(jq -n -c \
            --arg agent "my-nightly-agent" \
            --arg repo "$REPO" \
            --arg url "$ISSUE_URL" \
            --arg num "$ISSUE_NUMBER" \
            --arg dry "$DRY_RUN" \
            '{include: [{
                agent: $agent,
                source_repo: $repo,
                role: "harness",
                event_payload: ({issue: {html_url: $url, number: ($num | tonumber)}, dry_run: ($dry == "true")} | tojson),
                status_repo: $repo,
                status_number: $num
              }]}')
          # ... (same DELIM / GITHUB_OUTPUT logic as above)
```

The `status_repo` and `status_number` fields tell the harness where to post status comments. Here they reference the same tracking issue used in the event payload.

## Running multiple agents

To run several agents on the same schedule, add entries to the `include` array:

```yaml
      - name: Build dispatch matrix
        id: matrix
        env:
          REPO: ${{ github.repository }}
          ISSUE_NUMBER: "42"
        run: |
          ISSUE_URL="https://github.com/${REPO}/issues/${ISSUE_NUMBER}"
          PAYLOAD=$(jq -n -c \
            --arg url "$ISSUE_URL" --arg num "$ISSUE_NUMBER" \
            '{issue: {html_url: $url, number: ($num | tonumber)}}')
          MATRIX=$(jq -n -c \
            --arg repo "$REPO" \
            --arg payload "$PAYLOAD" \
            '{include: [
                {agent: "nightly-scan", source_repo: $repo, role: "harness", event_payload: $payload, status_repo: "", status_number: ""},
                {agent: "dep-audit",    source_repo: $repo, role: "harness", event_payload: $payload, status_repo: "", status_number: ""}
              ]}')
          DELIM="MATRIX_$(openssl rand -hex 8)"
          {
            echo "matrix<<${DELIM}"
            printf '%s' "${MATRIX}"
            echo
            echo "${DELIM}"
          } >> "${GITHUB_OUTPUT}"
```

Each agent runs as a separate matrix job in parallel.

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

- **Test first with `workflow_dispatch`:** use the manual trigger to verify your agent runs before relying on the cron schedule. The `dry_run` input is wired into `event_payload` (as `dry_run: true/false`) via the `DRY_RUN` env variable in the `build-matrix` step. Your agent reads it from the event payload file (`.fullsend/dispatch/event-payload.json`) inside the sandbox. To add other `workflow_dispatch` inputs, include them in the `jq` command that constructs the payload.
- **Cron syntax:** GitHub Actions uses UTC. `"0 2 * * *"` means 2:00 AM UTC daily. See [GitHub's cron docs](https://docs.github.com/en/actions/writing-workflows/choosing-when-your-workflow-runs/events-that-trigger-workflows#schedule) for syntax.
- **Concurrency:** if your nightly runs overlap (e.g., the agent takes longer than 24 hours), add a `concurrency` group to the `build-matrix` job to prevent duplicates.

## See also

- [Bring Your Own Agent](bring-your-own-agent.md) — create and register a custom agent
- [Custom Poller Example](custom-poller-example.md) — schedule agents that poll external systems (Jira, Linear, etc.)
- [Configuring agent behavior](customizing-agents.md) — harness YAML structure and `base:` composition
