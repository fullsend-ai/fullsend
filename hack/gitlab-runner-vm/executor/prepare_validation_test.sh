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
# those helpers must succeed against these no-op binaries. The job image
# probe (`podman run ... openshell --version`) reports the host's version so
# no install is attempted; an unreadable version is rejected. The image's
# revision label names a Fullsend commit that the curl stub reports as on
# main and as pinning that same version, and the image's digest equals the one
# the registry stub serves for that commit, so provenance verification passes.
REV="$(printf 'a%.0s' $(seq 1 40))"
DIGEST="sha256:$(printf 'd%.0s' $(seq 1 64))"
printf '#!/bin/sh\ncase "$1" in run) echo "openshell 0.1.1";; image) case "$*" in *RepoDigests*) echo "%s";; *) echo "%s";; esac;; esac\nexit 0\n' \
  "${DIGEST}" "${REV}" > "${SHIM_DIR}/podman"
printf '#!/bin/sh\nexit 0\n' > "${SHIM_DIR}/systemctl"
printf '#!/bin/sh\ncase "$1" in --version) echo "openshell 0.1.1";; gateway) echo "  * openshell";; esac\nexit 0\n' > "${SHIM_DIR}/openshell"
cat > "${SHIM_DIR}/curl" <<CURL
#!/bin/sh
for a in "\$@"; do url="\$a"; done
case "\${url}" in
  https://ghcr.io/token*) echo '{"token":"anonymous-pull-token"}' ;;
  https://ghcr.io/v2/fullsend-ai/fullsend-runner/manifests/*) printf 'HTTP/2 200\r\ndocker-content-digest: ${DIGEST}\r\n\r\n\n200' ;;
  https://api.github.com/repos/fullsend-ai/fullsend/compare/main...*) echo '{"status": "behind"}' ;;
  https://raw.githubusercontent.com/fullsend-ai/fullsend/*/.github/scripts/openshell-version.sh)
    printf 'OPENSHELL_VERSION=0.1.1\nOPENSHELL_SHA=%s\n' "$(printf 'b%.0s' $(seq 1 40))" ;;
  *) exit 22 ;;
esac
CURL
chmod +x "${SHIM_DIR}/podman" "${SHIM_DIR}/systemctl" "${SHIM_DIR}/openshell" "${SHIM_DIR}/curl"

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

echo "== podman secret store =="
# An unreadable secret store makes OpenShell sandbox creation fail (#9675).
# prepare.sh must fail as a runner system failure before pulling the image or
# starting a gateway. The probe runs inside the prune-lock section, after the
# prune and before the pull (prune -> probe -> pull), so a failed probe must
# also release the lock via the EXIT trap. Emptying secrets/secrets.json
# breaks every `podman secret` command; emptying
# secrets/filedriver/secretsdata.json breaks only `secret create`, while
# `secret ls` still exits 0. The probe must catch both.
# The broken shims log every call and delegate everything else to the shims
# above; SECRET_FAULT selects which secret commands fail:
#   index  - ls and create fail (secrets.json emptied)
#   data   - ls passes, create fails (secretsdata.json emptied)
#   rm     - ls and create pass, rm fails
#   stale  - a healthy store that keeps state: create fails while the probe
#            secret exists (a leftover from an earlier attempt of the job),
#            rm deletes it
PROBE_STATE="${FAKE_HOME}/probe-secret-exists"
BROKEN_DIR="${FAKE_HOME}/broken-shims"
CALL_LOG="${FAKE_HOME}/calls.log"
LOCK_LOG="${FAKE_HOME}/lock.log"
REAL_FLOCK=$(command -v flock)
mkdir -p "${BROKEN_DIR}"
# prune_unused_podman_storage only runs an executable podman-prune.sh; install
# a stub that records its call so the prune -> probe -> pull order is visible.
mkdir -p "${FAKE_HOME}/.local/lib/fullsend"
printf '#!/bin/sh\necho "prune" >> "%s"\nexit 0\n' "${CALL_LOG}" \
  > "${FAKE_HOME}/.local/lib/fullsend/podman-prune.sh"
