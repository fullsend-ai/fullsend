#!/usr/bin/env bash
# Waits for a PR's required checks and approvals, then enqueues it.
# Exits early if any required check fails.
#
# Usage: await-and-enqueue.sh [PR_NUMBER_OR_URL]
#
# If no argument is given, uses the current branch's PR.
# Polls every 30 seconds. Requires: gh CLI, jq.

set -euo pipefail

POLL_INTERVAL="${POLL_INTERVAL:-30}"
pr="${1:-}"

# Resolve PR URL and repo
if [[ -z "$pr" ]]; then
  pr_json_init="$(gh pr view --json url,baseRefName,headRepository -q '{url,baseRefName,headRepository}')"
else
  pr_json_init="$(gh pr view "$pr" --json url,baseRefName,headRepository -q '{url,baseRefName,headRepository}')"
fi

pr_url="$(echo "$pr_json_init" | jq -r .url)"
base_branch="$(echo "$pr_json_init" | jq -r .baseRefName)"

# Extract owner/repo from the PR URL
repo_nwo="$(echo "$pr_url" | sed -E 's|https://github.com/([^/]+/[^/]+)/pull/.*|\1|')"

# Fetch required status checks from branch rulesets
required_checks="$(gh api "repos/$repo_nwo/rules/branches/$base_branch" \
  --jq '[.[] | select(.type == "required_status_checks") | .parameters.required_status_checks[].context] | unique | .[]' 2>/dev/null || true)"

if [[ -n "$required_checks" ]]; then
  echo "Required checks: $(echo "$required_checks" | tr '\n' ', ' | sed 's/,$//')"
fi

echo "Waiting for checks and approvals on: $pr_url"

while true; do
  # Get check rollup and review decision in one call
  pr_json="$(gh pr view "$pr_url" --json statusCheckRollup,reviewDecision)"

  review_decision="$(echo "$pr_json" | jq -r '.reviewDecision // "NONE"')"

  # Build a map of check name -> conclusion
  declare -A check_status=()
  while IFS=$'\t' read -r state name; do
    check_status["$name"]="$state"
  done < <(echo "$pr_json" | jq -r '.statusCheckRollup[] | [(.conclusion // .status // "PENDING"), .name] | @tsv')

  has_pending=false
  has_failure=false

  # Check reported statuses
  for name in "${!check_status[@]}"; do
    state="${check_status[$name]}"
    case "$state" in
      SUCCESS|NEUTRAL|SKIPPED|COMPLETED)
        ;;
      FAILURE|ERROR|CANCELLED|TIMED_OUT|STARTUP_FAILURE|ACTION_REQUIRED)
        echo "FAILED: $name ($state)"
        has_failure=true
        ;;
      *)
        has_pending=true
        ;;
    esac
  done

  # Check for required checks that haven't appeared yet
  if [[ -n "$required_checks" ]]; then
    while IFS= read -r req; do
      if [[ -z "${check_status[$req]+x}" ]]; then
        echo "Required check not yet reported: $req"
        has_pending=true
      fi
    done <<< "$required_checks"
  fi

  unset check_status

  if [[ "$has_failure" == "true" ]]; then
    echo "Aborting — one or more required checks failed."
    exit 1
  fi

  if [[ "$has_pending" == "true" ]]; then
    echo "Waiting ${POLL_INTERVAL}s..."
    sleep "$POLL_INTERVAL"
    continue
  fi

  if [[ "$review_decision" != "APPROVED" ]]; then
    echo "Checks passed but review not yet approved (status: $review_decision)... waiting ${POLL_INTERVAL}s"
    sleep "$POLL_INTERVAL"
    continue
  fi

  echo "All checks passed and PR is approved. Enqueuing..."
  break
done

# Delegate to the enqueue script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec bash "$SCRIPT_DIR/enqueue-pr.sh" "$pr_url"
