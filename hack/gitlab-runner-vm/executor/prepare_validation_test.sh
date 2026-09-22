#!/usr/bin/env bash
# Fixture test for prepare.sh's bind-mount path validation.
#
# CUSTOM_ENV_CI_BUILDS_DIR and CUSTOM_ENV_CI_CACHE_DIR are job-controlled, and
# their values are passed to `podman create -v` (which also relabels the host
# tree via :z). A plain prefix test accepts "<root>-evil" and lets "<root>/.."
# escape, so these cases are asserted directly.
#
# Usage: hack/gitlab-runner-vm/executor/prepare_validation_test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PREPARE="${SCRIPT_DIR}/prepare.sh"

# prepare.sh's path checks need GNU realpath (-m). Provide a shim when only
# the BSD one is on PATH (macOS with coreutils installed as grealpath).
SHIM_DIR=$(mktemp -d)
trap 'rm -rf "${SHIM_DIR}"' EXIT
if ! realpath -m / >/dev/null 2>&1; then
  if command -v grealpath >/dev/null 2>&1; then
    printf '#!/bin/sh\nexec grealpath "$@"\n' > "${SHIM_DIR}/realpath"
    chmod +x "${SHIM_DIR}/realpath"
  else
    echo "SKIP: GNU realpath (or grealpath) is required to run this test" >&2
    exit 0
  fi
fi

# Stub podman / systemctl / openshell so the test exercises path validation
# only. prepare.sh now starts a per-job OpenShell gateway after the pull;
# those helpers must succeed against these no-op binaries.
printf '#!/bin/sh\nexit 0\n' > "${SHIM_DIR}/podman"
printf '#!/bin/sh\nexit 0\n' > "${SHIM_DIR}/systemctl"
printf '#!/bin/sh\ncase "$1" in --version) echo "openshell 0.0.116";; gateway) echo "  * openshell";; esac\nexit 0\n' > "${SHIM_DIR}/openshell"
chmod +x "${SHIM_DIR}/podman" "${SHIM_DIR}/systemctl" "${SHIM_DIR}/openshell"

FAKE_HOME=$(mktemp -d)
trap 'rm -rf "${SHIM_DIR}" "${FAKE_HOME}"' EXIT

# prepare.sh reads its job id from the runner-provided JOB_RESPONSE_FILE.
JOB_RESPONSE="${FAKE_HOME}/job-response.json"
printf '{"id": 1, "token": "x"}\n' > "${JOB_RESPONSE}"

# A symlink under the allowed root that points outside it: realpath must
# follow it and the resolved target must be rejected.
mkdir -p "${FAKE_HOME}/builds"
ln -s /etc "${FAKE_HOME}/builds/escape"

failures=0

# run_case <expected: accept|reject> <var> <value>
# JOB_RESPONSE_OVERRIDE, when set, replaces the JOB_RESPONSE_FILE path.
run_case() {
  local expected="$1" var="$2" value="$3" rc=0 output
  output=$(
    cd "${FAKE_HOME}" && \
    PATH="${SHIM_DIR}:${PATH}" \
    HOME="${FAKE_HOME}" \
    JOB_RESPONSE_FILE="${JOB_RESPONSE_OVERRIDE-${JOB_RESPONSE}}" \
    CUSTOM_ENV_CI_JOB_IMAGE="registry.example.com/img:latest" \
    env "${var}=${value}" bash "${PREPARE}" 2>&1
  ) || rc=$?

  # Classify on the actual validation message so an unrelated early failure
  # in prepare.sh cannot masquerade as a rejection.
  local short="${var#CUSTOM_ENV_CI_}"   # BUILDS_DIR / CACHE_DIR
  local got
  if [ "${rc}" -eq 0 ]; then
    got="accept"
  elif printf '%s' "${output}" | grep -Eq "ERROR: ${short} must (be under|not contain)"; then
    got="reject"
  elif printf '%s' "${output}" | grep -q "ERROR: could not read job id from JOB_RESPONSE_FILE"; then
    got="reject-identity"
  else
    got="error(rc=${rc}): $(printf '%s' "${output}" | tail -1)"
  fi

  if [ "${got}" = "${expected}" ]; then
    printf 'ok       %-22s %s\n' "${expected}" "${value}"
  else
    printf 'FAIL     want=%-8s got=%-8s %s=%s\n' "${expected}" "${got}" "${var}" "${value}"
    failures=$((failures + 1))
  fi
}

