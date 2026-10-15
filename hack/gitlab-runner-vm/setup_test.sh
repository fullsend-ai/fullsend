#!/usr/bin/env bash
# setup_test.sh — Tests for setup.sh idempotency hygiene (patch_config backup
# and stale custom-executor path reconciliation, configured executor path
# verification, configure_per_job_gateway seed-start skip, setup_runner_user
# UID drop-in),
# the OpenShell 0.1 upgrade path (configure_gateway, install_openshell),
# CA hook permissions for rootless Podman (install_ca_hook), and the Fedora
# repo repair (fix_fedora_repos).
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
  local test_host_ca_bundle="${HOST_CA_BUNDLE}"
  local test_ca_hook_script="${CA_HOOK_SCRIPT}"
  local test_oci_hooks_dir="${OCI_HOOKS_DIR}"
  local test_yum_repos_dir="${YUM_REPOS_DIR}"
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
    HOST_CA_BUNDLE="${test_host_ca_bundle}"
    CA_HOOK_SCRIPT="${test_ca_hook_script}"
    OCI_HOOKS_DIR="${test_oci_hooks_dir}"
    YUM_REPOS_DIR="${test_yum_repos_dir}"
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
CA_ROOT="${WORK_DIR}/ca-root"
HOST_CA_BUNDLE="${CA_ROOT}/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem"
CA_HOOK_SCRIPT="${CA_ROOT}/usr/local/bin/inject-ca-certs.sh"
OCI_HOOKS_DIR="${CA_ROOT}/etc/containers/oci/hooks.d"
YUM_REPOS_DIR="${WORK_DIR}/yum.repos.d"
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

# Usage: write_custom_config [home]
# Writes a custom-executor config whose managed paths live under home
# (default: FAKE_HOME, i.e. what setup.sh would write for this user).
write_custom_config() {
  local home="${1:-${FAKE_HOME}}"
  cat > "${CONFIG_TOML}" <<TOML
concurrent = 1
check_interval = 0

[[runners]]
  name = "test"
  url = "https://gitlab.example.com"
  id = 42
  token = "glrt-test"
  executor = "custom"
  builds_dir = "${home}/builds"
  cache_dir = "${home}/cache"
  [runners.cache]
    MaxUploadedArchiveSize = 0
  [runners.custom]
    prepare_exec = "${home}/gitlab-runner-executor/prepare.sh"
    prepare_exec_timeout = 300
    run_exec = "${home}/gitlab-runner-executor/run.sh"
    cleanup_exec = "${home}/gitlab-runner-executor/cleanup.sh"
    cleanup_exec_timeout = 120
TOML
}

