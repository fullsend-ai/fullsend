#!/usr/bin/env bash
# post-triage.sh — Parse triage agent JSON output and perform GitHub mutations.
#
# Runs on the host after sandbox cleanup. Working directory is the fullsend
# run output directory (e.g., /tmp/fullsend/agent-triage-<id>/iteration-1/).
#
# Required env vars:
#   GITHUB_ISSUE_URL  — HTML URL of the issue (e.g., https://github.com/org/repo/issues/42)
#   GH_TOKEN          — GitHub token with issues read/write scope
#
# The agent writes its decision to output/agent-result.json (relative to
# the iteration directory). This script finds the most recent iteration's output.
#
# IMPORTANT: Label mutations use the labels API directly (gh api) instead of
# gh issue edit. gh issue edit uses PATCH /issues/{number} which fires
# issues.edited, re-triggering the triage dispatch in the shim workflow.
# The labels API (POST/DELETE /issues/{number}/labels) only fires
# issues.labeled/issues.unlabeled, avoiding the re-triage loop.

set -euo pipefail

# Find the triage result JSON. The run dir contains iteration-N/ subdirectories;
# we want the last one's output.
RESULT_FILE=""
for dir in iteration-*/output; do
  if [[ -f "${dir}/agent-result.json" ]]; then
    RESULT_FILE="${dir}/agent-result.json"
  fi
done

if [[ -z "${RESULT_FILE}" ]]; then
  echo "ERROR: agent-result.json not found in any iteration output directory"
  exit 1
fi

echo "Reading triage result from: ${RESULT_FILE}"

# Validate JSON is parseable.
if ! jq empty "${RESULT_FILE}" 2>/dev/null; then
  echo "ERROR: ${RESULT_FILE} is not valid JSON"
  exit 1
fi

ACTION=$(jq -r '.action' "${RESULT_FILE}")
COMMENT=$(jq -r '.comment // empty' "${RESULT_FILE}")

# Validate and extract repo and issue number from the HTML URL.
# GITHUB_ISSUE_URL is e.g. https://github.com/org/repo/issues/42
if [[ ! "${GITHUB_ISSUE_URL}" =~ ^https://github\.com/[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+/issues/[0-9]+$ ]]; then
  echo "ERROR: GITHUB_ISSUE_URL does not match expected pattern: ${GITHUB_ISSUE_URL}"
  exit 1
fi
REPO=$(echo "${GITHUB_ISSUE_URL}" | sed 's|https://github.com/||; s|/issues/.*||')
ISSUE_NUMBER=$(basename "${GITHUB_ISSUE_URL}")

echo "Action: ${ACTION}"
echo "Repo: ${REPO}"
echo "Issue: #${ISSUE_NUMBER}"

# add_label uses the labels API to avoid firing issues.edited.
add_label() {
  if ! gh api "repos/${REPO}/issues/${ISSUE_NUMBER}/labels" -f "labels[]=$1" --silent; then
    echo "ERROR: failed to add label '$1' to issue #${ISSUE_NUMBER}" >&2
    exit 1
  fi
}

case "${ACTION}" in
  insufficient)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'insufficient' but no comment provided"
      exit 1
    fi
    echo "Posting clarifying question..."
    printf '%s' "${COMMENT}" | fullsend post-comment --repo "${REPO}" --number "${ISSUE_NUMBER}" --marker "<!-- fullsend:triage-agent -->" --token "${GH_TOKEN}" --result -

    echo "Applying label..."
    add_label "needs-info"
    ;;

  duplicate)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'duplicate' but no comment provided"
      exit 1
    fi
    DUPLICATE_OF=$(jq -r '.duplicate_of' "${RESULT_FILE}")
    if [[ "${DUPLICATE_OF}" -eq "${ISSUE_NUMBER}" ]]; then
      echo "ERROR: issue cannot be a duplicate of itself (#${ISSUE_NUMBER})"
      exit 1
    fi
    echo "Posting duplicate notice..."
    printf '%s' "${COMMENT}" | fullsend post-comment --repo "${REPO}" --number "${ISSUE_NUMBER}" --marker "<!-- fullsend:triage-agent -->" --token "${GH_TOKEN}" --result -

    echo "Applying label and closing..."
    add_label "duplicate"
    gh issue close "${ISSUE_NUMBER}" --repo "${REPO}" --reason "not planned"
    ;;

  sufficient)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'sufficient' but no comment provided"
      exit 1
    fi

    # Guard: reject sufficient results that contain information_gaps.
    # If the agent identified open questions, it should have used "insufficient".
    GAP_COUNT=$(jq '.triage_summary.information_gaps // [] | length' "${RESULT_FILE}")
    if [[ "${GAP_COUNT}" -gt 0 ]]; then
      echo "ERROR: action is 'sufficient' but triage_summary contains ${GAP_COUNT} information_gaps — open questions must block triage"
      exit 1
    fi

    echo "Posting triage summary..."
    printf '%s' "${COMMENT}" | fullsend post-comment --repo "${REPO}" --number "${ISSUE_NUMBER}" --marker "<!-- fullsend:triage-agent -->" --token "${GH_TOKEN}" --result -

    # Only bugs get the ready-to-code label (which triggers the code agent).
    # Non-bug sufficient results (enhancement, performance, documentation, etc.)
    # receive the triaged label instead and wait for human prioritization.
    CATEGORY=$(jq -r '.triage_summary.category // "unknown"' "${RESULT_FILE}")
    echo "Category: ${CATEGORY}"
    if [[ "${CATEGORY}" == "bug" ]]; then
      echo "Applying ready-to-code label (bug)..."
      add_label "ready-to-code"
    else
      echo "Applying triaged label (non-bug: ${CATEGORY})..."
      add_label "triaged"
    fi
    ;;

  feature-request)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'feature-request' but no comment provided"
      exit 1
    fi
    echo "Posting feature-request comment..."
    printf '%s' "${COMMENT}" | gh issue comment "${ISSUE_NUMBER}" --repo "${REPO}" --body-file -

    echo "Removing bug-related labels..."
    for label in bug bug-report type/bug; do
      gh api "repos/${REPO}/issues/${ISSUE_NUMBER}/labels/${label}" -X DELETE --silent 2>/dev/null || true
    done

    echo "Applying type/feature label..."
    add_label "type/feature"
    ;;

  *)
    echo "ERROR: unknown action '${ACTION}' — this may be a newer action that post-triage.sh does not handle yet"
    exit 1
    ;;
esac

echo "Post-triage complete."
