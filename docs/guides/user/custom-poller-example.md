# Custom Poller Example

This guide shows how to create a custom poller in your own repository that invokes fullsend [harness](../../glossary.md#harness) agents directly, bypassing the standard GitHub event trigger flow.

## Use Case

Custom pollers are useful when you want to:
- Poll external systems (Jira, Linear, Slack, etc.) on a schedule
- Trigger [harness](../../glossary.md#harness) agents based on custom logic
- Reuse fullsend's [harness](../../glossary.md#harness) infrastructure without duplicating workflow code

## Example: Jira Polling Workflow

This example polls Jira for bugs and dispatches fullsend agents to process them:

```yaml
name: Fullsend Jira Poll

on:
  schedule:
    - cron: '*/30 * * * *'  # every 30 minutes
  workflow_dispatch:

permissions:
  actions: write
  contents: write
  id-token: write
  issues: write
  packages: read
  pull-requests: write

jobs:
  poll:
    runs-on: ubuntu-24.04
    outputs:
      matrix: ${{ steps.dispatch.outputs.matrix }}
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Install fullsend CLI
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          gh release download --repo fullsend-ai/fullsend \
            -p 'fullsend_*_linux_amd64.tar.gz' -O - | tar xz
          sudo mv fullsend /usr/local/bin/

      - name: Poll Jira and build dispatch matrix
        id: dispatch
        env:
          JIRA_TOKEN: ${{ secrets.JIRA_TOKEN }}
          JIRA_USER_EMAIL: ${{ secrets.JIRA_USER_EMAIL }}
          JIRA_BASE_URL: ${{ vars.JIRA_BASE_URL }}
          TARGET_REPO: ${{ github.repository }}
        run: |
          fullsend poll \
            --input-driver jira-poll \
            --jira-url "${JIRA_BASE_URL}" \
            --jira-project MYPROJECT \
            --jql 'project=MYPROJECT and statusCategory != Done and updated > -1week and type=Bug' \
            --target-repo "${TARGET_REPO}" \
            --output dispatches.json \
            --fullsend-dir .fullsend

          # Build GitHub Actions matrix format
          if ! jq -e 'length > 0' dispatches.json > /dev/null 2>&1; then
            echo 'matrix={"include":[]}' >> "${GITHUB_OUTPUT}"
            exit 0
          fi
          MATRIX=$(jq -c '{include: .}' dispatches.json)
          DELIM="MATRIX_$(openssl rand -hex 8)"
          {
            echo "matrix<<${DELIM}"
            printf '%s' "${MATRIX}"
            echo
            echo "${DELIM}"
          } >> "${GITHUB_OUTPUT}"

  harness:
    needs: poll
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v0
    with:
      matrix: ${{ needs.poll.outputs.matrix }}
      mint_url: ${{ vars.FULLSEND_MINT_URL }}
      gcp_region: ${{ vars.FULLSEND_GCP_REGION }}
      jira_base_url: ${{ vars.JIRA_BASE_URL }}
    secrets:
      FULLSEND_GCP_WIF_PROVIDER: ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}
      FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}
      OTEL_EXPORTER_OTLP_TRACES_HEADERS: ${{ secrets.OTEL_EXPORTER_OTLP_TRACES_HEADERS }}
      OTEL_EXPORTER_OTLP_HEADERS: ${{ secrets.OTEL_EXPORTER_OTLP_HEADERS }}
      JIRA_TOKEN: ${{ secrets.JIRA_TOKEN }}
      JIRA_USER_EMAIL: ${{ secrets.JIRA_USER_EMAIL }}
```

## Matrix Format

The `matrix` input (a GitHub Actions [matrix strategy](https://docs.github.com/en/actions/writing-workflows/choosing-what-your-workflow-does/running-variations-of-jobs-in-a-workflow#using-a-matrix-strategy) that runs parallel jobs) must follow the format produced by `fullsend dispatch --output-driver gha-matrix`:

```json
{
  "include": [
    {
      "agent": "agent-name",
      "source_repo": "org/repo",
      "role": "harness",
      "event_payload": "{...}",
      "status_repo": "org/repo",
      "status_number": "123"
    }
  ]
}
```

## Prerequisites

Your external repository needs these variables and secrets configured:

**Variables:**
- `FULLSEND_MINT_URL` - Token mint service URL
- `FULLSEND_GCP_REGION` - GCP region for Vertex AI
- `OTEL_EXPORTER_OTLP_ENDPOINT` - [OpenTelemetry](../../glossary.md#otel-primary-facts) endpoint (optional)
- `JIRA_BASE_URL` - Jira instance URL (if using Jira agents)

**Secrets:**
- `FULLSEND_GCP_WIF_PROVIDER` - GCP Workload Identity Federation (WIF) provider
- `FULLSEND_GCP_PROJECT_ID` - GCP project ID
- `OTEL_EXPORTER_OTLP_TRACES_HEADERS` - [OTEL](../../glossary.md#otel-primary-facts) auth headers (optional)
- `JIRA_TOKEN` - Jira API token (if using Jira agents)
- `JIRA_USER_EMAIL` - Jira user email (if using Jira agents)

## How It Works

1. **Poll job** runs your custom logic to query an external system and builds a dispatch matrix
2. **Harness job** calls `reusable-dispatch.yml` with the pre-computed matrix
3. `reusable-dispatch.yml` skips the routing and dispatch steps (see [architecture.md](../../architecture.md) for the standard dispatch flow), directly invoking `harness-run` with your matrix
4. [Harness](../../glossary.md#harness) agents execute according to your matrix configuration

## Permissions

The top-level `permissions:` block grants the maximum permissions required by `reusable-dispatch.yml`. Even though you're only running harness agents via the pre-computed matrix, GitHub validates that the caller grants sufficient permissions for all jobs in the reusable workflow (including code and fix jobs that won't actually run).

Required permissions:
- `contents: write` - needed by code/fix agents
- `packages: read` - needed by code/fix agents
- `actions: write`, `id-token: write`, `issues: write`, `pull-requests: write` - needed by all agents

## Authorization

When using a pre-computed matrix, the `.fullsend/config.yaml` agent-enablement checks are bypassed. The token mint service acts as the authorization boundary (see [mint-administration.md](../infrastructure/mint-administration.md)) - ensure your mint service is properly configured to control which agents external callers can invoke.
