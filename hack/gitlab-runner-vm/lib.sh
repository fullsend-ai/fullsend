#!/usr/bin/env bash
#
# lib.sh — Shared helpers for GitLab Runner VM provisioning scripts.
#
# Source this file at the top of any script that needs gl_curl(),
# validate_runner_scope(), build_scope_args(), uses_runner_token(),
# or drain_runner_vm().

# uses_runner_token reports whether RUNNER_TOKEN is set, selecting the
# join-existing-pool (runner-hub) path over GitLab API registration.
uses_runner_token() {
  [ -n "${RUNNER_TOKEN:-}" ]
}

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

# drain_runner_vm <remote_exec_fn>
#
# Gracefully stop the GitLab runner on a VM so it stops accepting new jobs
# and in-flight jobs can finish, then return. Policy is drain-then-proceed:
# operational failures and cap overruns warn and return 0 so the caller can
# delete or recreate the VM instead of hanging forever.
#
# remote_exec_fn is the name of a function that takes a single command string
# and runs it on the VM over the caller's SSH plumbing (virtctl ssh, or
# gcloud compute ssh --tunnel-through-iap). stdout of the remote command is
# captured; the function must put diagnostics on stderr.
#
# Idle is detected by watching per-job runner-* containers (podman ps), not
# process exit: gitlab-runner's shutdown_timeout=0 only waits ~30s, so the
# process can exit while a job container is still running.
#
# The remote side sends SIGQUIT (not systemctl stop, which trips Fedora's
# 45s TimeoutStopUSec abort) and runtime-masks the unit so Restart=always
# does not bring the runner back mid-drain. shutdown_timeout is raised to
# the drain cap so SIGQUIT does not kill the job at the ~30s default.
#
# Env:
#   DRAIN_TIMEOUT_SEC — cap in seconds (default 600). Non-numeric values
#                       fall back to 600.
#   DRAIN_POLL_SEC    — poll interval in seconds (default 5). Used by tests.
drain_runner_vm() {
  local remote_exec="$1"
  local timeout_sec poll_sec start now elapsed remaining rc containers
  local remote_cmd

  if test -z "${remote_exec}"; then
    echo "ERROR: drain_runner_vm requires a remote exec function" >&2
    return 1
  fi

  timeout_sec="${DRAIN_TIMEOUT_SEC:-600}"
  if ! test "${timeout_sec}" -ge 0 2>/dev/null; then
    timeout_sec=600
  fi
  poll_sec="${DRAIN_POLL_SEC:-5}"
  if ! test "${poll_sec}" -ge 1 2>/dev/null; then
    poll_sec=5
  fi

  echo "  draining runner (cap ${timeout_sec}s)..."

  # Expand timeout_sec into the remote script; everything else is literal.
  remote_cmd=$(cat <<EOF
cfg=/etc/gitlab-runner/config.toml
if test -f "\$cfg"; then
  if grep -q '^shutdown_timeout' "\$cfg"; then
    sed -i 's/^shutdown_timeout.*/shutdown_timeout = ${timeout_sec}/' "\$cfg" || true
  else
    echo "shutdown_timeout = ${timeout_sec}" >> "\$cfg" || true
  fi
fi
sudo systemctl kill --kill-whom=main -s HUP gitlab-runner.service >/dev/null 2>&1 || true
sudo systemctl mask --runtime gitlab-runner.service >/dev/null 2>&1 || true
sudo systemctl kill --kill-whom=main -s QUIT gitlab-runner.service >/dev/null 2>&1 || true
true
EOF
)

  rc=0
  "${remote_exec}" "${remote_cmd}" || rc=$?
  if test "${rc}" -ne 0; then
    echo "  WARN: drain signal failed (exit ${rc}) — proceeding" >&2
    return 0
  fi

  start=$(date +%s)
  while true; do
    now=$(date +%s)
    elapsed=$(( now - start ))
    if test "${elapsed}" -ge "${timeout_sec}"; then
      echo "  WARN: drain cap ${timeout_sec}s reached — proceeding" >&2
      return 0
    fi

    rc=0
    containers=$("${remote_exec}" "podman ps --format '{{.Names}}'" 2>/dev/null) || rc=$?
    if test "${rc}" -ne 0; then
      echo "  WARN: drain poll failed (exit ${rc}) — proceeding" >&2
      return 0
    fi
    containers=$(printf '%s\n' "${containers}" | grep '^runner-' || true)
    if test -z "${containers}"; then
      echo "  OK: runner idle"
      return 0
    fi

    remaining=$(( timeout_sec - elapsed ))
    if test "${remaining}" -le 0; then
      echo "  WARN: drain cap ${timeout_sec}s reached — proceeding" >&2
      return 0
    fi
    if test "${remaining}" -lt "${poll_sec}"; then
      sleep "${remaining}"
    else
      sleep "${poll_sec}"
    fi
  done
}
