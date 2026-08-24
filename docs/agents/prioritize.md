# Prioritize Agent

![Prioritize agent icon](icons/prioritize.png)

Scores a GitHub issue using the RICE framework (Reach, Impact, Confidence, Effort) and produces structured scores with reasoning for project board ranking.

## How the agent works

Triggered on a schedule (the prioritize scheduler polls the project board for unscored issues) or on-demand via `/fs-prioritize`.

The prioritize agent fetches the issue and all its context, then evaluates it across the four RICE dimensions. It can invoke customer-research skills to gather additional signal about reach and impact. The output is a structured JSON result with per-dimension scores and written reasoning, which the post-script uses to update the project board.

## How it helps

- Issues are ranked consistently using the same framework, reducing bias from whoever happens to see them first.
- Scoring reasoning is transparent and auditable — anyone can read why an issue was ranked the way it was.
- Project boards stay sorted by value, so humans can focus on the highest-impact work first.

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-prioritize` | Issue comment | Runs RICE scoring on the issue |

Requires write-level repository permission (admin, maintain, or write).

The `/fs-prioritize` command does not accept arguments. It scores the issue
using the current content, comments, and any available `customer-research`
skill data.

## Control labels

The prioritize agent does not apply or consume control labels. It reads the
issue content and produces a structured score — the post-script updates the
project board directly.

## Configuration and extension

### Skill: `customer-research`

The prioritize agent looks for a `customer-research` skill and, when available,
uses it to inform Reach and Impact scores. To provide it, create a skill directory
in your target repository at `.agents/skills/customer-research/` with a `SKILL.md` and
any helper scripts organized in a `scripts/` subdirectory. Then symlink `.claude/skills`
to `.agents/skills` so the skill is discoverable by both Fullsend and any local
agent tooling:

```
your-repo/
  .agents/skills/customer-research/
    SKILL.md
    scripts/
  .claude/skills -> ../.agents/skills
```

This gives the prioritize agent concrete data to distinguish between "one user
wants this" (Reach 0.25) and "three strategic accounts have filed support cases
about it" (Reach 2.0), instead of guessing from the issue text alone.

### Scheduled scoring

The prioritize agent can be triggered automatically by a scheduler workflow
that polls a GitHub Project board for unscored issues. This scheduler
is **not managed by fullsend** — it is bespoke org-level automation that you
create manually in your `{org}/.fullsend` repo (required for cross-repo
mint token access).

#### Prerequisites

- A GitHub Projects (v2) board with a numeric **RICE Score** field.
  Run `scripts/setup-prioritize.sh` to create the field if it does not
  exist. The script is installed to the `.fullsend` repo by org-mode
  scaffold; per-repo orgs can find it in the fullsend source tree at
  `internal/scaffold/fullsend-repo/scripts/setup-prioritize.sh`.
- `prioritize.yml` installed in every target repo (handled by `repos install`).
- The scheduler passes `project_number` to each target repo's
  `prioritize.yml` thin caller via `workflow_dispatch` input. If the input
  is not provided, the thin caller falls back to
  `vars.FULLSEND_PROJECT_NUMBER` (repo or org-level variable).
- The following variables set on the repo that hosts the scheduler:

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `FULLSEND_PROJECT_NUMBER` | Yes | — | GitHub Project number to poll for issues |
| `FULLSEND_MINT_URL` | Yes | — | Mint service URL for token minting |
| `FULLSEND_PRIORITIZE_REPOS` | **Yes** | — | Comma-separated bare repo names (no org prefix, no spaces) the mint token can access. Must include every repo whose issues appear on the project board. The minted token has write access to all listed repos — keep this list as narrow as possible. Example: `my-repo,other-repo` |
| `PRIORITIZE_WIP_LIMIT` | No | `5` | Max concurrent dispatch jobs per run |

#### Example workflow

Create this as `.github/workflows/prioritize-scheduler.yml` in your
`{org}/.fullsend` repo. Replace `<your-ref>` with the fullsend version
ref and `<your-runner>` with your runner label.

The scheduler needs two mint roles: `prioritize` for reading the project
board (`organization_projects`) and `fullsend` for cross-repo workflow
dispatch (`actions: write`). See the
[role permissions table](../guides/infrastructure/infrastructure-reference.md#role-permissions-matrix)
for details.

```yaml
---
name: Prioritize Scheduler

on:
  # Uncomment the schedule trigger once your project board and
  # FULLSEND_PRIORITIZE_REPOS are configured.
  # schedule:
  #   - cron: '*/10 * * * *'
  workflow_dispatch:
    inputs:
      wip_limit:
        description: "Max number of prioritize jobs to dispatch"
        required: false
        type: number

concurrency:
  group: fullsend-prioritize-scheduler
  cancel-in-progress: false

