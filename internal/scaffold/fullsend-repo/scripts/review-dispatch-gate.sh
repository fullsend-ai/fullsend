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
if [[ ! "${CURRENT_HEAD_SHA}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "::error::GitHub returned an invalid head SHA for PR #${PR_NUMBER}"
  exit 1
fi
if [[ "${CURRENT_HEAD_SHA}" != "${EXPECTED_HEAD_SHA}" ]]; then
  echo "::notice::Skipping stale review dispatch for ${EXPECTED_HEAD_SHA}; PR #${PR_NUMBER} is now at ${CURRENT_HEAD_SHA}"
  echo "skip=true" >> "${GITHUB_OUTPUT}"
  exit 0
fi

GATE_NAME="Review dispatch gate #${PR_NUMBER} ${EXPECTED_HEAD_SHA}"
RUN_IDS=()
for status in in_progress queued pending; do
  STATUS_RUN_IDS="$(gh api --paginate "repos/${SOURCE_REPO}/actions/runs?status=${status}&per_page=100" \
    | jq -r --argjson current "${CURRENT_RUN_ID}" '.workflow_runs[] | select(.id != $current) | .id')" || {
    echo "::error::Could not inspect ${status} workflow runs for duplicate review dispatches"
    exit 1
  }
  while IFS= read -r run_id; do
    [[ -n "${run_id}" ]] && RUN_IDS+=("${run_id}")
  done <<< "${STATUS_RUN_IDS}"
done
OTHER_RUN_IDS="$(printf '%s\n' "${RUN_IDS[@]}" | sort -u)"

while IFS= read -r run_id; do
  [[ -z "${run_id}" ]] && continue
  JOBS="$(gh api --paginate "repos/${SOURCE_REPO}/actions/runs/${run_id}/jobs?per_page=100" \
    --jq '.jobs[] | [.name, .status] | @tsv')" || {
    echo "::error::Could not inspect jobs for workflow run ${run_id}"
    exit 1
  }
  matching_gate=false
  active_review=false
  while IFS=$'\t' read -r job_name job_status; do
    if [[ "${job_name}" == "${GATE_NAME}" || "${job_name}" == *" / ${GATE_NAME}" ]]; then
      matching_gate=true
    fi
    if [[ ( "${job_name}" == "Review" || "${job_name}" == *" / Review" ) \
      && ( "${job_status}" == "pending" || "${job_status}" == "queued" || "${job_status}" == "in_progress" ) ]]; then
      active_review=true
    fi
  done <<< "${JOBS}"
  if [[ "${matching_gate}" == "true" && "${active_review}" == "true" ]]; then
    echo "::notice::A review of ${EXPECTED_HEAD_SHA} is already pending, queued or in progress (workflow run ${run_id}); skipping duplicate dispatch"
    echo "skip=true" >> "${GITHUB_OUTPUT}"
    echo "duplicate_run_id=${run_id}" >> "${GITHUB_OUTPUT}"
    exit 0
  fi
done <<< "${OTHER_RUN_IDS}"

echo "skip=false" >> "${GITHUB_OUTPUT}"
echo "duplicate_run_id=" >> "${GITHUB_OUTPUT}"
