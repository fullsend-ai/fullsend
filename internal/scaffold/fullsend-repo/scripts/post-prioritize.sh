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

# --- Inlined from lib/github-api-csma.sh ---
# CSMA/CD-style resilience for GitHub API calls via gh/fullsend.
# Carrier sense: check rate_limit before transmitting.
# Slot time: random jitter between calls to desynchronize parallel runners.
# Collision detection: retry on 429 / secondary rate limit errors with exponential backoff.
#
# Environment (all optional):
#   GITHUB_CSMA_MAX_ATTEMPTS          — default 8
#   GITHUB_CSMA_MIN_REMAINING_CORE    — default 100
#   GITHUB_CSMA_MIN_REMAINING_GRAPHQL — default 100
#   GITHUB_CSMA_SLOT_MIN_MS           — default 250
#   GITHUB_CSMA_SLOT_MAX_MS           — default 750 (0 disables jitter)
#   GITHUB_CSMA_SPREAD_MAX_SEC        — default 60 (post-reset desync spread)
#   GITHUB_CSMA_BACKOFF_CAP_SEC       — default 120

# shellcheck shell=bash

_github_csma_max_attempts() {
  echo "${GITHUB_CSMA_MAX_ATTEMPTS:-8}"
}

_github_csma_min_remaining() {
  local resource="$1"
  case "${resource}" in
    graphql) echo "${GITHUB_CSMA_MIN_REMAINING_GRAPHQL:-100}" ;;
    *) echo "${GITHUB_CSMA_MIN_REMAINING_CORE:-100}" ;;
  esac
}

_github_csma_slot_min_ms() {
  echo "${GITHUB_CSMA_SLOT_MIN_MS:-250}"
}

_github_csma_slot_max_ms() {
  echo "${GITHUB_CSMA_SLOT_MAX_MS:-750}"
}

_github_csma_spread_max_sec() {
  echo "${GITHUB_CSMA_SPREAD_MAX_SEC:-60}"
}

_github_csma_backoff_cap_sec() {
  echo "${GITHUB_CSMA_BACKOFF_CAP_SEC:-120}"
}

# Add a random spread delay after a rate-limit sleep to desynchronize runners.
# Called from both github_csma_sense and _github_csma_sleep_after_rate_limit.
_github_csma_post_reset_spread() {
  local spread_max
  spread_max=$(_github_csma_spread_max_sec)
  if (( spread_max > 0 )); then
    local spread_secs=$(( RANDOM % spread_max ))
    echo "Rate limit reset — spreading ${spread_secs}s to desync from other runners..." >&2
    sleep "${spread_secs}"
  fi
}

_github_csma_emit_failure() {
  printf '%s\n' "$1" >&2
}

# Wait until the named rate_limit resource has enough quota (carrier sense).
# Usage: github_csma_sense [core|graphql] [min_remaining]
github_csma_sense() {
  local resource="${1:-core}"
  local min_remaining="${2:-$(_github_csma_min_remaining "${resource}")}"

  local info remaining reset now wait_secs
  if ! info=$(gh api rate_limit 2>/dev/null); then
    echo "WARNING: github_csma_sense: could not read rate_limit; proceeding" >&2
    return 0
  fi

  remaining=$(echo "${info}" | jq -r --arg r "${resource}" '.resources[$r].remaining // empty')
  reset=$(echo "${info}" | jq -r --arg r "${resource}" '.resources[$r].reset // empty')

  if [[ -z "${remaining}" || "${remaining}" == "null" ]]; then
    echo "WARNING: github_csma_sense: no .resources.${resource} in rate_limit; proceeding" >&2
    return 0
  fi

  if (( remaining >= min_remaining )); then
    return 0
  fi

  now=$(date +%s)
  wait_secs=$(( reset - now + 1 ))
  if (( wait_secs < 1 )); then
    wait_secs=1
  fi
  cap=$(_github_csma_backoff_cap_sec)
  if (( wait_secs > cap )); then
    wait_secs="${cap}"
  fi

  echo "Rate limit sense: ${resource} remaining=${remaining} (min=${min_remaining}); waiting ${wait_secs}s until reset..." >&2
  sleep "${wait_secs}"

  # After a rate-limit sleep, all runners wake at the same reset timestamp.
  # Spread them over a wide window to avoid a thundering herd.
  _github_csma_post_reset_spread
}

# Random inter-call delay (slot time) to reduce synchronized collisions.
github_csma_slot() {
  local max_ms min_ms span_ms delay_ms
  max_ms=$(_github_csma_slot_max_ms)
  if (( max_ms <= 0 )); then
    return 0
  fi
  min_ms=$(_github_csma_slot_min_ms)
  if (( min_ms > max_ms )); then
    min_ms="${max_ms}"
  fi
  span_ms=$(( max_ms - min_ms + 1 ))
  delay_ms=$(( min_ms + RANDOM % span_ms ))
  sleep "$(awk -v ms="${delay_ms}" 'BEGIN { printf "%.3f", ms / 1000 }')"
}

# Return 0 if combined output looks like a retryable GitHub rate limit error.
github_csma_is_rate_limit() {
  local text="$1"
  local lower
  lower=$(echo "${text}" | tr '[:upper:]' '[:lower:]')

  if echo "${lower}" | grep -qE 'http 429|status: 429'; then
    return 0
  fi
  if echo "${lower}" | grep -qE 'secondary rate limit|rate limit exceeded|api rate limit'; then
    return 0
  fi
  if echo "${lower}" | grep -qE 'http 403|status: 403'; then
    if echo "${lower}" | grep -qE 'secondary|rate limit|abuse|retry.after'; then
      return 0
    fi
  fi
  return 1
}

