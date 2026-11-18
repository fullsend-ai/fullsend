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

echo "== job_image_release_version only accepts release tags =="
for _case in "ghcr.io/fullsend-ai/fullsend-runner:1.2.3|1.2.3" \
  "registry:5000/x/runner:0.9.0-rc.1|0.9.0-rc.1" \
  "ghcr.io/fullsend-ai/fullsend-runner:1.2.3@sha256:abc|1.2.3"; do
  _ref="${_case%%|*}"
  if [ "$(job_image_release_version "${_ref}" || true)" = "${_case##*|}" ]; then
    pass "job_image_release_version treats ${_ref} as release ${_case##*|}"
  else
    fail "job_image_release_version did not accept ${_ref}"
  fi
done
for _ref in "ghcr.io/fullsend-ai/fullsend-runner:dev" "ghcr.io/fullsend-ai/fullsend-runner:latest" \
  "ghcr.io/fullsend-ai/fullsend-runner:1.2" "ghcr.io/fullsend-ai/fullsend-runner:abc1234" \
  "ghcr.io/fullsend-ai/fullsend-runner" "ghcr.io/fullsend-ai/fullsend-runner@sha256:abc" \
  "registry:5000/runner"; do
  if job_image_release_version "${_ref}" >/dev/null; then
    fail "job_image_release_version must not treat ${_ref} as a release image"
  else
    pass "job_image_release_version rejects ${_ref} (moving tag, digest, or untagged)"
  fi
done

# --- Provenance fixtures -----------------------------------------------------
# podman, openshell, curl and the installer are driven by files so each case
# only states the world it runs in. The VM-local pin (OPENSHELL_VERSION /
# OPENSHELL_SHA, sourced from .github/scripts/openshell-version.sh) is set to a
# version no case uses, to show it plays no part in per-job resolution.
NET_DIR="${SHIM_DIR}/net"
HOST_VERSION_FILE="${SHIM_DIR}/host-version"
JOB_VERSION_OUT_FILE="${SHIM_DIR}/job-version-out"
IMAGE_REVISION_FILE="${SHIM_DIR}/image-revision"
IMAGE_DIGESTS_FILE="${SHIM_DIR}/image-digests"
INSTALL_ENV_LOG="${SHIM_DIR}/install-env.log"
INSTALLER_NOOP_FILE="${SHIM_DIR}/installer-noop"
mkdir -p "${NET_DIR}"

SHA_NEW="$(printf 'a%.0s' $(seq 1 40))"
SHA_OLD="$(printf 'b%.0s' $(seq 1 40))"
SHA_PROVISION="$(printf 'c%.0s' $(seq 1 40))"
REV_NEW="$(printf '1%.0s' $(seq 1 40))"
REV_OLD="$(printf '2%.0s' $(seq 1 40))"
REV_FORK="$(printf '3%.0s' $(seq 1 40))"
REV_MOVED="$(printf '4%.0s' $(seq 1 40))"
REV_BADPIN="$(printf '5%.0s' $(seq 1 40))"
REV_MISMATCH="$(printf '6%.0s' $(seq 1 40))"
REV_OFFLINE="$(printf '7%.0s' $(seq 1 40))"
_saved_version="${OPENSHELL_VERSION:-}"
_saved_sha="${OPENSHELL_SHA:-}"
OPENSHELL_VERSION=7.0.0
OPENSHELL_SHA="${SHA_PROVISION}"

cat > "${SHIM_DIR}/podman" <<PODMAN
#!/bin/sh
echo "\$@" >> "${PODMAN_LOG}"
case "\$1" in
  run) cat "${JOB_VERSION_OUT_FILE}"; exit 0 ;;
  image)
    if [ "\$2" = inspect ]; then
      case "\$*" in
        *RepoDigests*) cat "${IMAGE_DIGESTS_FILE}" ;;
        *) cat "${IMAGE_REVISION_FILE}" ;;
      esac
      exit 0
    fi ;;
esac
exit 0
PODMAN
cat > "${SHIM_DIR}/openshell" <<OS
#!/bin/sh
echo "\$@" >> "${OPENSHELL_LOG}"
case "\$1" in
  --version) echo "openshell \$(cat "${HOST_VERSION_FILE}" 2>/dev/null)";;
  gateway) echo "  * openshell";;
esac
exit 0
OS
# Like the real 0.1 installer, which starts the gateway, the stub fails unless
# the config is already schema v2 and the pre-0.1 store is gone. On success it
# "installs" the requested version by updating the simulated host version,
# unless INSTALLER_NOOP_FILE exists (an installer that refuses and exits 0).
cat > "${SHIM_DIR}/install.sh" <<INSTALL
#!/bin/sh
echo "ack=\${OPENSHELL_ACK_BREAKING_UPGRADE:-} version=\${OPENSHELL_VERSION:-}" >> "${INSTALL_ENV_LOG}"
if ! grep -Eq '^[[:space:]]*version[[:space:]]*=[[:space:]]*2[[:space:]]*(#.*)?\$' "${HOME}/.config/openshell/gateway.toml" 2>/dev/null; then
  echo "stub install.sh: gateway config preflight failed: ${HOME}/.config/openshell/gateway.toml missing or not schema v2" >&2
  exit 1
