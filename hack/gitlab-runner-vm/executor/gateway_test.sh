#!/usr/bin/env bash
# Unit tests for gateway.sh helpers. Stubs podman/systemctl/openshell/curl
# so the tests run without a real OpenShell install.
#
# Usage: hack/gitlab-runner-vm/executor/gateway_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=gateway.sh
source "${SCRIPT_DIR}/gateway.sh"

FAILURES=0
pass() { echo "ok       $*"; }
fail() {
  echo "FAIL     $*" >&2
  FAILURES=$((FAILURES + 1))
}

# assert_v2_gateway_toml <file> <image> <label>: schema v2, compute_driver
# kept, exactly one supervisor_image, and it sits in [openshell.drivers.podman].
assert_v2_gateway_toml() {
  local toml="$1" image="$2" label="$3" section keys tables
  section=$(awk -v key="supervisor_image = \"${image}\"" '/^\[/ { s = $0 } $0 == key { print s }' "${toml}")
  # || true: a zero count must reach fail(), not trip set -e/pipefail.
  keys=$({ grep -o 'supervisor_image' "${toml}" || true; } | wc -l | tr -d ' ')
  tables=$({ grep -o '^\[openshell\.drivers\.podman\]' "${toml}" || true; } | wc -l | tr -d ' ')
  if grep -qx 'version = 2' "${toml}" \
    && ! grep -Eq '^version[[:space:]]*=[[:space:]]*1' "${toml}" \
    && grep -qx 'compute_driver = "podman"' "${toml}" \
    && [ "${section}" = "[openshell.drivers.podman]" ] \
    && [ "${keys}" = "1" ] && [ "${tables}" = "1" ]; then
    pass "${label}: v2 gateway.toml pins ${image} under [openshell.drivers.podman]"
  else
    fail "${label}: gateway.toml is not the expected v2 layout: $(tr '\n' '|' < "${toml}")"
  fi
}

FAKE_HOME=$(mktemp -d)
SHIM_DIR=$(mktemp -d)
trap 'rm -rf "${FAKE_HOME}" "${SHIM_DIR}"' EXIT
export HOME="${FAKE_HOME}"
export PATH="${SHIM_DIR}:${PATH}"

PODMAN_LOG="${SHIM_DIR}/podman.log"
SYSTEMCTL_LOG="${SHIM_DIR}/systemctl.log"
OPENSHELL_LOG="${SHIM_DIR}/openshell.log"
CURL_LOG="${SHIM_DIR}/curl.log"

# Default stubs. Individual tests rewrite them as needed.
printf '#!/bin/sh\necho "$@" >> "%s"\nexit 0\n' "${PODMAN_LOG}" > "${SHIM_DIR}/podman"
printf '#!/bin/sh\necho "$@" >> "%s"\nexit 0\n' "${SYSTEMCTL_LOG}" > "${SHIM_DIR}/systemctl"
printf '#!/bin/sh\necho "$@" >> "%s"\ncase "$1" in --version) echo "openshell 0.0.83";; gateway) echo "  * openshell";; esac\nexit 0\n' "${OPENSHELL_LOG}" > "${SHIM_DIR}/openshell"
printf '#!/bin/sh\necho "$@" >> "%s"\nexit 0\n' "${CURL_LOG}" > "${SHIM_DIR}/curl"
chmod +x "${SHIM_DIR}/podman" "${SHIM_DIR}/systemctl" "${SHIM_DIR}/openshell" "${SHIM_DIR}/curl"

reset_logs() {
  : > "${PODMAN_LOG}"
  : > "${SYSTEMCTL_LOG}"
  : > "${OPENSHELL_LOG}"
  : > "${CURL_LOG}"
}

echo "== wiring (static) =="
PREPARE="${SCRIPT_DIR}/prepare.sh"
CLEANUP="${SCRIPT_DIR}/cleanup.sh"
SETUP="${SCRIPT_DIR}/../setup.sh"
if grep -Fq 'source "$(dirname "${BASH_SOURCE[0]}")/gateway.sh"' "${PREPARE}" \
  && grep -Fq 'ensure_job_openshell_gateway' "${PREPARE}"; then
  pass "prepare.sh sources gateway.sh and starts a per-job gateway"
else
  fail "prepare.sh does not start a per-job gateway"
fi
if grep -Fq 'source "$(dirname "${BASH_SOURCE[0]}")/gateway.sh"' "${CLEANUP}" \
  && grep -Fq 'teardown_openshell_gateway' "${CLEANUP}"; then
  pass "cleanup.sh sources gateway.sh and tears the gateway down"
