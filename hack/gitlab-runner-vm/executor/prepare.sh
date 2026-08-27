#!/usr/bin/env bash
# GitLab Runner custom executor — prepare stage.
# Pulls the job image and creates a container for the build.
set -euo pipefail

IMAGE="${CUSTOM_ENV_CI_JOB_IMAGE:-}"
if [ -z "${IMAGE}" ]; then
  echo "ERROR: CUSTOM_ENV_CI_JOB_IMAGE is not set — job must specify an image"
  exit 1
fi
if [[ "${IMAGE}" == -* ]]; then
  echo "ERROR: CUSTOM_ENV_CI_JOB_IMAGE must not start with a dash (got: ${IMAGE})"
  exit 1
fi

# shellcheck source=job_id.sh
source "$(dirname "${BASH_SOURCE[0]}")/job_id.sh"
JOB_ID=$(resolve_job_id) || {
  echo "ERROR: could not read job id from JOB_RESPONSE_FILE" >&2
  exit 1
}
CONTAINER_NAME="runner-${JOB_ID}"

# CUSTOM_ENV_* values come from the job's own CI/CD variables, so they are
# job-controlled and must be treated as untrusted. Resolve each path before
# comparing: a plain prefix test accepts "${HOME}/builds-evil" and lets
# "${HOME}/builds/../.config/openshell" escape the intended root, and the
# resolved path is what podman bind-mounts (and relabels, via :z).
# Roots are derived from ${HOME} so the executor works on any cloud image
# whose default user is not "fedora".
BUILDS_ROOT="${HOME}/builds"
CACHE_ROOT="${HOME}/cache"

require_under_root() {
  local name="$1" root="$2" value="$3" resolved resolved_root
  # Resolve both sides: comparing a resolved value against an unresolved root
  # rejects legitimate paths whenever ${HOME} itself traverses a symlink.
  resolved_root=$(realpath -m -- "${root}") || {
    echo "ERROR: ${name} root could not be resolved (${root})" >&2
    exit 1
  }
  resolved=$(realpath -m -- "${value}") || {
    echo "ERROR: ${name} could not be resolved (got: ${value})" >&2
    exit 1
  }
  if [[ "${resolved}" != "${resolved_root}" && "${resolved}" != "${resolved_root}"/* ]]; then
    echo "ERROR: ${name} must be under ${resolved_root} (got: ${value} -> ${resolved})" >&2
    exit 1
  fi
  # ':' and ',' are field separators in podman's -v spec; a path containing
  # them would produce an opaque volume-parse error after the image pull.
  if [[ "${resolved}" == *[:,]* || "${resolved}" == *[[:cntrl:]]* ]]; then
    echo "ERROR: ${name} must not contain ':' ',' or control characters (got: ${resolved})" >&2
    exit 1
  fi
  printf '%s' "${resolved}"
}

BUILDS_DIR=$(require_under_root BUILDS_DIR "${BUILDS_ROOT}" \
  "${CUSTOM_ENV_CI_BUILDS_DIR:-${BUILDS_ROOT}}")
CACHE_DIR=$(require_under_root CACHE_DIR "${CACHE_ROOT}" \
  "${CUSTOM_ENV_CI_CACHE_DIR:-${CACHE_ROOT}}")
STATE_DIR="${HOME}/.local/state/gitlab-runner"
mkdir -p "${STATE_DIR}"
STATE_FILE="${STATE_DIR}/container-${JOB_ID}"

echo "Pulling image: ${IMAGE}"
podman pull -- "${IMAGE}"

mkdir -p "${BUILDS_DIR}" "${CACHE_DIR}"

# --network=host is required so the container can reach the OpenShell gateway.
# The host CA trust bundle is injected into containers via the OCI createRuntime
# hook (install_ca_hook in setup.sh). Gateway mTLS credentials are mounted
# read-only from the runner user's OpenShell config.
OPENSHELL_CONFIG="${HOME}/.config/openshell"

# Job ids are unique per runner, so a container of this name can only be a
# stopped leftover from an earlier failed stage. Use plain `podman rm` (no -f):
# it refuses a running container atomically, with no inspect/rm window.
if podman container exists "${CONTAINER_NAME}" 2>/dev/null; then
  if ! rm_err=$(podman rm "${CONTAINER_NAME}" 2>&1); then
    if podman inspect --format '{{.State.Running}}' "${CONTAINER_NAME}" 2>/dev/null | grep -q true; then
      echo "ERROR: container ${CONTAINER_NAME} exists and is running — refusing to reuse it" >&2
    else
      echo "ERROR: could not remove stale container ${CONTAINER_NAME}: ${rm_err}" >&2
    fi
    exit 1
  fi
fi

OPENSHELL_MOUNT=()
if [ -d "${OPENSHELL_CONFIG}" ]; then
  OPENSHELL_STAGING="${STATE_DIR}/openshell-${JOB_ID}"
  rm -rf "${OPENSHELL_STAGING}"
  cp -a "${OPENSHELL_CONFIG}" "${OPENSHELL_STAGING}"
  OPENSHELL_MOUNT=(-v "${OPENSHELL_STAGING}:/root/.config/openshell:ro,z")
fi

# Bind-mount the host's gitlab-runner binary into the container so that
# artifact upload/download and cache stages work with the custom executor.
# Without this, GitLab Runner's generated upload_artifacts_on_success script
# checks for `gitlab-runner --version` inside the container, fails to find it,
# and silently disables artifact upload — breaking downstream jobs that depend
# on artifacts.
GITLAB_RUNNER_MOUNT=()
if [ -x /usr/local/bin/gitlab-runner ]; then
  GITLAB_RUNNER_MOUNT=(-v /usr/local/bin/gitlab-runner:/usr/local/bin/gitlab-runner:ro)
fi

echo "Creating container: ${CONTAINER_NAME}"
# Record the name before creating it: if prepare.sh dies between create and
# the write, cleanup.sh has no way to find the container and it leaks.
echo "${CONTAINER_NAME}" > "${STATE_FILE}"
podman create \
  --name "${CONTAINER_NAME}" \
  --network=host \
  --pids-limit 4096 \
  --cap-drop=ALL \
  --security-opt=no-new-privileges \
  --env XDG_CACHE_HOME=/tmp/cache \
  --entrypoint "" \
  -v "${BUILDS_DIR}:${BUILDS_DIR}:z" \
  -v "${CACHE_DIR}:${CACHE_DIR}:z" \
  "${OPENSHELL_MOUNT[@]}" \
  "${GITLAB_RUNNER_MOUNT[@]}" \
  -- "${IMAGE}" \
  sleep infinity

podman start "${CONTAINER_NAME}"

# Host CA trust is injected into all containers by the OCI createRuntime hook
# installed by setup.sh (install_ca_hook). No per-container CA injection needed.

echo "Container ${CONTAINER_NAME} started"
