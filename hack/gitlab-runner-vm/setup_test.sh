#!/usr/bin/env bash
# setup_test.sh — Tests for setup.sh idempotency hygiene (patch_config backup
# and configure_per_job_gateway seed-start skip).
#
# Run from the repo root:
#   bash hack/gitlab-runner-vm/setup_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP="${SCRIPT_DIR}/setup.sh"
FAILURES=0

pass() { echo "ok       $*"; }
fail() {
  echo "FAIL     $*" >&2
  FAILURES=$((FAILURES + 1))
}

# Run a setup.sh function in a subshell so setup's fail() cannot abort the
# test process. setup.sh overwrites CONFIG_TOML/RUNNER_USER at source time,
# so restore the test paths afterwards.
#
# Usage: run_setup <function>
# Sets: RUN_SETUP_RC, RUN_SETUP_OUT
run_setup() {
  local fn="$1"
  local test_config_toml="${CONFIG_TOML}"
  local test_builds_dir="${BUILDS_DIR}"
  local test_cache_dir="${CACHE_DIR}"
  local test_executor_dir="${EXECUTOR_DIR}"
  RUN_SETUP_RC=0
  RUN_SETUP_OUT=$(
    export PATH="${SHIM_DIR}:${PATH}"
    export HOME="${FAKE_HOME}"
    # shellcheck source=setup.sh
    source "${SETUP}"
    CONFIG_TOML="${test_config_toml}"
    BUILDS_DIR="${test_builds_dir}"
    CACHE_DIR="${test_cache_dir}"
    EXECUTOR_DIR="${test_executor_dir}"
    export RUNNER_USER="testuser"
    "${fn}"
  ) && RUN_SETUP_RC=0 || RUN_SETUP_RC=$?
}

FAKE_HOME=$(mktemp -d)
SHIM_DIR=$(mktemp -d)
WORK_DIR=$(mktemp -d)
trap 'rm -rf "${FAKE_HOME}" "${SHIM_DIR}" "${WORK_DIR}"' EXIT

CONFIG_TOML="${WORK_DIR}/config.toml"
BUILDS_DIR="${FAKE_HOME}/builds"
CACHE_DIR="${FAKE_HOME}/cache"
EXECUTOR_DIR="${FAKE_HOME}/gitlab-runner-executor"
SYSTEMCTL_LOG="${SHIM_DIR}/systemctl.log"
OPENSHELL_LOG="${SHIM_DIR}/openshell.log"
SUDO_LOG="${SHIM_DIR}/sudo.log"

# sudo is a no-op (patch_config chown/chmod the config dir).
printf '#!/bin/sh\necho "$@" >> "%s"\nexit 0\n' "${SUDO_LOG}" > "${SHIM_DIR}/sudo"
# openshell gateway remove is a no-op.
printf '#!/bin/sh\necho "$@" >> "%s"\nexit 0\n' "${OPENSHELL_LOG}" > "${SHIM_DIR}/openshell"
chmod +x "${SHIM_DIR}/sudo" "${SHIM_DIR}/openshell"

# Default systemctl: unit exists, is disabled, is inactive. start/stop/disable
# succeed. is-active becomes true after a start is logged (seed path).
write_systemctl_stub() {
  local mode="$1" # seeded | needs-seed | enabled | active
  cat > "${SHIM_DIR}/systemctl" <<STUB
#!/bin/sh
echo "\$@" >> "${SYSTEMCTL_LOG}"
cmd=""
for a in "\$@"; do
  case "\$a" in
    --user|--quiet) ;;
    *)
      if [ -z "\$cmd" ]; then cmd="\$a"; fi
      ;;
  esac
