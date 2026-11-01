#!/usr/bin/env bash
# os-update_test.sh — Tests for os-update.sh (update check exit codes, busy
# deferral, graceful drain, reboot and resume, health-gated release) and for
# setup.sh installing the per-VM daily update timer.
#
# Run from the repo root:
#   bash hack/gitlab-runner-vm/os-update_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OS_UPDATE="${SCRIPT_DIR}/os-update.sh"
SETUP="${SCRIPT_DIR}/setup.sh"
CREATE_GCP="${SCRIPT_DIR}/create-gcp-vm.sh"
CREATE_OCP="${SCRIPT_DIR}/create-openshift-vm.sh"
FAILURES=0

pass() { echo "ok       $*"; }
fail() {
  echo "FAIL     $*" >&2
  FAILURES=$((FAILURES + 1))
}

WORK_DIR=$(mktemp -d)
SHIM_DIR=$(mktemp -d)
trap 'rm -rf "${WORK_DIR}" "${SHIM_DIR}"' EXIT

STATE_DIR="${WORK_DIR}/state"
HOLD_FILE="${STATE_DIR}/os-update.hold"
CONFIG_TOML="${WORK_DIR}/config.toml"
CALLS="${SHIM_DIR}/calls.log"
SVC_STATE="${SHIM_DIR}/svc-state"
CONTAINERS_FILE="${SHIM_DIR}/containers"
BUSY_COUNT_FILE="${SHIM_DIR}/busy-count"
export CALLS SVC_STATE CONTAINERS_FILE BUSY_COUNT_FILE HOLD_FILE

# --- stubs ---------------------------------------------------------------
# dnf: exit codes come from DNF_CHECK_RC / DNF_UPGRADE_RC / DNF_RESTART_RC.
cat > "${SHIM_DIR}/dnf" <<'STUB'
#!/bin/sh
echo "dnf $*" >> "${CALLS}"
case "$1" in
  check-upgrade) exit "${DNF_CHECK_RC:-0}" ;;
  needs-restarting) exit "${DNF_RESTART_RC:-0}" ;;
esac
for a in "$@"; do
  if [ "$a" = "upgrade" ]; then exit "${DNF_UPGRADE_RC:-0}"; fi
done
exit 0
STUB

# systemctl: gitlab-runner state lives in SVC_STATE. QUIT stops the runner
# unless QUIT_KEEPS_RUNNING=1; start honours the hold-file condition.
cat > "${SHIM_DIR}/systemctl" <<'STUB'
#!/bin/sh
echo "systemctl $*" >> "${CALLS}"
case "$1" in
  is-active)
    state=$(cat "${SVC_STATE}" 2>/dev/null || echo inactive)
    # "empty" simulates a failed state query: no output, nonzero exit.
    if [ "${state}" = "empty" ]; then exit 4; fi
    echo "${state}"
    [ "${state}" = "active" ]
    exit $?
    ;;
  show) echo "${MAIN_PID:-4242}" ;;
  kill)
    case "$*" in
      *"-s HUP"*)
        if [ "${HUP_FAILS:-0}" = "1" ]; then exit 1; fi
        ;;
      *"-s QUIT"*)
        if [ "${STATE_EMPTY_AFTER_QUIT:-0}" = "1" ]; then echo empty > "${SVC_STATE}"
        elif [ "${QUIT_KEEPS_RUNNING:-0}" != "1" ]; then echo inactive > "${SVC_STATE}"; fi
        ;;
    esac
    ;;
  start)
    if [ "${START_RC:-0}" != "0" ]; then exit "${START_RC}"; fi
    if [ ! -e "${HOLD_FILE}" ] && [ "${START_FAILS:-0}" != "1" ]; then echo active > "${SVC_STATE}"; fi
    ;;
esac
exit 0
STUB

# mktemp: fails on demand (MKTEMP_FAILS=1), otherwise the real one.
cat > "${SHIM_DIR}/mktemp" <<'STUB'
#!/bin/sh
if [ "${MKTEMP_FAILS:-0}" = "1" ]; then exit 1; fi
exec /usr/bin/mktemp "$@"
STUB

# pgrep: a child of gitlab-runner exists for the first BUSY_CALLS calls.
cat > "${SHIM_DIR}/pgrep" <<'STUB'
#!/bin/sh
n=$(cat "${BUSY_COUNT_FILE}" 2>/dev/null || echo 0)
n=$((n + 1))
echo "${n}" > "${BUSY_COUNT_FILE}"
echo "pgrep $*" >> "${CALLS}"
[ "${n}" -le "${BUSY_CALLS:-0}" ]
STUB

