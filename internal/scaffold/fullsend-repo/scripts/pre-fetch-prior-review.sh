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

# How many times to retry fetching comments when none are found.
# This mitigates the race condition where the prior review run has not
# posted its comment yet (e.g. overlapping runs from rapid pushes).
FETCH_RETRIES="${PREFETCH_RETRIES:-1}"
FETCH_RETRY_DELAY="${PREFETCH_RETRY_DELAY:-15}"

# --- Helper: fetch and filter comments ---
# Sets COMMENT_JSON to the last matching review-bot comment, or "".
fetch_prior_comment() {
  local api_stderr
  api_stderr="$(mktemp)"

  # Fetch full comment object (not just body) for provenance validation.
  # stderr is captured separately so API errors are not silently lost.
  local raw_comments=""
  raw_comments=$(gh api "repos/${SOURCE_REPO}/issues/${PR_NUM}/comments" \
    --paginate --jq '.[]' 2>"${api_stderr}") || {
    local api_exit=$?
    echo "::warning::gh api failed (exit ${api_exit}): $(cat "${api_stderr}")"
    rm -f "${api_stderr}"
    COMMENT_JSON=""
    return
  }

  if [[ -n "$(cat "${api_stderr}")" ]]; then
    echo "::debug::gh api stderr: $(cat "${api_stderr}")"
  fi
  rm -f "${api_stderr}"

  if [[ -z "${raw_comments}" ]]; then
    echo "::debug::No comments returned by API"
    COMMENT_JSON=""
    return
  fi

  # Count total comments for diagnostics.
  local total_comments
  total_comments=$(echo "${raw_comments}" | jq -s 'length' 2>/dev/null || echo "?")
  echo "::debug::Total comments on PR: ${total_comments}"

  # Filter to review-bot comments with the sentinel marker.
  COMMENT_JSON=$(echo "${raw_comments}" \
    | jq --arg bot "${REVIEW_BOT}" -s \
      '[.[] | select(.user.login == $bot
        and (.body | contains("<!-- fullsend:review-agent -->")))] | last // empty' \
    2>/dev/null || echo "")

  # Log how many matching comments were found (for diagnostics).
  local matching_count
  matching_count=$(echo "${raw_comments}" \
    | jq --arg bot "${REVIEW_BOT}" -s \
      '[.[] | select(.user.login == $bot
        and (.body | contains("<!-- fullsend:review-agent -->")))] | length' \
    2>/dev/null || echo "?")
  echo "::debug::Matching review-bot comments: ${matching_count}"
}

# --- Fetch comments (with retry for race conditions) ---
COMMENT_JSON=""
fetch_prior_comment

attempt=0
while [[ -z "${COMMENT_JSON}" || "${COMMENT_JSON}" == "null" ]] && \
      [[ ${attempt} -lt ${FETCH_RETRIES} ]]; do
  attempt=$((attempt + 1))
  echo "::notice::No prior review comment found — retrying in ${FETCH_RETRY_DELAY}s (attempt ${attempt}/${FETCH_RETRIES})"
  sleep "${FETCH_RETRY_DELAY}"
  fetch_prior_comment
done

if [[ -z "${COMMENT_JSON}" || "${COMMENT_JSON}" == "null" ]]; then
    echo "No prior review found (first review or prior review not yet posted)"

    # Observability: warn if human reviewers already commented before the
    # first automated review dispatch. This surfaces cases where the
    # automated review missed the initial PR-creation window (e.g. due to
    # a transient webhook delivery failure) and a human had to review first.
    HUMAN_REVIEW_COUNT=$(gh api "repos/${SOURCE_REPO}/pulls/${PR_NUM}/reviews" \
      --paginate --jq '[.[] | select(.user.type != "Bot")] | length' \
      2>/dev/null || echo "0")
    if [[ "${HUMAN_REVIEW_COUNT}" -gt 0 ]]; then
        echo "::warning::First automated review dispatch but PR already" \
          "has ${HUMAN_REVIEW_COUNT} human review(s). The automated review" \
          "may have missed the initial PR creation window."
    fi

    : > "${PRIOR_FILE}"  # truncate to 0 bytes
    # shellcheck disable=SC2129
    echo "prior_review_file=${PRIOR_FILE}" >> "${GITHUB_OUTPUT}"
    echo "prior_sha=" >> "${GITHUB_OUTPUT}"
    echo "prior_review_provenance=${PROVENANCE}" >> "${GITHUB_OUTPUT}"
    exit 0
fi

# Previous review exists — extract ID
COMMENT_ID="$(echo "${COMMENT_JSON}" | jq -r '.id')"
echo "::debug::Prior review comment ID: ${COMMENT_ID}"

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
    echo "::warning::Prior review body too large (${BYTE_COUNT} bytes > ${MAX_BYTES}), skipping anchoring"
    echo "" > "${PRIOR_FILE}"
    BYTE_COUNT=0
fi

echo "prior_review_file=${PRIOR_FILE}" >> "${GITHUB_OUTPUT}"

if [[ "${BYTE_COUNT}" -gt 1 ]]; then
    # Extract SHA from current section only (before sticky history sentinels)
    CURRENT_SECTION="$(awk '/<!-- sticky:history-start -->/{exit} {print}' "${PRIOR_FILE}")"
    PRIOR_SHA="$(echo "${CURRENT_SECTION}" \
        | grep -oP '(?<=\*\*Head SHA:\*\* )[0-9a-f]{7,64}' | head -1 || true)"
    echo "prior_sha=${PRIOR_SHA}" >> "${GITHUB_OUTPUT}"
    echo "Prior review SHA: ${PRIOR_SHA:-none}"

    # Diagnostic: if we found the body but no SHA, log a warning so this
    # failure mode is visible in the Actions run summary.
    if [[ -z "${PRIOR_SHA}" ]]; then
        # Show the first 200 chars of the current section for debugging.
        local_preview="$(echo "${CURRENT_SECTION}" | head -c 200)"
        echo "::warning::Prior review body found (${BYTE_COUNT} bytes) but no Head SHA extracted. Body preview: ${local_preview}"
    fi
else
    echo "No usable prior review content"
    echo "prior_sha=" >> "${GITHUB_OUTPUT}"
fi

echo "prior_review_provenance=${PROVENANCE}" >> "${GITHUB_OUTPUT}"