done
case "\$cmd" in
  cat) exit 0 ;;
  daemon-reload|start|stop|disable|enable) exit 0 ;;
  is-enabled)
    if [ "${mode}" = "enabled" ]; then exit 0; fi
    exit 1
    ;;
  is-active)
    # "active": unit is already running (skip check fails, wait_for succeeds).
    # "enabled"/"needs-seed": fall through to seed; wait_for succeeds after start.
    if [ "${mode}" = "active" ]; then exit 0; fi
    if [ "${mode}" = "needs-seed" ] || [ "${mode}" = "enabled" ]; then
      if grep -q 'start openshell-gateway.service' "${SYSTEMCTL_LOG}" 2>/dev/null; then
        exit 0
      fi
    fi
    exit 1
    ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "${SHIM_DIR}/systemctl"
  : > "${SYSTEMCTL_LOG}"
  : > "${OPENSHELL_LOG}"
  : > "${SUDO_LOG}"
}

write_shell_config() {
  cat > "${CONFIG_TOML}" <<'TOML'
concurrent = 1
check_interval = 0

[[runners]]
  name = "test"
  url = "https://gitlab.example.com"
  token = "glrt-test"
  executor = "shell"
TOML
}

write_custom_config() {
  cat > "${CONFIG_TOML}" <<'TOML'
concurrent = 1
check_interval = 0

[[runners]]
  name = "test"
  url = "https://gitlab.example.com"
  token = "glrt-test"
  executor = "custom"
  builds_dir = "/home/test/builds"
  cache_dir = "/home/test/cache"
  [runners.custom]
    prepare_exec = "/home/test/gitlab-runner-executor/prepare.sh"
    run_exec = "/home/test/gitlab-runner-executor/run.sh"
    cleanup_exec = "/home/test/gitlab-runner-executor/cleanup.sh"
TOML
}

echo "== contract comments (static) =="
if grep -Eq 'Idempotent: safe to re-run' "${SETUP}" \
  && grep -Fq 'Recreation (drain → delete → create) is the compliance' "${SETUP}"; then
  pass "setup.sh header states idempotency and that recreation is the compliance path"
else
  fail "setup.sh header missing idempotency/compliance-path contract"
fi

for script in prepare.sh run.sh cleanup.sh gateway.sh; do
  if grep -q 'Idempotent:' "${SCRIPT_DIR}/executor/${script}" \
    && grep -q 'must stay that way' "${SCRIPT_DIR}/executor/${script}"; then
    pass "executor/${script} carries an idempotency contract comment"
  else
    fail "executor/${script} missing idempotency contract comment"
  fi
done

if grep -Fq 'config.toml.bak.$(date' "${SETUP}" \
  || grep -Fq 'CONFIG_TOML}.bak.$(date' "${SETUP}"; then
  fail "patch_config still writes a timestamped .bak"
else
  pass "patch_config does not write a timestamped .bak"
fi

if grep -Fq 'CONFIG_TOML}.bak"' "${SETUP}"; then
  pass "patch_config uses a single overwriting .bak"
else
  fail "patch_config does not write CONFIG_TOML.bak"
fi

if grep -Fq 'skipping seed start' "${SETUP}"; then
  pass "configure_per_job_gateway has a re-run skip for the seed start"
else
  fail "configure_per_job_gateway missing seed-start skip"
fi

if grep -Fq 'Single-runner VM assumption' "${SETUP}"; then
  pass "patch_config documents the single-runner assumption"
else
  fail "patch_config missing single-runner assumption comment"
fi

echo "== patch_config backup =="
write_systemctl_stub seeded
write_shell_config
run_setup patch_config
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "patch_config on a single shell runner should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif [ ! -f "${CONFIG_TOML}.bak" ]; then
  fail "patch_config did not write config.toml.bak"
elif grep -q 'executor = "custom"' "${CONFIG_TOML}" \
  && grep -q 'executor = "shell"' "${CONFIG_TOML}.bak"; then
  pass "patch_config writes a single .bak and switches executor to custom"
else
  fail "patch_config did not patch executor=custom (out=${RUN_SETUP_OUT})"
fi

# Revert to shell and patch again — the .bak must be overwritten, not
# timestamp-suffixed.
write_shell_config
run_setup patch_config
bak_count=$(find "${WORK_DIR}" -maxdepth 1 -name 'config.toml.bak*' | wc -l)
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "second patch_config should succeed (rc=${RUN_SETUP_RC})"
elif [ "${bak_count}" -ne 1 ]; then
  fail "second patch_config accumulated backups (count=${bak_count})"
