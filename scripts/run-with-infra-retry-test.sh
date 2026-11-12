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
RETRY_FROM_FILE="${TMPDIR}/retry-from"
STUB="${TMPDIR}/stub-cmd"

export COUNT_FILE MODES_FILE RETRY_FROM_FILE

# Stub suite command. Counts invocations, writes (or omits) a failure-class
# marker the way the real suite's report writer does, and exits with a
# configured status. Environment inputs:
#   STUB_MODE         write | malformed | missing — marker behavior on failure
#   STUB_MODE_2       marker behavior on later invocations (defaults to
#                     STUB_MODE; "missing" emulates a retry that dies before
#                     its report write)
#   STUB_CLASS        .class value written in write mode
#   STUB_FAIL_STATUS  exit status on the first failing invocation (default 1)
#   STUB_FAIL_STATUS_2 exit status on later failing invocations (defaults to
#                     STUB_FAIL_STATUS; distinct values prove the wrapper
#                     exits with the FINAL attempt's status)
#   STUB_PASS_ON      invocation number at which the stub starts succeeding
#   STUB_EMIT         text to print before failing (fatal-pattern coverage)
#   STUB_EMIT_FD      stdout | stderr — where STUB_EMIT goes (default stdout)
#   STUB_EMIT_NOISE   count of filler stdout lines printed after STUB_EMIT
#                     (competing-traffic coverage for the capture race)
#   STUB_SLEEP        seconds to sleep before exiting (attempt-duration guard)
# Every invocation appends "<n> <BEHAVIOUR_RETRY_FROM or unset> <class read
# from that file, or ->" to RETRY_FROM_FILE.
cat >"${STUB}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

COUNT_FILE="${COUNT_FILE:?}"
MODES_FILE="${MODES_FILE:?}"

n="$(cat "${COUNT_FILE}" 2>/dev/null || echo 0)"
n=$((n + 1))
echo "${n}" >"${COUNT_FILE}"

retry_class="-"
if [[ -n "${BEHAVIOUR_RETRY_FROM:-}" ]]; then
  retry_class="$(jq -r '.class // "-"' "${BEHAVIOUR_RETRY_FROM}" 2>/dev/null || echo unreadable)"
fi
echo "${n} ${BEHAVIOUR_RETRY_FROM:-unset} ${retry_class}" >>"${RETRY_FROM_FILE:?}"

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

if [[ -n "${STUB_SLEEP:-}" ]]; then
  sleep "${STUB_SLEEP}"
fi

if [[ -n "${STUB_PASS_ON:-}" && "${n}" -ge "${STUB_PASS_ON}" ]]; then
  exit 0
fi

stub_mode="${STUB_MODE:-missing}"
if [[ "${n}" -gt 1 && -n "${STUB_MODE_2:-}" ]]; then
  stub_mode="${STUB_MODE_2}"
fi

case "${stub_mode}" in
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
    echo "unexpected stub mode: ${stub_mode}" >&2
    exit 99
    ;;
esac

if [[ -n "${STUB_EMIT:-}" ]]; then
  if [[ "${STUB_EMIT_FD:-stdout}" == "stderr" ]]; then
    echo "${STUB_EMIT}" >&2
  else
    echo "${STUB_EMIT}"
  fi
fi

if [[ -n "${STUB_EMIT_NOISE:-}" ]]; then
  # Filler stdout lines after the emitted pattern, emulating a verbose run's
  # volume: in a two-writer capture this traffic overwrites the pattern's
  # bytes at overlapping offsets, so the guard must survive it.
  for ((i = 0; i < STUB_EMIT_NOISE; i++)); do
    echo "noise line ${i}"
  done
fi

if [[ "${n}" -gt 1 && -n "${STUB_FAIL_STATUS_2:-}" ]]; then
  exit "${STUB_FAIL_STATUS_2}"
fi
exit "${STUB_FAIL_STATUS:-1}"
EOF
chmod +x "${STUB}"