fi
if [ -e "${HOME}/.local/state/openshell/gateway" ] || [ -e "${HOME}/.local/state/openshell/tls" ]; then
  echo "stub install.sh: gateway start failed: pre-0.1 state still in ${HOME}/.local/state/openshell" >&2
  exit 1
fi
if [ ! -e "${INSTALLER_NOOP_FILE}" ]; then
  ver="\${OPENSHELL_VERSION:-}"
  echo "\${ver#v}" > "${HOST_VERSION_FILE}"
fi
exit 0
INSTALL
# curl answers from NET_DIR by URL; anything unlisted fails like a 404 (-f).
cat > "${SHIM_DIR}/curl" <<CURL
#!/bin/sh
echo "\$@" >> "${CURL_LOG}"
for a in "\$@"; do url="\$a"; done
case "\${url}" in
  https://api.github.com/repos/fullsend-ai/fullsend/commits/*)
    f="${NET_DIR}/tag-\${url##*/}" ;;
  https://api.github.com/repos/fullsend-ai/fullsend/compare/main...*)
    rev="\${url##*...}"
    f="${NET_DIR}/compare-\${rev%%\\?*}" ;;
  https://raw.githubusercontent.com/fullsend-ai/fullsend/*/.github/scripts/openshell-version.sh)
    rest="\${url#https://raw.githubusercontent.com/fullsend-ai/fullsend/}"
    f="${NET_DIR}/pin-\${rest%%/*}" ;;
  https://raw.githubusercontent.com/NVIDIA/OpenShell/*/install.sh)
    cat "${SHIM_DIR}/install.sh"
    exit 0 ;;
  https://ghcr.io/token*)
    f="${NET_DIR}/ghcr-token" ;;
  https://ghcr.io/v2/fullsend-ai/fullsend-runner/manifests/*)
    # Like curl -sSI -w '\\n%{http_code}': a transport failure exits non-zero,
    # a missing manifest answers HTTP 404, and a file holds the response
    # headers (HTTP 200). ghcr-status-<digest> forces another HTTP status.
    [ ! -f "${NET_DIR}/ghcr-net-error" ] || exit 6
    d="\${url##*/}"
    if [ -f "${NET_DIR}/ghcr-status-\${d}" ]; then
      printf 'HTTP/2 %s\r\n\r\n\n%s' "\$(cat "${NET_DIR}/ghcr-status-\${d}")" "\$(cat "${NET_DIR}/ghcr-status-\${d}")"
    elif [ -f "${NET_DIR}/ghcr-\${d}" ]; then
      cat "${NET_DIR}/ghcr-\${d}"
      printf '\n200'
    else
      printf 'HTTP/2 404\r\n\r\n\n404'
    fi
    exit 0 ;;
  *) exit 22 ;;
esac
[ -f "\${f}" ] || exit 22
cat "\${f}"
CURL
chmod +x "${SHIM_DIR}/podman" "${SHIM_DIR}/openshell" "${SHIM_DIR}/curl"

printf '{"token":"anonymous-pull-token","access_token":"anonymous-pull-token"}\n' > "${NET_DIR}/ghcr-token"

# digest_of <rev>: the image digest Fullsend's workflow published for <rev>.
digest_of() {
  printf 'sha256:%s' "$(printf '%s' "$1" | sha256sum | cut -c1-64)"
}
# publish_image <rev> [digest]: Fullsend's runner image repository holds a
# manifest with <digest> (default digest_of <rev>), served by digest.
publish_image() {
  local d="${2:-$(digest_of "$1")}"
  printf 'HTTP/2 200\r\ncontent-type: application/vnd.oci.image.index.v1+json\r\ndocker-content-digest: %s\r\n\r\n' \
    "${d}" > "${NET_DIR}/ghcr-${d}"
}

# add_fullsend_commit <rev> <openshell-version> <openshell-sha> [compare-status]
# Publishes commit <rev> with that pin and its runner image; compare-status
# (default behind) is what GitHub reports comparing main...<rev> (behind or
# identical = on main).
add_fullsend_commit() {
  publish_image "$1"
  printf '# pin file\nOPENSHELL_VERSION=%s\nOPENSHELL_SHA=%s\nexport OPENSHELL_VERSION\n' \
    "$2" "$3" > "${NET_DIR}/pin-$1"
  printf '{"status": "%s", "ahead_by": 0, "files": [{"status": "modified"}]}\n' \
    "${4:-behind}" > "${NET_DIR}/compare-$1"
}
# add_release_tag <version> <rev>: Git tag v<version> resolves to <rev>.
add_release_tag() {
  printf '%s\n' "$2" > "${NET_DIR}/tag-v$1"
}

