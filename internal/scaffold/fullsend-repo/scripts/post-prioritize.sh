#!/usr/bin/env bash
# post-prioritize.sh — Write RICE scores to the project board and post a reasoning comment.
#
# Runs on the host after sandbox cleanup. Working directory is the fullsend
# run output directory (e.g., /tmp/fullsend/agent-prioritize-<id>/).
#
# Required env vars:
#   GITHUB_ISSUE_URL  — HTML URL of the issue
#   GH_TOKEN          — GitHub token with project write + issues write scope
#   ORG               — GitHub organization
#   PROJECT_NUMBER    — Project board number

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/github-api-csma.sh
source "${SCRIPT_DIR}/lib/github-api-csma.sh"

: "${GITHUB_ISSUE_URL:?GITHUB_ISSUE_URL must be set}"
: "${GH_TOKEN:?GH_TOKEN must be set}"
: "${ORG:?ORG must be set}"
: "${PROJECT_NUMBER:?PROJECT_NUMBER must be set}"

# Validate URL format early, before any parsing or API calls.
if [[ ! "${GITHUB_ISSUE_URL}" =~ ^https://github\.com/[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+/issues/[0-9]+$ ]]; then
  echo "ERROR: GITHUB_ISSUE_URL does not match expected pattern: ${GITHUB_ISSUE_URL}" >&2
  exit 1
fi

# Find the result JSON from the last iteration.
RESULT_FILE=""
for dir in iteration-*/output; do
  if [[ -f "${dir}/agent-result.json" ]]; then
    RESULT_FILE="${dir}/agent-result.json"
  fi
done

if [[ -z "${RESULT_FILE}" ]]; then
  echo "ERROR: agent-result.json not found in any iteration output directory" >&2
  exit 1
fi

echo "Reading RICE result from: ${RESULT_FILE}"

if ! jq empty "${RESULT_FILE}" 2>/dev/null; then
  echo "ERROR: ${RESULT_FILE} is not valid JSON" >&2
  exit 1
fi

# Extract scores.
REACH=$(jq -r '.reach' "${RESULT_FILE}")
IMPACT=$(jq -r '.impact' "${RESULT_FILE}")
CONFIDENCE=$(jq -r '.confidence' "${RESULT_FILE}")
EFFORT=$(jq -r '.effort' "${RESULT_FILE}")

# Compute final RICE score: (R * I * C) / E
SCORE=$(jq -n --argjson r "${REACH}" --argjson i "${IMPACT}" \
  --argjson c "${CONFIDENCE}" --argjson e "${EFFORT}" \
  '(($r * $i * $c / $e) * 100 | round) / 100')

echo "RICE scores: R=${REACH} I=${IMPACT} C=${CONFIDENCE} E=${EFFORT} → Score=${SCORE}"

# Extract reasoning — sanitize for markdown table embedding:
#   1. Strip HTML tags to prevent HTML/markdown injection from attacker-controlled issue content.
#   2. Escape pipe characters to avoid breaking the markdown table layout.
REASONING_REACH=$(jq -r '.reasoning.reach' "${RESULT_FILE}" | sed 's/<[^>]*>//g; s/|/\\|/g')
REASONING_IMPACT=$(jq -r '.reasoning.impact' "${RESULT_FILE}" | sed 's/<[^>]*>//g; s/|/\\|/g')
REASONING_CONFIDENCE=$(jq -r '.reasoning.confidence' "${RESULT_FILE}" | sed 's/<[^>]*>//g; s/|/\\|/g')
REASONING_EFFORT=$(jq -r '.reasoning.effort' "${RESULT_FILE}" | sed 's/<[^>]*>//g; s/|/\\|/g')

# --- Write scores to the project board ---

# Resolve project and item IDs.
PROJECT_ID=$(github_csma_run graphql project view "${PROJECT_NUMBER}" --owner "${ORG}" --format json | jq -r '.id')

# Parse repo and issue number from URL.
REPO=$(echo "${GITHUB_ISSUE_URL}" | sed 's|https://github.com/||; s|/issues/.*||')
ISSUE_NUMBER=$(basename "${GITHUB_ISSUE_URL}")
ISSUE_NODE_ID=$(github_csma_run core api "repos/${REPO}/issues/${ISSUE_NUMBER}" --jq '.node_id')

# Find the project item ID for this issue via the issue's projectItems connection.
# This is a single API call regardless of project size, avoiding pagination and timeouts.
ITEM_RESPONSE=$(github_csma_run graphql api graphql -f query='
  query($issueId: ID!) {
    node(id: $issueId) {
      ... on Issue {
        projectItems(first: 10) {
          nodes {
            id
            project { id }
          }
        }
      }
    }
  }
' -f issueId="${ISSUE_NODE_ID}")

ITEM_ID=$(echo "${ITEM_RESPONSE}" | jq -r --arg pid "${PROJECT_ID}" \
  '(.data.node.projectItems.nodes // [])[] | select(.project.id == $pid) | .id')

if [[ -z "${ITEM_ID}" || "${ITEM_ID}" == "null" ]]; then
  echo "ERROR: issue ${GITHUB_ISSUE_URL} not found on project board (project: ${PROJECT_NUMBER}, org: ${ORG})" >&2
  exit 1
fi

# Get field IDs for all RICE fields.
FIELDS_JSON=$(github_csma_run graphql project field-list "${PROJECT_NUMBER}" --owner "${ORG}" --format json)

get_field_id() {
  echo "${FIELDS_JSON}" | jq -r --arg name "$1" '.fields[] | select(.name == $name) | .id'
}

REACH_FIELD_ID=$(get_field_id "RICE Reach")
IMPACT_FIELD_ID=$(get_field_id "RICE Impact")
CONFIDENCE_FIELD_ID=$(get_field_id "RICE Confidence")
EFFORT_FIELD_ID=$(get_field_id "RICE Effort")
SCORE_FIELD_ID=$(get_field_id "RICE Score")

for fid_var in REACH_FIELD_ID IMPACT_FIELD_ID CONFIDENCE_FIELD_ID EFFORT_FIELD_ID SCORE_FIELD_ID; do
  if [[ -z "${!fid_var}" ]]; then
    echo "ERROR: ${fid_var} not found on project board (project: ${PROJECT_NUMBER}, org: ${ORG}). Run scripts/setup-prioritize.sh first." >&2
    exit 1
  fi
done

# Update each field on the project item.
# Uses --input - with jq-built JSON variables to ensure proper Float coercion.
# The gh CLI's -F flag does not reliably coerce strings to GraphQL Float.
# The entire JSON body is built with jq to avoid unquoted heredoc expansion.
update_field() {
  local field_id="$1"
  local value="$2"
  jq -n \
    --arg pid "${PROJECT_ID}" \
    --arg iid "${ITEM_ID}" \
    --arg fid "${field_id}" \
    --argjson val "${value}" \
    '{
      query: "mutation($projectId: ID!, $itemId: ID!, $fieldId: ID!, $value: Float!) { updateProjectV2ItemFieldValue(input: { projectId: $projectId, itemId: $itemId, fieldId: $fieldId, value: { number: $value } }) { projectV2Item { id } } }",
      variables: {projectId: $pid, itemId: $iid, fieldId: $fid, value: $val}
    }' | github_csma_run_pipe graphql api graphql --input -
}

echo "Writing scores to project board (CSMA-aware)..."
update_field "${REACH_FIELD_ID}" "${REACH}"
update_field "${IMPACT_FIELD_ID}" "${IMPACT}"
update_field "${CONFIDENCE_FIELD_ID}" "${CONFIDENCE}"
update_field "${EFFORT_FIELD_ID}" "${EFFORT}"
update_field "${SCORE_FIELD_ID}" "${SCORE}"
echo "Project fields updated."

# Board reranking by RICE Score is deferred — the Projects V2 board supports
# sorting by custom fields natively, avoiding N sequential API mutations and
# secondary rate limit risk. See future work in the PR description.

# --- Post reasoning comment ---

# Build comment body with jq to avoid shell expansion of reasoning strings.
# Reasoning text originates from agent output processing untrusted issue content;
# using jq --arg ensures no shell interpretation of backticks or $(...) sequences.
COMMENT=$(jq -n \
  --arg score "${SCORE}" \
  --arg reach "${REACH}" \
  --arg impact "${IMPACT}" \
  --arg confidence "${CONFIDENCE}" \
  --arg effort "${EFFORT}" \
  --arg r_reach "${REASONING_REACH}" \
  --arg r_impact "${REASONING_IMPACT}" \
  --arg r_confidence "${REASONING_CONFIDENCE}" \
  --arg r_effort "${REASONING_EFFORT}" \
  -r '"<!-- fullsend:prioritize-agent -->
**RICE Priority Score: \($score)**

<details>
<summary>Score breakdown</summary>

| Dimension | Score | Reasoning |
|-----------|-------|-----------|
| **Reach** | \($reach) | \($r_reach) |
| **Impact** | \($impact) | \($r_impact) |
| **Confidence** | \($confidence) | \($r_confidence) |
| **Effort** | \($effort) | \($r_effort) |

**Formula:** (\($reach) x \($impact) x \($confidence)) / \($effort) = **\($score)**

</details>"')

echo "Posting RICE comment..."
printf '%s' "${COMMENT}" | github_csma_run_cmd core fullsend post-comment \
  --repo "${REPO}" \
  --number "${ISSUE_NUMBER}" \
  --marker "<!-- fullsend:prioritize-agent -->" \
  --token "${GH_TOKEN}" \
  --result - >/dev/null
echo "Post-prioritize complete."
