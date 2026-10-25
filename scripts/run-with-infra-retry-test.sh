#!/usr/bin/env bash
# run-with-infra-retry-test.sh — Tests for run-with-infra-retry.sh with a stub
# suite command.
#
# Run from repo root: bash scripts/run-with-infra-retry-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RETRY_SCRIPT="${SCRIPT_DIR}/run-with-infra-retry.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

COUNT_FILE="${TMPDIR}/invocations"
MODES_FILE="${TMPDIR}/marker-modes"
STUB="${TMPDIR}/stub-cmd"

export COUNT_FILE MODES_FILE

# Stub suite command. Counts invocations, writes (or omits) a failure-class
# marker the way the real suite's report writer does, and exits with a
# configured status. Environment inputs:
#   STUB_MODE         write | malformed | missing — marker behavior on failure
#   STUB_CLASS        .class value written in write mode
#   STUB_FAIL_STATUS  exit status on the first failing invocation (default 1)
#   STUB_FAIL_STATUS_2 exit status on later failing invocations (defaults to
#                     STUB_FAIL_STATUS; distinct values prove the wrapper
#                     exits with the FINAL attempt's status)
#   STUB_PASS_ON      invocation number at which the stub starts succeeding
cat >"${STUB}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

COUNT_FILE="${COUNT_FILE:?}"
MODES_FILE="${MODES_FILE:?}"

n="$(cat "${COUNT_FILE}" 2>/dev/null || echo 0)"
n=$((n + 1))
echo "${n}" >"${COUNT_FILE}"

# Mirror the wrapper's marker resolution to find where the report goes.
if [[ -n "${BEHAVIOUR_ARTIFACT_DIR:-}" ]]; then
  marker="${BEHAVIOUR_ARTIFACT_DIR}/failure-class.json"
else
  marker="${BEHAVIOUR_FAILURE_CLASS_FILE:?}"
  # Record the wrapper-created marker's permission mode on every invocation
  # (chmod 600 contract). stat flags differ between GNU and BSD coreutils.
  mode="$(stat -c '%a' "${marker}" 2>/dev/null || stat -f '%Lp' "${marker}")"
  echo "${mode}" >>"${MODES_FILE}"
fi

if [[ -n "${STUB_PASS_ON:-}" && "${n}" -ge "${STUB_PASS_ON}" ]]; then
  exit 0
fi

case "${STUB_MODE:-missing}" in
  write)
    jq -n --arg class "${STUB_CLASS:?}" \
      '{class: $class, infrastructure_failures: 1, scenario_failures: 0, failures: []}' \
      >"${marker}"
    ;;
  malformed)
    printf 'not json\n' >"${marker}"
    ;;
  missing)
    # Write nothing: with an artifact dir configured the marker stays absent,
    # which is how a run that died before its report write looks to the
    # wrapper; on the mktemp path the fresh empty marker stays empty.
    ;;
  *)
    echo "unexpected STUB_MODE: ${STUB_MODE}" >&2
    exit 99
    ;;
esac

if [[ "${n}" -gt 1 && -n "${STUB_FAIL_STATUS_2:-}" ]]; then
  exit "${STUB_FAIL_STATUS_2}"
fi
exit "${STUB_FAIL_STATUS:-1}"
EOF
chmod +x "${STUB}"

# Runs the wrapper against the stub and records its combined output and exit
# status. Always succeeds so set -e never aborts the test script.
run_wrapper() {
  rm -f "${COUNT_FILE}"
  WRAPPER_STATUS=0
  WRAPPER_OUTPUT="$("${RETRY_SCRIPT}" "${STUB}" 2>&1)" || WRAPPER_STATUS=$?
}

# check <name> <expected-status> <expected-invocations> <expected-notice>
# expected-notice: "yes" — the labelled retry notice must appear in the
# wrapper output; "no" — it must not.
check() {
  local name="$1" expected_status="$2" expected_count="$3" expected_notice="$4"
  local count failed=0
  count="$(cat "${COUNT_FILE}" 2>/dev/null || echo 0)"

  if [[ "${WRAPPER_STATUS}" -ne "${expected_status}" ]]; then
    echo "FAIL: ${name}"
    echo "  expected exit status ${expected_status}, got ${WRAPPER_STATUS}"
    failed=1
  fi
  if [[ "${count}" != "${expected_count}" ]]; then
    echo "FAIL: ${name}"
    echo "  expected ${expected_count} stub invocation(s), got ${count}"
    failed=1
  fi
  if [[ "${expected_notice}" == "yes" ]] && ! grep -q 'retrying once' <<<"${WRAPPER_OUTPUT}"; then
    echo "FAIL: ${name}"
    echo "  expected the labelled retry notice in the wrapper output"
    failed=1
  fi
  if [[ "${expected_notice}" == "no" ]] && grep -q 'retrying once' <<<"${WRAPPER_OUTPUT}"; then
    echo "FAIL: ${name}"
    echo "  unexpected retry notice in the wrapper output"
    failed=1
  fi
  if [[ "${failed}" -ne 0 ]]; then
    echo "  wrapper output:"
    sed 's/^/    /' <<<"${WRAPPER_OUTPUT}"
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "PASS: ${name}"
}