# Config lines other than the five managed custom-executor keys.
unmanaged_lines() {
  grep -Ev '^[[:space:]]*(builds_dir|cache_dir|prepare_exec|run_exec|cleanup_exec) =' "$1"
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

echo "== patch_config: reconcile stale custom-executor paths (#8160) =="
# Custom executor already configured for a different home directory.
write_custom_config /home/fedora
cp "${CONFIG_TOML}" "${WORK_DIR}/stale.toml"
rm -f "${CONFIG_TOML}.bak"
run_setup patch_config
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "patch_config on a stale custom config should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif grep -q '/home/fedora' "${CONFIG_TOML}"; then
  fail "patch_config left stale /home/fedora paths: $(tr '\n' '|' < "${CONFIG_TOML}")"
elif ! grep -Fxq "  builds_dir = \"${BUILDS_DIR}\"" "${CONFIG_TOML}" \
  || ! grep -Fxq "  cache_dir = \"${CACHE_DIR}\"" "${CONFIG_TOML}" \
  || ! grep -Fxq "    prepare_exec = \"${EXECUTOR_DIR}/prepare.sh\"" "${CONFIG_TOML}" \
  || ! grep -Fxq "    run_exec = \"${EXECUTOR_DIR}/run.sh\"" "${CONFIG_TOML}" \
  || ! grep -Fxq "    cleanup_exec = \"${EXECUTOR_DIR}/cleanup.sh\"" "${CONFIG_TOML}"; then
  fail "patch_config did not reconcile managed paths to ${FAKE_HOME}: $(tr '\n' '|' < "${CONFIG_TOML}")"
elif [ "$(unmanaged_lines "${WORK_DIR}/stale.toml")" != "$(unmanaged_lines "${CONFIG_TOML}")" ]; then
  fail "patch_config changed unmanaged config (registration/token/other keys): $(tr '\n' '|' < "${CONFIG_TOML}")"
elif ! cmp -s "${WORK_DIR}/stale.toml" "${CONFIG_TOML}.bak"; then
  fail "patch_config did not back up the stale config to config.toml.bak"
elif [ ! -d "${BUILDS_DIR}" ] || [ ! -d "${CACHE_DIR}" ]; then
  fail "patch_config did not create the reconciled builds/cache dirs"
else
  pass "stale custom-executor paths are reconciled; registration and other keys preserved"
fi

# Re-running on the repaired config is a no-op.
cp "${CONFIG_TOML}" "${WORK_DIR}/repaired.toml"
rm -f "${CONFIG_TOML}.bak"
run_setup patch_config
if [ "${RUN_SETUP_RC}" -eq 0 ] && cmp -s "${WORK_DIR}/repaired.toml" "${CONFIG_TOML}" \
  && [ ! -e "${CONFIG_TOML}.bak" ]; then
  pass "patch_config re-run after reconciliation is a no-op"
else
  fail "patch_config re-run changed a reconciled config (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

# Unconventional spacing with current values is not rewritten.
write_custom_config
awk '{ sub(/^    run_exec = /, "    run_exec=") } { print }' "${CONFIG_TOML}" > "${WORK_DIR}/spacing.toml"
cp "${WORK_DIR}/spacing.toml" "${CONFIG_TOML}"
rm -f "${CONFIG_TOML}.bak"
run_setup patch_config
if [ "${RUN_SETUP_RC}" -eq 0 ] && cmp -s "${WORK_DIR}/spacing.toml" "${CONFIG_TOML}" \
  && [ ! -e "${CONFIG_TOML}.bak" ]; then
  pass "patch_config compares values, not formatting"
else
  fail "patch_config rewrote a config whose values were already current (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

# Table headers with trailing comments are still recognised: current config is
# a no-op, stale config is reconciled.
write_custom_config
awk '/^[ \t]*\[/ { $0 = $0 " # section note" } { print }' "${CONFIG_TOML}" > "${WORK_DIR}/hdrcomment.toml"
cp "${WORK_DIR}/hdrcomment.toml" "${CONFIG_TOML}"
rm -f "${CONFIG_TOML}.bak"
run_setup patch_config
if [ "${RUN_SETUP_RC}" -eq 0 ] && cmp -s "${WORK_DIR}/hdrcomment.toml" "${CONFIG_TOML}" \
  && [ ! -e "${CONFIG_TOML}.bak" ]; then
  pass "patch_config no-op on a config with commented table headers"
else
  fail "patch_config mishandled commented table headers (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi
write_custom_config /home/fedora
awk '/^[ \t]*\[/ { $0 = $0 " # section note" } { print }' "${CONFIG_TOML}" > "${WORK_DIR}/hdrcomment.toml"
cp "${WORK_DIR}/hdrcomment.toml" "${CONFIG_TOML}"
run_setup patch_config
if [ "${RUN_SETUP_RC}" -eq 0 ] && ! grep -q '/home/fedora' "${CONFIG_TOML}" \
  && grep -Fxq "    run_exec = \"${EXECUTOR_DIR}/run.sh\"" "${CONFIG_TOML}" \
  && grep -Fxq '  [runners.cache] # section note' "${CONFIG_TOML}"; then
  pass "patch_config reconciles a stale config with commented table headers"
else
  fail "patch_config did not reconcile commented-header config (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

# CRLF line endings: current config is a no-op, stale config is reconciled.
write_custom_config
awk '{ printf "%s\r\n", $0 }' "${CONFIG_TOML}" > "${WORK_DIR}/crlf.toml"
cp "${WORK_DIR}/crlf.toml" "${CONFIG_TOML}"
rm -f "${CONFIG_TOML}.bak"
run_setup patch_config
if [ "${RUN_SETUP_RC}" -eq 0 ] && cmp -s "${WORK_DIR}/crlf.toml" "${CONFIG_TOML}" \
  && [ ! -e "${CONFIG_TOML}.bak" ]; then
  pass "patch_config no-op on a CRLF config"
else
  fail "patch_config mishandled a CRLF config (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi
write_custom_config /home/fedora
awk '{ printf "%s\r\n", $0 }' "${CONFIG_TOML}" > "${WORK_DIR}/crlf.toml"
cp "${WORK_DIR}/crlf.toml" "${CONFIG_TOML}"
run_setup patch_config
if [ "${RUN_SETUP_RC}" -eq 0 ] && ! grep -q '/home/fedora' "${CONFIG_TOML}" \
  && [ "$(grep -c "$(printf '\r')$" "${CONFIG_TOML}")" -eq "$(wc -l < "${CONFIG_TOML}")" ]; then
  pass "patch_config reconciles a stale CRLF config and keeps CRLF endings"
else
  fail "patch_config did not reconcile CRLF config (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

# Current values written with a trailing comment or as a literal string are
# already correct: no write, no backup.
write_custom_config
EXECUTOR_DIR_VAL="${EXECUTOR_DIR}" awk '{ sub(/^  builds_dir = .*/, "&  # note") } { sub(/^    run_exec = "[^"]*"/, "    run_exec = \047" ENVIRON["EXECUTOR_DIR_VAL"] "/run.sh\047") } { print }' "${CONFIG_TOML}" > "${WORK_DIR}/decoded.toml"
cp "${WORK_DIR}/decoded.toml" "${CONFIG_TOML}"
rm -f "${CONFIG_TOML}.bak"
run_setup patch_config
if grep -Fq "run_exec = '${EXECUTOR_DIR}/run.sh'" "${CONFIG_TOML}" \
  && grep -Fq '# note' "${CONFIG_TOML}" \
  && [ "${RUN_SETUP_RC}" -eq 0 ] && cmp -s "${WORK_DIR}/decoded.toml" "${CONFIG_TOML}" \
  && [ ! -e "${CONFIG_TOML}.bak" ]; then
  pass "patch_config no-op for a trailing comment and a literal string"
else
  fail "patch_config rewrote a config whose decoded values were current (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

# setup.sh's fail() writes to stderr; fold it in so the message is checkable.
patch_config_with_stderr() {
  patch_config 2>&1
}

# Multiple [[runners]] blocks: fail clearly, change nothing.
write_custom_config /home/fedora
printf '\n[[runners]]\n  name = "second"\n  executor = "shell"\n' >> "${CONFIG_TOML}"
cp "${CONFIG_TOML}" "${WORK_DIR}/multi.toml"
rm -f "${CONFIG_TOML}.bak"
run_setup patch_config_with_stderr
if [ "${RUN_SETUP_RC}" -eq 0 ]; then
  fail "patch_config should refuse a multi-runner config"
elif ! cmp -s "${WORK_DIR}/multi.toml" "${CONFIG_TOML}" || [ -e "${CONFIG_TOML}.bak" ]; then
  fail "patch_config modified a multi-runner config it refused"
elif printf '%s' "${RUN_SETUP_OUT}" | grep -Fq 'expected exactly 1 [[runners]] block'; then
  pass "multi-runner custom config fails clearly without modification"
else
  fail "patch_config multi-runner failure was unclear: ${RUN_SETUP_OUT}"
fi

# A managed key missing from its table: fail clearly, change nothing.
write_custom_config /home/fedora
grep -v 'cleanup_exec = ' "${CONFIG_TOML}" > "${WORK_DIR}/missing.toml"
cp "${WORK_DIR}/missing.toml" "${CONFIG_TOML}"
rm -f "${CONFIG_TOML}.bak"
run_setup patch_config_with_stderr
if [ "${RUN_SETUP_RC}" -eq 0 ]; then
  fail "patch_config should refuse a custom config missing cleanup_exec"
elif ! cmp -s "${WORK_DIR}/missing.toml" "${CONFIG_TOML}" || [ -e "${CONFIG_TOML}.bak" ]; then
  fail "patch_config modified a custom config it refused"
elif printf '%s' "${RUN_SETUP_OUT}" | grep -Fq 'cleanup_exec in config.toml, found 0'; then
  pass "custom config missing a managed key fails clearly without modification"
else
  fail "patch_config missing-key failure was unclear: ${RUN_SETUP_OUT}"
fi

# A current config with no final newline is left alone (no write, no backup).
write_custom_config
printf '%s' "$(cat "${CONFIG_TOML}")" > "${WORK_DIR}/nonl.toml"
cp "${WORK_DIR}/nonl.toml" "${CONFIG_TOML}"
printf 'old backup\n' > "${CONFIG_TOML}.bak"
run_setup patch_config
if [ "${RUN_SETUP_RC}" -eq 0 ] && cmp -s "${WORK_DIR}/nonl.toml" "${CONFIG_TOML}" \
  && [ "$(cat "${CONFIG_TOML}.bak")" = "old backup" ]; then
  pass "patch_config leaves a current config without a final newline untouched"
else
  fail "patch_config rewrote a current config lacking a final newline (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi
rm -f "${CONFIG_TOML}.bak"

# Paths containing a backslash and a double quote are written as valid TOML
# basic strings, read back exactly, and then left alone on a re-run.
ORIG_BUILDS_DIR="${BUILDS_DIR}"
ORIG_CACHE_DIR="${CACHE_DIR}"
ORIG_EXECUTOR_DIR="${EXECUTOR_DIR}"
ODD_HOME="${WORK_DIR}"'/odd\t"home'
BUILDS_DIR="${ODD_HOME}/builds"
CACHE_DIR="${ODD_HOME}/cache"
EXECUTOR_DIR="${ODD_HOME}/gitlab-runner-executor"
write_custom_config /home/fedora
rm -f "${CONFIG_TOML}.bak"
run_setup patch_config
ODD_ESCAPED="${WORK_DIR}"'/odd\\t\"home'
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "patch_config failed for a path with a backslash and quote (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif ! grep -Fxq "  builds_dir = \"${ODD_ESCAPED}/builds\"" "${CONFIG_TOML}" \
  || ! grep -Fxq "    run_exec = \"${ODD_ESCAPED}/gitlab-runner-executor/run.sh\"" "${CONFIG_TOML}"; then
  fail "patch_config did not TOML-escape the managed paths: $(tr '\n' '|' < "${CONFIG_TOML}")"
else
  cp "${CONFIG_TOML}" "${WORK_DIR}/odd.toml"
  rm -f "${CONFIG_TOML}.bak"
  run_setup patch_config
  if [ "${RUN_SETUP_RC}" -eq 0 ] && cmp -s "${WORK_DIR}/odd.toml" "${CONFIG_TOML}" \
    && [ ! -e "${CONFIG_TOML}.bak" ]; then
    pass "backslash and quote in managed paths round-trip and re-run is a no-op"
  else
    fail "re-run rewrote a config with escaped paths (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
  fi
fi
BUILDS_DIR="${ORIG_BUILDS_DIR}"
CACHE_DIR="${ORIG_CACHE_DIR}"
EXECUTOR_DIR="${ORIG_EXECUTOR_DIR}"

# The rewritten copy holds the runner token and must not outlive a failed run.
write_custom_config /home/fedora
LEAK_TMPDIR=$(mktemp -d)
printf '#!/bin/sh\nexit 1\n' > "${SHIM_DIR}/cp"
chmod +x "${SHIM_DIR}/cp"
TMPDIR="${LEAK_TMPDIR}" run_setup patch_config
rm -f "${SHIM_DIR}/cp"
if [ "${RUN_SETUP_RC}" -ne 0 ] && [ -z "$(ls -A "${LEAK_TMPDIR}")" ]; then
  pass "patch_config removes its temp copy of config.toml when reconciliation fails"
else
  fail "patch_config left a temp copy behind or did not fail (rc=${RUN_SETUP_RC}): $(ls -A "${LEAK_TMPDIR}")"
fi
rm -rf "${LEAK_TMPDIR}"

# executor declared with other valid TOML spellings is still reconciled.
for spelling in 'executor="custom"' "executor = 'custom'"; do
  write_custom_config /home/fedora
  EXECUTOR_SPELLING="${spelling}" awk '/^  executor = / { $0 = "  " ENVIRON["EXECUTOR_SPELLING"] } { print }' "${CONFIG_TOML}" > "${WORK_DIR}/spelling.toml"
  cp "${WORK_DIR}/spelling.toml" "${CONFIG_TOML}"
  rm -f "${CONFIG_TOML}.bak"
  run_setup patch_config
  if [ "${RUN_SETUP_RC}" -eq 0 ] && ! grep -q '/home/fedora' "${CONFIG_TOML}" \
    && cmp -s "${WORK_DIR}/spelling.toml" "${CONFIG_TOML}.bak" \
    && grep -Fxq "  ${spelling}" "${CONFIG_TOML}"; then
    pass "patch_config reconciles a config declaring ${spelling}"
  else
    fail "patch_config did not reconcile ${spelling} (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
  fi
done

# An indented [[runners]] header is counted and reconciled.
write_custom_config /home/fedora
awk '/^\[\[runners\]\]/ { $0 = "  " $0 } { print }' "${CONFIG_TOML}" > "${WORK_DIR}/indented.toml"
cp "${WORK_DIR}/indented.toml" "${CONFIG_TOML}"
run_setup patch_config
if [ "${RUN_SETUP_RC}" -eq 0 ] && ! grep -q '/home/fedora' "${CONFIG_TOML}"; then
  pass "patch_config reconciles a single indented [[runners]] config"
else
  fail "patch_config mishandled an indented [[runners]] header (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

# register_runner runs before patch_config: an indented [[runners]] header
# must count as already registered, not demand a registration token.
cp "${WORK_DIR}/indented.toml" "${CONFIG_TOML}"
unset REGISTRATION_TOKEN
GITLAB_URL="https://gitlab.example.com" run_setup register_runner
if [ "${RUN_SETUP_RC}" -eq 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq 'runner already registered'; then
  pass "register_runner detects an indented [[runners]] header as registered"
else
  fail "register_runner missed an indented [[runners]] header (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

# Mixed indentation with two runners is refused, changing nothing.
write_custom_config /home/fedora
printf '\n  [[runners]]\n    name = "second"\n    executor = "shell"\n' >> "${CONFIG_TOML}"
cp "${CONFIG_TOML}" "${WORK_DIR}/mixed.toml"
rm -f "${CONFIG_TOML}.bak"
run_setup patch_config_with_stderr
if [ "${RUN_SETUP_RC}" -ne 0 ] && cmp -s "${WORK_DIR}/mixed.toml" "${CONFIG_TOML}" \
  && [ ! -e "${CONFIG_TOML}.bak" ] \
  && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq 'expected exactly 1 [[runners]] block in config.toml, found 2'; then
  pass "mixed-indentation multi-runner config fails clearly without modification"
else
  fail "patch_config accepted or mishandled mixed-indentation runners (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

# A multiline TOML string for a managed key cannot be edited line by line: the
# rewrite must be refused and config.toml (and any .bak) left untouched.
write_custom_config /home/fedora
awk '/^    prepare_exec = / { print "    prepare_exec = \"\"\""; print "/home/fedora/gitlab-runner-executor/prepare.sh"; print "\"\"\""; next } { print }' "${CONFIG_TOML}" > "${WORK_DIR}/multiline.toml"
cp "${WORK_DIR}/multiline.toml" "${CONFIG_TOML}"
rm -f "${CONFIG_TOML}.bak"
run_setup patch_config_with_stderr
if [ "${RUN_SETUP_RC}" -ne 0 ] && cmp -s "${WORK_DIR}/multiline.toml" "${CONFIG_TOML}" \
  && [ ! -e "${CONFIG_TOML}.bak" ] \
  && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq 'is not valid TOML'; then
  pass "patch_config refuses a multiline managed value without modifying config.toml"
else
  fail "patch_config installed or mishandled a multiline managed value (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

# No [[runners]] block at all reaches the diagnostic instead of aborting on
# grep -c's exit status.
printf 'concurrent = 1\n' > "${CONFIG_TOML}"
run_setup patch_config_with_stderr
if [ "${RUN_SETUP_RC}" -ne 0 ] \
  && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq 'expected exactly 1 [[runners]] block in config.toml, found 0'; then
  pass "config with no [[runners]] block fails with the diagnostic"
else
  fail "patch_config gave no diagnostic for zero runners (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

echo "== verify_configured_executor_paths (#8160) =="
# Scripts installed under EXECUTOR_DIR, but config.toml points elsewhere.
mkdir -p "${EXECUTOR_DIR}" "${BUILDS_DIR}" "${CACHE_DIR}"
for script in prepare.sh run.sh cleanup.sh; do
  printf '#!/bin/sh\n' > "${EXECUTOR_DIR}/${script}"
  chmod +x "${EXECUTOR_DIR}/${script}"
done
write_custom_config "${WORK_DIR}/nonexistent-home"
run_setup verify_configured_executor_paths
if [ "${RUN_SETUP_RC}" -eq 5 ] \
  && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "configured prepare_exec ${WORK_DIR}/nonexistent-home/gitlab-runner-executor/prepare.sh missing or not executable"; then
  pass "verify flags configured executor paths that do not exist even when EXECUTOR_DIR scripts do"
else
  fail "verify missed stale configured paths (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

# Configured paths match and are accessible.
write_custom_config
run_setup verify_configured_executor_paths
if [ "${RUN_SETUP_RC}" -eq 0 ]; then
  pass "verify accepts configured executor paths that exist and are executable"
else
  fail "verify rejected a correct config (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

# Configured script present but not executable.
chmod -x "${EXECUTOR_DIR}/run.sh"
run_setup verify_configured_executor_paths
if [ "${RUN_SETUP_RC}" -eq 1 ] \
  && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "configured run_exec ${EXECUTOR_DIR}/run.sh missing or not executable"; then
  pass "verify flags a configured executor script that is not executable"
else
  fail "verify missed a non-executable configured script (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

# Configured build dir that is writable but not searchable (mode 0600) is
# unusable by the runner. Skipped when running as root, which bypasses modes.
chmod +x "${EXECUTOR_DIR}/run.sh"
if [ "$(id -u)" -ne 0 ]; then
  chmod 0600 "${BUILDS_DIR}"
  run_setup verify_configured_executor_paths
  chmod 0755 "${BUILDS_DIR}"
  if [ "${RUN_SETUP_RC}" -eq 1 ] \
    && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "configured builds_dir ${BUILDS_DIR} missing or not writable"; then
    pass "verify flags a configured build dir that is not searchable"
  else
    fail "verify accepted a 0600 build dir (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
  fi
fi
rm -rf "${EXECUTOR_DIR}" "${BUILDS_DIR}" "${CACHE_DIR}"

# verify() and the gateway seed skip decide "custom executor" from the decoded
# executor value, so every valid spelling must count and shell must not.
for spelling in 'executor = "custom"' 'executor="custom"' "executor = 'custom'"; do
  write_custom_config
  EXECUTOR_SPELLING="${spelling}" awk '/^  executor = / { $0 = "  " ENVIRON["EXECUTOR_SPELLING"] } { print }' "${CONFIG_TOML}" > "${WORK_DIR}/spelling.toml"
  cp "${WORK_DIR}/spelling.toml" "${CONFIG_TOML}"
  run_setup config_uses_custom_executor
  if [ "${RUN_SETUP_RC}" -eq 0 ]; then
    pass "custom executor check accepts ${spelling}"
  else
    fail "custom executor check rejected ${spelling} (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
  fi
done
write_shell_config
run_setup config_uses_custom_executor
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  pass "custom executor check rejects a shell executor"
else
  fail "custom executor check accepted a shell executor"
fi

echo "== configure_per_job_gateway seed skip =="
# An alternate executor spelling still counts as already seeded.
write_custom_config
EXECUTOR_SPELLING="executor='custom'" awk '/^  executor = / { $0 = "  " ENVIRON["EXECUTOR_SPELLING"] } { print }' "${CONFIG_TOML}" > "${WORK_DIR}/spelling.toml"
cp "${WORK_DIR}/spelling.toml" "${CONFIG_TOML}"
write_systemctl_stub seeded
run_setup configure_per_job_gateway
if [ "${RUN_SETUP_RC}" -eq 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq 'skipping seed start' \
  && ! grep -q 'start openshell-gateway.service' "${SYSTEMCTL_LOG}"; then
  pass "already-seeded VM with executor='custom' skips the gateway start/stop cycle"
else
  fail "seeded re-run with executor='custom' did not skip (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

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

echo "== install_ca_hook: hook resources readable by rootless Podman =="
# sudo runs the command as the test user, so files are created under the
# caller's umask exactly as root's would be on the VM.
cat > "${SHIM_DIR}/sudo" <<STUB
#!/bin/sh
echo "\$@" >> "${SUDO_LOG}"
exec "\$@"
STUB
chmod +x "${SHIM_DIR}/sudo"
: > "${SUDO_LOG}"
write_systemctl_stub seeded

file_mode() {
  stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1"
}

# Prints a description of every hook resource whose mode is wrong.
ca_hook_mode_errors() {
  local oci_dir path want got
  oci_dir="$(dirname "${OCI_HOOKS_DIR}")"
  for spec in \
    "${CA_HOOK_SCRIPT}:755" \
    "${oci_dir}:755" \
    "${OCI_HOOKS_DIR}:755" \
    "${OCI_HOOKS_DIR}/inject-ca-certs.json:644"; do
    path="${spec%:*}"
    want="${spec##*:}"
    got="$(file_mode "${path}" 2>/dev/null || echo missing)"
    if [ "${got}" != "${want}" ]; then
      printf '%s=%s (want %s) ' "${path#"${CA_ROOT}"}" "${got}" "${want}"
    fi
  done
}

install_ca_hook_umask077() {
  umask 077
  install_ca_hook
}

rm -rf "${CA_ROOT}"
# Pre-existing system directories on a provisioned VM.
mkdir -p "${CA_ROOT}/usr/local/bin" "${CA_ROOT}/etc/containers" \
  "$(dirname "${HOST_CA_BUNDLE}")"
echo "fake CA" > "${HOST_CA_BUNDLE}"

# Fresh install under a restrictive umask.
run_setup install_ca_hook_umask077
mode_errors="$(ca_hook_mode_errors)"
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "install_ca_hook under umask 077 should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif [ -n "${mode_errors}" ]; then
  fail "install_ca_hook under umask 077 left hook resources unreadable: ${mode_errors}"
elif ! grep -Fq "\"path\": \"${CA_HOOK_SCRIPT}\"" "${OCI_HOOKS_DIR}/inject-ca-certs.json"; then
  fail "hook JSON does not point at the hook script: $(tr '\n' ' ' < "${OCI_HOOKS_DIR}/inject-ca-certs.json")"
else
  pass "install_ca_hook under umask 077 writes 0755 script/dirs and 0644 hook JSON"
fi

# Existing root-only installation: re-run must repair the modes.
chmod 0700 "$(dirname "${OCI_HOOKS_DIR}")" "${OCI_HOOKS_DIR}"
chmod 0600 "${CA_HOOK_SCRIPT}" "${OCI_HOOKS_DIR}/inject-ca-certs.json"
run_setup install_ca_hook_umask077
mode_errors="$(ca_hook_mode_errors)"
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "install_ca_hook repair should succeed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif [ -n "${mode_errors}" ]; then
  fail "install_ca_hook did not repair root-only hook resources: ${mode_errors}"
else
  pass "install_ca_hook re-run repairs root-only hook dirs and files"
fi

# Idempotent: a third run leaves identical files and modes.
cp "${OCI_HOOKS_DIR}/inject-ca-certs.json" "${WORK_DIR}/hook.json.before"
cp "${CA_HOOK_SCRIPT}" "${WORK_DIR}/hook.sh.before"
run_setup install_ca_hook_umask077
mode_errors="$(ca_hook_mode_errors)"
if [ "${RUN_SETUP_RC}" -eq 0 ] && [ -z "${mode_errors}" ] \
  && cmp -s "${WORK_DIR}/hook.json.before" "${OCI_HOOKS_DIR}/inject-ca-certs.json" \
  && cmp -s "${WORK_DIR}/hook.sh.before" "${CA_HOOK_SCRIPT}"; then
  pass "install_ca_hook re-run is idempotent"
else
  fail "install_ca_hook re-run changed hook files or modes (rc=${RUN_SETUP_RC}): ${mode_errors}"
fi
rm -rf "${CA_ROOT}"
printf '#!/bin/sh\necho "$@" >> "%s"\nexit 0\n' "${SUDO_LOG}" > "${SHIM_DIR}/sudo"

echo "== check_registration =="
# gitlab-runner shim: logs its args. `verify` exits with the code in
# GITLAB_RUNNER_VERIFY_RC (0 = GitLab did not reject the token) after printing
# GITLAB_RUNNER_VERIFY_OUT (real verify prints "is valid" only on success);
# from the GITLAB_RUNNER_VALID_AFTER-th call on it reports the runner valid
# (a GitLab outage that clears). `register` exits with GITLAB_RUNNER_REGISTER_RC.
GITLAB_RUNNER_LOG="${SHIM_DIR}/gitlab-runner.log"
cat > "${SHIM_DIR}/gitlab-runner" <<STUB
#!/bin/sh
echo "\$@" >> "${GITLAB_RUNNER_LOG}"
case "\$1" in
  verify)
    if [ -n "\${GITLAB_RUNNER_VALID_AFTER:-}" ] \\
      && [ "\$(grep -c '^verify ' "${GITLAB_RUNNER_LOG}")" -ge "\${GITLAB_RUNNER_VALID_AFTER}" ]; then
      echo "Verifying runner... is valid"
      exit 0
    fi
    echo "\${GITLAB_RUNNER_VERIFY_OUT:-}"
    exit "\${GITLAB_RUNNER_VERIFY_RC:-0}" ;;
  register)
    exit "\${GITLAB_RUNNER_REGISTER_RC:-0}" ;;
esac
STUB
chmod +x "${SHIM_DIR}/gitlab-runner"
export GITLAB_URL="https://gitlab.example.com"
# Default: GitLab confirms the token. No real waiting between verify retries.
export GITLAB_RUNNER_VERIFY_OUT="Verifying runner... is valid runner=abc"
export VERIFY_RETRY_SEC=0

write_custom_config
rm -f "${GITLAB_RUNNER_LOG}"
run_setup check_registration
if [ "${RUN_SETUP_RC}" -eq 0 ] && grep -qx "verify --config ${CONFIG_TOML}" "${GITLAB_RUNNER_LOG}"; then
  pass "one runner on GITLAB_URL with an accepted token verifies"
else
  fail "valid registration should verify (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

GITLAB_RUNNER_VERIFY_OUT="Verifying runner... is valid runner=abc" run_setup check_registration
if [ "${RUN_SETUP_RC}" -eq 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "(token verified)"; then
  pass "a verify run that reports the runner valid is called verified"
else
  fail "valid verify output should say verified (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

# A zero exit without a success message (GitLab unreachable, unexpected HTTP
# status) is not proof of verification: retry a bounded number of times, then
# fail with the service stopped.
: > "${SUDO_LOG}"
rm -f "${GITLAB_RUNNER_LOG}"
GITLAB_RUNNER_VERIFY_OUT="WARNING: Checking for runner... failed status=503" run_setup check_registration
if [ "${RUN_SETUP_RC}" -ne 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "did not confirm the runner token is valid" \
  && ! printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "(token verified)" \
  && [ "$(grep -c '^verify ' "${GITLAB_RUNNER_LOG}")" -eq 3 ] \
  && grep -qx 'systemctl stop gitlab-runner' "${SUDO_LOG}"; then
  pass "an unconfirmed verify is retried 3 times, then fails with the service stopped"
else
  fail "unconfirmed verify should fail after bounded retries (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

rm -f "${GITLAB_RUNNER_LOG}"
GITLAB_RUNNER_VERIFY_OUT="WARNING: failed status=503" GITLAB_RUNNER_VALID_AFTER=2 run_setup check_registration
if [ "${RUN_SETUP_RC}" -eq 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "(token verified)" \
  && [ "$(grep -c '^verify ' "${GITLAB_RUNNER_LOG}")" -eq 2 ]; then
  pass "a transient GitLab outage that clears within the retries still verifies"
else
  fail "verify should succeed once GitLab confirms (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

GITLAB_URL="https://gitlab.example.com/" run_setup check_registration
if [ "${RUN_SETUP_RC}" -eq 0 ]; then
  pass "a trailing slash on GITLAB_URL still matches"
else
  fail "trailing slash on GITLAB_URL should match (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

GITLAB_URL="https://gitlab.com" run_setup check_registration
if [ "${RUN_SETUP_RC}" -ne 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "expected https://gitlab.com"; then
  pass "a runner registered with another GitLab instance fails"
else
  fail "registration with a different GitLab should fail (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

export GITLAB_RUNNER_VERIFY_RC=1
run_setup check_registration
unset GITLAB_RUNNER_VERIFY_RC
if [ "${RUN_SETUP_RC}" -ne 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "rejected the runner token"; then
  pass "a token GitLab rejects (stale registration) fails"
else
  fail "rejected token should fail (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

write_custom_config
cat >> "${CONFIG_TOML}" <<'TOML'

[[runners]]
  name = "second"
  url = "https://gitlab.example.com"
  token = "glrt-second"
  executor = "custom"
TOML
run_setup check_registration
if [ "${RUN_SETUP_RC}" -ne 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "found 2"; then
  pass "a duplicate registration in config.toml fails"
else
  fail "two [[runners]] entries should fail (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

write_custom_config
printf '  [[runners]]\n    name = "second"\n    url = "https://other.example.com"\n    token = "glrt-second"\n' >> "${CONFIG_TOML}"
run_setup check_registration
if [ "${RUN_SETUP_RC}" -ne 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "found 2"; then
  pass "an indented second [[runners]] entry is counted and fails"
else
  fail "indented second [[runners]] entry should fail (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

write_custom_config
printf 'not [valid toml\n' >> "${CONFIG_TOML}"
run_setup check_registration
if [ "${RUN_SETUP_RC}" -ne 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "cannot parse"; then
  pass "an unparseable config.toml fails closed"
else
  fail "unparseable config.toml should fail (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

write_custom_config
: > "${SUDO_LOG}"
GITLAB_URL="https://gitlab.com" run_setup check_registration
if [ "${RUN_SETUP_RC}" -ne 0 ] && grep -qx 'systemctl stop gitlab-runner' "${SUDO_LOG}"; then
  pass "a failed registration check leaves gitlab-runner stopped"
else
  fail "failed check should stop gitlab-runner (rc=${RUN_SETUP_RC}): $(tr '\n' '|' < "${SUDO_LOG}")"
fi

# Rejection paths claim "left stopped" only when the stop is confirmed with
# `systemctl is-active`; a service that stays active is reported as such.
write_systemctl_stub active
: > "${SUDO_LOG}"
GITLAB_URL="https://gitlab.com" run_setup check_registration
if [ "${RUN_SETUP_RC}" -ne 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "CONTAINMENT FAILED"; then
  pass "a failed registration check reports containment failure when the service stays active"
else
  fail "service that did not stop should be reported (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi
GITLAB_URL="https://gitlab.com" run_setup register_runner
if [ "${RUN_SETUP_RC}" -ne 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "CONTAINMENT FAILED" \
  && ! printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "gitlab-runner left stopped"; then
  pass "a rejected existing config does not claim gitlab-runner was stopped when the stop failed"
else
  fail "rejection must not claim containment when the stop failed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi
write_systemctl_stub seeded

echo "== register_runner first-time registration =="
# No config.toml: the supplied self-hosted URL and token reach `gitlab-runner
# register`, exactly once.
rm -f "${CONFIG_TOML}" "${GITLAB_RUNNER_LOG}"
GITLAB_URL="https://gitlab.selfhosted.example" REGISTRATION_TOKEN="glrt-selfhosted" run_setup register_runner
if [ "${RUN_SETUP_RC}" -eq 0 ] && [ "$(grep -c '^register ' "${GITLAB_RUNNER_LOG}")" -eq 1 ] \
  && grep '^register ' "${GITLAB_RUNNER_LOG}" | grep -Fq -- "--url https://gitlab.selfhosted.example " \
  && grep '^register ' "${GITLAB_RUNNER_LOG}" | grep -Fq -- "--token glrt-selfhosted " \
  && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "runner registered with https://gitlab.selfhosted.example"; then
  pass "first-time registration passes the supplied self-hosted --url and --token once"
else
  fail "first-time register should target the supplied GitLab (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT} / $(cat "${GITLAB_RUNNER_LOG}" 2>/dev/null)"
fi

rm -f "${CONFIG_TOML}" "${GITLAB_RUNNER_LOG}"
GITLAB_URL="https://gitlab.selfhosted.example" REGISTRATION_TOKEN="" run_setup register_runner
if [ "${RUN_SETUP_RC}" -ne 0 ] && [ ! -s "${GITLAB_RUNNER_LOG}" ]; then
  pass "first-time registration without a token fails before calling register"
else
  fail "first-time register without a token should fail (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

echo "== register_runner reusing an existing config =="
write_custom_config
: > "${SUDO_LOG}"
run_setup register_runner
if [ "${RUN_SETUP_RC}" -eq 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "runner already registered" \
  && ! grep -q 'systemctl stop' "${SUDO_LOG}"; then
  pass "an existing config for GITLAB_URL is reused"
else
  fail "existing config for GITLAB_URL should be reused (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

# Shared-pool mode: a supplied registration token must match the config's.
write_custom_config
: > "${SUDO_LOG}"
REGISTRATION_TOKEN="glrt-test" run_setup register_runner
if [ "${RUN_SETUP_RC}" -eq 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "runner already registered" \
  && ! grep -q 'systemctl stop' "${SUDO_LOG}"; then
  pass "an existing config holding the supplied token is reused"
else
  fail "matching supplied token should be reused (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

: > "${SUDO_LOG}"
REGISTRATION_TOKEN="glrt-poolb" run_setup register_runner
if [ "${RUN_SETUP_RC}" -ne 0 ] && grep -qx 'systemctl stop gitlab-runner' "${SUDO_LOG}" \
  && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "different runner token" \
  && ! printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "glrt-poolb" \
  && ! printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "glrt-test"; then
  pass "a supplied token that differs from the config's is rejected with the service stopped, without logging tokens"
else
  fail "mismatched supplied token should be rejected (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

: > "${SUDO_LOG}"
GITLAB_URL="https://gitlab.com" run_setup register_runner
if [ "${RUN_SETUP_RC}" -ne 0 ] && grep -qx 'systemctl stop gitlab-runner' "${SUDO_LOG}"; then
  pass "an existing config for another GitLab is rejected with the service stopped"
else
  fail "existing config for another GitLab should be rejected (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

write_custom_config
printf '  [[runners]]\n    name = "second"\n    url = "https://gitlab.example.com"\n    token = "glrt-second"\n' >> "${CONFIG_TOML}"
run_setup register_runner
if [ "${RUN_SETUP_RC}" -ne 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "found 2"; then
  pass "an existing config with two runners is rejected"
else
  fail "existing config with two runners should be rejected (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi
# Valid TOML spellings of the header must not skip the early check.
write_custom_config
printf '[[ runners ]]\n  name = "second"\n  url = "https://gitlab.example.com"\n  token = "glrt-second"\n' >> "${CONFIG_TOML}"
: > "${SUDO_LOG}"
run_setup register_runner
if [ "${RUN_SETUP_RC}" -ne 0 ] && printf '%s' "${RUN_SETUP_OUT}" | grep -Fq "found 2" \
  && grep -qx 'systemctl stop gitlab-runner' "${SUDO_LOG}"; then
  pass "a '[[ runners ]]' header is counted and rejected before registration"
else
  fail "'[[ runners ]]' second entry should be rejected (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

write_custom_config
sed 's/^\[\[runners\]\]/[["runners"]]/' "${CONFIG_TOML}" > "${CONFIG_TOML}.new" && mv "${CONFIG_TOML}.new" "${CONFIG_TOML}"
: > "${SUDO_LOG}"
GITLAB_URL="https://gitlab.com" run_setup register_runner
if [ "${RUN_SETUP_RC}" -ne 0 ] && grep -qx 'systemctl stop gitlab-runner' "${SUDO_LOG}"; then
  pass "a '[[\"runners\"]]' header for another GitLab is rejected with the service stopped"
else
  fail "'[[\"runners\"]]' config for another GitLab should be rejected (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

write_custom_config
printf 'not [valid toml\n' >> "${CONFIG_TOML}"
: > "${SUDO_LOG}"
run_setup register_runner
if [ "${RUN_SETUP_RC}" -ne 0 ] && grep -qx 'systemctl stop gitlab-runner' "${SUDO_LOG}"; then
  pass "an unparseable existing config is rejected with the service stopped"
else
  fail "unparseable existing config should be rejected (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi
unset GITLAB_URL
rm -f "${SHIM_DIR}/gitlab-runner"

echo "== fix_fedora_repos: HTTPS dl.fedoraproject.org baseurl (#8169) =="
# sudo runs sed for real; dnf config-manager fails so the OpenH264 repo is
# disabled through the sed fallback, as on a host without the plugin.
cat > "${SHIM_DIR}/sudo" <<STUB
#!/bin/sh
echo "\$@" >> "${SUDO_LOG}"
case "\$1" in
  sed) exec "\$@" ;;
  *) exit 1 ;;
esac
STUB
chmod +x "${SHIM_DIR}/sudo"
: > "${SUDO_LOG}"

# write_repo <name> <baseurl line> <metalink line> — one fedora*.repo file.
write_repo() {
  cat > "${YUM_REPOS_DIR}/$1.repo" <<EOF
[$1]
name=$1
$2
$3
enabled=1
gpgcheck=1
EOF
}

# check_repos — print the enabled repo sections without an HTTPS
# dl.fedoraproject.org baseurl (or with metalink= still active), and exit
# non-zero if there are any.
check_repos() {
  python3 - "${YUM_REPOS_DIR}" <<'PY'
import configparser, glob, sys
bad = []
for path in sorted(glob.glob(sys.argv[1] + "/fedora*.repo")):
    cp = configparser.RawConfigParser()
    cp.read(path)
    for section in cp.sections():
        if cp.get(section, "enabled", fallback="1") != "1":
            continue
        baseurl = cp.get(section, "baseurl", fallback="")
        if cp.has_option(section, "metalink"):
            bad.append(f"{section}: metalink= still active")
        elif not baseurl.startswith("https://dl.fedoraproject.org/"):
            bad.append(f"{section}: baseurl={baseurl!r}")
print("; ".join(bad))
sys.exit(1 if bad else 0)
PY
}

rm -rf "${YUM_REPOS_DIR}"
mkdir -p "${YUM_REPOS_DIR}"
# Stock cloud image: metalink= active, placeholder HTTP baseurl commented out.
# ($releasever/$basearch are dnf variables, kept literal.)
write_repo fedora '#baseurl=http://download.example/pub/fedora/linux/releases/$releasever/Everything/$basearch/os/' \
  'metalink=https://mirrors.fedoraproject.org/metalink?repo=fedora-$releasever&arch=$basearch'
# Repair case: an enabled plain-HTTP baseurl and no active metalink=, as an
# older bootstrap leaves it. This used to be skipped (no active metalink=).
write_repo fedora-updates 'baseurl=http://dl.fedoraproject.org/pub/fedora/linux/updates/$releasever/Everything/$basearch/' \
  '#metalink=https://mirrors.fedoraproject.org/metalink?repo=updates-released-f$releasever&arch=$basearch'
# Already repaired: must be left alone.
write_repo fedora-updates-testing 'baseurl=https://dl.fedoraproject.org/pub/fedora/linux/updates/testing/$releasever/Everything/$basearch/' \
  '#metalink=https://mirrors.fedoraproject.org/metalink?repo=updates-testing-f$releasever&arch=$basearch'
write_repo fedora-cisco-openh264 '' \
  'metalink=https://mirrors.fedoraproject.org/metalink?repo=fedora-cisco-openh264-$releasever&arch=$basearch'
cp "${YUM_REPOS_DIR}/fedora-updates-testing.repo" "${WORK_DIR}/https.repo.before"

run_setup fix_fedora_repos
if [ "${RUN_SETUP_RC}" -ne 0 ]; then
  fail "fix_fedora_repos failed (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
elif ! problems=$(check_repos); then
  fail "fix_fedora_repos left unusable enabled repos: ${problems}"
else
  pass "every enabled repo ends on an HTTPS dl.fedoraproject.org baseurl"
fi
if grep -h '^[^#]' "${YUM_REPOS_DIR}"/fedora*.repo | grep -Fq 'download.example'; then
  fail "an active line still points at the download.example placeholder"
else
  pass "no active line points at download.example"
fi
if cmp -s "${WORK_DIR}/https.repo.before" "${YUM_REPOS_DIR}/fedora-updates-testing.repo"; then
  pass "an already-HTTPS repo file is left unchanged"
else
  fail "fix_fedora_repos rewrote an already-HTTPS repo file"
fi

cat "${YUM_REPOS_DIR}"/*.repo > "${WORK_DIR}/repos.before"
run_setup fix_fedora_repos
cat "${YUM_REPOS_DIR}"/*.repo > "${WORK_DIR}/repos.after"
if [ "${RUN_SETUP_RC}" -eq 0 ] && cmp -s "${WORK_DIR}/repos.before" "${WORK_DIR}/repos.after" \
  && grep -Fq 'Fedora repos already using HTTPS baseurl' <<< "${RUN_SETUP_OUT}"; then
  pass "a second run changes no Fedora repo file"
else
  fail "second fix_fedora_repos run was not a no-op (rc=${RUN_SETUP_RC}): ${RUN_SETUP_OUT}"
fi

if [ "${FAILURES}" -ne 0 ]; then
  echo "${FAILURES} case(s) failed" >&2
  exit 1
fi
echo "all cases passed"