# run_job <image> <image-revision> <job-reported-version> <host-version>
# Runs ensure_job_openshell_gateway in that world; leaves its stderr in JOB_ERR.
# The pulled image carries the digests of the image Fullsend published for its
# revision (index digest plus an unrelated one) unless IMAGE_DIGESTS_OVERRIDE
# is set (use "-" for an image podman records no digests for).
run_job() {
  local image="$1" rev="$2" job="$3" host="$4"
  reset_logs
  : > "${INSTALL_ENV_LOG}"
  printf '%s\n' "${rev}" > "${IMAGE_REVISION_FILE}"
  if [ "${IMAGE_DIGESTS_OVERRIDE:-}" = "-" ]; then
    : > "${IMAGE_DIGESTS_FILE}"
  elif [ -n "${IMAGE_DIGESTS_OVERRIDE:-}" ]; then
    printf '%s\n' "${IMAGE_DIGESTS_OVERRIDE}" > "${IMAGE_DIGESTS_FILE}"
  else
    printf '%s %s@%s \n' "sha256:$(printf 'f%.0s' $(seq 1 64))" "${image%%[:@]*}" "$(digest_of "${rev}")" > "${IMAGE_DIGESTS_FILE}"
  fi
  printf 'openshell %s\n' "${job}" > "${JOB_VERSION_OUT_FILE}"
  printf '%s\n' "${host}" > "${HOST_VERSION_FILE}"
  JOB_RC=0
  JOB_ERR=$(ensure_job_openshell_gateway "${image}" 2>&1 >/dev/null) || JOB_RC=$?
}

# refused <label>: the last run_job failed without installing or starting.
refused() {
  if [ "${JOB_RC}" -eq 0 ]; then
    fail "$1: ensure_job_openshell_gateway must fail"
  elif grep -q 'install.sh' "${CURL_LOG}" || grep -q 'start openshell-gateway.service' "${SYSTEMCTL_LOG}"; then
    fail "$1: must neither install nor start a gateway: curl=$(tr '\n' '|' < "${CURL_LOG}")"
  else
    pass "$1: refused without installing or starting a gateway"
  fi
}

echo "== provisioning pin and list: no VM-local release list =="
if [ -z "${OPENSHELL_SUPPORTED_RELEASES+x}" ] \
  && ! grep -q 'OPENSHELL_SUPPORTED_RELEASES' "${SCRIPT_DIR}/gateway.sh" "${SCRIPT_DIR}/../setup.sh"; then
  pass "no OPENSHELL_SUPPORTED_RELEASES list exists in the executor or setup"
else
  fail "a VM-local OPENSHELL_SUPPORTED_RELEASES list is still referenced"
fi

echo "== job_image_revision reads the already-pulled image's label =="
reset_logs
printf '%s\n' "${REV_NEW}" > "${IMAGE_REVISION_FILE}"
if [ "$(job_image_revision "registry.example.com/runner:dev")" = "${REV_NEW}" ] \
  && grep -q 'image inspect' "${PODMAN_LOG}" \
  && grep -q 'org.opencontainers.image.revision' "${PODMAN_LOG}" \
  && ! grep -q 'pull' "${PODMAN_LOG}"; then
  pass "job_image_revision inspects the local image and does not re-pull"
else
  fail "job_image_revision: $(tr '\n' '|' < "${PODMAN_LOG}")"
fi
for _bad in "" "<no value>" "not-a-sha" "${REV_NEW:0:39}"; do
  printf '%s\n' "${_bad}" > "${IMAGE_REVISION_FILE}"
  if job_image_revision "registry.example.com/runner:dev" >/dev/null 2>&1; then
    fail "job_image_revision accepted label '${_bad}'"
  else
    pass "job_image_revision rejects label '${_bad}'"
  fi
done

echo "== job_image_openshell_version fails hard on a podman error instead of silently keeping the host version =="
reset_logs
cat > "${SHIM_DIR}/podman.err" <<PODMAN
#!/bin/sh
echo "\$@" >> "${PODMAN_LOG}"
if [ "\$1" = "run" ]; then
  echo "OCI runtime exec failed: exec: openshell: executable file not found" >&2
  exit 126
fi
exit 0
PODMAN
cp "${SHIM_DIR}/podman" "${SHIM_DIR}/podman.ok"
cp "${SHIM_DIR}/podman.err" "${SHIM_DIR}/podman"
chmod +x "${SHIM_DIR}/podman"
if job_image_openshell_version "registry.example.com/runner:dev" >/dev/null 2>/dev/null; then
  fail "job_image_openshell_version should fail on a podman error, not report a version"
else
  pass "job_image_openshell_version propagates a hard podman error instead of masking it as 'no CLI'"
fi
cp "${SHIM_DIR}/podman.ok" "${SHIM_DIR}/podman"
chmod +x "${SHIM_DIR}/podman"

echo "== ensure_job_openshell_gateway migrates a pre-0.1 host to the verified pin =="
add_fullsend_commit "${REV_NEW}" 7.0.2 "${SHA_NEW}"
# Pre-0.1 host: a schema-v1 gateway.toml and a 0.0.x gateway store and TLS.
mkdir -p "${HOME}/.config/openshell" "${HOME}/.local/state/openshell/gateway" \
  "${HOME}/.local/state/openshell/tls"
echo cert > "${HOME}/.local/state/openshell/tls/server.crt"
V1_TOML='[openshell]
version = 1

