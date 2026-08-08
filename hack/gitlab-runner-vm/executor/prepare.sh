#!/usr/bin/env bash
# GitLab Runner custom executor — prepare stage.
# Pulls the job image and creates a container for the build.
set -euo pipefail

IMAGE="${CUSTOM_ENV_CI_JOB_IMAGE:-}"
if [ -z "${IMAGE}" ]; then
  echo "ERROR: CUSTOM_ENV_CI_JOB_IMAGE is not set — job must specify an image"
  exit 1
fi

CONTAINER_NAME="runner-${CUSTOM_ENV_CI_JOB_ID}"
BUILDS_DIR="${CUSTOM_ENV_CI_BUILDS_DIR:-/home/fedora/builds}"
CACHE_DIR="${CUSTOM_ENV_CI_CACHE_DIR:-/home/fedora/cache}"
STATE_FILE="/tmp/gitlab-runner-container-${CUSTOM_ENV_CI_JOB_ID}"

echo "Pulling image: ${IMAGE}"
podman pull "${IMAGE}"

mkdir -p "${BUILDS_DIR}" "${CACHE_DIR}"

# --network=host is required so the container can reach the OpenShell gateway
# on 127.0.0.1:17670. The gateway binds to localhost only.
echo "Creating container: ${CONTAINER_NAME}"
podman create \
  --name "${CONTAINER_NAME}" \
  --network=host \
  --memory 4g \
  --pids-limit 4096 \
  --entrypoint "" \
  -v "${BUILDS_DIR}:${BUILDS_DIR}:z" \
  -v "${CACHE_DIR}:${CACHE_DIR}:z" \
  "${IMAGE}" \
  sleep infinity

podman start "${CONTAINER_NAME}"

# CA trust is injected into all containers by the OCI createRuntime hook
# installed by setup.sh (install_ca_hook). No per-container injection needed.

echo "${CONTAINER_NAME}" > "${STATE_FILE}"
echo "Container ${CONTAINER_NAME} started"