else
  fail "cleanup.sh does not tear the gateway down"
fi
if grep -Fq 'systemctl --user enable openshell-gateway.service' "${SETUP}"; then
  fail "setup.sh still enables the long-lived openshell-gateway unit"
else
  pass "setup.sh no longer enables a long-lived gateway"
fi
if grep -Fq 'configure_per_job_gateway' "${SETUP}"; then
  pass "setup.sh configures the per-job gateway (disable after seed start)"
else
  fail "setup.sh missing configure_per_job_gateway"
fi
if grep -E '^[[:space:]]+systemctl --user' "${SCRIPT_DIR}/gateway.sh" >/dev/null; then
  fail "gateway.sh still invokes systemctl --user directly; use user_systemctl so the user-session env is set"
else
  pass "gateway.sh routes user-systemd calls through user_systemctl"
fi
if type ensure_user_systemd_env >/dev/null 2>&1 && type user_systemctl >/dev/null 2>&1; then
  pass "gateway.sh defines ensure_user_systemd_env and user_systemctl"
else
  fail "gateway.sh missing ensure_user_systemd_env / user_systemctl"
fi
if grep -Fq 'EXECUTOR_DIR}/.github/scripts' "${SETUP}"; then
  pass "install_executor ships the version pin alongside the flattened executor scripts"
else
  fail "install_executor does not copy openshell-version.sh into EXECUTOR_DIR"
fi
if type prune_unused_podman_storage >/dev/null 2>&1; then
  pass "gateway.sh defines prune_unused_podman_storage"
else
  fail "gateway.sh missing prune_unused_podman_storage"
fi

echo "== flattened EXECUTOR_DIR layout resolves the version pin (regression: #7244) =="
# install_executor copies job_id.sh/prepare.sh/run.sh/cleanup.sh/gateway.sh into
# a flat EXECUTOR_DIR with no .github/scripts sibling — that's what prepare.sh/
# cleanup.sh actually source at per-job runtime. Reproduce that layout in an
# isolated temp dir (not under this repo checkout, so neither the VM-layout
# nor the repo-checkout relative guess can accidentally resolve) and confirm
# gateway.sh's flattened-layout guess picks up the pin.
FLAT_DIR=$(mktemp -d)
mkdir -p "${FLAT_DIR}/.github/scripts"
cp "${SCRIPT_DIR}/gateway.sh" "${FLAT_DIR}/gateway.sh"
# Use sentinel values distinct from the real pin, so a pass can only mean
# "read from this flattened file" — not a false pass from OPENSHELL_VERSION/
# OPENSHELL_SHA leaking in via the environment (gateway.sh's real pin file
# exports both, and this test's own parent process already sourced gateway.sh
# once at the top of this file).
printf 'OPENSHELL_VERSION=9.9.9\nOPENSHELL_SHA=%s\nexport OPENSHELL_VERSION OPENSHELL_SHA\n' \
  "$(printf 'f%.0s' $(seq 1 40))" > "${FLAT_DIR}/.github/scripts/openshell-version.sh"
if [ -e "${FLAT_DIR}/../.github/scripts" ] || [ -e "${FLAT_DIR}/../../../.github/scripts" ]; then
  fail "flattened-layout test fixture is not isolated: a relative guess other than the flattened one would resolve"
else
  FLAT_OUT=$(env -u OPENSHELL_VERSION -u OPENSHELL_SHA bash -c \
    'source "$1/gateway.sh"; printf "%s %s" "${OPENSHELL_VERSION:-}" "${OPENSHELL_SHA:-}"' _ "${FLAT_DIR}")
  FLAT_VER="${FLAT_OUT%% *}"
  FLAT_SHA="${FLAT_OUT##* }"
  if [ "${FLAT_VER}" = "9.9.9" ] && [ "${FLAT_SHA}" = "$(printf 'f%.0s' $(seq 1 40))" ]; then
    pass "gateway.sh resolves OPENSHELL_VERSION/OPENSHELL_SHA from a flattened EXECUTOR_DIR"
  else
    fail "gateway.sh did not resolve the version pin from a flattened EXECUTOR_DIR layout (version='${FLAT_VER}' sha='${FLAT_SHA}')"
  fi
fi
rm -rf "${FLAT_DIR}"

echo "== parse / version compare =="
if [ "$(parse_openshell_version 'openshell 0.0.116 (commit abc)')" = "0.0.116" ]; then
  pass "parse_openshell_version extracts semver"
else
  fail "parse_openshell_version did not extract 0.0.116"
fi
if [ "$(parse_openshell_version 'openshell 0.1.1')" = "0.1.1" ]; then
  pass "parse_openshell_version extracts a 0.1.x semver"
