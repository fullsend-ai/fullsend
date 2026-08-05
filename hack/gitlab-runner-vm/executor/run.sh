#!/usr/bin/env bash
# GitLab Runner custom executor — run stage.
# Executes each build script inside the container, forwarding CI env vars.
set -euo pipefail

SCRIPT_PATH="$1"
STATE_FILE="/tmp/gitlab-runner-container-${CUSTOM_ENV_CI_JOB_ID}"

if [ ! -f "${STATE_FILE}" ]; then
  echo "ERROR: container state file not found — prepare stage may have failed"
  exit 1
fi

CONTAINER_NAME=$(cat "${STATE_FILE}")

# Forward CI environment variables into the container.
# GitLab Runner exposes job variables as CUSTOM_ENV_* — strip the prefix
# and write to a temp file for --env-file.
# Limitation: multiline CI variable values are not supported (env output
# is line-delimited; GitLab Runner's CUSTOM_ENV_* are single-line in practice).
ENV_FILE=$(mktemp "/tmp/gitlab-runner-env-${CUSTOM_ENV_CI_JOB_ID}-XXXXXX")
trap 'rm -f "${ENV_FILE}"' EXIT

while IFS='=' read -r key value; do
  printf '%s=%s\n' "${key#CUSTOM_ENV_}" "${value}"
done < <(env | grep '^CUSTOM_ENV_') > "${ENV_FILE}"

# The script lives on the host — copy it into the container before executing.
SCRIPT_DIR=$(dirname "${SCRIPT_PATH}")
podman exec "${CONTAINER_NAME}" mkdir -p "${SCRIPT_DIR}"
podman cp "${SCRIPT_PATH}" "${CONTAINER_NAME}:${SCRIPT_PATH}"

podman exec \
  --env-file "${ENV_FILE}" \
  "${CONTAINER_NAME}" \
  bash "${SCRIPT_PATH}"