[openshell.gateway]
compute_drivers = ["podman"]
supervisor_image = "ghcr.io/nvidia/openshell/supervisor:0.0.1"'
printf '%s\n' "${V1_TOML}" > "${HOME}/.config/openshell/gateway.toml"
rm -f "${HOME}/.config/openshell/gateway.toml.pre-0.1"
echo db > "${HOME}/.local/state/openshell/gateway/state.db"
export OPENSHELL_PACKAGED_GATEWAY_TOML="${SHIM_DIR}/no-packaged-default"
rm -rf "${HOME}/.local/state/fullsend"
run_job "registry.example.com/runner:dev" "${REV_NEW}" 7.0.2 0.0.1
if [ "${JOB_RC}" -eq 0 ]; then
  if grep -q "NVIDIA/OpenShell/${SHA_NEW}/install.sh" "${CURL_LOG}"; then
    pass "mismatch installs from the verified commit's OPENSHELL_SHA, not a job-chosen tag or the VM pin"
  else
    fail "curl log missing SHA-pinned URL: $(tr '\n' '|' < "${CURL_LOG}")"
  fi
  if grep -q -- '--cap-drop=ALL' "${PODMAN_LOG}" && grep -q -- '--security-opt=no-new-privileges' "${PODMAN_LOG}"; then
    pass "job image version probe is hardened like the real job container"
  else
    fail "podman log missing hardening flags: $(tr '\n' '|' < "${PODMAN_LOG}")"
  fi
  if grep -qx "ack=1 version=v7.0.2" "${INSTALL_ENV_LOG}"; then
    pass "install.sh runs with OPENSHELL_ACK_BREAKING_UPGRADE=1"
  else
    fail "install.sh env missing the breaking-upgrade ack: $(tr '\n' '|' < "${INSTALL_ENV_LOG}")"
  fi
  assert_v2_gateway_toml "${HOME}/.config/openshell/gateway.toml" \
    "ghcr.io/nvidia/openshell/supervisor:7.0.2" "mismatch install"
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
  fail "ensure_job_openshell_gateway returned non-zero on mismatch: ${JOB_ERR}"
fi

# assert_switch <image> <rev> <version> <host-before> <want-sha> <label>
# Succeeds only if the host really ends on <version> (the installer fixture
# updates the simulated host version) and everything is pinned to it.
assert_switch() {
  local image="$1" rev="$2" ver="$3" host="$4" want_sha="$5" label="$6"
  run_job "${image}" "${rev}" "${ver}" "${host}"
  if [ "${JOB_RC}" -ne 0 ]; then
    fail "${label}: ensure_job_openshell_gateway failed: ${JOB_ERR}"
  elif [ "$(cat "${HOST_VERSION_FILE}")" = "${ver}" ] \
    && grep -q "NVIDIA/OpenShell/${want_sha}/install.sh" "${CURL_LOG}" \
    && grep -qx "ack=1 version=v${ver}" "${INSTALL_ENV_LOG}" \
    && grep -q "pull -- ghcr.io/nvidia/openshell/supervisor:${ver}" "${PODMAN_LOG}" \
    && grep -qx "supervisor_image = \"ghcr.io/nvidia/openshell/supervisor:${ver}\"" "${HOME}/.config/openshell/gateway.toml"; then
    pass "${label}: host switched ${host} -> ${ver} from commit ${want_sha:0:7} and pinned its supervisor"
  else
    fail "${label}: host=$(cat "${HOST_VERSION_FILE}") curl=$(tr '\n' '|' < "${CURL_LOG}") install=$(tr '\n' '|' < "${INSTALL_ENV_LOG}")"
  fi
}

echo "== one VM serves newly published and older releases in either order, with no VM-local pin update =="
# The VM was provisioned at 7.0.0 (OPENSHELL_VERSION above). Release 1.2.0
# (OpenShell 7.0.2) was published after provisioning; 1.1.0 pins 7.0.1.
add_fullsend_commit "${REV_NEW}" 7.0.2 "${SHA_NEW}"
add_fullsend_commit "${REV_OLD}" 7.0.1 "${SHA_OLD}"
add_release_tag 1.2.0 "${REV_NEW}"
add_release_tag 1.1.0 "${REV_OLD}"
assert_switch "ghcr.io/fullsend-ai/fullsend-runner:1.1.0" "${REV_OLD}" 7.0.1 7.0.0 "${SHA_OLD}" "provisioned host, older release"
assert_switch "ghcr.io/fullsend-ai/fullsend-runner:1.2.0" "${REV_NEW}" 7.0.2 7.0.1 "${SHA_NEW}" "newer release after older (upgrade)"
assert_switch "ghcr.io/fullsend-ai/fullsend-runner:1.1.0" "${REV_OLD}" 7.0.1 7.0.2 "${SHA_OLD}" "older release after newer (downgrade)"
assert_switch "ghcr.io/fullsend-ai/fullsend-runner:1.2.0" "${REV_NEW}" 7.0.2 7.0.1 "${SHA_NEW}" "newer release again"
if grep -q "${SHA_PROVISION}" "${CURL_LOG}"; then
  fail "the provisioning pin was used for a job install"
else
  pass "the VM's provisioning pin plays no part in per-job installs"
fi