else
  fail "parse_openshell_version did not extract 0.1.1"
fi
if [ -z "$(parse_openshell_version 'not a version')" ]; then
  pass "parse_openshell_version empty on garbage"
else
  fail "parse_openshell_version should be empty on garbage"
fi
if openshell_versions_differ "0.0.116" "0.0.83"; then
  pass "versions_differ true on mismatch"
else
  fail "versions_differ should be true for 0.0.116 vs 0.0.83"
fi
if openshell_versions_differ "0.1.1" "0.0.116"; then
  pass "versions_differ true across the 0.0.x -> 0.1.x break"
else
  fail "versions_differ should be true for 0.1.1 vs 0.0.116"
fi
if openshell_versions_differ "0.0.116" "0.0.116"; then
  fail "versions_differ should be false when equal"
else
  pass "versions_differ false on match"
fi
if openshell_versions_differ "" "0.0.83"; then
  fail "versions_differ should be false when job version is unknown"
else
  pass "versions_differ false when job version is empty"
fi
if [ "$(openshell_install_script_url abc123def)" = "https://raw.githubusercontent.com/NVIDIA/OpenShell/abc123def/install.sh" ]; then
  pass "install URL is built from a commit SHA, not a job-chosen release tag"
else
  fail "install URL was $(openshell_install_script_url abc123def)"
fi

echo "== wipe store =="
mkdir -p "${HOME}/.local/state/openshell/gateway" "${HOME}/.local/state/openshell/tls"
echo db > "${HOME}/.local/state/openshell/gateway/state.db"
echo cert > "${HOME}/.local/state/openshell/tls/ca.crt"
wipe_openshell_gateway_store
if [ ! -e "${HOME}/.local/state/openshell/gateway" ] \
  && [ ! -e "${HOME}/.local/state/openshell/tls" ]; then
  pass "wipe_openshell_gateway_store removes gateway db and tls"
else
  fail "wipe_openshell_gateway_store left state behind"
fi

echo "== reap sandboxes =="
reset_logs
# podman ps -aq --filter label=... prints an id; podman ps -a --format prints names.
cat > "${SHIM_DIR}/podman" <<'PODMAN'
#!/bin/sh
echo "$@" >> "${PODMAN_LOG}"
for a in "$@"; do
  case "$a" in
    label=openshell.managed=true) echo "ctr-managed"; exit 0 ;;
    '{{.Names}}') echo "openshell-ws---abc"; echo "runner-1"; exit 0 ;;
  esac
done
exit 0
PODMAN
# The heredoc cannot expand PODMAN_LOG (quoted), so rewrite the log path.
sed -i "s|\${PODMAN_LOG}|${PODMAN_LOG}|" "${SHIM_DIR}/podman"
chmod +x "${SHIM_DIR}/podman"
reap_openshell_sandboxes
if grep -q 'rm -f -- ctr-managed' "${PODMAN_LOG}" \
  && grep -q 'rm -f -- openshell-ws---abc' "${PODMAN_LOG}" \
  && ! grep -q 'rm -f -- runner-1' "${PODMAN_LOG}"; then
  pass "reap_openshell_sandboxes removes labeled and openshell-* containers only"
else
  fail "reap_openshell_sandboxes podman log: $(tr '\n' '|' < "${PODMAN_LOG}")"
fi

echo "== reap orphaned runner containers =="
reset_logs
cat > "${SHIM_DIR}/podman" <<'PODMAN'
#!/bin/sh
echo "$@" >> "${PODMAN_LOG}"
for a in "$@"; do
  case "$a" in
    '{{.Names}}') echo "runner-1"; echo "runner-2"; echo "openshell-abc"; exit 0 ;;
  esac
done
exit 0
PODMAN
sed -i "s|\${PODMAN_LOG}|${PODMAN_LOG}|" "${SHIM_DIR}/podman"
chmod +x "${SHIM_DIR}/podman"
reap_orphaned_runner_containers "runner-2"
if grep -q 'rm -f -- runner-1' "${PODMAN_LOG}" \
  && ! grep -q 'rm -f -- runner-2' "${PODMAN_LOG}" \
  && ! grep -q 'rm -f -- openshell-abc' "${PODMAN_LOG}"; then
  pass "reap_orphaned_runner_containers removes other runner-* containers, keeps the caller's own and openshell-*"
else
  fail "reap_orphaned_runner_containers podman log: $(tr '\n' '|' < "${PODMAN_LOG}")"
fi