elif [ ! -f "${CONFIG_TOML}.bak" ]; then
  fail "second patch_config did not overwrite config.toml.bak"
else
  pass "re-patch overwrites the same .bak (no timestamped accumulation)"
fi

# Already-custom is a no-op: no new backup, file unchanged.
write_custom_config
before=$(cat "${CONFIG_TOML}")
rm -f "${CONFIG_TOML}.bak"
run_setup patch_config
after=$(cat "${CONFIG_TOML}")
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "patch_config on already-custom should succeed (rc=${RUN_SETUP_RC})"
elif [ "${before}" != "${after}" ]; then
  fail "patch_config mutated an already-custom config"
elif [ -e "${CONFIG_TOML}.bak" ]; then
  fail "patch_config wrote a .bak on an already-custom no-op"
elif printf '%s' "${RUN_SETUP_OUT}" | grep -Fq 'already using custom executor'; then
  pass "already-custom config is a no-op (no .bak)"
else
  fail "patch_config did not report already-custom: ${RUN_SETUP_OUT}"
fi

echo "== configure_per_job_gateway seed skip =="
write_custom_config
write_systemctl_stub seeded
run_setup configure_per_job_gateway
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "seeded re-run should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif grep -q 'start openshell-gateway.service' "${SYSTEMCTL_LOG}"; then
  fail "seeded re-run started the gateway: $(tr '\n' '|' < "${SYSTEMCTL_LOG}")"
elif grep -q 'stop openshell-gateway.service' "${SYSTEMCTL_LOG}"; then
  fail "seeded re-run stopped the gateway: $(tr '\n' '|' < "${SYSTEMCTL_LOG}")"
elif printf '%s' "${RUN_SETUP_OUT}" | grep -Fq 'skipping seed start'; then
  pass "already-seeded VM skips the gateway start/stop cycle"
else
  fail "seeded re-run did not report skip: ${RUN_SETUP_OUT}"
fi

# First run (executor still shell) must seed.
write_shell_config
write_systemctl_stub needs-seed
run_setup configure_per_job_gateway
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "first-run seed should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif ! grep -q 'start openshell-gateway.service' "${SYSTEMCTL_LOG}"; then
  fail "first run did not start the gateway: $(tr '\n' '|' < "${SYSTEMCTL_LOG}")"
elif ! grep -q 'stop openshell-gateway.service' "${SYSTEMCTL_LOG}"; then
  fail "first run did not stop the gateway after seed"
elif ! grep -q 'disable openshell-gateway.service' "${SYSTEMCTL_LOG}"; then
  fail "first run did not disable the gateway after seed"
else
  pass "first run seeds with start → stop → disable"
fi

# Custom executor but unit still enabled: must fall through and pin it.
write_custom_config
write_systemctl_stub enabled
run_setup configure_per_job_gateway
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "enabled-unit re-run should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif ! grep -q 'start openshell-gateway.service' "${SYSTEMCTL_LOG}"; then
  fail "enabled unit should not take the skip path: $(tr '\n' '|' < "${SYSTEMCTL_LOG}")"
else
  pass "re-enabled unit falls through and re-seeds (pins back to per-job)"
fi

# Custom executor but unit still active: must fall through.
write_custom_config
write_systemctl_stub active
run_setup configure_per_job_gateway
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "active-unit re-run should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif ! grep -q 'start openshell-gateway.service' "${SYSTEMCTL_LOG}"; then
  fail "active unit should not take the skip path: $(tr '\n' '|' < "${SYSTEMCTL_LOG}")"
else
  pass "running unit falls through and re-seeds (pins back to per-job)"
fi

if [ "${FAILURES}" -ne 0 ]; then
  echo "${FAILURES} case(s) failed" >&2
  exit 1
fi
echo "all cases passed"
