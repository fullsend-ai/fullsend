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
if grep -Fq 'EXECUTOR_DIR}/.github/scripts' "${SETUP}"; then
  pass "install_executor ships the version pin alongside the flattened executor scripts"
else
  fail "install_executor does not copy openshell-version.sh into EXECUTOR_DIR"
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

# curl | sh: emit a no-op script so the pipe succeeds without a real install.
cat > "${SHIM_DIR}/curl" <<CURL
#!/bin/sh
echo "\$@" >> "${CURL_LOG}"
printf '#!/bin/sh\\necho stub-install\\n'
exit 0
CURL
chmod +x "${SHIM_DIR}/curl"

mkdir -p "${HOME}/.config/openshell"
printf '[openshell.gateway]\nsupervisor_image = "ghcr.io/nvidia/openshell/supervisor:0.0.1"\n' \
  > "${HOME}/.config/openshell/gateway.toml"

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
  if grep -q "supervisor:${OPENSHELL_VERSION}" "${HOME}/.config/openshell/gateway.toml"; then
    pass "supervisor_image pinned to the matched version"
  else
    fail "supervisor_image not updated: $(cat "${HOME}/.config/openshell/gateway.toml")"
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
  --version) echo "openshell 0.0.116";;
  gateway) echo "  * openshell";;
esac
exit 0
OS
chmod +x "${SHIM_DIR}/openshell"
cat > "${SHIM_DIR}/podman" <<PODMAN
#!/bin/sh
echo "\$@" >> "${PODMAN_LOG}"
if [ "\$1" = "run" ]; then
  echo "openshell 0.0.116"
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

if [ "${FAILURES}" -ne 0 ]; then
  echo "${FAILURES} case(s) failed" >&2
  exit 1
fi
echo "all cases passed"