reset_logs
cat > "${SHIM_DIR}/podman" <<'PODMAN'
#!/bin/sh
echo "$@" >> "${PODMAN_LOG}"
for a in "$@"; do
  case "$a" in
    '{{.Names}}') echo "runner-9"; exit 0 ;;
  esac
done
exit 0
PODMAN
sed -i "s|\${PODMAN_LOG}|${PODMAN_LOG}|" "${SHIM_DIR}/podman"
chmod +x "${SHIM_DIR}/podman"
reap_orphaned_runner_containers "runner-9"
if ! grep -q 'rm -f -- runner-9' "${PODMAN_LOG}"; then
  pass "reap_orphaned_runner_containers never removes the caller's own container name"
else
  fail "reap_orphaned_runner_containers removed its own container: $(tr '\n' '|' < "${PODMAN_LOG}")"
fi

echo "== start_fresh wipes then starts (does not enable) =="
reset_logs
mkdir -p "${HOME}/.local/state/openshell/gateway"
echo stale > "${HOME}/.local/state/openshell/gateway/state.db"
printf '#!/bin/sh\necho "$@" >> "%s"\nexit 0\n' "${PODMAN_LOG}" > "${SHIM_DIR}/podman"
chmod +x "${SHIM_DIR}/podman"
if start_fresh_openshell_gateway; then
  if [ ! -e "${HOME}/.local/state/openshell/gateway/state.db" ]; then
    pass "start_fresh_openshell_gateway wipes the stale store"
  else
    fail "start_fresh_openshell_gateway left the stale store"
  fi
  if grep -q 'start openshell-gateway.service' "${SYSTEMCTL_LOG}" \
    && ! grep -q 'enable openshell-gateway.service' "${SYSTEMCTL_LOG}"; then
    pass "start_fresh starts the unit and never enables it"
  else
    fail "systemctl log: $(tr '\n' '|' < "${SYSTEMCTL_LOG}")"
  fi
  if grep -q 'gateway add --local' "${OPENSHELL_LOG}"; then
    pass "start_fresh re-registers the CLI after cert regen"
  else
    fail "CLI was not re-registered: $(tr '\n' '|' < "${OPENSHELL_LOG}")"
  fi
else
  fail "start_fresh_openshell_gateway returned non-zero"
fi

echo "== ensure_user_systemd_env pins canonical paths (does not trust inherited env) =="
unset XDG_RUNTIME_DIR || true
unset DBUS_SESSION_BUS_ADDRESS || true
export XDG_RUNTIME_DIR=/wrong
export DBUS_SESSION_BUS_ADDRESS=unix:path=/wrong
ensure_user_systemd_env
expected_runtime="/run/user/$(id -u)"
expected_bus="unix:path=${expected_runtime}/bus"
if [ "${XDG_RUNTIME_DIR}" = "${expected_runtime}" ] \
  && [ "${DBUS_SESSION_BUS_ADDRESS}" = "${expected_bus}" ]; then
  pass "ensure_user_systemd_env overwrites inherited values with canonical paths"
else
  fail "ensure_user_systemd_env left XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR} DBUS_SESSION_BUS_ADDRESS=${DBUS_SESSION_BUS_ADDRESS}"
fi

echo "== start/stop helpers set user-systemd env themselves =="
reset_logs
# GitLab Runner is a system service and does not inherit a login session.
# Simulate that by unsetting both variables; start/stop must still succeed
# and the systemctl stub must observe the canonical paths (regression: #7453).
cat > "${SHIM_DIR}/systemctl" <<SYS
#!/bin/sh
echo "\$@" >> "${SYSTEMCTL_LOG}"
if [ -z "\${XDG_RUNTIME_DIR:-}" ] || [ -z "\${DBUS_SESSION_BUS_ADDRESS:-}" ]; then
  echo "Failed to connect to user scope bus via local transport: env not defined" >&2
  exit 1
fi
printf 'XDG_RUNTIME_DIR=%s\\nDBUS_SESSION_BUS_ADDRESS=%s\\n' \\
  "\${XDG_RUNTIME_DIR}" "\${DBUS_SESSION_BUS_ADDRESS}" > "${SYSTEMCTL_LOG}.env"
exit 0
SYS
chmod +x "${SHIM_DIR}/systemctl"
printf '#!/bin/sh\necho "$@" >> "%s"\nexit 0\n' "${PODMAN_LOG}" > "${SHIM_DIR}/podman"
chmod +x "${SHIM_DIR}/podman"

unset XDG_RUNTIME_DIR || true
unset DBUS_SESSION_BUS_ADDRESS || true
if ! start_fresh_openshell_gateway; then
  fail "start_fresh_openshell_gateway should succeed without inherited user-session env"