chmod +x "${FAKE_HOME}/.local/lib/fullsend/podman-prune.sh"
# Record flock calls (acquire -x, release -u) in a separate log so the
# last-call check on CALL_LOG is unaffected, then run the real flock on the
# inherited lock fd.
printf '#!/bin/sh\necho "flock $*" >> "%s"\nexec "%s" "$@"\n' \
  "${LOCK_LOG}" "${REAL_FLOCK}" > "${BROKEN_DIR}/flock"
cat > "${BROKEN_DIR}/podman" <<PODMAN
#!/bin/sh
echo "podman \$*" >> "${CALL_LOG}"
if [ "\$1" = secret ]; then
  case "\${SECRET_FAULT}:\$2" in
    index:ls)
      echo "Error: unexpected end of JSON input" >&2; exit 125 ;;
    index:create)
      echo "Error: unexpected end of JSON input" >&2; exit 125 ;;
    data:create)
      echo "Error: creating secret \$3: unexpected end of JSON input" >&2; exit 125 ;;
    rm:rm)
      echo "Error: removing secret \$3: permission denied" >&2; exit 125 ;;
    stale:create)
      if [ -e "${PROBE_STATE}" ]; then
        echo "Error: creating secret \$3: secret name in use" >&2; exit 125
      fi
      : > "${PROBE_STATE}"; exit 0 ;;
    stale:rm)
      if [ ! -e "${PROBE_STATE}" ]; then
        echo "Error: no secret with name or id \$3: no such secret" >&2; exit 125
      fi
      rm -f "${PROBE_STATE}"; exit 0 ;;
  esac
fi
exec "${SHIM_DIR}/podman" "\$@"
PODMAN
cat > "${BROKEN_DIR}/openshell" <<OPENSHELL
#!/bin/sh
echo "openshell \$*" >> "${CALL_LOG}"
exec "${SHIM_DIR}/openshell" "\$@"
OPENSHELL
chmod +x "${BROKEN_DIR}/podman" "${BROKEN_DIR}/openshell" "${BROKEN_DIR}/flock"

# first_call_line <extended regex>: line number of the first matching call in
# CALL_LOG, or empty.
first_call_line() {
  grep -nE "$1" "${CALL_LOG}" | head -n 1 | cut -d: -f1
}

# run_secret_case <label> <fault> <expected rc> <expected message>
#   <last call prefix> [SYSTEM_FAILURE_EXIT_CODE value]
run_secret_case() {
  local label="$1" fault="$2" want_rc="$3" want_msg="$4" want_last="$5"
  local rc=0 output problem="" last
  local sys_env=(-u SYSTEM_FAILURE_EXIT_CODE)
  [ "$#" -ge 6 ] && sys_env=("SYSTEM_FAILURE_EXIT_CODE=$6")
  : > "${CALL_LOG}"
  : > "${LOCK_LOG}"
  output=$(
    cd "${FAKE_HOME}" && \
    PATH="${BROKEN_DIR}:${SHIM_DIR}:${PATH}" \
    HOME="${FAKE_HOME}" \
    JOB_RESPONSE_FILE="${JOB_RESPONSE}" \
    CUSTOM_ENV_CI_JOB_IMAGE="registry.example.com/img:latest" \
    SECRET_FAULT="${fault}" \
    env "${sys_env[@]}" bash "${PREPARE}" 2>&1
  ) || rc=$?
  last=$(tail -n 1 "${CALL_LOG}")

  if [ "${rc}" -ne "${want_rc}" ]; then
    problem="rc=${rc}, want ${want_rc}"
  elif ! printf '%s' "${output}" | grep -qF "ERROR: Podman secret store is unusable on this runner VM: ${want_msg}"; then
    problem="missing secret store error: $(printf '%s' "${output}" | tail -1)"
  elif grep -q '^podman pull' "${CALL_LOG}"; then
    problem="image was pulled"
  elif [ -z "$(first_call_line '^prune$')" ] ||
    [ "$(first_call_line '^prune$')" -ge "$(first_call_line '^podman secret create')" ]; then
    problem="probe did not run after the prune"
  elif tail -n +"$(first_call_line '^podman secret create')" "${CALL_LOG}" | grep -q '^openshell'; then
    # Leftover-gateway teardown runs before the lock; a gateway start would
    # come after the probe.
    problem="gateway was started after the failed probe"
  elif ! grep -q '^flock -x' "${LOCK_LOG}" ||
    [ "$(grep -c '^flock -u' "${LOCK_LOG}")" -lt 1 ]; then
    problem="prune lock was not acquired and released after the failed probe"
  elif [ "${last#"${want_last}"}" = "${last}" ]; then
    # Leftover reaping may run first; nothing (gateway start included)
    # may follow the failed probe call.
    problem="last call is '${last}', want '${want_last}'"
  fi

  if [ -z "${problem}" ]; then
    printf 'ok       %s\n' "${label}"
  else
    printf 'FAIL     %s: %s\n' "${label}" "${problem}"
    failures=$((failures + 1))
  fi
}

