#!/usr/bin/env bash
# Apply routine Fedora package updates on a GitLab runner VM without
# interrupting jobs (issue #8963).
#
# This script is the ExecStart of the fullsend-os-update.service system unit
# that setup.sh installs. Its daily timer fires at a per-VM time picked at
# random once and persisted, so the fleet does not drain in lockstep. A second
# unit, fullsend-os-update-resume.service, runs `os-update.sh --resume` at boot
# to finish the post-reboot health check.
#
# Flow (timer run):
#   1. `dnf check-upgrade`: 0 = nothing to do (runner untouched), 100 = updates
#      available, anything else = error reported, runner untouched (DNF5
#      check-upgrade exit codes).
#   2. If a job is running (an executor stage is a child of gitlab-runner, or
#      a runner-* container is not stopped), wait and retry every
#      FULLSEND_OS_UPDATE_RETRY_SEC. After FULLSEND_OS_UPDATE_DEFER_MAX_SEC the
#      update is deferred to the next scheduled run. The runner keeps taking
#      jobs while deferring; nothing is drained.
#   3. Drain: create the hold file (gitlab-runner.service has a drop-in with
#      ConditionPathExists=!<hold file>, so Restart=always cannot bring it
#      back), raise shutdown_timeout, then SIGQUIT gitlab-runner — the same
#      graceful stop as lib.sh's drain_runner_vm, never `systemctl stop`
#      (which trips the unit's stop timeout and kills the job). A job that
#      raced the idle check finishes; if the drain outlasts
#      FULLSEND_OS_UPDATE_DRAIN_TIMEOUT_SEC the hold is released, the job is
#      left alone, and the update is deferred. shutdown_timeout is raised to
#      DRAIN_TIMEOUT_SEC + FULLSEND_OS_UPDATE_JOB_MAX_SEC (default 24h) so the
#      graceful shutdown already under way does not kill that job before it
#      ends; set JOB_MAX_SEC to at least the longest job timeout.
#   4. `dnf upgrade` within the installed release. This never passes
#      --releasever or runs system-upgrade: a Fedora major-version upgrade is
#      a separate lifecycle change (recreate the VM).
#   5. If `dnf needs-restarting -r` says a reboot is needed (or cannot tell),
#      reboot; the hold file keeps gitlab-runner from starting at boot until
#      fullsend-os-update-resume.service verifies it.
#   6. Release: check podman works as the runner user and `gitlab-runner
#      verify` confirms the runner token is valid with GitLab, then remove the
#      hold file and start gitlab-runner. If the checks fail, the runner stays
#      drained and the unit fails so the error shows in `systemctl --failed`.
#
# A gitlab-runner service that is not running when the timer fires is left
# alone (an operator may have stopped it on purpose).
#
# Idempotent: safe to re-run. A held runner from an interrupted run is
# verified and released before the next update check.
set -euo pipefail