echo "== release images are checked against their Git tag; moving tags are not re-resolved =="
run_job "ghcr.io/fullsend-ai/fullsend-runner:1.2.0" "${REV_NEW}" 7.0.2 7.0.2
if [ "${JOB_RC}" -eq 0 ] && grep -q 'commits/v1.2.0' "${CURL_LOG}" \
  && ! grep -q 'install.sh' "${CURL_LOG}" \
  && grep -q 'start openshell-gateway.service' "${SYSTEMCTL_LOG}" \
  && grep -qx 'supervisor_image = "ghcr.io/nvidia/openshell/supervisor:7.0.2"' "${HOME}/.config/openshell/gateway.toml"; then
  pass "verified release image on a matching host skips the install and starts the gateway"
else
  fail "matching verified release: rc=${JOB_RC} err=${JOB_ERR} curl=$(tr '\n' '|' < "${CURL_LOG}")"
fi
run_job "ghcr.io/fullsend-ai/fullsend-runner:dev" "${REV_NEW}" 7.0.2 7.0.2
if [ "${JOB_RC}" -eq 0 ] && ! grep -q 'commits/v' "${CURL_LOG}"; then
  pass "moving tag :dev is verified by its commit on main, not re-resolved as a Git tag"
else
  fail ":dev: rc=${JOB_RC} err=${JOB_ERR} curl=$(tr '\n' '|' < "${CURL_LOG}")"
fi
run_job "ghcr.io/fullsend-ai/fullsend-runner@sha256:$(printf 'e%.0s' $(seq 1 64))" "${REV_NEW}" 7.0.2 7.0.2
if [ "${JOB_RC}" -eq 0 ] && ! grep -q 'commits/v' "${CURL_LOG}"; then
  pass "digest reference is verified by its commit on main"
else
  fail "digest ref: rc=${JOB_RC} err=${JOB_ERR} curl=$(tr '\n' '|' < "${CURL_LOG}")"
fi
add_fullsend_commit "${REV_MOVED}" 7.0.2 "${SHA_NEW}"
add_release_tag 1.3.0 "${REV_NEW}"
run_job "ghcr.io/fullsend-ai/fullsend-runner:1.3.0" "${REV_MOVED}" 7.0.2 7.0.1
refused "release image whose commit differs from its Git tag"
if [[ "${JOB_ERR}" == *"Git tag v1.3.0 resolves to ${REV_NEW}"* ]]; then
  pass "tag mismatch error names the tag and the commit it resolves to"
else
  fail "tag mismatch error unhelpful: ${JOB_ERR}"
fi
run_job "ghcr.io/fullsend-ai/fullsend-runner:1.4.0" "${REV_NEW}" 7.0.2 7.0.1
refused "release image whose Git tag does not exist"

echo "== images without trusted Fullsend provenance cannot authorize a host install =="
# A fork commit is served under the parent repo's URLs but is not on main.
add_fullsend_commit "${REV_FORK}" 7.0.1 "${SHA_OLD}" diverged
run_job "registry.example.com/runner:dev" "${REV_FORK}" 7.0.1 7.0.2
refused "commit not on main (fork or unmerged)"
add_fullsend_commit "${REV_FORK}" 7.0.1 "${SHA_OLD}" ahead
run_job "registry.example.com/runner:dev" "${REV_FORK}" 7.0.1 7.0.2
refused "commit ahead of main"
run_job "registry.example.com/runner:dev" "" 7.0.2 7.0.1
refused "image without a revision label"
run_job "registry.example.com/runner:dev" "${REV_OFFLINE}" 7.0.2 7.0.1
refused "commit GitHub does not know"
# Same version as the host: still must be verified (a matching host used to
# skip every check).
run_job "registry.example.com/runner:dev" "${REV_OFFLINE}" 7.0.2 7.0.2
refused "matching version but unverifiable provenance"
run_job "registry.example.com/runner:dev" "${REV_NEW}" 7.0.1 7.0.1
refused "image reporting the old version it merely claims, on a commit that pins another"
if [[ "${JOB_ERR}" == *"reports OpenShell 7.0.1"*"pins 7.0.2"* ]]; then
  pass "version disagreement error names both versions"
else
  fail "version disagreement error unhelpful: ${JOB_ERR}"
fi
printf '# not a pin\n' > "${NET_DIR}/pin-${REV_BADPIN}"
printf '{"status": "behind"}\n' > "${NET_DIR}/compare-${REV_BADPIN}"
publish_image "${REV_BADPIN}"
run_job "registry.example.com/runner:dev" "${REV_BADPIN}" 7.0.2 7.0.1
refused "commit without a valid pin file"
printf 'OPENSHELL_VERSION=7.0.2\nOPENSHELL_SHA=not-a-sha\n' > "${NET_DIR}/pin-${REV_MISMATCH}"
printf '{"status": "identical"}\n' > "${NET_DIR}/compare-${REV_MISMATCH}"
publish_image "${REV_MISMATCH}"
run_job "registry.example.com/runner:dev" "${REV_MISMATCH}" 7.0.2 7.0.1
refused "pin file with a malformed commit SHA"
run_job "registry.example.com/runner:dev" "${REV_NEW}" "(unknown build)" 7.0.1
refused "image whose OpenShell version cannot be read"