jobs:
  dispatch:
    name: Find and dispatch issues for RICE scoring
    runs-on: <your-runner>
    timeout-minutes: 5
    permissions:
      actions: write
      contents: read
      id-token: write

    steps:
      - name: Checkout repository
        uses: actions/checkout@v4  # pin to SHA per your org policy

      - name: Mint prioritize token (project board access)
        id: prioritize-token
        uses: fullsend-ai/fullsend/.github/actions/mint-token@<your-ref>
        with:
          role: prioritize
          repos: ${{ vars.FULLSEND_PRIORITIZE_REPOS }}
          mint_url: ${{ vars.FULLSEND_MINT_URL }}

      - name: Mint fullsend token (cross-repo dispatch)
        id: dispatch-token
        uses: fullsend-ai/fullsend/.github/actions/mint-token@<your-ref>
        with:
          role: fullsend
          repos: ${{ vars.FULLSEND_PRIORITIZE_REPOS }}
          mint_url: ${{ vars.FULLSEND_MINT_URL }}

      - name: Find issues and dispatch prioritize runs
        env:
          GH_TOKEN: ${{ steps.prioritize-token.outputs.token }}
          DISPATCH_TOKEN: ${{ steps.dispatch-token.outputs.token }}
          ORG: ${{ github.repository_owner }}
          PROJECT_NUMBER: ${{ vars.FULLSEND_PROJECT_NUMBER }}
          WIP_LIMIT: ${{ inputs.wip_limit || vars.PRIORITIZE_WIP_LIMIT || '5' }}
        run: |
          set -euo pipefail

          if [[ -z "${PROJECT_NUMBER}" ]]; then
            echo "::notice::FULLSEND_PROJECT_NUMBER is not set; skipping."
            exit 0
          fi

          # Fetch project metadata
          PROJECT_ID=$(gh project view "${PROJECT_NUMBER}" \
            --owner "${ORG}" --format json | jq -r '.id')

          if [[ -z "${PROJECT_ID}" || "${PROJECT_ID}" == "null" ]]; then
            echo "ERROR: Failed to fetch project ${PROJECT_NUMBER}."
            exit 1
          fi

          SCORE_FIELD_ID=$(gh project field-list "${PROJECT_NUMBER}" \
            --owner "${ORG}" --format json \
            | jq -r '.fields[] | select(.name == "RICE Score") | .id')

          if [[ -z "${SCORE_FIELD_ID}" ]]; then
            echo "ERROR: 'RICE Score' field not found on project."
            echo "Run scripts/setup-prioritize.sh to create it."
            exit 1
          fi

          # Paginate through all project items (100 per page).
          ALL_ITEMS="[]"
          CURSOR=""
          while true; do
            CURSOR_ARG=""
            if [[ -n "${CURSOR}" ]]; then
              CURSOR_ARG="-f cursor=${CURSOR}"
            fi
            PAGE=$(gh api graphql -f query='
              query($projectId: ID!, $cursor: String) {
                node(id: $projectId) {
                  ... on ProjectV2 {
                    items(first: 100, after: $cursor) {
                      pageInfo { hasNextPage endCursor }
                      nodes {
                        fieldValues(first: 20) { nodes {
                          ... on ProjectV2ItemFieldNumberValue {
                            field { ... on ProjectV2Field { id } }
                          }
                        } }
                        content { ... on Issue { url number state } }
                      }
                    }
                  }
                }
              }' -f projectId="${PROJECT_ID}" ${CURSOR_ARG})

            ALL_ITEMS=$(echo "${ALL_ITEMS}" | jq \
              --argjson page "$(echo "${PAGE}" \
                | jq '[.data.node.items.nodes[]]')" \
              '. + $page')

            HAS_NEXT=$(echo "${PAGE}" \
              | jq -r '.data.node.items.pageInfo.hasNextPage')
            if [[ "${HAS_NEXT}" != "true" ]]; then
              break
            fi
            CURSOR=$(echo "${PAGE}" \
              | jq -r '.data.node.items.pageInfo.endCursor')
          done

          # Find unscored open issues (up to WIP_LIMIT)
          UNSCORED=$(echo "${ALL_ITEMS}" | jq -r \
            --arg fid "${SCORE_FIELD_ID}" --argjson limit "${WIP_LIMIT}" '
            [.[]
             | select(.content.state == "OPEN")
             | select(.content.url != null)
             | select([.fieldValues.nodes[]
                       | select(.field.id == $fid)] | length == 0)
             | {url: .content.url, number: .content.number}
            ] | .[:$limit]')

          COUNT=$(echo "${UNSCORED}" | jq 'length')
          if [[ "${COUNT}" -eq 0 ]]; then
            echo "All issues scored. Nothing to do."
            exit 0
          fi

          DISPATCHED=0
          FAILED=0

          for row in $(echo "${UNSCORED}" | jq -c '.[]'); do
            ISSUE_URL=$(echo "${row}" | jq -r '.url')
            ISSUE_NUMBER=$(echo "${row}" | jq -r '.number')
            SOURCE_REPO="${ISSUE_URL#https://github.com/}"
            SOURCE_REPO="${SOURCE_REPO%%/issues/*}"

            # Skip issues from outside this org
            if [[ "${SOURCE_REPO}" != "${ORG}/"* ]]; then
              echo "::warning::Skipping issue from unexpected org: ${SOURCE_REPO}"
              continue
            fi

            EVENT_PAYLOAD=$(jq -n \
              --arg url "${ISSUE_URL}" \
              --argjson number "${ISSUE_NUMBER}" \
              '{issue: {html_url: $url, number: $number}}')

            if GH_TOKEN="${DISPATCH_TOKEN}" gh workflow run prioritize.yml \
              --repo "${SOURCE_REPO}" \
              -f event_type="schedule" \
              -f source_repo="${SOURCE_REPO}" \
              -f event_payload="${EVENT_PAYLOAD}" \
              -f project_number="${PROJECT_NUMBER}"; then
              DISPATCHED=$((DISPATCHED + 1))
            else
              echo "::warning::Failed to dispatch for ${ISSUE_URL}"
              FAILED=$((FAILED + 1))
            fi
          done

          echo "Dispatched ${DISPATCHED} prioritize run(s), ${FAILED} failed."
```

The scheduler dispatches `prioritize.yml` to `${SOURCE_REPO}` (the repo that
owns the issue), not to the scheduler's own repo. `FULLSEND_PRIORITIZE_REPOS`
must list every target repo so the minted token has sufficient scope for
cross-repo dispatch.

## Source

[`fullsend-ai/agents` — `harness/prioritize.yaml`](https://github.com/fullsend-ai/agents/blob/main/harness/prioritize.yaml)