RUNNER_USER="${FULLSEND_RUNNER_USER:-}"
RUNNER_SERVICE="gitlab-runner.service"
CONFIG_TOML="${FULLSEND_GITLAB_RUNNER_CONFIG:-/etc/gitlab-runner/config.toml}"
STATE_DIR="${FULLSEND_OS_UPDATE_STATE_DIR:-/var/lib/fullsend-gitlab-runner}"
# Must match the ConditionPathExists= drop-in setup.sh writes for gitlab-runner.
HOLD_FILE="${FULLSEND_OS_UPDATE_HOLD_FILE:-${STATE_DIR}/os-update.hold}"
SHUTDOWN_TIMEOUT_ORIG_FILE="${STATE_DIR}/os-update.shutdown-timeout.orig"
LOCK_FILE="${FULLSEND_OS_UPDATE_LOCK_FILE:-/run/fullsend-os-update.lock}"
RETRY_SEC="${FULLSEND_OS_UPDATE_RETRY_SEC:-600}"
DEFER_MAX_SEC="${FULLSEND_OS_UPDATE_DEFER_MAX_SEC:-21600}"
DRAIN_TIMEOUT_SEC="${FULLSEND_OS_UPDATE_DRAIN_TIMEOUT_SEC:-21600}"
POLL_SEC="${FULLSEND_OS_UPDATE_POLL_SEC:-10}"
RELOAD_SETTLE_SEC="${FULLSEND_OS_UPDATE_RELOAD_SETTLE_SEC:-2}"
VERIFY_ATTEMPTS="${FULLSEND_OS_UPDATE_VERIFY_ATTEMPTS:-10}"
VERIFY_RETRY_SEC="${FULLSEND_OS_UPDATE_VERIFY_RETRY_SEC:-30}"
START_SETTLE_SEC="${FULLSEND_OS_UPDATE_START_SETTLE_SEC:-5}"
# Longest job the update must not cut off. shutdown_timeout is raised to
# DRAIN_TIMEOUT_SEC + JOB_MAX_SEC; keep it at or above the longest job timeout
# the runner can be given.
JOB_MAX_SEC="${FULLSEND_OS_UPDATE_JOB_MAX_SEC:-86400}"
# Matches a top-level shutdown_timeout key, indented or not (an indented key
# is still valid TOML, so a column-0 match would leave a duplicate key).
SHUTDOWN_TIMEOUT_KEY_RE='^[[:space:]]*shutdown_timeout[[:space:]]*='

log() { echo "==> $*"; }
warn() { echo "WARN: $*" >&2; }
err() { echo "ERROR: $*" >&2; }

for _var in RETRY_SEC DEFER_MAX_SEC DRAIN_TIMEOUT_SEC JOB_MAX_SEC POLL_SEC RELOAD_SETTLE_SEC \
  VERIFY_ATTEMPTS VERIFY_RETRY_SEC START_SETTLE_SEC; do
  if ! [[ "${!_var}" =~ ^[0-9]+$ ]]; then
    err "${_var} must be a non-negative integer (got: ${!_var})"
    exit 1
  fi
done

if [ -z "${RUNNER_USER}" ] || ! [[ "${RUNNER_USER}" =~ ^[a-z_][a-z0-9_-]*$ ]]; then
  err "FULLSEND_RUNNER_USER must be the gitlab-runner service user (got: '${RUNNER_USER}')"
  exit 1
fi
if ! RUNNER_UID=$(id -u "${RUNNER_USER}" 2>/dev/null); then
  err "cannot resolve UID for ${RUNNER_USER}"
  exit 1
fi

# Run a command as the runner user with its rootless Podman session.
as_runner() {
  runuser -u "${RUNNER_USER}" -- env \
    XDG_RUNTIME_DIR="/run/user/${RUNNER_UID}" \
    DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/${RUNNER_UID}/bus" \
    "$@"
}

runner_state() {
  systemctl is-active "${RUNNER_SERVICE}" 2>/dev/null || true
}

# Prints running runner-* job containers. Fails when podman cannot be queried.
running_job_containers() {
  local names
  names=$(as_runner podman ps --format '{{.Names}}') || return 1
  printf '%s\n' "${names}" | grep '^runner-' || true
}

# True when a job is running. Custom-executor stages (prepare/run/cleanup) run
# as children of the gitlab-runner main process, which also covers a job still
# pulling its image before its runner-* container exists. A podman failure
# counts as busy: an update must never start on a guess.
runner_busy() {
  local main_pid containers
  main_pid=$(systemctl show -p MainPID --value "${RUNNER_SERVICE}" 2>/dev/null || true)
  if [ -n "${main_pid}" ] && [ "${main_pid}" != "0" ] && pgrep -P "${main_pid}" >/dev/null 2>&1; then
    log "runner busy: executor stage running under gitlab-runner (pid ${main_pid})"
    return 0
  fi
  if ! containers=$(running_job_containers); then
    log "runner busy: cannot list job containers with podman — treating as busy"
    return 0
  fi
  if [ -n "${containers}" ]; then
    log "runner busy: job container(s) running: $(printf '%s' "${containers}" | tr '\n' ' ')"
    return 0
  fi
  return 1
}

