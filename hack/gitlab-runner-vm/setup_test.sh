#!/usr/bin/env bash
# setup_test.sh — Tests for setup.sh idempotency hygiene (patch_config backup,
# configure_per_job_gateway seed-start skip, setup_runner_user UID drop-in)
# and the OpenShell 0.1 upgrade path (configure_gateway, install_openshell).
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
  local test_override_dir="${GITLAB_RUNNER_OVERRIDE_DIR}"
  # Not an && / || list: bash ignores errexit inside one, so a failing
  # command in setup.sh (e.g. the installer) would not stop the function.
  set +e
  RUN_SETUP_OUT=$(
    export PATH="${SHIM_DIR}:${PATH}"
    export HOME="${FAKE_HOME}"
    # shellcheck source=setup.sh
    source "${SETUP}"
    CONFIG_TOML="${test_config_toml}"
    BUILDS_DIR="${test_builds_dir}"
    CACHE_DIR="${test_cache_dir}"
    EXECUTOR_DIR="${test_executor_dir}"
    GITLAB_RUNNER_OVERRIDE_DIR="${test_override_dir}"
    export RUNNER_USER="testuser"
    "${fn}"
  )
  RUN_SETUP_RC=$?
  set -e
}

FAKE_HOME=$(mktemp -d)
SHIM_DIR=$(mktemp -d)
WORK_DIR=$(mktemp -d)
trap 'rm -rf "${FAKE_HOME}" "${SHIM_DIR}" "${WORK_DIR}"' EXIT

CONFIG_TOML="${WORK_DIR}/config.toml"
BUILDS_DIR="${FAKE_HOME}/builds"
CACHE_DIR="${FAKE_HOME}/cache"
EXECUTOR_DIR="${FAKE_HOME}/gitlab-runner-executor"
GITLAB_RUNNER_OVERRIDE_DIR="${WORK_DIR}/systemd-override"
SYSTEMCTL_LOG="${SHIM_DIR}/systemctl.log"
OPENSHELL_LOG="${SHIM_DIR}/openshell.log"
SUDO_LOG="${SHIM_DIR}/sudo.log"
FAKE_UID=1000
# The Renovate-tracked pin setup.sh sources (GITHUB_ENV unset: no CI side effect).
PIN_VERSION=$(env -u GITHUB_ENV bash -c 'source "$1"; printf "%s" "${OPENSHELL_VERSION}"' \
  _ "${SCRIPT_DIR}/../../.github/scripts/openshell-version.sh")
PIN_IMAGE="ghcr.io/nvidia/openshell/supervisor:${PIN_VERSION}"
GW_TOML="${FAKE_HOME}/.config/openshell/gateway.toml"
# No packaged default: configure_gateway takes the literal v2 fallback.
export OPENSHELL_PACKAGED_GATEWAY_TOML="${WORK_DIR}/no-packaged-default"

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

# sudo that actually mkdir/tee so setup_runner_user can write a drop-in.
write_sudo_passthrough_stub() {
  cat > "${SHIM_DIR}/sudo" <<STUB
#!/bin/sh
echo "\$@" >> "${SUDO_LOG}"
cmd="\$1"
shift
case "\$cmd" in
  mkdir) mkdir "\$@" ;;
  tee) cat > "\$1" ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "${SHIM_DIR}/sudo"
  : > "${SUDO_LOG}"
}

write_id_stub() {
  cat > "${SHIM_DIR}/id" <<STUB
#!/bin/sh
if [ "\$1" = "-u" ]; then
  echo "${FAKE_UID}"
  exit 0
fi
exit 1
STUB
  chmod +x "${SHIM_DIR}/id"
}

