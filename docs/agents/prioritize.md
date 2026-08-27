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
create manually in your `{org}/.fullsend` repo.

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
- The following variables and secrets set on the repo that hosts the scheduler:

| Variable / Secret | Type | Required | Description |
|-------------------|------|----------|-------------|
| `FULLSEND_PRIORITIZE_CLIENT_ID` | Variable | Yes | Client ID of the GitHub App with project board access (`organization_projects: write`) |
| `FULLSEND_PRIORITIZE_APP_PRIVATE_KEY` | Secret | Yes | Private key for the prioritize GitHub App |
| `FULLSEND_FULLSEND_CLIENT_ID` | Variable | Yes | Client ID of the GitHub App with cross-repo dispatch access (`actions: write`) |
| `FULLSEND_FULLSEND_APP_PRIVATE_KEY` | Secret | Yes | Private key for the fullsend GitHub App |

#### Example workflow

Create this as `.github/workflows/prioritize-scheduler.yml` in your
`{org}/.fullsend` repo. Replace `<your-project-number>`, `<your-repos>`,
and `<your-runner>` with your org-specific values.

The scheduler uses two GitHub App tokens, kept separate for
least-privilege: the **prioritize** app has only project board access
(`organization_projects: write`) and the **fullsend** app has cross-repo
workflow dispatch (`actions: write`). Combining them into a single app
would give every per-repo prioritize run more permissions than it needs.
See the
[Role Permissions Matrix](../guides/infrastructure/infrastructure-reference.md#role-permissions-matrix)
for the full permission breakdown.

All operational configuration (project number, target repos, score field,
thresholds) is self-contained in the workflow file via `||` fallback
expressions — the only repository variables and secrets needed are the
GitHub App credentials listed in Prerequisites above. Input descriptions
document the defaults; the `||` fallbacks in the `env` block and the
`repositories` field are the single source of truth (input `default`
values only apply to `workflow_dispatch`, not `schedule` triggers).

```yaml
---
name: Prioritize Scheduler

on:
  schedule:
    - cron: '*/10 * * * *'
  workflow_dispatch:
    inputs:
      project_number:
        description: "GitHub Project number to poll (default: <your-project-number>)"
        required: false
        type: string
      repos:
        description: "Comma-separated repo names to dispatch to (default: <your-repos>)"
        required: false
        type: string
      score_field:
        description: "Project board field name for the RICE score (default: RICE Score)"
        required: false
        type: string
      workflow:
        description: "Workflow filename to dispatch on target repos (default: prioritize.yml)"
        required: false
        type: string
      stale_threshold:
        description: "Re-score issues whose RICE score is older than this (default: 7d)"
        required: false
        type: string
      wip_limit:
        description: "Max number of prioritize jobs to dispatch (default: 5)"
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

    steps:
      - name: Checkout repository
        uses: actions/checkout@v4  # pin to SHA per your org policy

      - name: Generate prioritize token (project board access)
        id: prioritize-token
        uses: actions/create-github-app-token@v3
        with:
          client-id: ${{ vars.FULLSEND_PRIORITIZE_CLIENT_ID }}
          private-key: ${{ secrets.FULLSEND_PRIORITIZE_APP_PRIVATE_KEY }}
          owner: ${{ github.repository_owner }}

      - name: Generate fullsend token (cross-repo dispatch)
        id: dispatch-token
        uses: actions/create-github-app-token@v3
        with:
          client-id: ${{ vars.FULLSEND_FULLSEND_CLIENT_ID }}
          private-key: ${{ secrets.FULLSEND_FULLSEND_APP_PRIVATE_KEY }}
          owner: ${{ github.repository_owner }}
          repositories: ${{ inputs.repos || '<your-repos>' }}

      - name: Find issues and dispatch prioritize runs
        env:
          GH_TOKEN: ${{ steps.prioritize-token.outputs.token }}
          DISPATCH_TOKEN: ${{ steps.dispatch-token.outputs.token }}
          ORG: ${{ github.repository_owner }}
          PROJECT_NUMBER: ${{ inputs.project_number || '<your-project-number>' }}
          SCORE_FIELD: ${{ inputs.score_field || 'RICE Score' }}
          STALE_THRESHOLD: ${{ inputs.stale_threshold || '7d' }}
          WORKFLOW: ${{ inputs.workflow || 'prioritize.yml' }}
          WIP_LIMIT: ${{ inputs.wip_limit || '5' }}
        run: |
          set -euo pipefail

          # Parse stale threshold into seconds
          parse_threshold() {
            local val="${1%[dh]}"
            local unit="${1: -1}"
            if [[ -z "${val}" || ! "${val}" =~ ^[1-9][0-9]*$ ]]; then
              echo "ERROR: invalid threshold '${1}' (use Nd or Nh, e.g. 7d)" >&2; exit 1
            fi
            case "${unit}" in
              d) echo $(( val * 86400 )) ;;
              h) echo $(( val * 3600 )) ;;
              *) echo "ERROR: unsupported threshold unit '${unit}' (use Nd or Nh)" >&2; exit 1 ;;
            esac
          }
          THRESHOLD_SECONDS=$(parse_threshold "${STALE_THRESHOLD}")

          # Fetch project metadata
          PROJECT_ID=$(gh project view "${PROJECT_NUMBER}" \
            --owner "${ORG}" --format json | jq -r '.id')

          if [[ -z "${PROJECT_ID}" || "${PROJECT_ID}" == "null" ]]; then
            echo "ERROR: Failed to fetch project ${PROJECT_NUMBER}."
            exit 1
          fi

          SCORE_FIELD_ID=$(gh project field-list "${PROJECT_NUMBER}" \
            --owner "${ORG}" --format json \
            | jq -r --arg sf "${SCORE_FIELD}" '.fields[] | select(.name == $sf) | .id')

          if [[ -z "${SCORE_FIELD_ID}" ]]; then
            echo "ERROR: '${SCORE_FIELD}' field not found on project."
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
                            updatedAt
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
          if [[ "${COUNT}" -gt 0 ]]; then
            echo "Found ${COUNT} unscored issue(s) to dispatch."
          else
            echo "All issues scored. Checking for stale scores..."

            NOW_EPOCH=$(date +%s)

            UNSCORED=$(echo "${ALL_ITEMS}" | jq -r \
              --arg fid "${SCORE_FIELD_ID}" \
              --argjson limit "${WIP_LIMIT}" \
              --argjson threshold "${THRESHOLD_SECONDS}" \
              --argjson now "${NOW_EPOCH}" '
              [.[]
               | select(.content.state == "OPEN")
               | select(.content.url != null)
               | {
                   url: .content.url,
                   number: .content.number,
                   updatedAt: ([.fieldValues.nodes[]
                                | select(.field.id == $fid)
                                | .updatedAt] | first)
                 }
               | select(.updatedAt != null)
               | select(($now - (.updatedAt | fromdateiso8601)) > $threshold)
              ]
              | sort_by(.updatedAt)
              | .[:$limit]')

            STALE_COUNT=$(echo "${UNSCORED}" | jq 'length')

            if [[ "${STALE_COUNT}" -eq 0 ]]; then
              echo "No stale scores found. Nothing to do."
              exit 0
            fi

            echo "Found ${STALE_COUNT} stale issue(s) to re-score."
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

            echo "Dispatching prioritize for ${SOURCE_REPO}#${ISSUE_NUMBER}..."

            if GH_TOKEN="${DISPATCH_TOKEN}" gh workflow run "${WORKFLOW}" \
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
owns the issue), not to the scheduler's own repo. The `repos` input (passed
to `create-github-app-token` via the `repositories` field) must list every
target repo so the generated token has sufficient scope for cross-repo
dispatch.

When all issues are already scored, the scheduler checks for **stale scores**
— issues whose RICE Score field was last updated longer ago than
`stale_threshold` (default: 7 days). Stale issues are re-dispatched for
re-scoring, sorted oldest-first, up to `wip_limit`.

## Source

[`fullsend-ai/agents` — `harness/prioritize.yaml`](https://github.com/fullsend-ai/agents/blob/main/harness/prioritize.yaml)