cat > "${SHIM_DIR}/podman" <<'STUB'
#!/bin/sh
echo "podman $*" >> "${CALLS}"
case "$1" in
  ps) cat "${CONTAINERS_FILE}" ;;
  info) exit "${PODMAN_INFO_RC:-0}" ;;
esac
exit 0
STUB

cat > "${SHIM_DIR}/gitlab-runner" <<'STUB'
#!/bin/sh
echo "gitlab-runner $*" >> "${CALLS}"
if [ "${VERIFY_FAILS:-0}" = "1" ]; then
  echo "ERROR: Verifying runner... is removed"
  exit 1
fi
echo "Verifying runner... is valid"
STUB

# runuser -u USER -- cmd...: run cmd as the current user.
cat > "${SHIM_DIR}/runuser" <<'STUB'
#!/bin/sh
echo "runuser $*" >> "${CALLS}"
while [ $# -gt 0 ] && [ "$1" != "--" ]; do shift; done
shift
exec "$@"
STUB

cat > "${SHIM_DIR}/id" <<'STUB'
#!/bin/sh
echo 1000
STUB

# mv: fails on demand (MV_FAILS=1), otherwise the real one.
cat > "${SHIM_DIR}/mv" <<'STUB'
#!/bin/sh
if [ "${MV_FAILS:-0}" = "1" ]; then exit 1; fi
exec /usr/bin/mv "$@"
STUB

chmod +x "${SHIM_DIR}"/*

# --- helpers ---------------------------------------------------------------
reset_fixtures() {
  rm -rf "${STATE_DIR}"
  mkdir -p "${STATE_DIR}"
  : > "${CALLS}"
  : > "${CONTAINERS_FILE}"
  echo 0 > "${BUSY_COUNT_FILE}"
  echo active > "${SVC_STATE}"
  printf 'concurrent = 1\ncheck_interval = 0\n\n[[runners]]\n  name = "r"\n' > "${CONFIG_TOML}"
}

# Usage: run_update [args...]; extra env is passed by the caller.
run_update() {
  RUN_RC=0
  RUN_OUT=$(
    PATH="${SHIM_DIR}:${PATH}" \
    FULLSEND_RUNNER_USER=testuser \
    FULLSEND_GITLAB_RUNNER_CONFIG="${CONFIG_TOML}" \
    FULLSEND_OS_UPDATE_STATE_DIR="${STATE_DIR}" \
    FULLSEND_OS_UPDATE_LOCK_FILE="${WORK_DIR}/os-update.lock" \
    FULLSEND_OS_UPDATE_RETRY_SEC=0 \
    FULLSEND_OS_UPDATE_POLL_SEC=0 \
    FULLSEND_OS_UPDATE_RELOAD_SETTLE_SEC=0 \
    FULLSEND_OS_UPDATE_VERIFY_RETRY_SEC=0 \
    FULLSEND_OS_UPDATE_VERIFY_ATTEMPTS="${FULLSEND_OS_UPDATE_VERIFY_ATTEMPTS:-2}" \
    FULLSEND_OS_UPDATE_START_SETTLE_SEC=0 \
    bash "${OS_UPDATE}" "$@" 2>&1
  ) || RUN_RC=$?
}

called() { grep -Fq -- "$1" "${CALLS}"; }
calls() { tr '\n' '|' < "${CALLS}"; }

echo "== static contracts =="
OS_UPDATE_CODE=$(grep -Ev '^[[:space:]]*#' "${OS_UPDATE}")
if grep -Eq 'system-upgrade|--releasever|releasever=' <<< "${OS_UPDATE_CODE}"; then
  fail "os-update.sh must not change the Fedora release"
else
  pass "os-update.sh never runs system-upgrade or changes releasever"
fi
if grep -Eq 'systemctl (stop|restart) |podman (stop|kill|rm)' <<< "${OS_UPDATE_CODE}"; then
  fail "os-update.sh must not stop gitlab-runner or job containers forcibly"
else
  pass "os-update.sh drains with SIGQUIT, never systemctl stop"
fi
if grep -Fq 'Idempotent: safe to re-run' "${OS_UPDATE}"; then
  pass "os-update.sh carries an idempotency contract comment"
else
  fail "os-update.sh missing idempotency contract comment"
fi
if grep -Fq 'os-update.sh' "${CREATE_GCP}" && grep -Fq 'os-update.sh' "${CREATE_OCP}"; then
  pass "both create scripts copy and checksum os-update.sh"
else
  fail "create scripts missing os-update.sh in copy/checksum lists"
fi

echo "== check-upgrade exit codes =="
reset_fixtures
DNF_CHECK_RC=0 run_update
if [ "${RUN_RC}" -ne 0 ]; then
  fail "no updates should exit 0 (rc=${RUN_RC}): ${RUN_OUT}"
elif called 'QUIT' || called 'dnf -y upgrade' || [ -e "${HOLD_FILE}" ]; then
  fail "no updates still drained or upgraded: $(calls)"
else
  pass "check-upgrade=0 leaves the runner available"
fi

reset_fixtures
DNF_CHECK_RC=1 run_update
if [ "${RUN_RC}" -eq 0 ]; then
  fail "check-upgrade error should fail the unit: ${RUN_OUT}"
elif called 'QUIT' || called 'dnf -y upgrade' || [ -e "${HOLD_FILE}" ]; then
  fail "check-upgrade error still drained or upgraded: $(calls)"
elif printf '%s' "${RUN_OUT}" | grep -Fq 'ERROR: dnf check-upgrade failed (exit 1) — runner left available'; then
  pass "check-upgrade=1 reports the error and leaves the runner available"
else
  fail "check-upgrade error output mismatch: ${RUN_OUT}"
fi

echo "== update without reboot =="
reset_fixtures
printf 'shutdown_timeout = 7\n' | cat - "${CONFIG_TOML}" > "${CONFIG_TOML}.new"
cat "${CONFIG_TOML}.new" > "${CONFIG_TOML}"
rm -f "${CONFIG_TOML}.new"
chmod 640 "${CONFIG_TOML}"
DNF_CHECK_RC=100 DNF_RESTART_RC=0 run_update
if [ "${RUN_RC}" -ne 0 ]; then
  fail "update should succeed (rc=${RUN_RC}): ${RUN_OUT}"
elif ! called 'kill --kill-whom=main -s HUP gitlab-runner.service' \
  || ! called 'kill --kill-whom=main -s QUIT gitlab-runner.service'; then
  fail "update did not drain with HUP+QUIT: $(calls)"
elif ! called 'dnf -y upgrade'; then
  fail "update did not run dnf upgrade: $(calls)"
elif called 'systemctl reboot'; then
  fail "update rebooted although needs-restarting said no"
elif ! called 'gitlab-runner verify'; then
  fail "update released the runner without gitlab-runner verify: $(calls)"
elif [ -e "${HOLD_FILE}" ] || [ "$(cat "${SVC_STATE}")" != "active" ]; then
  fail "runner not released after update (state=$(cat "${SVC_STATE}"))"
elif [ "$(grep -c '^shutdown_timeout' "${CONFIG_TOML}")" -ne 1 ] \
  || ! grep -qx 'shutdown_timeout = 7' "${CONFIG_TOML}"; then
  fail "shutdown_timeout not restored: $(cat "${CONFIG_TOML}")"
elif [ "$(stat -c '%a %u:%g' "${CONFIG_TOML}")" != "640 $(id -u):$(id -g)" ]; then
  fail "config.toml lost its mode/owner: $(stat -c '%a %u:%g' "${CONFIG_TOML}")"
elif ls "${CONFIG_TOML}".* >/dev/null 2>&1; then
  fail "temp files left next to config.toml: $(ls "${CONFIG_TOML}".*)"
elif ! grep -Fq '[[runners]]' "${CONFIG_TOML}"; then
  fail "config.toml lost its runner block"
else
  pass "check-upgrade=100 drains, upgrades, verifies, and re-enables the runner"
fi
# Ordering: drain before upgrade, upgrade before start.
quit_line=$(grep -n 's QUIT' "${CALLS}" | head -1 | cut -d: -f1)
upgrade_line=$(grep -n 'dnf -y upgrade' "${CALLS}" | head -1 | cut -d: -f1)
start_line=$(grep -n 'systemctl start gitlab-runner' "${CALLS}" | head -1 | cut -d: -f1)
if [ -n "${quit_line}" ] && [ -n "${upgrade_line}" ] && [ -n "${start_line}" ] \
  && [ "${quit_line}" -lt "${upgrade_line}" ] && [ "${upgrade_line}" -lt "${start_line}" ]; then
  pass "drain precedes upgrade, upgrade precedes restart"
else
  fail "wrong ordering: $(calls)"
fi

echo "== busy runner =="
reset_fixtures
DNF_CHECK_RC=100 BUSY_CALLS=1000 FULLSEND_OS_UPDATE_DEFER_MAX_SEC=0 run_update
if [ "${RUN_RC}" -ne 0 ]; then
  fail "busy deferral should exit 0 (rc=${RUN_RC}): ${RUN_OUT}"
elif called 'QUIT' || called 'dnf -y upgrade' || [ -e "${HOLD_FILE}" ]; then
  fail "busy runner was drained or upgraded: $(calls)"
elif printf '%s' "${RUN_OUT}" | grep -Fq 'update deferred to the next scheduled run'; then
  pass "busy runner defers without draining or touching the job"
else
  fail "busy deferral output mismatch: ${RUN_OUT}"
fi

reset_fixtures
printf 'runner-77\n' > "${CONTAINERS_FILE}"
DNF_CHECK_RC=100 FULLSEND_OS_UPDATE_DEFER_MAX_SEC=0 run_update
if called 'QUIT' || called 'dnf -y upgrade'; then
  fail "running runner-* container did not defer: $(calls)"
elif printf '%s' "${RUN_OUT}" | grep -Fq 'runner-77'; then
  pass "running runner-* container defers the update"
else
  fail "runner-* deferral output mismatch: ${RUN_OUT}"
fi

reset_fixtures
DNF_CHECK_RC=100 BUSY_CALLS=2 FULLSEND_OS_UPDATE_DEFER_MAX_SEC=3600 run_update
if [ "${RUN_RC}" -ne 0 ]; then
  fail "retry-then-update should succeed (rc=${RUN_RC}): ${RUN_OUT}"
elif [ "$(grep -c '^pgrep' "${CALLS}")" -lt 3 ] || ! called 'dnf -y upgrade'; then
  fail "busy runner was not retried until idle: $(calls)"
else
  pass "busy runner is retried and updated once idle"
fi

echo "== drain cap =="
reset_fixtures
DNF_CHECK_RC=100 QUIT_KEEPS_RUNNING=1 FULLSEND_OS_UPDATE_DRAIN_TIMEOUT_SEC=0 run_update
if [ "${RUN_RC}" -ne 0 ]; then
  fail "drain cap should defer with rc=0 (rc=${RUN_RC}): ${RUN_OUT}"
elif called 'dnf -y upgrade'; then
  fail "upgrade ran although the drain did not finish: $(calls)"
elif [ -e "${HOLD_FILE}" ]; then
  fail "hold file left behind after a deferred drain (runner could not come back)"
elif ! grep -qx 'shutdown_timeout = 86400' "${CONFIG_TOML}"; then
  fail "shutdown_timeout should stay raised past the cap while the job runs: $(cat "${CONFIG_TOML}")"
elif printf '%s' "${RUN_OUT}" | grep -Fq 'update deferred, job left running'; then
  pass "drain cap releases the hold, leaves the job running, and defers"
else
  fail "drain cap output mismatch: ${RUN_OUT}"
fi
# The next run restores the original (absent) shutdown_timeout.
: > "${CALLS}"
echo 0 > "${BUSY_COUNT_FILE}"
echo active > "${SVC_STATE}"
DNF_CHECK_RC=100 run_update
if [ "${RUN_RC}" -eq 0 ] && ! grep -q '^shutdown_timeout' "${CONFIG_TOML}"; then
  pass "a later run restores the shutdown_timeout saved before the deferred drain"
else
  fail "shutdown_timeout not restored on a later run (rc=${RUN_RC}): $(cat "${CONFIG_TOML}")"
fi

# The graceful shutdown started by QUIT cannot be cancelled, so the deadline
# left on a deferred job must be the drain cap plus the longest job, not just
# a fixed hour past the cap.
reset_fixtures
DNF_CHECK_RC=100 QUIT_KEEPS_RUNNING=1 FULLSEND_OS_UPDATE_DRAIN_TIMEOUT_SEC=100 \
  FULLSEND_OS_UPDATE_JOB_MAX_SEC=5000 run_update
if [ "${RUN_RC}" -eq 0 ] && grep -qx 'shutdown_timeout = 5100' "${CONFIG_TOML}" \
  && [ "$(grep -c '^shutdown_timeout' "${CONFIG_TOML}")" -eq 1 ]; then
  pass "a deferred drain leaves shutdown_timeout at the drain cap plus the longest job"
else
  fail "deferred shutdown_timeout is not cap + job max (rc=${RUN_RC}): $(cat "${CONFIG_TOML}")"
fi

reset_fixtures
FULLSEND_OS_UPDATE_JOB_MAX_SEC=abc DNF_CHECK_RC=100 run_update
if [ "${RUN_RC}" -ne 0 ] && ! called 'dnf' && printf '%s' "${RUN_OUT}" | grep -Fq 'JOB_MAX_SEC must be a non-negative integer'; then
  pass "a non-numeric JOB_MAX_SEC is rejected before doing anything"
else
  fail "non-numeric JOB_MAX_SEC not rejected (rc=${RUN_RC}): ${RUN_OUT}"
fi

echo "== indented shutdown_timeout =="
reset_fixtures
printf 'concurrent = 1\n  shutdown_timeout = 7\n\n[[runners]]\n  name = "r"\n' > "${CONFIG_TOML}"
DNF_CHECK_RC=100 QUIT_KEEPS_RUNNING=1 FULLSEND_OS_UPDATE_DRAIN_TIMEOUT_SEC=0 run_update
if [ "$(grep -Ec '^[[:space:]]*shutdown_timeout' "${CONFIG_TOML}")" -ne 1 ] \
  || ! grep -qx 'shutdown_timeout = 86400' "${CONFIG_TOML}"; then
  fail "indented shutdown_timeout left a duplicate key: $(cat "${CONFIG_TOML}")"
else
  pass "an indented shutdown_timeout is replaced, not duplicated"
fi
: > "${CALLS}"
echo 0 > "${BUSY_COUNT_FILE}"
echo active > "${SVC_STATE}"
DNF_CHECK_RC=100 run_update
if [ "${RUN_RC}" -eq 0 ] && [ "$(grep -Ec '^[[:space:]]*shutdown_timeout' "${CONFIG_TOML}")" -eq 1 ] \
  && grep -qx '  shutdown_timeout = 7' "${CONFIG_TOML}"; then
  pass "an indented original shutdown_timeout is saved and restored"
else
  fail "indented shutdown_timeout not restored (rc=${RUN_RC}): $(cat "${CONFIG_TOML}")"
fi

echo "== reboot and resume =="
reset_fixtures
DNF_CHECK_RC=100 DNF_RESTART_RC=1 run_update
if [ "${RUN_RC}" -ne 0 ]; then
  fail "reboot path should exit 0 (rc=${RUN_RC}): ${RUN_OUT}"
elif ! called 'systemctl reboot'; then
  fail "needs-restarting=1 did not reboot: $(calls)"
elif called 'systemctl start gitlab-runner'; then
  fail "runner started before the reboot"
elif [ ! -e "${HOLD_FILE}" ]; then
  fail "hold file missing before reboot (runner would start unverified at boot)"
else
  pass "needs-restarting=1 reboots with the runner held"
fi
echo inactive > "${SVC_STATE}"
: > "${CALLS}"
run_update --resume
if [ "${RUN_RC}" -ne 0 ]; then
  fail "resume should succeed (rc=${RUN_RC}): ${RUN_OUT}"
elif called 'dnf'; then
  fail "resume should not check or apply updates: $(calls)"
elif [ -e "${HOLD_FILE}" ] || [ "$(cat "${SVC_STATE}")" != "active" ]; then
  fail "resume did not release the runner"
elif ! called 'podman info' || ! called 'gitlab-runner verify'; then
  fail "resume released without health checks: $(calls)"
else
  pass "--resume verifies health, then releases the runner"
fi

reset_fixtures
DNF_CHECK_RC=100 DNF_RESTART_RC=2 run_update
if called 'systemctl reboot'; then
  pass "unknown reboot requirement (needs-restarting unavailable) reboots"
else
  fail "needs-restarting exit 2 did not reboot: $(calls)"
fi

echo "== failed health check keeps the runner drained =="
reset_fixtures
: > "${HOLD_FILE}"
echo inactive > "${SVC_STATE}"
VERIFY_FAILS=1 run_update --resume
if [ "${RUN_RC}" -eq 0 ]; then
  fail "failed health check should fail the unit: ${RUN_OUT}"
elif called 'systemctl start gitlab-runner' || [ ! -e "${HOLD_FILE}" ]; then
  fail "unhealthy runner was released: $(calls)"
elif printf '%s' "${RUN_OUT}" | grep -Fq 'gitlab-runner left drained'; then
  pass "unhealthy runner stays drained and the error is reported"
else
  fail "health failure output mismatch: ${RUN_OUT}"
fi

reset_fixtures
: > "${HOLD_FILE}"
echo inactive > "${SVC_STATE}"
DNF_CHECK_RC=0 run_update
if [ "${RUN_RC}" -eq 0 ] && [ ! -e "${HOLD_FILE}" ] && [ "$(cat "${SVC_STATE}")" = "active" ]; then
  pass "timer run releases a runner held by an earlier run before checking updates"
else
  fail "held runner not released by the timer run (rc=${RUN_RC}): ${RUN_OUT}"
fi

echo "== failed start restores the hold =="
reset_fixtures
: > "${HOLD_FILE}"
echo inactive > "${SVC_STATE}"
START_RC=1 run_update --resume
if [ "${RUN_RC}" -eq 0 ]; then
  fail "failed systemctl start should fail the unit: ${RUN_OUT}"
elif [ ! -e "${HOLD_FILE}" ]; then
  fail "hold file not restored after a failed start (--resume would be a no-op)"
else
  pass "a failed start restores the hold file"
fi
: > "${CALLS}"
run_update --resume
if [ "${RUN_RC}" -eq 0 ] && [ ! -e "${HOLD_FILE}" ] && [ "$(cat "${SVC_STATE}")" = "active" ]; then
  pass "a later --resume retries and recovers the runner"
else
  fail "later --resume did not recover the runner (rc=${RUN_RC}): ${RUN_OUT}"
fi

reset_fixtures
: > "${HOLD_FILE}"
echo inactive > "${SVC_STATE}"
START_FAILS=1 run_update --resume
if [ "${RUN_RC}" -eq 0 ]; then
  fail "a runner that is inactive after start should fail the unit: ${RUN_OUT}"
elif [ ! -e "${HOLD_FILE}" ]; then
  fail "hold file not restored when the runner is inactive after start"
else
  pass "an inactive runner after start restores the hold file"
fi

echo "== drain preparation failures do not send QUIT =="
reset_fixtures
DNF_CHECK_RC=100 HUP_FAILS=1 run_update
if [ "${RUN_RC}" -eq 0 ]; then
  fail "failed HUP should fail the unit: ${RUN_OUT}"
elif called 'QUIT' || called 'dnf -y upgrade'; then
  fail "QUIT or upgrade ran although the config reload failed: $(calls)"
elif [ -e "${HOLD_FILE}" ] || [ "$(cat "${SVC_STATE}")" != "active" ]; then
  fail "runner not left available after a failed HUP"
else
  pass "a failed config reload aborts the drain before QUIT"
fi

reset_fixtures
DNF_CHECK_RC=100 MKTEMP_FAILS=1 run_update
if [ "${RUN_RC}" -eq 0 ]; then
  fail "failed shutdown_timeout rewrite should fail the unit: ${RUN_OUT}"
elif called 'QUIT' || called 'dnf -y upgrade'; then
  fail "QUIT or upgrade ran although shutdown_timeout was not raised: $(calls)"
elif [ -e "${HOLD_FILE}" ] || [ "$(cat "${SVC_STATE}")" != "active" ]; then
  fail "runner not left available after a failed shutdown_timeout rewrite"
else
  pass "a failed shutdown_timeout rewrite aborts the drain before QUIT"
fi

reset_fixtures
config_before=$(cat "${CONFIG_TOML}")
DNF_CHECK_RC=100 MV_FAILS=1 run_update
if [ "${RUN_RC}" -eq 0 ]; then
  fail "a failed config.toml replacement should fail the unit: ${RUN_OUT}"
elif [ "$(cat "${CONFIG_TOML}")" != "${config_before}" ]; then
  fail "config.toml changed although the replacement failed: $(cat "${CONFIG_TOML}")"
elif ls "${CONFIG_TOML}".* >/dev/null 2>&1; then
  fail "temp file left behind after a failed replacement: $(ls "${CONFIG_TOML}".*)"
elif called 'QUIT' || called 'dnf -y upgrade'; then
  fail "QUIT or upgrade ran although config.toml was not rewritten: $(calls)"
else
  pass "a failed config.toml replacement leaves the original config untouched"
fi

echo "== unknown runner state is not treated as drained =="
reset_fixtures
DNF_CHECK_RC=100 STATE_EMPTY_AFTER_QUIT=1 FULLSEND_OS_UPDATE_DRAIN_TIMEOUT_SEC=0 run_update
if called 'dnf -y upgrade'; then
  fail "upgrade ran although the runner state could not be read: $(calls)"
elif printf '%s' "${RUN_OUT}" | grep -Fq 'update deferred, job left running'; then
  pass "an empty state query defers instead of upgrading"
else
  fail "empty state output mismatch (rc=${RUN_RC}): ${RUN_OUT}"
fi

echo "== other guards =="
reset_fixtures
echo inactive > "${SVC_STATE}"
DNF_CHECK_RC=100 run_update
if [ "${RUN_RC}" -eq 0 ] && ! called 'dnf -y upgrade' && ! called 'systemctl start'; then
  pass "stopped runner is left alone"
else
  fail "stopped runner was updated or started (rc=${RUN_RC}): $(calls)"
fi

reset_fixtures
DNF_CHECK_RC=100 DNF_UPGRADE_RC=1 run_update
if [ "${RUN_RC}" -ne 0 ] && [ ! -e "${HOLD_FILE}" ] && [ "$(cat "${SVC_STATE}")" = "active" ]; then
  pass "failed dnf upgrade is reported and the healthy runner is released"
else
  fail "failed upgrade path (rc=${RUN_RC}, state=$(cat "${SVC_STATE}")): ${RUN_OUT}"
fi

reset_fixtures
(
  exec 9>"${WORK_DIR}/os-update.lock"
  flock -x 9
  sleep 2
) &
HOLDER_PID=$!
sleep 0.3
DNF_CHECK_RC=100 run_update
wait "${HOLDER_PID}"
if [ "${RUN_RC}" -eq 0 ] && ! called 'dnf'; then
  pass "concurrent run skips while the lock is held"
else
  fail "concurrent run did not skip (rc=${RUN_RC}): $(calls)"
fi

reset_fixtures
RUN_RC=0
RUN_OUT=$(PATH="${SHIM_DIR}:${PATH}" FULLSEND_RUNNER_USER='' bash "${OS_UPDATE}" 2>&1) || RUN_RC=$?
if [ "${RUN_RC}" -ne 0 ] && ! called 'dnf'; then
  pass "missing FULLSEND_RUNNER_USER fails before doing anything"
else
  fail "missing FULLSEND_RUNNER_USER not rejected (rc=${RUN_RC})"
fi

echo "== install_os_update =="
cat > "${SHIM_DIR}/sudo" <<'STUB'
#!/bin/sh
exec "$@"
STUB
chmod +x "${SHIM_DIR}/sudo"
SYS_DIR="${WORK_DIR}/systemd"
OVERRIDE_DIR="${WORK_DIR}/gitlab-runner.service.d"
LIBEXEC="${WORK_DIR}/libexec/os-update.sh"
INSTALL_STATE="${WORK_DIR}/install-state"

# The assignments below override setup.sh globals read by install_os_update.
# shellcheck disable=SC2034
run_install() {
  INSTALL_RC=0
  INSTALL_OUT=$(
    export PATH="${SHIM_DIR}:${PATH}"
    # shellcheck source=setup.sh
    source "${SETUP}"
    SYSTEMD_SYSTEM_DIR="${SYS_DIR}"
    GITLAB_RUNNER_OVERRIDE_DIR="${OVERRIDE_DIR}"
    OS_UPDATE_SCRIPT="${LIBEXEC}"
    OS_UPDATE_STATE_DIR="${INSTALL_STATE}"
    RUNNER_USER="testuser"
    install_os_update
  ) || INSTALL_RC=$?
}

: > "${CALLS}"
run_install
TIMER="${SYS_DIR}/fullsend-os-update.timer"
SERVICE="${SYS_DIR}/fullsend-os-update.service"
RESUME="${SYS_DIR}/fullsend-os-update-resume.service"
DROPIN="${OVERRIDE_DIR}/os-update-hold.conf"
first_time=$(cat "${INSTALL_STATE}/os-update-time" 2>/dev/null || true)
if [ "${INSTALL_RC}" -ne 0 ]; then
  fail "install_os_update should succeed (rc=${INSTALL_RC}): ${INSTALL_OUT}"
elif [ ! -x "${LIBEXEC}" ]; then
  fail "install_os_update did not install an executable os-update.sh"
elif ! [[ "${first_time}" =~ ^([01][0-9]|2[0-3]):[0-5][0-9]$ ]]; then
  fail "persisted update time is not HH:MM: '${first_time}'"
elif ! grep -Fxq "OnCalendar=*-*-* ${first_time}:00 UTC" "${TIMER}"; then
  fail "timer does not run at the persisted time: $(cat "${TIMER}")"
elif grep -Eq '^(Persistent|RandomizedDelaySec)=' "${TIMER}"; then
  fail "timer must keep a fixed daily time (no Persistent/RandomizedDelaySec)"
elif ! grep -Fxq "Environment=FULLSEND_RUNNER_USER=testuser" "${SERVICE}" \
  || ! grep -Fxq "ExecStart=${LIBEXEC}" "${SERVICE}" \
  || ! grep -Fxq 'TimeoutStartSec=infinity' "${SERVICE}"; then
  fail "update service unit mismatch: $(cat "${SERVICE}")"
elif ! grep -Fxq "ExecStart=${LIBEXEC} --resume" "${RESUME}" \
  || ! grep -Fxq "ConditionPathExists=${INSTALL_STATE}/os-update.hold" "${RESUME}" \
  || ! grep -Fxq 'WantedBy=multi-user.target' "${RESUME}"; then
  fail "resume unit mismatch: $(cat "${RESUME}")"
elif ! grep -Fxq "ConditionPathExists=!${INSTALL_STATE}/os-update.hold" "${DROPIN}"; then
  fail "gitlab-runner hold drop-in mismatch: $(cat "${DROPIN}" 2>/dev/null)"
elif ! called 'systemctl daemon-reload' \
  || ! called 'systemctl enable --now fullsend-os-update.timer' \
  || ! called 'systemctl enable fullsend-os-update-resume.service'; then
  fail "install_os_update did not reload and enable the units: $(calls)"
else
  pass "install_os_update installs the script, units, hold drop-in, and enables the timer"
fi

run_install
if [ "${INSTALL_RC}" -eq 0 ] && [ "$(cat "${INSTALL_STATE}/os-update-time")" = "${first_time}" ] \
  && grep -Fxq "OnCalendar=*-*-* ${first_time}:00 UTC" "${TIMER}"; then
  pass "re-run keeps the VM's persisted update time"
else
  fail "re-run changed the update time (was ${first_time}, now $(cat "${INSTALL_STATE}/os-update-time"))"
fi

echo "25:99" > "${INSTALL_STATE}/os-update-time"
run_install
regen=$(cat "${INSTALL_STATE}/os-update-time")
if [ "${INSTALL_RC}" -eq 0 ] && [[ "${regen}" =~ ^([01][0-9]|2[0-3]):[0-5][0-9]$ ]] \
  && grep -Fxq "OnCalendar=*-*-* ${regen}:00 UTC" "${TIMER}"; then
  pass "an invalid persisted time is replaced with a valid one"
else
  fail "invalid persisted time not regenerated: '${regen}'"
fi

: > "${INSTALL_STATE}/os-update.hold"
run_install
if [ "${INSTALL_RC}" -eq 0 ] && printf '%s' "${INSTALL_OUT}" | grep -Fq -- '--resume'; then
  pass "install_os_update tells the operator how to release a held runner"
else
  fail "held-runner hint missing: ${INSTALL_OUT}"
fi

RANDOM_TIMES=$(
  # shellcheck source=setup.sh
  source "${SETUP}"
  for _ in $(seq 1 500); do os_update_random_time; done
)
hours=$(printf '%s\n' "${RANDOM_TIMES}" | cut -d: -f1 | sort -u)
bad=$(printf '%s\n' "${RANDOM_TIMES}" | grep -Evc '^([01][0-9]|2[0-3]):[0-5][0-9]$' || true)
if [ "${bad}" -eq 0 ] && printf '%s\n' "${hours}" | grep -Eqx '0[0-2]' \
  && printf '%s\n' "${hours}" | grep -Eqx '2[1-3]' \
  && [ "$(printf '%s\n' "${RANDOM_TIMES}" | sort -u | wc -l)" -gt 1 ]; then
  pass "random update times are valid and span the full day"
else
  fail "random update times out of range or clustered: $(printf '%s' "${hours}" | tr '\n' ' ')"
fi

if [ "${FAILURES}" -ne 0 ]; then
  echo "${FAILURES} case(s) failed" >&2
  exit 1
fi
echo "all cases passed"