elif grep -q "XDG_RUNTIME_DIR=${expected_runtime}" "${SYSTEMCTL_LOG}.env" \
  && grep -q "DBUS_SESSION_BUS_ADDRESS=${expected_bus}" "${SYSTEMCTL_LOG}.env"; then
  pass "start_fresh sets XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS itself"
else
  fail "start_fresh did not pin user-session env: $(tr '\n' '|' < "${SYSTEMCTL_LOG}.env" 2>/dev/null)"
fi

reset_logs
rm -f "${SYSTEMCTL_LOG}.env"
unset XDG_RUNTIME_DIR || true
unset DBUS_SESSION_BUS_ADDRESS || true
stop_openshell_gateway
if [ ! -f "${SYSTEMCTL_LOG}.env" ]; then
  fail "stop_openshell_gateway did not invoke systemctl with user-session env"
elif grep -q "XDG_RUNTIME_DIR=${expected_runtime}" "${SYSTEMCTL_LOG}.env" \
  && grep -q "DBUS_SESSION_BUS_ADDRESS=${expected_bus}" "${SYSTEMCTL_LOG}.env"; then
  pass "stop_openshell_gateway sets XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS itself"
else
  fail "stop did not pin user-session env: $(tr '\n' '|' < "${SYSTEMCTL_LOG}.env")"
fi
# Restore the default stub so later cases do not require the env file.
printf '#!/bin/sh\necho "$@" >> "%s"\nexit 0\n' "${SYSTEMCTL_LOG}" > "${SHIM_DIR}/systemctl"
chmod +x "${SHIM_DIR}/systemctl"

echo "== teardown stops gateway and wipes =="
reset_logs
mkdir -p "${HOME}/.local/state/openshell/gateway"
echo db > "${HOME}/.local/state/openshell/gateway/state.db"
teardown_openshell_gateway
if [ ! -e "${HOME}/.local/state/openshell/gateway" ] \
  && grep -q 'stop openshell-gateway.service' "${SYSTEMCTL_LOG}"; then
  pass "teardown_openshell_gateway stops the unit and wipes the store"
else
  fail "teardown did not stop+wipe"
fi

echo "== pin_supervisor_image writes a schema-v2 gateway.toml =="
GW_TOML="${HOME}/.config/openshell/gateway.toml"
IMG_NEW="ghcr.io/nvidia/openshell/supervisor:0.1.1"
reset_gateway_toml() { rm -rf "${HOME}/.config/openshell"; }

# No config, no packaged default: literal v2 fallback.
reset_gateway_toml
export OPENSHELL_PACKAGED_GATEWAY_TOML="${SHIM_DIR}/no-packaged-default"
pin_supervisor_image 0.1.1 >/dev/null
assert_v2_gateway_toml "${GW_TOML}" "${IMG_NEW}" "fresh, literal fallback"
if grep -qx 'health_check_interval_secs = 10' "${GW_TOML}"; then
  pass "literal fallback keeps the packaged health_check_interval_secs"
else
  fail "literal fallback dropped health_check_interval_secs: $(tr '\n' '|' < "${GW_TOML}")"
fi

# Idempotent: a second run is byte-identical.
cp "${GW_TOML}" "${SHIM_DIR}/gw.before"
pin_supervisor_image 0.1.1 >/dev/null
if cmp -s "${SHIM_DIR}/gw.before" "${GW_TOML}" && [ ! -e "${GW_TOML}.pre-0.1" ]; then
  pass "re-run of pin_supervisor_image is a no-op"
else
  fail "re-run changed gateway.toml: $(tr '\n' '|' < "${GW_TOML}")"
fi

# Version bump on a v2 file rewrites the key in place.
pin_supervisor_image 0.1.2 >/dev/null
assert_v2_gateway_toml "${GW_TOML}" "ghcr.io/nvidia/openshell/supervisor:0.1.2" "v2 version bump"

# Packaged v2 default (upstream deploy/rpm/gateway.toml.default at v0.1.1).
reset_gateway_toml
cat > "${SHIM_DIR}/packaged-default.toml" <<'TOML'
# Default gateway configuration for RPM installs.
[openshell]
version = 2

[openshell.gateway]
compute_driver = "podman"

[openshell.drivers.podman]
health_check_interval_secs = 10
TOML
export OPENSHELL_PACKAGED_GATEWAY_TOML="${SHIM_DIR}/packaged-default.toml"
pin_supervisor_image 0.1.1 >/dev/null
assert_v2_gateway_toml "${GW_TOML}" "${IMG_NEW}" "seeded from packaged default"
if grep -q '^# Default gateway configuration for RPM installs.' "${GW_TOML}"; then
  pass "packaged default is the seed, not the literal fallback"
