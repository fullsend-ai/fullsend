#!/usr/bin/env bash
# pre-fetch-review-body.sh — Fetch the latest review-bot change request body
# for the fix agent.
#
# Inputs (env vars):
#   GH_TOKEN         — GitHub token for API calls
#   SOURCE_REPO      — Repository in owner/repo format
#   PR_NUM           — Pull request number
#   TRIGGER_SOURCE   — Username that triggered the fix
#   ORG_NAME         — Repository owner (org-specific review bot prefix)
#   FULLSEND_APP_SET — Optional configured GitHub App set prefix
#   GITHUB_WORKSPACE — Directory the review body file is written to
#   GITHUB_OUTPUT    — GitHub Actions step output file (optional)
#
# Writes the body of the latest CHANGES_REQUESTED review submitted by a
# recognized review bot to ${GITHUB_WORKSPACE}/review-body.txt and emits
# review_file=<path> to GITHUB_OUTPUT.
#
# Reviews are fetched across every API page. The reviews endpoint returns
# 30 reviews per page by default (100 at most), so on a PR with many
# reviews the latest change request is often not on the first page (#8353).
#
# Exits 1 if the body exceeds the size limit, or if a bot-triggered run
# finds no review body to act on.

set -euo pipefail

REVIEW_FILE="${GITHUB_WORKSPACE:-.}/review-body.txt"
REVIEW_BOT="${ORG_NAME}-review[bot]"
SHARED_REVIEW_BOT="fullsend-ai-review[bot]"
CUSTOM_REVIEW_BOT=""
if [[ -n "${FULLSEND_APP_SET:-}" ]]; then
  CUSTOM_REVIEW_BOT="${FULLSEND_APP_SET}-review[bot]"
fi

ERR_FILE="$(mktemp)"
trap 'rm -f "${ERR_FILE}"' EXIT

# Collect every page before selecting: `last` must run over the combined
# review list, not per page (see docs/contributing/shell-scripting.md).
# Reviews are returned in chronological order, so `last` is the latest.
if ! gh api --paginate "repos/${SOURCE_REPO}/pulls/${PR_NUM}/reviews?per_page=100" \
    2>"${ERR_FILE}" \
  | jq -r -s --arg review_bot "${REVIEW_BOT}" \
    --arg shared_bot "${SHARED_REVIEW_BOT}" \
    --arg custom_bot "${CUSTOM_REVIEW_BOT}" \
    '(add // []) | [.[] | select(.state == "CHANGES_REQUESTED" and
      (.user.login == $review_bot or .user.login == $shared_bot or
       ($custom_bot != "" and .user.login == $custom_bot)))]
      | last | .body // ""' \
    > "${REVIEW_FILE}" 2>>"${ERR_FILE}"; then
  ERR_MSG="$(head -c 500 "${ERR_FILE}" | tr '\r\n' '  ')"
  echo "::warning::Failed to fetch reviews for PR #${PR_NUM}: ${ERR_MSG//::/__}"
  echo "" > "${REVIEW_FILE}"
fi

BYTE_COUNT="$(wc -c < "${REVIEW_FILE}")"
echo "Pre-fetched review body: ${BYTE_COUNT} bytes"
MAX_REVIEW_BYTES=1048576
if [[ "${BYTE_COUNT}" -gt "${MAX_REVIEW_BYTES}" ]]; then
  echo "::error::Review body is ${BYTE_COUNT} bytes (max: ${MAX_REVIEW_BYTES})"
  exit 1
fi
if [[ "${TRIGGER_SOURCE}" =~ \[bot\]$ && "${BYTE_COUNT}" -le 1 ]]; then
  echo "::error::Bot-triggered run but review body is empty — nothing to fix"
  exit 1
fi
echo "review_file=${REVIEW_FILE}" >> "${GITHUB_OUTPUT:-/dev/null}"
