#!/usr/bin/env bash
# GitLab Runner custom executor — cleanup stage.
# Stops and removes the job container and the per-job OpenShell gateway.
# Always succeeds.
# -e intentionally omitted — cleanup must not abort on individual failures.
set -uo pipefail

# shellcheck source=job_id.sh
source "$(dirname "${BASH_SOURCE[0]}")/job_id.sh"
# shellcheck source=gateway.sh
source "$(dirname "${BASH_SOURCE[0]}")/gateway.sh"

JOB_ID=$(resolve_job_id) || JOB_ID=""
STATE_DIR="${HOME}/.local/state/gitlab-runner"

if [ -n "${JOB_ID}" ]; then
  STATE_FILE="${STATE_DIR}/container-${JOB_ID}"

  if [ -f "${STATE_FILE}" ]; then
    CONTAINER_NAME=$(cat "${STATE_FILE}")
    if [[ "${CONTAINER_NAME}" =~ ^runner-[0-9]+$ ]]; then
      echo "Cleaning up container: ${CONTAINER_NAME}"
      podman stop --time 10 "${CONTAINER_NAME}" 2>/dev/null || true
      podman rm -f "${CONTAINER_NAME}" 2>/dev/null || true
    else
      # Skip only the podman calls — the staging copy of the gateway mTLS
      # material below must still be removed.
      echo "WARN: state file holds an unexpected container name (${CONTAINER_NAME}) — not touching podman"
    fi
    rm -f "${STATE_FILE}"
  fi

  OPENSHELL_STAGING="${STATE_DIR}/openshell-${JOB_ID}"
  rm -rf "${OPENSHELL_STAGING}" 2>/dev/null || true
else
  echo "WARN: could not read job id from JOB_RESPONSE_FILE — skipping job container cleanup"
fi

# Always tear the per-job gateway down (killing any sandboxes it still holds),
# even when the job id is missing. A leftover is also reaped by the next
# job's prepare.sh.
teardown_openshell_gateway || true
