#!/usr/bin/env bash
#
# lib.sh — Shared helpers for GitLab Runner VM provisioning scripts.
#
# Source this file at the top of any script that needs gl_curl(),
# validate_runner_scope(), or build_scope_args().

# Wrap curl with GL_TOKEN passed via a temp config file to avoid
# exposing the token in /proc/<pid>/cmdline.
gl_curl() {
  local config old_umask rc
  old_umask=$(umask)
  umask 077
  config=$(mktemp)
  umask "${old_umask}"
  printf 'header = "PRIVATE-TOKEN: %s"\n' "${GL_TOKEN}" > "${config}"
  rc=0
  curl --max-time 30 --connect-timeout 10 -sf -K "${config}" "$@" || rc=$?
  rm -f "${config}"
  return "${rc}"
}

# Validate and resolve PROJECT_ID / GROUP_ID into RUNNER_SCOPE and SCOPE_ID.
# Exactly one of the two env vars must be set and numeric.
# Returns 1 (with an error message on stderr) when neither is set — the
# caller should print a usage hint and exit. All other validation errors
# exit directly because a usage hint would not help.
# Sets:
#   RUNNER_SCOPE  — "project" or "group"
#   SCOPE_ID      — the numeric GitLab ID
validate_runner_scope() {
  if [ -n "${PROJECT_ID:-}" ] && [ -n "${GROUP_ID:-}" ]; then
    echo "ERROR: PROJECT_ID and GROUP_ID are mutually exclusive — set one, not both" >&2
    exit 1
  fi
  if [ -z "${PROJECT_ID:-}" ] && [ -z "${GROUP_ID:-}" ]; then
    echo "ERROR: one of PROJECT_ID or GROUP_ID is required" >&2
    return 1
  fi
  if [ -n "${PROJECT_ID:-}" ]; then
    if ! [[ "${PROJECT_ID}" =~ ^[0-9]+$ ]]; then
      echo "ERROR: PROJECT_ID must be numeric (got: ${PROJECT_ID})" >&2
      exit 1
    fi
    RUNNER_SCOPE="project"
    # shellcheck disable=SC2034  # consumed by callers
    SCOPE_ID="${PROJECT_ID}"
  else
    if ! [[ "${GROUP_ID}" =~ ^[0-9]+$ ]]; then
      echo "ERROR: GROUP_ID must be numeric (got: ${GROUP_ID})" >&2
      exit 1
    fi
    RUNNER_SCOPE="group"
    # shellcheck disable=SC2034  # consumed by callers
    SCOPE_ID="${GROUP_ID}"
  fi
}

# Build scope-specific curl arguments for the GitLab runner registration API.
# Requires: RUNNER_SCOPE, SCOPE_ID (set by validate_runner_scope)
# Sets:
#   scope_args  — array of --data-urlencode flags for curl
build_scope_args() {
  # shellcheck disable=SC2034  # consumed by callers
  scope_args=()
  if [ "${RUNNER_SCOPE}" = "project" ]; then
    # shellcheck disable=SC2034  # consumed by callers
    scope_args=(
      --data-urlencode "runner_type=project_type"
      --data-urlencode "project_id=${PROJECT_ID}"
      --data-urlencode "locked=true"
    )
  else
    # shellcheck disable=SC2034  # consumed by callers
    scope_args=(
      --data-urlencode "runner_type=group_type"
      --data-urlencode "group_id=${GROUP_ID}"
      --data-urlencode "locked=false"
    )
  fi
}