# Replace config.toml atomically: build the new file next to it, copy the
# original owner and mode (gitlab-runner runs as RUNNER_USER and must keep
# reading it), then mv it into place. A failed write (for example a full root
# filesystem) never touches the live config, which holds the runner
# registration.
# Usage: write_shutdown_timeout <line or empty to drop the key>
write_shutdown_timeout() {
  local line="$1" tmp
  tmp=$(mktemp "${CONFIG_TOML}.XXXXXX") || return 1
  # grep exits 1 when nothing is left to print (fine); 2 or more is an error.
  if ! {
    if [ -n "${line}" ]; then
      printf '%s\n' "${line}"
    fi
    grep -Ev "${SHUTDOWN_TIMEOUT_KEY_RE}" "${CONFIG_TOML}" || [ $? -eq 1 ]
  } > "${tmp}"; then
    rm -f "${tmp}"
    return 1
  fi
  if ! chmod --reference="${CONFIG_TOML}" "${tmp}" \
    || ! chown --reference="${CONFIG_TOML}" "${tmp}" \
    || ! mv -f "${tmp}" "${CONFIG_TOML}"; then
    rm -f "${tmp}"
    return 1
  fi
}

# shutdown_timeout bounds how long gitlab-runner waits for jobs after SIGQUIT
# (lib.sh's drain_runner_vm relies on the same). Once SIGQUIT is sent that
# deadline cannot be cancelled: releasing the hold and starting the unit does
# nothing to a runner that is still shutting down. So raise it past the drain
# cap by JOB_MAX_SEC, the longest job we must not cut off: a job that raced
# the idle check and is still running when the drain gives up and defers is
# left alone for that long. The original value is saved once, so a deferred
# drain that leaves the raised value in place is still restored by a later
# run.
raise_shutdown_timeout() {
  local orig grep_rc=0
  [ -f "${CONFIG_TOML}" ] || return 0
  if [ ! -f "${SHUTDOWN_TIMEOUT_ORIG_FILE}" ]; then
    # grep exits 1 when no shutdown_timeout is set (saved as an empty line).
    orig=$(grep -E -m1 "${SHUTDOWN_TIMEOUT_KEY_RE}" "${CONFIG_TOML}") || grep_rc=$?
    if [ "${grep_rc}" -gt 1 ]; then
      return 1
    fi
    if ! printf '%s\n' "${orig}" > "${SHUTDOWN_TIMEOUT_ORIG_FILE}"; then
      rm -f "${SHUTDOWN_TIMEOUT_ORIG_FILE}"
      return 1
    fi
  fi
  write_shutdown_timeout "shutdown_timeout = $((DRAIN_TIMEOUT_SEC + JOB_MAX_SEC))"
}

restore_shutdown_timeout() {
  [ -f "${SHUTDOWN_TIMEOUT_ORIG_FILE}" ] || return 0
  if [ -f "${CONFIG_TOML}" ]; then
    write_shutdown_timeout "$(head -n1 "${SHUTDOWN_TIMEOUT_ORIG_FILE}")"
  fi
  rm -f "${SHUTDOWN_TIMEOUT_ORIG_FILE}"
}

