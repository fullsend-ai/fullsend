#!/usr/bin/env bash
# pre-triage.sh — Strip triage-related labels before the agent runs.
#
# Runs on the host via the harness pre_script mechanism. Ensures every
# triage invocation starts from a clean label baseline, preventing
# mutual-exclusion violations (Story 2, #125).
#
# Required env vars:
#   GITHUB_ISSUE_URL — HTML URL of the issue
#   GH_TOKEN         — GitHub token with issues read/write scope
#
# IMPORTANT: Uses the labels API directly (DELETE /issues/{number}/labels/{name})
# instead of gh issue edit --remove-label. gh issue edit uses PATCH /issues/{number}
# which fires issues.edited, re-triggering the triage dispatch in the shim workflow.

set -euo pipefail

echo "::notice::🔗 Triage target: ${GITHUB_ISSUE_URL}"

if [[ ! "${GITHUB_ISSUE_URL}" =~ ^https://github\.com/[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+/issues/[0-9]+$ ]]; then
  echo "ERROR: GITHUB_ISSUE_URL does not match expected pattern: ${GITHUB_ISSUE_URL}"
  exit 1
fi

REPO=$(echo "${GITHUB_ISSUE_URL}" | sed 's|https://github.com/||; s|/issues/.*||')
ISSUE_NUMBER=$(basename "${GITHUB_ISSUE_URL}")

echo "Resetting triage labels on ${REPO}#${ISSUE_NUMBER}"

for label in needs-info ready-to-code duplicate not-ready not-reproducible feature-request; do
  gh api "repos/${REPO}/issues/${ISSUE_NUMBER}/labels/${label}" -X DELETE --silent 2>/dev/null || true
done

# Verify no triage labels remain — the pipeline depends on mutual exclusivity.
REMAINING=$(gh api "repos/${REPO}/issues/${ISSUE_NUMBER}/labels" \
  --jq '[.[] | select(.name == "needs-info" or .name == "ready-to-code" or .name == "duplicate" or .name == "feature-request") | .name] | join(", ")' 2>/dev/null || echo "VERIFY_FAILED")

if [[ "${REMAINING}" == "VERIFY_FAILED" ]]; then
  echo "ERROR: cannot verify label state — API call failed"
  exit 1
fi
if [[ -n "${REMAINING}" ]]; then
  echo "ERROR: triage labels still present after reset: ${REMAINING}"
  exit 1
fi

echo "Label reset complete."