else
  fail "packaged default was not used as the seed"
fi

# A pre-0.1 packaged default (v1) is never used as a seed.
reset_gateway_toml
printf '[openshell]\nversion = 1\n\n[openshell.gateway]\ncompute_drivers = ["podman"]\n' \
  > "${SHIM_DIR}/packaged-v1.toml"
export OPENSHELL_PACKAGED_GATEWAY_TOML="${SHIM_DIR}/packaged-v1.toml"
pin_supervisor_image 0.1.1 >/dev/null
assert_v2_gateway_toml "${GW_TOML}" "${IMG_NEW}" "v1 packaged default ignored"

# A version-less file (older pin_supervisor_image output) is pre-0.1 too.
reset_gateway_toml
mkdir -p "${HOME}/.config/openshell"
printf '[openshell.gateway]\nsupervisor_image = "ghcr.io/nvidia/openshell/supervisor:0.0.116"\n' > "${GW_TOML}"
cp "${GW_TOML}" "${SHIM_DIR}/versionless.toml"
export OPENSHELL_PACKAGED_GATEWAY_TOML="${SHIM_DIR}/no-packaged-default"
pin_supervisor_image 0.1.1 >/dev/null
assert_v2_gateway_toml "${GW_TOML}" "${IMG_NEW}" "version-less file replaced"
if cmp -s "${SHIM_DIR}/versionless.toml" "${GW_TOML}.pre-0.1"; then
  pass "version-less gateway.toml moved aside to gateway.toml.pre-0.1"
else
  fail "version-less gateway.toml was not moved aside intact"
fi
# The backup is not rewritten by a later run.
cp "${GW_TOML}.pre-0.1" "${SHIM_DIR}/pre01.before" 2>/dev/null || : > "${SHIM_DIR}/pre01.before"
pin_supervisor_image 0.1.1 >/dev/null
if cmp -s "${SHIM_DIR}/pre01.before" "${GW_TOML}.pre-0.1"; then
  pass "gateway.toml.pre-0.1 is left alone on re-run"
else
  fail "re-run rewrote gateway.toml.pre-0.1"
fi

# A v2 file without [openshell.drivers.podman] gets the table appended.
reset_gateway_toml
mkdir -p "${HOME}/.config/openshell"
printf '[openshell]\nversion = 2\n\n[openshell.gateway]\ncompute_driver = "podman"\n' > "${GW_TOML}"
pin_supervisor_image 0.1.1 >/dev/null
assert_v2_gateway_toml "${GW_TOML}" "${IMG_NEW}" "v2 without drivers table"
reset_gateway_toml

echo "== install_openshell_at_version rejects junk =="
if install_openshell_at_version "../evil" 2>/dev/null; then
  fail "install_openshell_at_version accepted a non-semver version"
else
  pass "install_openshell_at_version rejects non-semver"
fi

echo "== ensure_job_openshell_gateway installs on mismatch (only the Renovate pin) =="
reset_logs
if [ -z "${OPENSHELL_VERSION:-}" ] || [ -z "${OPENSHELL_SHA:-}" ]; then
  fail "OPENSHELL_VERSION/OPENSHELL_SHA were not sourced from .github/scripts/openshell-version.sh"
else
  pass "gateway.sh sourced the Renovate-tracked OpenShell pin (${OPENSHELL_VERSION})"
fi
# Job image reports the Renovate-pinned version; host stub reports 0.0.1 (stale).
cat > "${SHIM_DIR}/podman" <<PODMAN
#!/bin/sh
echo "\$@" >> "${PODMAN_LOG}"
for a in "\$@"; do
  case "\$a" in
    --version) echo "openshell ${OPENSHELL_VERSION}"; exit 0 ;;
  esac
done
# podman run --entrypoint openshell -- IMAGE --version
if [ "\$1" = "run" ]; then
  echo "openshell ${OPENSHELL_VERSION}"
  exit 0
fi
exit 0
PODMAN
chmod +x "${SHIM_DIR}/podman"
cat > "${SHIM_DIR}/openshell" <<'OS'
#!/bin/sh
case "$1" in
  --version) echo "openshell 0.0.1";;
  gateway) echo "  * openshell";;
esac
exit 0
OS
chmod +x "${SHIM_DIR}/openshell"