# Stop gitlab-runner taking new jobs and wait for any job to finish.
# Returns 0 when drained, 1 when the drain cap was reached (job left running),
# 2 when a preparation step failed (SIGQUIT was never sent). The caller runs
# this under `if`/`||`, where errexit is off, so every step is checked here.
drain_runner() {
  local start now state containers
  log "draining gitlab-runner (cap ${DRAIN_TIMEOUT_SEC}s)"
  if ! mkdir -p "$(dirname "${HOLD_FILE}")" || ! : > "${HOLD_FILE}"; then
    err "could not create the hold file ${HOLD_FILE} — not draining"
    return 2
  fi
  if ! raise_shutdown_timeout; then
    err "could not raise shutdown_timeout in ${CONFIG_TOML} — not draining"
    return 2
  fi
  # HUP reloads config.toml so the raised shutdown_timeout is in effect before
  # SIGQUIT starts the graceful shutdown. Without the reload a running job
  # could be killed at the old timeout, so a failed HUP aborts the drain.
  if ! systemctl kill --kill-whom=main -s HUP "${RUNNER_SERVICE}" >/dev/null 2>&1; then
    err "could not signal ${RUNNER_SERVICE} to reload its config — not draining"
    return 2
  fi
  sleep "${RELOAD_SETTLE_SEC}"
  if ! systemctl kill --kill-whom=main -s QUIT "${RUNNER_SERVICE}" >/dev/null 2>&1; then
    warn "could not signal ${RUNNER_SERVICE} — waiting for it to stop anyway"
  fi

  start=$(date +%s)
  while true; do
    state=$(runner_state)
    # Only an observed inactive/failed counts as stopped; an empty or
    # unrecognised state (a failed query) is unknown, so keep waiting.
    case "${state}" in
      inactive|failed)
        if containers=$(running_job_containers) && [ -z "${containers}" ]; then
          log "runner drained"
          return 0
        fi
        ;;
    esac
    now=$(date +%s)
    if [ $((now - start)) -ge "${DRAIN_TIMEOUT_SEC}" ]; then
      return 1
    fi
    sleep "${POLL_SEC}"
  done
}

health_check() {
  local out
  if [ ! -f "${CONFIG_TOML}" ]; then
    echo "missing ${CONFIG_TOML}"
    return 1
  fi
  if ! as_runner podman info >/dev/null 2>&1; then
    echo "rootless podman is not usable as ${RUNNER_USER}"
    return 1
  fi
  if ! out=$(as_runner gitlab-runner verify --config "${CONFIG_TOML}" 2>&1); then
    echo "gitlab-runner verify failed: $(printf '%s' "${out}" | tail -n 3 | tr '\n' ' ')"
    return 1
  fi
  if ! printf '%s\n' "${out}" | grep -qi 'is valid'; then
    echo "GitLab did not confirm the runner is valid: $(printf '%s' "${out}" | tail -n 3 | tr '\n' ' ')"
    return 1
  fi
  return 0
}

restore_hold() {
  if ! : > "${HOLD_FILE}"; then
    err "could not recreate the hold file ${HOLD_FILE}"
    return 0
  fi
  err "hold file restored; run: sudo FULLSEND_RUNNER_USER=${RUNNER_USER} $0 --resume (or wait for the next scheduled run) to retry"
}

# Verify the runner, then let it take jobs again. On failure the hold file
# stays (or is restored), so gitlab-runner stays stopped (including across
# reboots) until a later run retries.
release_runner() {
  local attempt=1 reason
  [ -f "${HOLD_FILE}" ] || return 0
  log "verifying runner health before releasing the hold"
  while ! reason=$(health_check); do
    if [ "${attempt}" -ge "${VERIFY_ATTEMPTS}" ]; then
      err "runner health check failed after ${attempt} attempt(s): ${reason}"
      err "gitlab-runner left drained (hold file ${HOLD_FILE}). Fix the cause, then run: sudo FULLSEND_RUNNER_USER=${RUNNER_USER} $0 --resume"
      return 1
    fi
    warn "runner health check failed (attempt ${attempt}/${VERIFY_ATTEMPTS}): ${reason} — retrying in ${VERIFY_RETRY_SEC}s"
    attempt=$((attempt + 1))
    sleep "${VERIFY_RETRY_SEC}"
  done
  rm -f "${HOLD_FILE}"
  systemctl reset-failed "${RUNNER_SERVICE}" >/dev/null 2>&1 || true
  # If the runner does not come up, put the hold back so a later --resume or
  # scheduled run goes through release_runner again instead of leaving it
  # stopped with nothing left to retry the recovery.
  if ! systemctl start "${RUNNER_SERVICE}"; then
    err "failed to start ${RUNNER_SERVICE}"
    restore_hold
    return 1
  fi
  sleep "${START_SETTLE_SEC}"
  if [ "$(runner_state)" != "active" ]; then
    err "${RUNNER_SERVICE} is not active after start (state: $(runner_state))"
    restore_hold
    return 1
  fi
  log "runner healthy and accepting jobs"
  return 0
}