echo "== an image that only claims an official build cannot authorize a host change =="
# The label and `openshell --version` come from the image. Naming a real
# commit on main (here the old pin, which would downgrade the host) is not
# enough: the pulled image must be the one Fullsend published for it.
IMAGE_DIGESTS_OVERRIDE="$(digest_of "some-custom-build")" \
  run_job "registry.example.com/runner:dev" "${REV_OLD}" 7.0.1 7.0.2
refused "custom image copying an official image's label and version"
if [[ "${JOB_ERR}" == *"is not an image Fullsend published for commit ${REV_OLD}"* ]] \
  && [ "$(cat "${HOST_VERSION_FILE}")" = "7.0.2" ]; then
  pass "label-copying image is refused with a clear error and the host keeps its version"
else
  fail "label-copying image: err=${JOB_ERR} host=$(cat "${HOST_VERSION_FILE}")"
fi
IMAGE_DIGESTS_OVERRIDE="$(digest_of "some-custom-build")" \
  run_job "ghcr.io/fullsend-ai/fullsend-runner:1.1.0" "${REV_OLD}" 7.0.1 7.0.2
refused "release-tagged reference whose content is not the published image"
IMAGE_DIGESTS_OVERRIDE="-" run_job "registry.example.com/runner:dev" "${REV_NEW}" 7.0.2 7.0.1
refused "image with no recorded digest"
rm -f "${NET_DIR}/ghcr-$(digest_of "${REV_NEW}")"
run_job "registry.example.com/runner:dev" "${REV_NEW}" 7.0.2 7.0.1
refused "image whose digest Fullsend's registry does not hold"
if [[ "${JOB_ERR}" == *"is not an image Fullsend published for commit ${REV_NEW}"* ]]; then
  pass "HEAD 404 from the registry is reported as an unpublished image"
else
  fail "HEAD 404: err=${JOB_ERR}"
fi
publish_image "${REV_NEW}"
# A failed lookup is not evidence the image is unpublished: it is refused, but
# reported as unverifiable so the operator retries instead of replacing it.
touch "${NET_DIR}/ghcr-net-error"
run_job "registry.example.com/runner:dev" "${REV_NEW}" 7.0.2 7.0.1
rm -f "${NET_DIR}/ghcr-net-error"
refused "registry network failure"
if [[ "${JOB_ERR}" == *"could not verify that job image"* ]] \
  && [[ "${JOB_ERR}" != *"is not an image Fullsend published"* ]]; then
  pass "registry network failure is reported as unverifiable, not as an unpublished image"
else
  fail "registry network failure: err=${JOB_ERR}"
fi
printf '503' > "${NET_DIR}/ghcr-status-$(digest_of "${REV_NEW}")"
run_job "registry.example.com/runner:dev" "${REV_NEW}" 7.0.2 7.0.1
rm -f "${NET_DIR}/ghcr-status-$(digest_of "${REV_NEW}")"
refused "registry HTTP 503"
if [[ "${JOB_ERR}" == *"could not verify that job image"* ]]; then
  pass "registry HTTP error is reported as unverifiable"
else
  fail "registry HTTP 503: err=${JOB_ERR}"
fi
# A failed lookup of one digest does not stop the remaining digests being tried.
printf '503' > "${NET_DIR}/ghcr-status-sha256:$(printf 'e%.0s' $(seq 1 64))"
IMAGE_DIGESTS_OVERRIDE="sha256:$(printf 'e%.0s' $(seq 1 64)) registry.example.com/mirror/runner@$(digest_of "${REV_NEW}")" \
  run_job "registry.example.com/mirror/runner:dev" "${REV_NEW}" 7.0.2 7.0.2
rm -f "${NET_DIR}/ghcr-status-sha256:$(printf 'e%.0s' $(seq 1 64))"
if [ "${JOB_RC}" -eq 0 ]; then
  pass "a failed lookup of one digest still accepts another published digest"
else
  fail "failed-then-published digests: ${JOB_ERR}"
fi
mv "${NET_DIR}/ghcr-token" "${NET_DIR}/ghcr-token.off"
run_job "registry.example.com/runner:dev" "${REV_NEW}" 7.0.2 7.0.1
mv "${NET_DIR}/ghcr-token.off" "${NET_DIR}/ghcr-token"
refused "GHCR token endpoint unavailable"
printf 'HTTP/2 200\r\ncontent-type: text/plain\r\n\r\n' > "${NET_DIR}/ghcr-$(digest_of "${REV_NEW}")"
run_job "registry.example.com/runner:dev" "${REV_NEW}" 7.0.2 7.0.1
refused "registry response without a digest header"
printf 'HTTP/2 200\r\ndocker-content-digest: %s\r\n\r\n' "$(digest_of "some-other-manifest")" \
  > "${NET_DIR}/ghcr-$(digest_of "${REV_NEW}")"
run_job "registry.example.com/runner:dev" "${REV_NEW}" 7.0.2 7.0.1
refused "registry response naming a different digest than requested"
publish_image "${REV_NEW}"
# The multi-arch index digest may appear only among the repo digests.
IMAGE_DIGESTS_OVERRIDE="sha256:$(printf 'e%.0s' $(seq 1 64)) registry.example.com/mirror/runner@$(digest_of "${REV_NEW}")" \
  run_job "registry.example.com/mirror/runner:dev" "${REV_NEW}" 7.0.2 7.0.2