run_secret_case "secrets.json emptied -> SYSTEM_FAILURE_EXIT_CODE" \
  index 42 "Error: unexpected end of JSON input" "podman secret create" 42
run_secret_case "secrets.json emptied, SYSTEM_FAILURE_EXIT_CODE unset -> 1" \
  index 1 "Error: unexpected end of JSON input" "podman secret create"
# `secret ls` passes here; only the create probe catches the fault.
run_secret_case "secretsdata.json emptied (secret ls passes) -> SYSTEM_FAILURE_EXIT_CODE" \
  data 42 "Error: creating secret fullsend-secret-probe-1: unexpected end of JSON input" \
  "podman secret create" 42
run_secret_case "secret rm fails after create -> SYSTEM_FAILURE_EXIT_CODE" \
  rm 42 "Error: removing secret fullsend-secret-probe-1: permission denied" \
  "podman secret rm" 42

# A probe secret left by an earlier attempt of the same job (create succeeded,
# rm failed or prepare was killed in between) must not make a healthy store
# look broken: prepare.sh removes the leftover before creating the probe.
: > "${PROBE_STATE}"
: > "${CALL_LOG}"
stale_rc=0
stale_output=$(
  cd "${FAKE_HOME}" && \
  PATH="${BROKEN_DIR}:${SHIM_DIR}:${PATH}" \
  HOME="${FAKE_HOME}" \
  JOB_RESPONSE_FILE="${JOB_RESPONSE}" \
  CUSTOM_ENV_CI_JOB_IMAGE="registry.example.com/img:latest" \
  SECRET_FAULT=stale \
  bash "${PREPARE}" 2>&1
) || stale_rc=$?
if [ "${stale_rc}" -ne 0 ]; then
  printf 'FAIL     leftover probe secret, healthy store: rc=%s: %s\n' \
    "${stale_rc}" "$(printf '%s' "${stale_output}" | tail -1)"
  failures=$((failures + 1))
elif printf '%s' "${stale_output}" | grep -qF "Podman secret store is unusable"; then
  printf 'FAIL     leftover probe secret, healthy store: reported unusable\n'
  failures=$((failures + 1))
elif [ -e "${PROBE_STATE}" ]; then
  printf 'FAIL     leftover probe secret, healthy store: probe secret not removed\n'
  failures=$((failures + 1))
else
  printf 'ok       leftover probe secret, healthy store -> prepare passes\n'
fi

# On a healthy store the probe sits between the prune and the pull.
prune_line=$(first_call_line '^prune$')
probe_line=$(first_call_line '^podman secret create')
pull_line=$(first_call_line '^podman pull')
if [ -n "${prune_line}" ] && [ -n "${probe_line}" ] && [ -n "${pull_line}" ] &&
  [ "${prune_line}" -lt "${probe_line}" ] && [ "${probe_line}" -lt "${pull_line}" ]; then
  printf 'ok       call order is prune -> secret probe -> pull\n'
else
  printf 'FAIL     call order: prune=%s probe=%s pull=%s, want prune < probe < pull\n' \
    "${prune_line:-none}" "${probe_line:-none}" "${pull_line:-none}"
  failures=$((failures + 1))
fi

if [ "${failures}" -ne 0 ]; then
  echo "${failures} case(s) failed" >&2
  exit 1
fi
echo "all cases passed"
