#!/usr/bin/env bash
# post-review-test.sh — Test the outcome-label logic in post-review.sh.
#
# Extracts and tests the label-application logic in isolation using shell
# functions. This avoids needing a live GitHub API or fullsend CLI.
#
# Run from the repo root:
#   bash internal/scaffold/fullsend-repo/scripts/post-review-test.sh

set -euo pipefail

FAILURES=0

# ---------------------------------------------------------------------------
# Test helper — reimplements the outcome-label logic from post-review.sh
# so we can test it without network access.
#
# Arguments:
#   $1 — ACTION (the original action from agent-result.json)
#   $2 — DOWNGRADED ("true" or "false")
#
# Prints the label that would be applied, or "none" if no label.
# ---------------------------------------------------------------------------
determine_outcome_label() {
  local action="$1"
  local downgraded="$2"

  if [ "${action}" = "approve" ] && [ "${downgraded}" = "false" ]; then
    echo "ready-for-merge"
  elif [ "${action}" = "approve" ] && [ "${downgraded}" = "true" ]; then
    echo "requires-manual-review"
  elif [ "${action}" = "comment" ]; then
    echo "requires-manual-review"
  elif [ "${action}" = "request_changes" ]; then
    echo "none"
  elif [ "${action}" = "reject" ]; then
    echo "rejected"
  else
    echo "none"
  fi
}

run_test() {
  local test_name="$1"
  local action="$2"
  local downgraded="$3"
  local expected="$4"

  local actual
  actual="$(determine_outcome_label "${action}" "${downgraded}")"

  if [ "${actual}" != "${expected}" ]; then
    echo "FAIL: ${test_name}"
    echo "  action:     '${action}'"
    echo "  downgraded: '${downgraded}'"
    echo "  expected:   '${expected}'"
    echo "  actual:     '${actual}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Test cases ---

# Approve without protected-path downgrade → ready-for-merge
run_test "approve-no-downgrade" \
  "approve" "false" "ready-for-merge"

# Approve with protected-path downgrade → requires-manual-review
run_test "approve-with-downgrade" \
  "approve" "true" "requires-manual-review"

# Comment (split/conflicting review) → requires-manual-review
run_test "comment-split-review" \
  "comment" "false" "requires-manual-review"

# request_changes → no outcome label
run_test "request-changes-no-label" \
  "request_changes" "false" "none"

# reject → rejected
run_test "reject-label" \
  "reject" "false" "rejected"

# Defensive: comment + downgraded=true can't occur in production (DOWNGRADED is
# only set inside the approve branch), but verify the label logic handles it.
run_test "comment-with-downgrade-flag" \
  "comment" "true" "requires-manual-review"

# Edge cases: ensure unknown/empty actions produce no label
run_test "empty-action-no-label" \
  "" "false" "none"

run_test "failure-action-no-label" \
  "failure" "false" "none"

run_test "unknown-action-no-label" \
  "banana" "false" "none"

# --- Summary ---

echo ""
if [ "${FAILURES}" -gt 0 ]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