if [ "${JOB_RC}" -eq 0 ]; then
  pass "a mirrored copy with the published digest is accepted (content-addressed)"
else
  fail "mirrored official image refused: ${JOB_ERR}"
fi

echo "== moving tags are never consulted: an official image stays verified after its tags move =="
# runner-image.yml writes the 7-char SHA tag on main and on tag-push builds, and
# `dev` moves on every main build. The registry is asked about the pulled
# image's digest, so an official image keeps verifying after a release build of
# the same commit took the SHA tag and a later main build advanced :dev.
MAIN_DIGEST="$(digest_of "main-build-${REV_NEW}")"
RELEASE_DIGEST="$(digest_of "release-build-${REV_NEW}")"
LATER_MAIN_DIGEST="$(digest_of "later-main-build")"
publish_image "${REV_NEW}" "${MAIN_DIGEST}"
publish_image "${REV_NEW}" "${RELEASE_DIGEST}"
publish_image "${REV_NEW}" "${LATER_MAIN_DIGEST}"
# Tag manifests now point at other builds (or are gone); a lookup through any
# of them would refuse the original main image.
printf 'HTTP/2 200\r\ndocker-content-digest: %s\r\n\r\n' "${RELEASE_DIGEST}" > "${NET_DIR}/ghcr-${REV_NEW:0:7}"
printf 'HTTP/2 200\r\ndocker-content-digest: %s\r\n\r\n' "${LATER_MAIN_DIGEST}" > "${NET_DIR}/ghcr-dev"
IMAGE_DIGESTS_OVERRIDE="${MAIN_DIGEST}" \
  run_job "ghcr.io/fullsend-ai/fullsend-runner:dev" "${REV_NEW}" 7.0.2 7.0.1
if [ "${JOB_RC}" -eq 0 ]; then
  pass "the original main image is accepted after the SHA tag and :dev moved"
else
  fail "main image refused after its tags moved: ${JOB_ERR}"
fi
if ! grep -qE "manifests/(dev|latest|${REV_NEW:0:7}|[0-9]+\.[0-9]+)( |\$)" "${CURL_LOG}"; then
  pass "registry lookups use digests, never tags"
else
  fail "a mutable tag was looked up: $(tr '\n' '|' < "${CURL_LOG}")"
fi
IMAGE_DIGESTS_OVERRIDE="ghcr.io/fullsend-ai/fullsend-runner@${MAIN_DIGEST}" \
  run_job "ghcr.io/fullsend-ai/fullsend-runner@${MAIN_DIGEST}" "${REV_NEW}" 7.0.2 7.0.1
if [ "${JOB_RC}" -eq 0 ]; then
  pass "an immutable digest reference to the original main image is accepted"
else
  fail "digest reference refused after its tags moved: ${JOB_ERR}"
fi
IMAGE_DIGESTS_OVERRIDE="${RELEASE_DIGEST}" \
  run_job "ghcr.io/fullsend-ai/fullsend-runner:1.2.0" "${REV_NEW}" 7.0.2 7.0.1
if [ "${JOB_RC}" -eq 0 ]; then
  pass "the release image is accepted alongside the main image of the same commit"
else
  fail "release image refused: ${JOB_ERR}"
fi
# An image whose digest the repository does not hold is refused even when a tag
# for its commit exists.
IMAGE_DIGESTS_OVERRIDE="$(digest_of "unpublished-build-${REV_NEW}")" \
  run_job "registry.example.com/runner:dev" "${REV_NEW}" 7.0.2 7.0.1
refused "image whose digest the repository does not hold"
rm -f "${NET_DIR}/ghcr-dev" "${NET_DIR}/ghcr-${REV_NEW:0:7}" "${NET_DIR}/ghcr-${MAIN_DIGEST}" \
  "${NET_DIR}/ghcr-${RELEASE_DIGEST}" "${NET_DIR}/ghcr-${LATER_MAIN_DIGEST}"
publish_image "${REV_NEW}"

echo "== a successful installer run that does not switch the host version is an error =="
: > "${INSTALLER_NOOP_FILE}"
run_job "ghcr.io/fullsend-ai/fullsend-runner:1.1.0" "${REV_OLD}" 7.0.1 7.0.2
rm -f "${INSTALLER_NOOP_FILE}"
if [ "${JOB_RC}" -ne 0 ] && [[ "${JOB_ERR}" == *"host OpenShell is 7.0.2 after installing 7.0.1"* ]] \
  && ! grep -q 'start openshell-gateway.service' "${SYSTEMCTL_LOG}"; then
  pass "no-op installer is detected and no gateway is started"
else
  fail "no-op installer not detected: rc=${JOB_RC} err=${JOB_ERR}"
fi

