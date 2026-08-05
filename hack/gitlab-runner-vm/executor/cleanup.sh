#!/usr/bin/env bash
# GitLab Runner custom executor — cleanup stage.
# Stops and removes the job container. Always succeeds.
set -uo pipefail

STATE_FILE="/tmp/gitlab-runner-container-${CUSTOM_ENV_CI_JOB_ID}"

if [ -f "${STATE_FILE}" ]; then
  CONTAINER_NAME=$(cat "${STATE_FILE}")
  echo "Cleaning up container: ${CONTAINER_NAME}"
  podman stop --time 10 "${CONTAINER_NAME}" 2>/dev/null || true
  podman rm -f "${CONTAINER_NAME}" 2>/dev/null || true
  rm -f "${STATE_FILE}"
fi
