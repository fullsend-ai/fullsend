#!/usr/bin/env bash
# pre-fetch-prior-review.sh - Fetch any previous review before the review agent runs
#
# Required environment variables (set by the workflow)
#
#   - GH_TOKEN
#   - ORG_NAME
#   - PR_NUM
#   - REVIEW_APP_CLIENT_ID - review agent's GitHub ID
#   - SOURCE_REPO
set -euo pipefail

PRIOR_FILE=${GITHUB_WORKSPACE}/prior-review.txt
REVIEW_BOT="${ORG_NAME}-review[bot]"
PROVENANCE="none"

# Fetch full comment object (not just body) for provenance validation
COMMENT_JSON=$(gh api "repos/${SOURCE_REPO}/issues/${PR_NUM}/comments" \
  --paginate --jq '.[]' \
  | jq --arg bot "${REVIEW_BOT}" -s \
    '[.[] | select(.user.login == $bot
      and (.body | contains("<!-- fullsend:review-agent -->")))] | last // empty' \
  2>/dev/null || echo "")

if [[ -z "${COMMENT_JSON}" || "${COMMENT_JSON}" == "null" ]]; then
    echo "No prior review found (first review)"
    : > "${PRIOR_FILE}"  # truncate to 0 bytes
    # shellcheck disable=SC2129
    echo "prior_review_file=${PRIOR_FILE}" >> "${GITHUB_OUTPUT}"
    echo "prior_sha=" >> "${GITHUB_OUTPUT}"
    echo "prior_review_provenance=${PROVENANCE}" >> "${GITHUB_OUTPUT}"
    exit 0
fi

# Previous review exists — extract ID
COMMENT_ID="$(echo "${COMMENT_JSON}" | jq -r '.id')"

# Validate that the comment was created by the expected GitHub App.
# The REST API does not expose comment edit history — we can verify
# original authorship but not post-creation edits. HMAC-based content
# integrity is tracked as a follow-up to close the edit-detection gap.
APP_CLIENT_ID="$(echo "${COMMENT_JSON}" | jq -r '.performed_via_github_app.client_id // ""')"

if [[ -z "${APP_CLIENT_ID}" ]]; then
    echo "::warning::Prior review comment ${COMMENT_ID} has no GitHub App provenance — discarding (cannot verify authorship)"
    PROVENANCE="unverifiable-no-app"
elif [[ "${APP_CLIENT_ID}" != "${REVIEW_APP_CLIENT_ID}" ]]; then
    echo "::error::Prior review comment ${COMMENT_ID} created by app client_id=${APP_CLIENT_ID}, expected ${REVIEW_APP_CLIENT_ID} — discarding (wrong app)"
    PROVENANCE="unverifiable-wrong-app"
else
    PROVENANCE="app-verified"
fi

if [[ "${PROVENANCE}" != "app-verified" ]]; then
    : > "${PRIOR_FILE}"  # truncate to 0 bytes
    # shellcheck disable=SC2129
    echo "prior_review_file=${PRIOR_FILE}" >> "${GITHUB_OUTPUT}"
    echo "prior_sha=" >> "${GITHUB_OUTPUT}"
    echo "prior_review_provenance=${PROVENANCE}" >> "${GITHUB_OUTPUT}"
    exit 0
fi

# Provenance passed — extract body
echo "${COMMENT_JSON}" | jq -r '.body // ""' > "${PRIOR_FILE}"

BYTE_COUNT="$(wc -c < "${PRIOR_FILE}")"
echo "Prior review body: ${BYTE_COUNT} bytes"

MAX_BYTES=1048576  # 1 MB
if [[ "${BYTE_COUNT}" -gt "${MAX_BYTES}" ]]; then
    echo "::warning::Prior review body too large, skipping anchoring"
    echo "" > "${PRIOR_FILE}"
    BYTE_COUNT=0
fi

echo "prior_review_file=${PRIOR_FILE}" >> "${GITHUB_OUTPUT}"

if [[ "${BYTE_COUNT}" -gt 1 ]]; then
    # Extract SHA from current section only (before sticky history sentinels)
    CURRENT_SECTION="$(awk '/<!-- sticky:history-start -->/{exit} {print}' "${PRIOR_FILE}")"
    PRIOR_SHA="$(echo "${CURRENT_SECTION}" \
        | grep -oP '(?<=\*\*Head SHA:\*\* )[0-9a-f]{7,64}' | head -1)"
    echo "prior_sha=${PRIOR_SHA}" >> "${GITHUB_OUTPUT}"
    echo "Prior review SHA: ${PRIOR_SHA:-none}"
else
    echo "No usable prior review content"
    echo "prior_sha=" >> "${GITHUB_OUTPUT}"
fi

echo "prior_review_provenance=${PROVENANCE}" >> "${GITHUB_OUTPUT}"