write_dropin() {
  local xdg_path="$1"
  mkdir -p "${GITLAB_RUNNER_OVERRIDE_DIR}"
  cat > "${GITLAB_RUNNER_OVERRIDE_DIR}/user.conf" <<EOF
[Service]
User=testuser
Group=testuser
WorkingDirectory=${FAKE_HOME}
Environment=XDG_RUNTIME_DIR=${xdg_path}
Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=${xdg_path}/bus
ExecStart=
ExecStart=/usr/local/bin/gitlab-runner run --config ${CONFIG_TOML} --working-directory ${FAKE_HOME} --service gitlab-runner
EOF
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

if grep -Eq '^[[:space:]]*(Environment=)?(XDG_RUNTIME_DIR=/run/user/%U|DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%U/bus)' "${SETUP}"; then
  fail "setup_runner_user still interpolates systemd %U (expands to UID 0 on system units)"
else
  pass "setup_runner_user does not interpolate systemd %U for the user-session env"
fi

if grep -Fq 'id -u "${RUNNER_USER}"' "${SETUP}" \
  && grep -Fq 'Environment=XDG_RUNTIME_DIR=/run/user/${runner_uid}' "${SETUP}" \
  && grep -Fq 'Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/${runner_uid}/bus' "${SETUP}"; then
  pass "setup_runner_user writes user-session env with the resolved numeric UID"
else
  fail "setup_runner_user missing numeric-UID XDG_RUNTIME_DIR/DBUS_SESSION_BUS_ADDRESS Environment= lines"
fi

# A VM provisioned before #7453 has User= already; a VM provisioned with
# the #7453 %U drop-in has env lines that expand to /run/user/0 (#7696).
# The skip must require the env lines with the resolved numeric UID so
# both generations converge on re-run.
if grep -B12 'systemd override already in place' "${SETUP}" \
  | grep -Fq 'XDG_RUNTIME_DIR=/run/user/${runner_uid}' \
  && grep -B12 'systemd override already in place' "${SETUP}" \
  | grep -Fq 'DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/${runner_uid}/bus'; then
  pass "setup_runner_user skip path requires numeric-UID env (re-run converges a pre-fix override)"
else
  fail "setup_runner_user skip path does not require numeric-UID env — existing VMs would not converge"
fi

if grep -E '^[[:space:]]+systemctl --user' "${SETUP}" >/dev/null; then
  fail "setup.sh still invokes systemctl --user directly; use user_systemctl so the user-session env is set"
else
  pass "setup.sh routes user-systemd calls through user_systemctl"
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

echo "== setup_runner_user numeric UID generation and convergence =="
write_id_stub
write_sudo_passthrough_stub
write_systemctl_stub seeded

# Generation: a missing drop-in is written with the resolved numeric UID,
# not systemd %U (which expands to 0 on a system-scope unit).
rm -rf "${GITLAB_RUNNER_OVERRIDE_DIR}"
run_setup setup_runner_user
override_file="${GITLAB_RUNNER_OVERRIDE_DIR}/user.conf"
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "setup_runner_user generation should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif [ ! -f "${override_file}" ]; then
  fail "setup_runner_user did not write ${override_file}"
elif grep -Fq '/run/user/%U' "${override_file}"; then
  fail "setup_runner_user still wrote systemd %U into the drop-in"
elif grep -Fq "Environment=XDG_RUNTIME_DIR=/run/user/${FAKE_UID}" "${override_file}" \
  && grep -Fq "Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/${FAKE_UID}/bus" "${override_file}" \
  && grep -Fq "User=testuser" "${override_file}"; then
  pass "setup_runner_user writes /run/user/${FAKE_UID} (resolved UID) into the drop-in"
else
  fail "setup_runner_user drop-in missing numeric-UID env: $(tr '\n' '|' < "${override_file}")"
fi

# Skip: a drop-in that already has the resolved UID is left alone.
write_dropin "/run/user/${FAKE_UID}"
: > "${SUDO_LOG}"
run_setup setup_runner_user
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "setup_runner_user skip should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif grep -q ' tee ' "${SUDO_LOG}" || grep -q '^tee ' "${SUDO_LOG}"; then
  fail "setup_runner_user rewrote a drop-in that already had the resolved UID"
elif printf '%s' "${RUN_SETUP_OUT}" | grep -Fq 'already in place'; then
  pass "setup_runner_user skips rewrite when the drop-in already has the resolved UID"
else
  fail "setup_runner_user did not skip a current drop-in: ${RUN_SETUP_OUT}"
fi

# Convergence: a #7453 %U drop-in is rewritten to the numeric UID.
write_dropin '/run/user/%U'
: > "${SUDO_LOG}"
run_setup setup_runner_user
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "setup_runner_user %U convergence should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif grep -Fq '/run/user/%U' "${override_file}"; then
  fail "setup_runner_user left a %U drop-in in place"
elif grep -Fq "Environment=XDG_RUNTIME_DIR=/run/user/${FAKE_UID}" "${override_file}" \
  && grep -Fq "Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/${FAKE_UID}/bus" "${override_file}"; then
  pass "setup_runner_user rewrites a %U drop-in to the resolved numeric UID"
else
  fail "setup_runner_user %U rewrite produced: $(tr '\n' '|' < "${override_file}")"
fi

# Convergence: /run/user/0 (what %U actually expands to) is also rewritten.
write_dropin '/run/user/0'
: > "${SUDO_LOG}"
run_setup setup_runner_user
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "setup_runner_user UID-0 convergence should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif grep -Fxq 'Environment=XDG_RUNTIME_DIR=/run/user/0' "${override_file}"; then
  fail "setup_runner_user left a /run/user/0 drop-in in place"
elif grep -Fq "Environment=XDG_RUNTIME_DIR=/run/user/${FAKE_UID}" "${override_file}" \
  && grep -Fq "Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/${FAKE_UID}/bus" "${override_file}"; then
  pass "setup_runner_user rewrites a /run/user/0 drop-in to the resolved numeric UID"
else
  fail "setup_runner_user UID-0 rewrite produced: $(tr '\n' '|' < "${override_file}")"
fi

echo "== configure_gateway: pre-0.1 (schema v1) host =="
rm -rf "${FAKE_HOME}/.config/openshell"
mkdir -p "${FAKE_HOME}/.config/openshell"
# The v1 file an 0.0.x setup.sh wrote.
printf '[openshell]\nversion = 1\n\n[openshell.gateway]\nbind_address = "0.0.0.0:17670"\ncompute_drivers = ["podman"]\nsupervisor_image = "ghcr.io/nvidia/openshell/supervisor:0.0.116"\n' > "${GW_TOML}"
cp "${GW_TOML}" "${WORK_DIR}/v1.toml"
run_setup configure_gateway
podman_section=$(awk -v key="supervisor_image = \"${PIN_IMAGE}\"" '/^\[/ { s = $0 } $0 == key { print s }' "${GW_TOML}")
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "configure_gateway should succeed on a v1 host (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif ! cmp -s "${WORK_DIR}/v1.toml" "${GW_TOML}.pre-0.1"; then
  fail "v1 gateway.toml was not moved aside intact to gateway.toml.pre-0.1"
elif ! grep -qx 'version = 2' "${GW_TOML}" || grep -q 'version = 1' "${GW_TOML}"; then
  fail "gateway.toml is not schema v2: $(tr '\n' '|' < "${GW_TOML}")"
elif ! grep -qx 'compute_driver = "podman"' "${GW_TOML}"; then
  fail "gateway.toml dropped compute_driver: $(tr '\n' '|' < "${GW_TOML}")"
elif [ "${podman_section}" != "[openshell.drivers.podman]" ]; then
  fail "supervisor_image not under [openshell.drivers.podman]: $(tr '\n' '|' < "${GW_TOML}")"
elif ! grep -qx 'OPENSHELL_BIND_ADDRESS=0.0.0.0' "${FAKE_HOME}/.config/openshell/gateway.env"; then
  fail "gateway.env bind address not set"
else
  pass "configure_gateway moves a v1 gateway.toml aside and writes v2 with ${PIN_IMAGE} under [openshell.drivers.podman]"
fi

cp "${GW_TOML}" "${WORK_DIR}/v2.before"
cp "${GW_TOML}.pre-0.1" "${WORK_DIR}/pre01.before" 2>/dev/null || : > "${WORK_DIR}/pre01.before"
run_setup configure_gateway
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "configure_gateway re-run should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif cmp -s "${WORK_DIR}/v2.before" "${GW_TOML}" \
  && cmp -s "${WORK_DIR}/pre01.before" "${GW_TOML}.pre-0.1" \
  && [ "$(grep -o 'supervisor_image' "${GW_TOML}" | wc -l | tr -d ' ')" = "1" ]; then
  pass "configure_gateway re-run is a no-op"
else
  fail "configure_gateway re-run changed files: $(tr '\n' '|' < "${GW_TOML}")"
fi
rm -rf "${FAKE_HOME}/.config/openshell"

echo "== install_openshell: breaking-upgrade ack and stale state =="
write_systemctl_stub seeded
INSTALL_ENV_LOG="${SHIM_DIR}/install-env.log"
: > "${INSTALL_ENV_LOG}"
# Like the real 0.1 installer, which starts the gateway: fail unless the
# config is already schema v2 and the pre-0.1 store is gone.
cat > "${WORK_DIR}/install.sh" <<INSTALL
#!/bin/sh
echo "ack=\${OPENSHELL_ACK_BREAKING_UPGRADE:-}" >> "${INSTALL_ENV_LOG}"
if ! grep -Eq '^[[:space:]]*version[[:space:]]*=[[:space:]]*2[[:space:]]*(#.*)?\$' "${GW_TOML}" 2>/dev/null; then
  echo "stub install.sh: gateway config preflight failed: ${GW_TOML} missing or not schema v2" >&2
  exit 1
fi
if [ -e "${FAKE_HOME}/.local/state/openshell/gateway" ] || [ -e "${FAKE_HOME}/.local/state/openshell/tls" ]; then
  echo "stub install.sh: gateway start failed: pre-0.1 state still in ${FAKE_HOME}/.local/state/openshell" >&2
  exit 1
fi
INSTALL
cat > "${SHIM_DIR}/curl" <<CURL
#!/bin/sh
cat "${WORK_DIR}/install.sh"
CURL
printf '#!/bin/sh\nexit 0\n' > "${SHIM_DIR}/podman"
# Host still on 0.0.x.
cat > "${SHIM_DIR}/openshell" <<OS
#!/bin/sh
echo "\$@" >> "${OPENSHELL_LOG}"
case "\$1" in --version) echo "openshell 0.0.116";; esac
exit 0
OS
chmod +x "${SHIM_DIR}/curl" "${SHIM_DIR}/podman" "${SHIM_DIR}/openshell"
mkdir -p "${FAKE_HOME}/.local/state/openshell/gateway" "${FAKE_HOME}/.local/state/openshell/tls"
echo db > "${FAKE_HOME}/.local/state/openshell/gateway/state.db"
run_setup install_openshell
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "install_openshell should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif ! grep -qx 'ack=1' "${INSTALL_ENV_LOG}"; then
  fail "install.sh ran without OPENSHELL_ACK_BREAKING_UPGRADE=1: $(tr '\n' '|' < "${INSTALL_ENV_LOG}")"