# --- Local-run path: BEHAVIOUR_ARTIFACT_DIR unset, mktemp marker ---

unset BEHAVIOUR_ARTIFACT_DIR BEHAVIOUR_FAILURE_CLASS_FILE
export STUB_MODE=write STUB_CLASS=infrastructure STUB_FAIL_STATUS=7 STUB_FAIL_STATUS_2=8

# Success on attempt 1 exits 0 without a retry, even for a stub whose failures
# would classify as infrastructure.
export STUB_PASS_ON=1
run_wrapper
check "success on attempt 1 exits 0 without retry" 0 1 "no"

# Infrastructure failure with attempt 2 also failing: exactly one retry (two
# invocations) and the wrapper exits with attempt 2's status, not attempt 1's.
unset STUB_PASS_ON
run_wrapper
check "infrastructure failure retries exactly once and exits with attempt 2 status" 8 2 "yes"

# Infrastructure failure with attempt 2 succeeding: exit 0 after one retry.
export STUB_PASS_ON=2
run_wrapper
check "infrastructure failure succeeds on attempt 2 exits 0" 0 2 "yes"
unset STUB_PASS_ON

# Scenario failure: no retry, attempt 1's status propagates.
export STUB_CLASS=scenario
run_wrapper
check "scenario failure does not retry" 7 1 "no"

# Empty marker on the mktemp path (run died before its report write): the
# jq read of an empty file yields no class, so no retry.
export STUB_MODE=missing
run_wrapper
check "empty failure-class marker does not retry" 7 1 "no"

# Malformed marker: unparseable, so unclassified and no retry.
export STUB_MODE=malformed
run_wrapper
check "malformed failure-class marker does not retry" 7 1 "no"
if grep -q 'unparseable failure-class report' <<<"${WRAPPER_OUTPUT}"; then
  echo "PASS: malformed marker surfaces a parse warning"
else
  echo "FAIL: malformed marker surfaces a parse warning"
  echo "  wrapper output:"
  sed 's/^/    /' <<<"${WRAPPER_OUTPUT}"
  FAILURES=$((FAILURES + 1))
fi

# --- Artifact-dir path: BEHAVIOUR_ARTIFACT_DIR set, as in CI ---

ART_DIR="${TMPDIR}/artifacts"
mkdir -p "${ART_DIR}"
export BEHAVIOUR_ARTIFACT_DIR="${ART_DIR}"

# Missing marker (run died before its report write): no retry, and the reason
# is surfaced.
export STUB_MODE=missing
run_wrapper
check "missing failure-class report in artifact dir does not retry" 7 1 "no"
if grep -q 'no failure-class report' <<<"${WRAPPER_OUTPUT}"; then
  echo "PASS: missing report in artifact dir surfaces a warning"
else
  echo "FAIL: missing report in artifact dir surfaces a warning"
  echo "  wrapper output:"
  sed 's/^/    /' <<<"${WRAPPER_OUTPUT}"
  FAILURES=$((FAILURES + 1))
fi

# A stale marker from an earlier run must be deleted before attempt 1: the
# stub writes nothing, so only a leftover infrastructure marker (wrongly kept)
# could trigger a retry here.
jq -n --arg class infrastructure \
  '{class: $class, infrastructure_failures: 1, scenario_failures: 0, failures: []}' \
  >"${ART_DIR}/failure-class.json"
run_wrapper
check "stale infrastructure marker is deleted before attempt 1" 7 1 "no"

# Infrastructure failure through the artifact-dir report retries once and
# succeeds on attempt 2.
export STUB_MODE=write STUB_CLASS=infrastructure STUB_PASS_ON=2
run_wrapper
check "infrastructure failure via artifact dir retries once" 0 2 "yes"
unset STUB_PASS_ON

# --- Marker permission contract ---

# The wrapper must pin the mktemp'd marker to mode 600 (spec-review advisory:
# mktemp defaults are platform-dependent; the report may carry redacted
# messages). The stub recorded the mode of every local-path invocation.
recorded="$(grep -c . "${MODES_FILE}" || true)"
if [[ "${recorded}" -eq 0 ]]; then
  echo "FAIL: no marker permission modes were recorded"
  FAILURES=$((FAILURES + 1))
elif grep -qv '^600$' "${MODES_FILE}"; then
  echo "FAIL: wrapper-created marker is not mode 600 everywhere:"
  grep -v '^600$' "${MODES_FILE}" | sed 's/^/  /'
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: wrapper-created marker is mode 600"
fi

if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi

echo "All run-with-infra-retry tests passed."
