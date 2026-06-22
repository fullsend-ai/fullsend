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
  pr_json_init="$(gh pr view --json url,baseRefName -q '{url,baseRefName}')"
else
  pr_json_init="$(gh pr view "$pr" --json url,baseRefName -q '{url,baseRefName}')"
fi

pr_url="$(echo "$pr_json_init" | jq -r .url)"
base_branch="$(echo "$pr_json_init" | jq -r .baseRefName)"

# Extract owner/repo from the PR URL
repo_nwo="$(echo "$pr_url" | sed -E 's|https://github.com/([^/]+/[^/]+)/pull/.*|\1|')"

# Fetch required status checks from branch rulesets as a JSON array
required_json="$(gh api "repos/$repo_nwo/rules/branches/$base_branch" \
  --jq '[.[] | select(.type == "required_status_checks") | .parameters.required_status_checks[].context] | unique' 2>/dev/null || echo '[]')"

if [[ "$(echo "$required_json" | jq 'length')" -gt 0 ]]; then
  echo "Required checks: $(echo "$required_json" | jq -r 'join(", ")')"
fi

echo "Waiting for checks and approvals on: $pr_url"

while true; do
  # Get check rollup and review decision in one call
  pr_json="$(gh pr view "$pr_url" --json statusCheckRollup,reviewDecision)"

  review_decision="$(echo "$pr_json" | jq -r '.reviewDecision // "NONE"')"

  # Use jq to analyze all check statuses and required check coverage in one pass
  result="$(echo "$pr_json" | jq -r --argjson required "$required_json" '
    .statusCheckRollup as $checks |
    # Build map of name -> conclusion
    ($checks | map({(.name): (.conclusion // .status // "PENDING")}) | add // {}) as $map |
    # Check for failures
    [$map | to_entries[] | select(.value | test("FAILURE|ERROR|CANCELLED|TIMED_OUT|STARTUP_FAILURE|ACTION_REQUIRED")) | .key + " (" + .value + ")"] as $failures |
    # Check for pending
    [$map | to_entries[] | select(.value | test("SUCCESS|NEUTRAL|SKIPPED|COMPLETED|FAILURE|ERROR|CANCELLED|TIMED_OUT|STARTUP_FAILURE|ACTION_REQUIRED") | not) | .key] as $pending |
    # Check for missing required checks
    [$required[] | select(. as $r | $map | has($r) | not)] as $missing |
    {failures: $failures, pending: $pending, missing: $missing}
  ')"

  failures="$(echo "$result" | jq -r '.failures[]' 2>/dev/null || true)"
  pending="$(echo "$result" | jq -r '.pending[]' 2>/dev/null || true)"
  missing="$(echo "$result" | jq -r '.missing[]' 2>/dev/null || true)"

  if [[ -n "$failures" ]]; then
    echo "$failures" | while IFS= read -r f; do echo "FAILED: $f"; done
    echo "Aborting — one or more required checks failed."
    exit 1
  fi

  has_pending=false
  if [[ -n "$pending" ]]; then
    has_pending=true
  fi
  if [[ -n "$missing" ]]; then
    echo "$missing" | while IFS= read -r m; do echo "Required check not yet reported: $m"; done
    has_pending=true
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