# Runs the wrapper against the stub and records its combined output and exit
# status. Always succeeds so set -e never aborts the test script.
run_wrapper() {
  rm -f "${COUNT_FILE}" "${RETRY_FROM_FILE}"
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

# --- Fatal output patterns refuse the retry (PR #8801 review) ---

# A race warning, panic, or hard-timeout line in attempt 1's output forbids
# the retry even when the marker class says infrastructure: a report written
# before the process died cannot authorize retrying a product defect.
export STUB_MODE=write STUB_CLASS=infrastructure STUB_FAIL_STATUS=7
export STUB_EMIT="WARNING: DATA RACE"
run_wrapper
check "data-race output does not retry despite infrastructure marker" 7 1 "no"
if grep -q 'fatal pattern' <<<"${WRAPPER_OUTPUT}"; then
  echo "PASS: fatal-pattern refusal surfaces a notice"
else
  echo "FAIL: fatal-pattern refusal surfaces a notice"
  echo "  wrapper output:"
  sed 's/^/    /' <<<"${WRAPPER_OUTPUT}"
  FAILURES=$((FAILURES + 1))
fi

export STUB_EMIT="panic: suite fixture exploded"
run_wrapper
check "panic output does not retry despite infrastructure marker" 7 1 "no"

export STUB_EMIT="panic: test timed out after 45m0s"
run_wrapper
check "test-timeout output does not retry despite infrastructure marker" 7 1 "no"

# The race detector writes to stderr; the fatal-pattern check must see it.
export STUB_EMIT_FD=stderr
run_wrapper
check "fatal pattern on stderr does not retry" 7 1 "no"

# Heavy stdout traffic after the stderr pattern must not bury it: a verbose
# run floods stdout while the fatal line lands on stderr, and the single-tee
# capture keeps both. A two-tee capture clobbered the streams at independent
# offsets, exactly the race this case pins down.
export STUB_EMIT_NOISE=5000
run_wrapper
check "fatal pattern on stderr under heavy stdout traffic does not retry" 7 1 "no"
unset STUB_EMIT_NOISE
unset STUB_EMIT_FD

# Clean output plus an infrastructure marker still retries: the fatal check
# must not over-trigger and starve the retry the class authorizes.
unset STUB_EMIT
run_wrapper
check "clean output with infrastructure marker still retries" 8 2 "yes"

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

# A retry that dies before writing its report leaves the active report empty
# rather than still describing attempt 1, and attempt 1's report stays
# readable beside it.
export STUB_MODE=write STUB_CLASS=infrastructure STUB_MODE_2=missing
run_wrapper
check "retry without a report leaves no stale classification" 8 2 "yes"
if [[ -s "${ART_DIR}/failure-class.json" ]]; then
  echo "FAIL: attempt 1's classification survived as the final report"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: the active report no longer describes attempt 1"
fi
if [[ "$(jq -r .class "${ART_DIR}/failure-class.json.attempt-1")" == "infrastructure" ]]; then
  echo "PASS: attempt 1's report is kept beside the active report"
else
  echo "FAIL: attempt 1's report was not kept"
  FAILURES=$((FAILURES + 1))
fi
unset STUB_MODE_2

# Attempt 2 is pointed at attempt 1's kept report so the suite reruns only
# the failed scenarios; attempt 1 never gets a selection, even one left in
# the caller's environment.
export STUB_MODE=write STUB_CLASS=infrastructure STUB_PASS_ON=2
BEHAVIOUR_RETRY_FROM="${TMPDIR}/leftover.json" run_wrapper
check "retry passes attempt 1's report to attempt 2" 0 2 "yes"
expected_retry_from="1 unset -
2 ${ART_DIR}/failure-class.json.attempt-1 infrastructure"
if [[ "$(cat "${RETRY_FROM_FILE}")" == "${expected_retry_from}" ]]; then
  echo "PASS: only attempt 2 gets BEHAVIOUR_RETRY_FROM, naming attempt 1's report"
else
  echo "FAIL: BEHAVIOUR_RETRY_FROM per attempt"
  echo "  expected:"
  sed 's/^/    /' <<<"${expected_retry_from}"
  echo "  got:"
  sed 's/^/    /' "${RETRY_FROM_FILE}"
  FAILURES=$((FAILURES + 1))
fi
unset STUB_PASS_ON

# If attempt 1's report cannot be copied aside, it is not truncated: attempt
# 2 reads it in place, so neither the classification nor the failed
# scenarios are lost (PR #8801 review).
mkdir -p "${TMPDIR}/failing-cp"
printf '#!/usr/bin/env bash\nexit 1\n' >"${TMPDIR}/failing-cp/cp"
chmod +x "${TMPDIR}/failing-cp/cp"
saved_path="${PATH}"
PATH="${TMPDIR}/failing-cp:${PATH}"
export STUB_MODE=write STUB_CLASS=infrastructure STUB_MODE_2=missing
run_wrapper
PATH="${saved_path}"
check "failed report copy still retries" 8 2 "yes"
if [[ "$(sed -n 2p "${RETRY_FROM_FILE}")" == "2 ${ART_DIR}/failure-class.json infrastructure" ]]; then
  echo "PASS: failed report copy leaves attempt 1's report in place for attempt 2"
else
  echo "FAIL: failed report copy leaves attempt 1's report in place for attempt 2"
  sed 's/^/    /' "${RETRY_FROM_FILE}"
  FAILURES=$((FAILURES + 1))
fi
unset STUB_MODE_2

# An attempt 1 that ran past INFRA_RETRY_MAX_ATTEMPT1_SECONDS is not retried:
# the retry could not finish inside the job's timeout.
export STUB_MODE=write STUB_CLASS=infrastructure STUB_SLEEP=2
INFRA_RETRY_MAX_ATTEMPT1_SECONDS=0 run_wrapper
check "attempt 1 over the duration limit does not retry" 7 1 "no"
if grep -q 'INFRA_RETRY_MAX_ATTEMPT1_SECONDS=0; not retrying' <<<"${WRAPPER_OUTPUT}"; then
  echo "PASS: duration-limit refusal surfaces a notice"
else
  echo "FAIL: duration-limit refusal surfaces a notice"
  sed 's/^/    /' <<<"${WRAPPER_OUTPUT}"
  FAILURES=$((FAILURES + 1))
fi
unset STUB_SLEEP

# Within the limit, the retry still happens.
export STUB_PASS_ON=2
INFRA_RETRY_MAX_ATTEMPT1_SECONDS=600 run_wrapper
check "attempt 1 within the duration limit retries" 0 2 "yes"
unset STUB_PASS_ON

# A malformed limit is a usage error, before anything runs.
INFRA_RETRY_MAX_ATTEMPT1_SECONDS=40m run_wrapper
check "malformed duration limit is a usage error" 2 0 "no"

# A failed output capture can't rule out fatal patterns, so it refuses the
# retry and the command's own status is reported, not tee's.
mkdir -p "${TMPDIR}/failing-tee"
cat >"${TMPDIR}/failing-tee/tee" <<'EOF'
#!/usr/bin/env bash
cat >/dev/null
exit 3
EOF
chmod +x "${TMPDIR}/failing-tee/tee"
saved_path="${PATH}"
PATH="${TMPDIR}/failing-tee:${PATH}"
run_wrapper
PATH="${saved_path}"
check "failed output capture refuses the retry" 7 1 "no"

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