elif [ -e "${FAKE_HOME}/.local/state/openshell/gateway" ] || [ -e "${FAKE_HOME}/.local/state/openshell/tls" ]; then
  fail "pre-0.1 gateway store survived install_openshell"
elif ! grep -q 'stop openshell-gateway.service' "${SYSTEMCTL_LOG}"; then
  fail "install_openshell did not stop the gateway before wiping: $(tr '\n' '|' < "${SYSTEMCTL_LOG}")"
elif ! grep -q 'gateway remove openshell' "${OPENSHELL_LOG}"; then
  fail "install_openshell did not drop the stale CLI gateway registration"
else
  pass "install_openshell acks the breaking upgrade and discards pre-0.1 gateway state"
fi

# Already on the pin: no install, state untouched.
write_systemctl_stub seeded
: > "${INSTALL_ENV_LOG}"
cat > "${SHIM_DIR}/openshell" <<OS
#!/bin/sh
echo "\$@" >> "${OPENSHELL_LOG}"
case "\$1" in --version) echo "openshell ${PIN_VERSION}";; esac
exit 0
OS
chmod +x "${SHIM_DIR}/openshell"
mkdir -p "${FAKE_HOME}/.local/state/openshell/gateway"
echo db > "${FAKE_HOME}/.local/state/openshell/gateway/state.db"
cp "${GW_TOML}" "${WORK_DIR}/gw.pinned" 2>/dev/null || : > "${WORK_DIR}/gw.pinned"
run_setup install_openshell
if [ "${RUN_SETUP_RC}" -eq 0 ] && [ ! -s "${INSTALL_ENV_LOG}" ] \
  && [ -e "${FAKE_HOME}/.local/state/openshell/gateway/state.db" ] \
  && cmp -s "${WORK_DIR}/gw.pinned" "${GW_TOML}" && [ ! -e "${GW_TOML}.pre-0.1" ]; then
  pass "install_openshell on the pinned version skips the install and keeps state and config"