# Exit status of `dnf needs-restarting -r`: 0 = no reboot, 1 = reboot. Any
# other status (plugin missing, error) reboots: the runner is already drained,
# so an unneeded reboot only costs a few minutes.
reboot_required() {
  local rc=0
  dnf needs-restarting -r >/dev/null 2>&1 || rc=$?
  case "${rc}" in
    0) return 1 ;;
    1) return 0 ;;
    *)
      log "cannot determine whether a reboot is needed (dnf needs-restarting exit ${rc}) — rebooting"
      return 0
      ;;
  esac
}

mode="${1:-}"
case "${mode}" in
  ""|--resume) ;;
  *)
    err "usage: $0 [--resume]"
    exit 2
    ;;
esac

mkdir -p "$(dirname "${LOCK_FILE}")"
exec {OS_UPDATE_LOCK_FD}>"${LOCK_FILE}"
if ! flock -n "${OS_UPDATE_LOCK_FD}"; then
  log "another os-update run is in progress — skipping"
  exit 0
fi

if [ -f "${HOLD_FILE}" ]; then
  log "runner is held by an earlier os-update run"
  release_runner || exit 1
fi
if [ "${mode}" = "--resume" ]; then
  exit 0
fi

log "checking for package updates"
check_rc=0
dnf check-upgrade --refresh || check_rc=$?
case "${check_rc}" in
  0)
    log "no package updates available — runner left available"
    exit 0
    ;;
  100)
    log "package updates available"
    ;;
  *)
    err "dnf check-upgrade failed (exit ${check_rc}) — runner left available"
    exit 1
    ;;
esac

if [ "$(runner_state)" != "active" ]; then
  warn "${RUNNER_SERVICE} is not running (state: $(runner_state)) — leaving it alone, no update applied"
  exit 0
fi

start=$(date +%s)
while runner_busy; do
  now=$(date +%s)
  if [ $((now - start)) -ge "${DEFER_MAX_SEC}" ]; then
    log "runner still busy after ${DEFER_MAX_SEC}s — update deferred to the next scheduled run"
    exit 0
  fi
  log "retrying in ${RETRY_SEC}s"
  sleep "${RETRY_SEC}"
done

drain_rc=0
drain_runner || drain_rc=$?
if [ "${drain_rc}" -ne 0 ]; then
  # Do not touch the job: release the hold so Restart=always brings the
  # runner back once the job ends, and try again on the next scheduled run.
  # shutdown_timeout stays raised so the job is not cut off; a later run
  # restores the saved original.
  rm -f "${HOLD_FILE}"
  systemctl start "${RUNNER_SERVICE}" >/dev/null 2>&1 || true
  if [ "${drain_rc}" -eq 2 ]; then
    err "drain preparation failed — runner left available, no update applied"
    exit 1
  fi
  warn "drain did not finish within ${DRAIN_TIMEOUT_SEC}s — update deferred, job left running"
  exit 0
fi
restore_shutdown_timeout

log "applying package updates within the installed Fedora release"
upgrade_rc=0
dnf -y upgrade || upgrade_rc=$?
if [ "${upgrade_rc}" -ne 0 ]; then
  err "dnf upgrade failed (exit ${upgrade_rc})"
  release_runner || true
  exit 1
fi

if reboot_required; then
  log "rebooting; fullsend-os-update-resume.service verifies the runner at boot"
  systemctl reboot
  exit 0
fi

release_runner || exit 1
log "package updates applied"
