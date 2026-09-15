#!/usr/bin/env bash
# GitLab Runner custom executor — run stage.
# Executes each build script inside the container, forwarding CI env vars.
# Idempotent: safe to re-run and must stay that way.
set -uo pipefail

SCRIPT_PATH="${1:-}"
if [ -z "${SCRIPT_PATH}" ]; then
  echo "ERROR: no script path provided" >&2
  exit "${SYSTEM_FAILURE_EXIT_CODE}"
fi
# shellcheck source=job_id.sh
source "$(dirname "${BASH_SOURCE[0]}")/job_id.sh"
JOB_ID=$(resolve_job_id) || {
  echo "ERROR: could not read job id from JOB_RESPONSE_FILE" >&2
  exit "${SYSTEM_FAILURE_EXIT_CODE}"
}
STATE_FILE="${HOME}/.local/state/gitlab-runner/container-${JOB_ID}"

if [ ! -f "${STATE_FILE}" ]; then
  echo "ERROR: container state file not found — prepare stage may have failed" >&2
  exit "${SYSTEM_FAILURE_EXIT_CODE}"
fi

CONTAINER_NAME=$(cat "${STATE_FILE}") || exit "${SYSTEM_FAILURE_EXIT_CODE}"
if ! [[ "${CONTAINER_NAME}" =~ ^runner-[0-9]+$ ]]; then
  echo "ERROR: container state file holds an unexpected name (got: ${CONTAINER_NAME})" >&2
  exit "${SYSTEM_FAILURE_EXIT_CODE}"
fi

# Forward CI environment variables into the container.
# GitLab Runner exposes job variables as CUSTOM_ENV_* — strip the prefix and
# forward each one with `--env NAME` (no value). Podman then copies the value
# from its own environment, so secrets never appear in argv or on disk. A
# line-delimited --env-file cannot carry these safely: file-type CI/CD
# variables (PEM material, keys) contain newlines, which an `env`-parsing loop
# truncates to the first line, and a continuation line beginning with
# CUSTOM_ENV_ would be re-parsed as an attacker-chosen assignment.
# The exports happen in a subshell that execs podman, so a job variable
# named PATH or HOME cannot alter this script's own environment. Names that
# would change how the podman *process itself* behaves (locating conmon and
# the OCI runtime, its storage/config, its home) are never secrets, so those
# few are passed inline as NAME=VALUE instead of through podman's environ.
PODMAN_BIN=$(command -v podman) || exit "${SYSTEM_FAILURE_EXIT_CODE}"
is_process_critical() {
  case "$1" in
    PATH|HOME|TMPDIR|XDG_RUNTIME_DIR|XDG_CONFIG_HOME|XDG_DATA_HOME|\
    LD_PRELOAD|LD_LIBRARY_PATH|CONTAINERS_CONF|CONTAINERS_CONF_OVERRIDE|\
    CONTAINERS_STORAGE_CONF|CONTAINERS_REGISTRIES_CONF|CONTAINER_HOST|\
    CONTAINER_CONNECTION|CONTAINER_SSHKEY|DOCKER_HOST) return 0 ;;
    *) return 1 ;;
  esac
}
ENV_ARGS=()
while IFS= read -r name; do
  [ -n "${name}" ] || continue
  short="${name#CUSTOM_ENV_}"
  if is_process_critical "${short}"; then
    ENV_ARGS+=(--env "${short}=${!name}")
  else
    ENV_ARGS+=(--env "${short}")
  fi
done < <(compgen -v | grep '^CUSTOM_ENV_')

# run_in_container <podman exec args...>: exec podman with the job's variables
# exported under their short names.
run_in_container() {
  (
    while IFS= read -r name; do
      [ -n "${name}" ] || continue
      short="${name#CUSTOM_ENV_}"
      is_process_critical "${short}" && continue
      # Readonly shell names (UID, EUID, SHELLOPTS, ...) cannot be exported;
      # a CI variable colliding with one is dropped rather than aborting.
      export "${short}=${!name}" 2>/dev/null \
        || echo "WARN: cannot forward job variable ${short}" >&2
    done < <(compgen -v | grep '^CUSTOM_ENV_')
    exec "${PODMAN_BIN}" exec "$@"
  )
}

# The script lives on the host — copy it into the container before executing.
BUILD_SCRIPT_DIR=$(dirname "${SCRIPT_PATH}")
podman exec "${CONTAINER_NAME}" mkdir -p "${BUILD_SCRIPT_DIR}" || exit "${SYSTEM_FAILURE_EXIT_CODE}"
podman cp "${SCRIPT_PATH}" "${CONTAINER_NAME}:${SCRIPT_PATH}" || exit "${SYSTEM_FAILURE_EXIT_CODE}"

# Translate exit codes per the custom-executor contract.
# podman reserves 125 for its own failures. 126/127 are NOT distinguishable
# from the job: bash returns 127 for a command it cannot find and 126 for one
# it cannot execute inside the job script, and podman propagates that verbatim,
# so those stay build failures. 125 is ambiguous too (the script may exit
# 125), so fall back to container liveness there.
run_in_container \
  "${ENV_ARGS[@]}" \
  "${CONTAINER_NAME}" \
  bash -- "${SCRIPT_PATH}"; rc=$?
# Report the script's real status so `allow_failure:exit_codes` works. The
# runner requires a bare integer here and treats anything else as an unknown
# failure, so write nothing when the code did not come from the script.
write_build_exit_code() {
  [ -n "${BUILD_EXIT_CODE_FILE:-}" ] && printf '%s' "$1" > "${BUILD_EXIT_CODE_FILE}"
}
case $rc in
  0)   ;;
  125)
    if podman inspect --format '{{.State.Running}}' "${CONTAINER_NAME}" 2>/dev/null | grep -q true; then
      write_build_exit_code "$rc"
      exit "${BUILD_FAILURE_EXIT_CODE}"
    fi
    exit "${SYSTEM_FAILURE_EXIT_CODE}"
    ;;
  *)
    write_build_exit_code "$rc"
    exit "${BUILD_FAILURE_EXIT_CODE}"
    ;;
esac