# curl | sh: emit a stub installer that records the env install.sh sees.
INSTALL_ENV_LOG="${SHIM_DIR}/install-env.log"
cat > "${SHIM_DIR}/install.sh" <<INSTALL
#!/bin/sh
echo "ack=\${OPENSHELL_ACK_BREAKING_UPGRADE:-} version=\${OPENSHELL_VERSION:-}" >> "${INSTALL_ENV_LOG}"
INSTALL
cat > "${SHIM_DIR}/curl" <<CURL
#!/bin/sh
echo "\$@" >> "${CURL_LOG}"
cat "${SHIM_DIR}/install.sh"
exit 0
CURL
chmod +x "${SHIM_DIR}/curl"

# Pre-0.1 host: a schema-v1 gateway.toml and a 0.0.x gateway store.
mkdir -p "${HOME}/.config/openshell" "${HOME}/.local/state/openshell/gateway"
V1_TOML='[openshell]
version = 1

[openshell.gateway]
compute_drivers = ["podman"]
supervisor_image = "ghcr.io/nvidia/openshell/supervisor:0.0.1"'
printf '%s\n' "${V1_TOML}" > "${HOME}/.config/openshell/gateway.toml"
rm -f "${HOME}/.config/openshell/gateway.toml.pre-0.1"
echo db > "${HOME}/.local/state/openshell/gateway/state.db"
export OPENSHELL_PACKAGED_GATEWAY_TOML="${SHIM_DIR}/no-packaged-default"

if ensure_job_openshell_gateway "registry.example.com/runner:dev"; then
  if grep -q "NVIDIA/OpenShell/${OPENSHELL_SHA}/install.sh" "${CURL_LOG}"; then
    pass "mismatch installs from the Renovate-pinned commit SHA, not a job-chosen tag"
  else
    fail "curl log missing SHA-pinned URL: $(tr '\n' '|' < "${CURL_LOG}")"
  fi
  if grep -q -- '--cap-drop=ALL' "${PODMAN_LOG}" && grep -q -- '--security-opt=no-new-privileges' "${PODMAN_LOG}"; then
    pass "job image version probe is hardened like the real job container"
  else
    fail "podman log missing hardening flags: $(tr '\n' '|' < "${PODMAN_LOG}")"
  fi
  if grep -qx "ack=1 version=v${OPENSHELL_VERSION}" "${INSTALL_ENV_LOG}"; then
    pass "install.sh runs with OPENSHELL_ACK_BREAKING_UPGRADE=1"
  else
    fail "install.sh env missing the breaking-upgrade ack: $(tr '\n' '|' < "${INSTALL_ENV_LOG}")"
  fi
  assert_v2_gateway_toml "${HOME}/.config/openshell/gateway.toml" \
    "ghcr.io/nvidia/openshell/supervisor:${OPENSHELL_VERSION}" "mismatch install"
  if [ "$(cat "${HOME}/.config/openshell/gateway.toml.pre-0.1")" = "${V1_TOML}" ]; then
    pass "schema-v1 gateway.toml moved aside unchanged to gateway.toml.pre-0.1"
  else
    fail "gateway.toml.pre-0.1 missing or altered: $(cat "${HOME}/.config/openshell/gateway.toml.pre-0.1" 2>&1)"
  fi
  if [ ! -e "${HOME}/.local/state/openshell/gateway/state.db" ]; then
    pass "pre-0.1 gateway store is wiped, not reused"
  else
    fail "pre-0.1 gateway store survived the upgrade"
  fi
else
  fail "ensure_job_openshell_gateway returned non-zero on mismatch"
fi

echo "== ensure_job_openshell_gateway refuses a job version outside the Renovate pin =="
reset_logs
cat > "${SHIM_DIR}/podman" <<'PODMAN'
#!/bin/sh
echo "$@" >> "${PODMAN_LOG}"
if [ "$1" = "run" ]; then
  echo "openshell 9.9.9"
  exit 0
fi
exit 0
PODMAN
sed -i "s|\${PODMAN_LOG}|${PODMAN_LOG}|" "${SHIM_DIR}/podman"
chmod +x "${SHIM_DIR}/podman"
: > "${CURL_LOG}"
if ensure_job_openshell_gateway "registry.example.com/runner:dev" 2>/dev/null; then
  fail "ensure_job_openshell_gateway must refuse a job-chosen version outside the Renovate pin"
elif [ -s "${CURL_LOG}" ]; then
  fail "must not curl-install an unpinned version: $(tr '\n' '|' < "${CURL_LOG}")"
else
  pass "job image requesting a non-pinned OpenShell version is refused without installing"
fi