# Compute backoff seconds for attempt (0-based). Writes to stdout.
github_csma_backoff() {
  local attempt="$1"
  local cap base delay
  cap=$(_github_csma_backoff_cap_sec)
  base=$(( 1 << attempt ))
  if (( base > cap )); then
    base="${cap}"
  fi
  delay=$(( RANDOM % (base + 1) ))
  if (( delay < 1 )); then
    delay=1
  fi
  echo "${delay}"
}

_github_csma_sleep_after_rate_limit() {
  local attempt="$1"
  local resource="${2:-core}"
  local delay wait_secs now reset info cap

  delay=$(github_csma_backoff "${attempt}")
  if info=$(gh api rate_limit 2>/dev/null); then
    now=$(date +%s)
    reset=$(echo "${info}" | jq -r --arg r "${resource}" '.resources[$r].reset // empty')
    if [[ -n "${reset}" && "${reset}" != "null" ]]; then
      wait_secs=$(( reset - now + 1 ))
      cap=$(_github_csma_backoff_cap_sec)
      if (( wait_secs > cap )); then
        wait_secs="${cap}"
      fi
      if (( wait_secs > delay && wait_secs > 0 )); then
        delay="${wait_secs}"
      fi
    fi
  fi
  echo "GitHub API rate limit (attempt $(( attempt + 1 ))); backing off ${delay}s..." >&2
  sleep "${delay}"

  # After backing off, spread runners to avoid thundering herd on wake.
  _github_csma_post_reset_spread
}

# Run gh with CSMA/CD. First argument: rate_limit resource (core|graphql).
# Remaining arguments are passed to gh. Prints gh stdout on success.
github_csma_run() {
  local resource="${1:-core}"
  shift

  local max_attempts attempt outfile errfile combined
  max_attempts=$(_github_csma_max_attempts)
  outfile=$(mktemp)
  errfile=$(mktemp)
  # shellcheck disable=SC2064
  trap "rm -f '${outfile}' '${errfile}'" RETURN

  for (( attempt = 0; attempt < max_attempts; attempt++ )); do
    github_csma_sense "${resource}"
    github_csma_slot

    : >"${outfile}"
    : >"${errfile}"
    local rc=0
    gh "$@" >"${outfile}" 2>"${errfile}" || rc=$?

    combined=$(cat "${outfile}" "${errfile}")
    if github_csma_is_rate_limit "${combined}"; then
      if (( attempt < max_attempts - 1 )); then
        _github_csma_sleep_after_rate_limit "${attempt}" "${resource}"
        continue
      fi
      _github_csma_emit_failure "${combined}"
      return 1
    fi

    if (( rc != 0 )); then
      _github_csma_emit_failure "${combined}"
      return 1
    fi
    cat "${outfile}"
    return 0
  done

  return 1
}

# Run producer | gh with CSMA/CD. First argument: resource; rest are gh args.
# Reads producer output from stdin (save once for retries).
github_csma_run_pipe() {
  local resource="${1:-graphql}"
  shift

  local max_attempts attempt infile outfile errfile combined
  max_attempts=$(_github_csma_max_attempts)
  infile=$(mktemp)
  outfile=$(mktemp)
  errfile=$(mktemp)
  cat >"${infile}"
  # shellcheck disable=SC2064
  trap "rm -f '${infile}' '${outfile}' '${errfile}'" RETURN

  for (( attempt = 0; attempt < max_attempts; attempt++ )); do
    github_csma_sense "${resource}"
    github_csma_slot

    : >"${outfile}"
    : >"${errfile}"
    local rc=0
    gh "$@" <"${infile}" >"${outfile}" 2>"${errfile}" || rc=$?

    combined=$(cat "${outfile}" "${errfile}")
    if github_csma_is_rate_limit "${combined}"; then
      if (( attempt < max_attempts - 1 )); then
        _github_csma_sleep_after_rate_limit "${attempt}" "${resource}"
        continue
      fi
      _github_csma_emit_failure "${combined}"
      return 1
    fi

    if (( rc != 0 )); then
      _github_csma_emit_failure "${combined}"
      return 1
    fi
    cat "${outfile}"
    return 0
  done

  return 1
}

# Run an arbitrary command with stdin from caller; retries on rate-limit errors in output.
# First argument: rate_limit resource (core|graphql); remaining args are the command.
github_csma_run_cmd() {
  local resource="${1:-core}"
  shift

  local max_attempts attempt infile outfile errfile combined
  max_attempts=$(_github_csma_max_attempts)
  infile=$(mktemp)
  outfile=$(mktemp)
  errfile=$(mktemp)
  cat >"${infile}"
  # shellcheck disable=SC2064
  trap "rm -f '${infile}' '${outfile}' '${errfile}'" RETURN

  for (( attempt = 0; attempt < max_attempts; attempt++ )); do
    github_csma_sense "${resource}"
    github_csma_slot

    : >"${outfile}"
    : >"${errfile}"
    local rc=0
    "$@" <"${infile}" >"${outfile}" 2>"${errfile}" || rc=$?

    combined=$(cat "${outfile}" "${errfile}")
    if github_csma_is_rate_limit "${combined}"; then
      if (( attempt < max_attempts - 1 )); then
        _github_csma_sleep_after_rate_limit "${attempt}" "${resource}"
        continue
      fi
      _github_csma_emit_failure "${combined}"
      return 1
    fi

    if (( rc != 0 )); then
      _github_csma_emit_failure "${combined}"
      return 1
    fi
    cat "${outfile}"
    return 0
  done

  return 1
}
# --- End inlined CSMA ---

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
