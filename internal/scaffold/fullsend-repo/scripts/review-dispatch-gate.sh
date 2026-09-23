#!/usr/bin/env bash
# review-dispatch-gate.sh - Decide whether a review dispatch is a duplicate.
#
# Required environment variables:
#   GH_TOKEN, SOURCE_REPO, PR_NUMBER, EXPECTED_HEAD_SHA, CURRENT_RUN_ID
set -euo pipefail

if [[ ! "${PR_NUMBER}" =~ ^[0-9]+$ ]] || [[ ! "${EXPECTED_HEAD_SHA}" =~ ^[0-9a-f]{40}$ ]] \
  || [[ ! "${CURRENT_RUN_ID}" =~ ^[0-9]+$ ]]; then
  echo "::error::Review dispatch requires numeric PR/run IDs and a full head SHA"
  exit 1
fi

CURRENT_HEAD_SHA="$(gh api "repos/${SOURCE_REPO}/pulls/${PR_NUMBER}" --jq '.head.sha')" || {
  echo "::error::Could not resolve the current head SHA for PR #${PR_NUMBER}"
  exit 1
}
if [[ "${CURRENT_HEAD_SHA}" != "${EXPECTED_HEAD_SHA}" ]]; then
  echo "::notice::Skipping stale review dispatch for ${EXPECTED_HEAD_SHA}; PR #${PR_NUMBER} is now at ${CURRENT_HEAD_SHA}"
  echo "skip=true" >> "${GITHUB_OUTPUT}"
  exit 0
fi

GATE_NAME="Review dispatch gate #${PR_NUMBER} ${EXPECTED_HEAD_SHA}"
OTHER_RUN_IDS="$(gh api --paginate "repos/${SOURCE_REPO}/actions/runs?status=in_progress&per_page=100" \
  | jq -r --argjson current "${CURRENT_RUN_ID}" '.workflow_runs[] | select(.id != $current) | .id' \
  | sort -u)" || {
  echo "::error::Could not inspect active workflow runs for duplicate review dispatches"
  exit 1
}

while IFS= read -r run_id; do
  [[ -z "${run_id}" ]] && continue
  JOB_NAMES="$(gh api --paginate "repos/${SOURCE_REPO}/actions/runs/${run_id}/jobs?per_page=100" \
    --jq '.jobs[].name')" || {
    echo "::error::Could not inspect jobs for workflow run ${run_id}"
    exit 1
  }
  while IFS= read -r job_name; do
    if [[ "${job_name}" == "${GATE_NAME}" || "${job_name}" == *" / ${GATE_NAME}" ]]; then
      echo "::notice::A review of ${EXPECTED_HEAD_SHA} is already in progress (workflow run ${run_id}); skipping duplicate dispatch"
      echo "skip=true" >> "${GITHUB_OUTPUT}"
      exit 0
    fi
  done <<< "${JOB_NAMES}"
done <<< "${OTHER_RUN_IDS}"

echo "skip=false" >> "${GITHUB_OUTPUT}"
