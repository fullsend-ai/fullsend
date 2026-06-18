#!/usr/bin/env bash
# pipeline-helpers.sh — Shared helpers for refinement pipeline post-scripts.
#
# Source this file from post-explore.sh, post-refine.sh, post-critique.sh.
# Requires: GH_TOKEN, JIRA_HOST, JIRA_EMAIL, JIRA_API_TOKEN (Jira path only)

# Prevent double-sourcing
[[ -n "${_PIPELINE_HELPERS_LOADED:-}" ]] && return 0
_PIPELINE_HELPERS_LOADED=1

SCRIPT_DIR="${SCRIPT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"

add_label() {
  local repo="$1" number="$2" label="$3"
  gh api "repos/${repo}/issues/${number}/labels" -f "labels[]=${label}" --silent 2>/dev/null || true
}

remove_label() {
  local repo="$1" number="$2" label="$3"
  local encoded
  encoded=$(printf '%s' "$label" | jq -sRr @uri)
  gh api "repos/${repo}/issues/${number}/labels/${encoded}" -X DELETE --silent 2>/dev/null || true
}

github_comment() {
  local repo="$1" number="$2" body="$3"
  printf '%s' "$body" | gh issue comment "$number" --repo "$repo" --body-file -
}

jira_comment() {
  local key="$1" body="$2"
  local auth
  auth=$(printf '%s:%s' "$JIRA_EMAIL" "$JIRA_API_TOKEN" | base64 -w0)
  local adf_body
  adf_body=$(printf '%s' "$body" | python3 "${SCRIPT_DIR}/markdown-to-adf.py")
  curl -sSf -X POST \
    -H "Authorization: Basic $auth" \
    -H "Content-Type: application/json" \
    -d "$adf_body" \
    "https://${JIRA_HOST}/rest/api/3/issue/${key}/comment"
}

# post_comment dispatches to GitHub or Jira based on USE_GITHUB.
# Callers must set USE_GITHUB, REPO_FULL_NAME, GITHUB_ISSUE_NUMBER, ISSUE_KEY.
post_comment() {
  local body="$1"
  if ${USE_GITHUB:-false}; then
    github_comment "${REPO_FULL_NAME}" "$GITHUB_ISSUE_NUMBER" "$body"
  else
    jira_comment "$ISSUE_KEY" "$body"
  fi
}

# Determine reply target from environment. Sets USE_GITHUB and GITHUB_ISSUE_NUMBER.
determine_reply_target() {
  USE_GITHUB=false
  if [[ -n "${GITHUB_ISSUE_NUMBER:-}" && "${GITHUB_ISSUE_NUMBER}" != "" && "${GITHUB_ISSUE_NUMBER}" != "N/A" ]]; then
    USE_GITHUB=true
  elif [[ "${ISSUE_SOURCE:-}" == "github" ]]; then
    USE_GITHUB=true
    GITHUB_ISSUE_NUMBER="${ISSUE_KEY}"
  fi
}

# Build a run link from GITHUB_REPOSITORY and GITHUB_RUN_ID.
build_run_link() {
  local run_url="https://github.com/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID:-}"
  echo "[Run #${GITHUB_RUN_ID:-manual}](${run_url})"
}

# Find the last agent-result.json from iteration output directories.
find_agent_result() {
  local result_file=""
  for dir in iteration-*/output; do
    if [[ -f "${dir}/agent-result.json" ]]; then
      result_file="${dir}/agent-result.json"
    fi
  done
  if [[ -z "$result_file" ]]; then
    echo "ERROR: agent-result.json not found in any iteration output directory" >&2
    return 1
  fi
  if ! jq empty "$result_file" 2>/dev/null; then
    echo "ERROR: ${result_file} is not valid JSON" >&2
    return 1
  fi
  echo "$result_file"
}

# Read trace context for distributed tracing propagation.
get_traceparent() {
  local tp="${TRACEPARENT:-}"
  if [[ -z "$tp" && -f "/tmp/workspace/otel-trace-context.json" ]]; then
    tp=$(python3 -c "import json; print(json.load(open('/tmp/workspace/otel-trace-context.json'))['traceparent'])" 2>/dev/null || echo "")
  fi
  echo "$tp"
}