echo "== BUILDS_DIR =="
run_case accept CUSTOM_ENV_CI_BUILDS_DIR "${FAKE_HOME}/builds"
run_case accept CUSTOM_ENV_CI_BUILDS_DIR "${FAKE_HOME}/builds/project/1"
run_case reject CUSTOM_ENV_CI_BUILDS_DIR "${FAKE_HOME}/builds-evil"
run_case reject CUSTOM_ENV_CI_BUILDS_DIR "${FAKE_HOME}/buildsX/y"
run_case reject CUSTOM_ENV_CI_BUILDS_DIR "${FAKE_HOME}/builds/../../../etc"
run_case reject CUSTOM_ENV_CI_BUILDS_DIR "${FAKE_HOME}/builds/../.config/openshell"
run_case reject CUSTOM_ENV_CI_BUILDS_DIR "/etc/pki"
run_case reject CUSTOM_ENV_CI_BUILDS_DIR "${FAKE_HOME}/builds/escape"
run_case reject CUSTOM_ENV_CI_BUILDS_DIR "${FAKE_HOME}/builds/escape/pki"
run_case accept CUSTOM_ENV_CI_BUILDS_DIR "builds/relative"
run_case reject CUSTOM_ENV_CI_BUILDS_DIR "builds/../etc"
run_case reject CUSTOM_ENV_CI_BUILDS_DIR "${FAKE_HOME}/builds/a:b"
run_case reject CUSTOM_ENV_CI_BUILDS_DIR "${FAKE_HOME}/builds/a"$'\n'"b"

echo "== CACHE_DIR =="
run_case accept CUSTOM_ENV_CI_CACHE_DIR "${FAKE_HOME}/cache"
run_case reject CUSTOM_ENV_CI_CACHE_DIR "${FAKE_HOME}/cache-evil"
run_case reject CUSTOM_ENV_CI_CACHE_DIR "${FAKE_HOME}/cache/../../etc"

echo "== job identity =="
# A spoofed CUSTOM_ENV_CI_JOB_ID must be ignored: identity comes from the
# runner-written JOB_RESPONSE_FILE, so this still accepts under the real id.
run_case accept CUSTOM_ENV_CI_JOB_ID "999999"
# Missing, unreadable, or malformed response files must fail before podman.
JOB_RESPONSE_OVERRIDE="${FAKE_HOME}/does-not-exist.json" \
  run_case reject-identity CUSTOM_ENV_CI_BUILDS_DIR "${FAKE_HOME}/builds"
printf '{"id": "not-an-int"}\n' > "${FAKE_HOME}/bad-id.json"
JOB_RESPONSE_OVERRIDE="${FAKE_HOME}/bad-id.json" \
  run_case reject-identity CUSTOM_ENV_CI_BUILDS_DIR "${FAKE_HOME}/builds"
printf 'not json' > "${FAKE_HOME}/bad-json.json"
JOB_RESPONSE_OVERRIDE="${FAKE_HOME}/bad-json.json" \
  run_case reject-identity CUSTOM_ENV_CI_BUILDS_DIR "${FAKE_HOME}/builds"
JOB_RESPONSE_OVERRIDE="" \
  run_case reject-identity CUSTOM_ENV_CI_BUILDS_DIR "${FAKE_HOME}/builds"

if [ "${failures}" -ne 0 ]; then
  echo "${failures} case(s) failed" >&2
  exit 1
fi
echo "all cases passed"