echo "== openshell_version_at_least compares x.y.z numerically =="
for _case in "0.1.0 0.1.0 0" "0.1.4 0.1.0 0" "1.0.0 0.1.0 0" "0.10.0 0.9.9 0" \
  "0.0.116 0.1.0 1" "0.0.999 0.1.0 1" "0.1.0 0.1.1 1" "0.9.0 0.10.0 1"; do
  read -r _have _want _rc <<< "${_case}"
  _got=0
  openshell_version_at_least "${_have}" "${_want}" || _got=1
  if [ "${_got}" = "${_rc}" ]; then
    pass "openshell_version_at_least ${_have} ${_want} -> ${_rc}"
  else
    fail "openshell_version_at_least ${_have} ${_want}: got ${_got}, want ${_rc}"
  fi
done

echo "== a job image pinning pre-0.1 OpenShell is refused before the host is touched =="
# #8730: the executor writes only a schema-v2 gateway.toml, which a 0.0.x
# gateway rejects. A verified 0.0.x pin must fail before teardown or install.
REV_PRE01="$(printf '8%.0s' $(seq 1 40))"
add_fullsend_commit "${REV_PRE01}" 0.0.116 "${SHA_OLD}"
add_release_tag 0.43.0 "${REV_PRE01}"
mkdir -p "${HOME}/.local/state/openshell/gateway"
echo db > "${HOME}/.local/state/openshell/gateway/state.db"
_toml_before="$(cat "${HOME}/.config/openshell/gateway.toml")"
run_job "ghcr.io/fullsend-ai/fullsend-runner:0.43.0" "${REV_PRE01}" 0.0.116 0.1.4
refused "image pinning OpenShell 0.0.116 on a 0.1.4 host"
if [[ "${JOB_ERR}" == *"pins OpenShell 0.0.116"*"supports OpenShell 0.1.0 or newer"* ]]; then
  pass "pre-0.1 refusal names the pinned version and the minimum supported version"
else
  fail "pre-0.1 refusal error unhelpful: ${JOB_ERR}"
fi
if [ ! -s "${SYSTEMCTL_LOG}" ] \
  && ! grep -q 'gateway remove' "${OPENSHELL_LOG}" \
  && [ -e "${HOME}/.local/state/openshell/gateway/state.db" ] \
  && [ "$(cat "${HOST_VERSION_FILE}")" = "0.1.4" ] \
  && [ "$(cat "${HOME}/.config/openshell/gateway.toml")" = "${_toml_before}" ]; then
  pass "pre-0.1 refusal leaves the host gateway, store, version, and gateway.toml untouched"
else
  fail "pre-0.1 refusal touched the host: systemctl=$(tr '\n' '|' < "${SYSTEMCTL_LOG}") openshell=$(tr '\n' '|' < "${OPENSHELL_LOG}") host=$(cat "${HOST_VERSION_FILE}")"
fi
run_job "ghcr.io/fullsend-ai/fullsend-runner:0.43.0" "${REV_PRE01}" 0.0.116 0.0.116
refused "image pinning OpenShell 0.0.116 on a host already at 0.0.116"
rm -rf "${HOME}/.local/state/openshell/gateway"

echo "== 0.1.x to 0.1.x switches still succeed and keep a v2 config =="
REV_V012="$(printf '9%.0s' $(seq 1 40))"
REV_V014="$(printf 'd%.0s' $(seq 1 40))"
add_fullsend_commit "${REV_V012}" 0.1.2 "${SHA_OLD}"
add_fullsend_commit "${REV_V014}" 0.1.4 "${SHA_NEW}"
assert_switch "registry.example.com/runner:dev" "${REV_V012}" 0.1.2 0.1.4 "${SHA_OLD}" "0.1.4 -> 0.1.2 (downgrade)"
assert_v2_gateway_toml "${HOME}/.config/openshell/gateway.toml" \
  "ghcr.io/nvidia/openshell/supervisor:0.1.2" "0.1.4 -> 0.1.2"
assert_switch "registry.example.com/runner:dev" "${REV_V014}" 0.1.4 0.1.2 "${SHA_NEW}" "0.1.2 -> 0.1.4 (upgrade)"
assert_v2_gateway_toml "${HOME}/.config/openshell/gateway.toml" \
  "ghcr.io/nvidia/openshell/supervisor:0.1.4" "0.1.2 -> 0.1.4"

echo "== commits verified on main are cached =="
rm -rf "${HOME}/.local/state/fullsend"
run_job "registry.example.com/runner:dev" "${REV_NEW}" 7.0.2 7.0.2
run_job "registry.example.com/runner:dev" "${REV_NEW}" 7.0.2 7.0.2
if [ "${JOB_RC}" -eq 0 ] && ! grep -q "compare/main" "${CURL_LOG}" \
  && [ -e "${HOME}/.local/state/fullsend/verified-revisions/${REV_NEW}" ]; then
  pass "a second job on a verified commit skips the GitHub compare call"
else
  fail "verification cache: rc=${JOB_RC} curl=$(tr '\n' '|' < "${CURL_LOG}")"
fi
if [ ! -e "${HOME}/.local/state/fullsend/verified-revisions/${REV_FORK}" ]; then
  pass "rejected commits are never cached"
else
  fail "a rejected commit was cached"
fi

OPENSHELL_VERSION="${_saved_version}"
OPENSHELL_SHA="${_saved_sha}"

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