else
  fail "install_openshell re-ran on the pinned version (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi
rm -rf "${FAKE_HOME}/.local/state/openshell" "${FAKE_HOME}/.config/openshell"

echo "== install_openshell -> configure_gateway: in-place 0.0.x -> 0.1 upgrade =="
# main's order on a VM provisioned with 0.0.x: v1 config, 0.0.x store and TLS.
upgrade_sequence() {
  install_openshell
  configure_gateway
}
write_systemctl_stub seeded
: > "${INSTALL_ENV_LOG}"
cat > "${SHIM_DIR}/openshell" <<OS
#!/bin/sh
echo "\$@" >> "${OPENSHELL_LOG}"
case "\$1" in --version) echo "openshell 0.0.116";; esac
exit 0
OS
chmod +x "${SHIM_DIR}/openshell"
mkdir -p "${FAKE_HOME}/.config/openshell" \
  "${FAKE_HOME}/.local/state/openshell/gateway" "${FAKE_HOME}/.local/state/openshell/tls"
cp "${WORK_DIR}/v1.toml" "${GW_TOML}"
echo db > "${FAKE_HOME}/.local/state/openshell/gateway/state.db"
echo cert > "${FAKE_HOME}/.local/state/openshell/tls/server.crt"
run_setup upgrade_sequence
podman_section=$(awk -v key="supervisor_image = \"${PIN_IMAGE}\"" '/^\[/ { s = $0 } $0 == key { print s }' "${GW_TOML}")
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "0.0.x -> 0.1 upgrade should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif ! grep -qx 'ack=1' "${INSTALL_ENV_LOG}"; then
  fail "upgrade ran install.sh without the ack: $(tr '\n' '|' < "${INSTALL_ENV_LOG}")"