echo "== job_image_openshell_version fails hard on a podman error instead of silently keeping the host version =="
reset_logs
cat > "${SHIM_DIR}/podman" <<'PODMAN'
#!/bin/sh
echo "$@" >> "${PODMAN_LOG}"
if [ "$1" = "run" ]; then
  echo "OCI runtime exec failed: exec: openshell: executable file not found" >&2
  exit 126
fi
exit 0
PODMAN
sed -i "s|\${PODMAN_LOG}|${PODMAN_LOG}|" "${SHIM_DIR}/podman"
chmod +x "${SHIM_DIR}/podman"
if job_image_openshell_version "registry.example.com/runner:dev" >/dev/null 2>/dev/null; then
  fail "job_image_openshell_version should fail on a podman error, not report a version"
else
  pass "job_image_openshell_version propagates a hard podman error instead of masking it as 'no CLI'"
fi

echo "== ensure_job_openshell_gateway skips install on match =="
reset_logs
cat > "${SHIM_DIR}/openshell" <<OS
#!/bin/sh
echo "\$@" >> "${OPENSHELL_LOG}"
case "\$1" in
  --version) echo "openshell 0.1.1";;
  gateway) echo "  * openshell";;
esac
exit 0
OS
chmod +x "${SHIM_DIR}/openshell"
cat > "${SHIM_DIR}/podman" <<PODMAN
#!/bin/sh
echo "\$@" >> "${PODMAN_LOG}"
if [ "\$1" = "run" ]; then
  echo "openshell 0.1.1"
  exit 0
fi
exit 0
PODMAN
chmod +x "${SHIM_DIR}/podman"
: > "${CURL_LOG}"
if ensure_job_openshell_gateway "registry.example.com/runner:dev"; then
  if [ -s "${CURL_LOG}" ]; then
    fail "matched versions should not curl-install: $(tr '\n' '|' < "${CURL_LOG}")"
  else
    pass "matched versions skip the install"
  fi
else
  fail "ensure_job_openshell_gateway returned non-zero on match"
fi

echo "== prune_unused_podman_storage =="
reset_logs
# No helper installed: must be a no-op (existing VMs before re-provision).
if prune_unused_podman_storage; then
  pass "prune_unused_podman_storage is a no-op when the helper is missing"
else
  fail "prune_unused_podman_storage should succeed when the helper is missing"
fi

mkdir -p "${FAKE_HOME}/.local/lib/fullsend"
PRUNE_LOG="${SHIM_DIR}/prune.log"
printf '#!/bin/sh\necho ran >> "%s"\nexit 0\n' "${PRUNE_LOG}" \
  > "${FAKE_HOME}/.local/lib/fullsend/podman-prune.sh"
chmod +x "${FAKE_HOME}/.local/lib/fullsend/podman-prune.sh"
: > "${PRUNE_LOG}"
if prune_unused_podman_storage \
  && grep -Fq ran "${PRUNE_LOG}"; then
  pass "prune_unused_podman_storage invokes the installed helper"
else
  fail "prune_unused_podman_storage did not invoke the installed helper"
fi
printf '#!/bin/sh\necho ran >> "%s"\nexit 1\n' "${PRUNE_LOG}" \
  > "${FAKE_HOME}/.local/lib/fullsend/podman-prune.sh"
chmod +x "${FAKE_HOME}/.local/lib/fullsend/podman-prune.sh"
: > "${PRUNE_LOG}"
if prune_unused_podman_storage; then
  pass "prune_unused_podman_storage ignores helper failure"
else
  fail "prune_unused_podman_storage must not fail the job stage when prune errors"
fi

# prepare.sh passes the job's own image so podman-prune.sh protects it from
# rmi during the pre-pull invocation (#7663).
printf '#!/bin/sh\necho "extra=${FULLSEND_PODMAN_PRUNE_EXTRA_KEEP}" >> "%s"\nexit 0\n' "${PRUNE_LOG}" \
  > "${FAKE_HOME}/.local/lib/fullsend/podman-prune.sh"
chmod +x "${FAKE_HOME}/.local/lib/fullsend/podman-prune.sh"
: > "${PRUNE_LOG}"
if prune_unused_podman_storage "registry.example.com/job:latest" \
  && grep -Fq "extra=registry.example.com/job:latest" "${PRUNE_LOG}"; then
  pass "prune_unused_podman_storage forwards its argument as the extra keep-ref"
else
  fail "prune_unused_podman_storage did not forward the extra keep-ref: $(tr '\n' '|' < "${PRUNE_LOG}")"
fi

if [ "${FAILURES}" -ne 0 ]; then
  echo "${FAILURES} case(s) failed" >&2
  exit 1
fi
echo "all cases passed"