elif ! cmp -s "${WORK_DIR}/v1.toml" "${GW_TOML}.pre-0.1"; then
  fail "upgrade did not move the v1 gateway.toml aside intact"
elif ! grep -qx 'version = 2' "${GW_TOML}" || [ "${podman_section}" != "[openshell.drivers.podman]" ]; then
  fail "upgrade left gateway.toml without a v2 ${PIN_IMAGE} pin: $(tr '\n' '|' < "${GW_TOML}")"
elif [ -e "${FAKE_HOME}/.local/state/openshell/gateway" ] || [ -e "${FAKE_HOME}/.local/state/openshell/tls" ]; then
  fail "upgrade kept the pre-0.1 gateway store"
else
  pass "upgrade writes v2 config and drops pre-0.1 state before the installer starts the gateway"
fi
cp "${GW_TOML}" "${WORK_DIR}/v2.before"
run_setup configure_gateway
if [ "${RUN_SETUP_RC}" -eq 0 ] && cmp -s "${WORK_DIR}/v2.before" "${GW_TOML}" \
  && cmp -s "${WORK_DIR}/v1.toml" "${GW_TOML}.pre-0.1"; then
  pass "configure_gateway after the upgrade is a no-op"
else
  fail "configure_gateway after the upgrade changed files (rc=${RUN_SETUP_RC}): $(tr '\n' '|' < "${GW_TOML}")"
fi
rm -rf "${FAKE_HOME}/.local/state/openshell" "${FAKE_HOME}/.config/openshell"
printf '#!/bin/sh\necho "$@" >> "%s"\nexit 0\n' "${OPENSHELL_LOG}" > "${SHIM_DIR}/openshell"
rm -f "${SHIM_DIR}/curl" "${SHIM_DIR}/podman"

if [ "${FAILURES}" -ne 0 ]; then
  echo "${FAILURES} case(s) failed" >&2
  exit 1
fi
echo "all cases passed"
