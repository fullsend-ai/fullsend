#!/usr/bin/env bash
# scan-ratelimit-evidence_test.sh — tests for scan-ratelimit-evidence.sh: its
# pure analysis layer, driven from hermetic fixture logs with the scanner
# sourced (main only runs when the script is executed directly), and main's
# window bounding end to end against a stubbed gh.
#
# Covers: the marker contract — run-listing failures, the same-error poll and
# lookup tails, several hits on one diagnostic line and across lines, and the
# near-miss "(failed on N of M polls during the wait; last: ...)" that must
# NOT match; snippet bounding and tab/CRLF collapsing (the TSV hand-off to jq
# holds); a clean run; a retry-masked occurrence (markers on the first
# attempt of an ultimately-passing run); a failing run with markers; the
# report's window statement (a thinned window stays visible) and the
# incomplete flag for unfetchable logs; the two-level verdict (strict: any
# occurrence; lenient: retry-masked occurrences), including the zero case;
# and window bounding — only the requested number of most recent runs are
# scanned, the run outside the window is never queried, and the human table
# goes to stderr while stdout carries only the JSON report.
#
# Hermetic: no network. Every gh the test could reach — including any a
# sourced function might call — answers from a PATH-front stub backed by
# fixture files, and the analysis layer is checked to make no gh calls.
#
# Run from the repo root:
#   bash skills/e2e-health/scripts/scan-ratelimit-evidence_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCANNER="${SCRIPT_DIR}/scan-ratelimit-evidence.sh"
FAILURES=0

pass() { echo "ok       $*"; }
fail() {
  echo "FAIL     $*" >&2
  FAILURES=$((FAILURES + 1))
}

# expect_eq NAME GOT WANT
expect_eq() {
  if [[ "$2" == "$3" ]]; then
    pass "$1"
  else
    fail "$1: got '$2', want '$3'"
  fi
}

# expect_jq NAME JQ_FILTER FILE WANT — jq -r FILTER FILE must print WANT.
expect_jq() {
  local got
  got=$(jq -r "$2" "$3") || { fail "$1: jq filter failed: $2"; return; }
  expect_eq "$1" "${got}" "$4"
}

# expect_line NAME ERE FILE — one line of FILE must match the ERE.
expect_line() {
  if grep -Eq -- "$2" "$3"; then
    pass "$1"
  else
    fail "$1: $3 lacks /$2/"
    sed 's/^/         /' "$3" >&2
  fi
}

# expect_absent NAME ERE FILE — no line of FILE may match the ERE.
expect_absent() {
  if grep -Eq -- "$2" "$3"; then
    fail "$1: $3 unexpectedly matches /$2/"
  else
    pass "$1"
  fi
}

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
LOGS="${TMP}/logs"
SHIM_DIR="${TMP}/bin"
E2E="${TMP}/e2e-state"
mkdir -p "${LOGS}" "${SHIM_DIR}" "${E2E}"

# --- Fixture logs ------------------------------------------------------------
# Shapes copied from harnessTimeoutDiagnostics
# (pkg/behaviourtest/drivers/ci/githubactions/githubactions.go): the marker
# strings the closure criterion counts, the near-miss that reports differing
# poll errors, and clean output.

# A clean behaviour-job log.
cat > "${LOGS}/clean.txt" <<'EOF'
2026-10-20T12:00:01.1234567Z ##[group]Run behaviour suite
2026-10-20T12:00:02.0000000Z e2e: provisioned repo o/test-repo-01 (run 9876544)
2026-10-20T12:04:44.0000000Z e2e: harness run 9876544 succeeded in 4m42s
2026-10-20T12:04:45.0000000Z ##[endgroup]
EOF

# A rate-limited attempt: one timeout line carrying every marker kind (the
# run-listing failure with its same-error polls tail, the artifact listing's
# polls tail, and the run-lookups tail), then a summary step quoting the
# run-listing failure again — several hits per line and across lines.
cat > "${LOGS}/rate-limited.txt" <<'EOF'
2026-10-20T12:00:01.1234567Z ##[group]Run behaviour suite
2026-10-20T12:00:02.0000000Z e2e: provisioned repo o/test-repo-01 (run 9876543)
2026-10-20T12:15:03.7654321Z ##[error]harness wait exceeded its budget: waiting for the run: context deadline exceeded; run listing failed: github api: 403 retryable error after 5 attempts on GET /repos/o/r/actions/workflows/fullsend.yaml/runs (last delay: 54s) (same error on 14 of 14 polls during the wait); artifact listing failed: github api: 403 retryable error after 5 attempts on GET /repos/o/r/actions/artifacts (last delay: 7s) (same error on 14 of 14 polls during the wait); run lookups (same error on 6 of 6 lookups during the wait); github rate limit: 6 remaining of 5000, resets in 8m12s (observed 45s ago)
2026-10-20T12:15:04.0000000Z ##[error]behaviour suite failed: the harness wait exceeded its budget
2026-10-20T12:15:05.0000000Z ##[group]Publish behaviour summary
2026-10-20T12:15:06.0000000Z behaviour summary: the harness wait timed out; run listing failed: github api: 403 retryable error after 5 attempts on GET /repos/o/r/actions/workflows/fullsend.yaml/runs (last delay: 54s) (same error on 14 of 14 polls during the wait)
2026-10-20T12:15:07.0000000Z ##[endgroup]
EOF

# A flaky, not rate-limited, attempt: the run listing succeeded and the
# polls failed with differing errors — the near-miss shapes that must not
# match (the criterion targets the blind-wait signature of identical
# repetition).
cat > "${LOGS}/near-miss.txt" <<'EOF'
2026-10-20T09:00:01.0000000Z ##[group]Run behaviour suite
2026-10-20T09:10:02.0000000Z ##[error]harness wait exceeded its budget: waiting for the run: context deadline exceeded; 1 run since the trigger, agent job "behaviour" queued (failed on 3 of 3 polls during the wait; last: github api: 502 server error on GET /repos/o/r/actions/workflows/fullsend.yaml/runs); run lookups (failed on 3 of 3 lookups during the wait; last: github api: 502 server error on GET /repos/o/r/actions/runs/104954029); github rate limit: 4987 remaining of 5000, resets in 58m1s (observed 2m10s ago)
2026-10-20T09:10:03.0000000Z ##[endgroup]
EOF

# A log with a tab inside the snippet window and a CRLF line ending: the
# snippet must collapse the tab (so the TSV hand-off to jq holds) and strip
# the carriage return.
printf '2026-10-21T08:00:00Z provisioned repo o/test-repo-02 (run 9876550)\n' \
  > "${LOGS}/tabbed.txt"
printf '2026-10-21T08:10:00Z harness wait exceeded its budget: waiting for the run:\tcontext deadline exceeded; run lookups (same error on 2 of 2 lookups during the wait); github rate limit: unknown (no observation yet)\r\n' \
  >> "${LOGS}/tabbed.txt"

# --- gh stub -----------------------------------------------------------------
# Answers the scanner's call shapes from files under STUB_STATE and logs
# every invocation, so the test can prove which runs were touched:
#   gh run list --repo R --workflow=e2e.yml --branch=main --status=completed
#       -> cat ${STUB_STATE}/candidates.json
#   gh api repos/R/actions/runs/<run>/attempts/<n>/jobs?per_page=100
#       -> cat ${STUB_STATE}/runs/<run>/jobs-<n>.json
#   gh api repos/R/actions/runs/<run>/attempts/<n> --jq '.conclusion // ""'
#       -> the conclusion from ${STUB_STATE}/runs/<run>/attempt-<n>.json
#   gh api repos/R/actions/jobs/<job>/logs
#       -> cat ${STUB_STATE}/jobs/<job>/log.txt
cat > "${SHIM_DIR}/gh" <<'EOF'
#!/usr/bin/env bash
S="${STUB_STATE}"
echo "gh $*" >> "${S}/gh.log"
if [[ "${1:-}" == "run" ]]; then
  shift
  [[ "${1:-}" == "list" ]] && shift
  workflow="" branch="" status=""
  # The scanner mixes forms: --workflow=e2e.yml but --repo R and --json FIELDS.
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --workflow=*) workflow="${1#--workflow=}"; shift ;;
      --workflow) workflow="$2"; shift 2 ;;
      --branch=*) branch="${1#--branch=}"; shift ;;
      --branch) branch="$2"; shift 2 ;;
      --status=*) status="${1#--status=}"; shift ;;
      --status) status="$2"; shift 2 ;;
      --limit=*|--repo=*|--json=*) shift ;;
      --repo|--limit|--json) shift 2 ;;
      *) echo "gh run list stub: unexpected arg $1" >&2; exit 2 ;;
    esac
  done
  [[ "${workflow}" == "e2e.yml" && "${branch}" == "main" && "${status}" == "completed" ]] \
    || { echo "gh run list stub: unexpected filter: workflow=${workflow} branch=${branch} status=${status}" >&2; exit 2; }
  cat "${S}/candidates.json"
  exit 0
fi
[[ "${1:-}" == "api" ]] || { echo "unexpected gh $*" >&2; exit 2; }
shift
path="" jq_filter=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --jq) jq_filter="$2"; shift 2 ;;
    repos/*) path="${1%%\?*}"; shift ;;
    *) echo "gh api stub: unexpected arg $1" >&2; exit 2 ;;
  esac
done
# gh --jq prints a scalar null as an empty line, unlike jq -r.
out() {
  if [[ -n "${jq_filter}" ]]; then
    jq -r "${jq_filter} | if . == null then \"\" else . end" <<< "$1"
  else
    echo "$1"
  fi
}
run="" attempt="" job=""
case "${path}" in
  repos/*/actions/runs/*/attempts/*/jobs)
    run="${path#repos/*/actions/runs/}"; run="${run%%/*}"
    attempt="${path##*/attempts/}"; attempt="${attempt%%/*}"
    cat "${S}/runs/${run}/jobs-${attempt}.json" ;;
  repos/*/actions/runs/*/attempts/*)
    run="${path#repos/*/actions/runs/}"; run="${run%%/*}"
    attempt="${path##*/}"
    out "$(cat "${S}/runs/${run}/attempt-${attempt}.json")" ;;
  repos/*/actions/jobs/*/logs)
    job="${path#repos/*/actions/jobs/}"; job="${job%%/*}"
    cat "${S}/jobs/${job}/log.txt" ;;
  *)
    echo "gh api stub: unhandled path: ${path}" >&2; exit 2 ;;
esac
EOF
chmod +x "${SHIM_DIR}/gh"

# Front the stub for the whole test: the analysis layer must not call gh at
# all (asserted below), and anything that does reaches the stub, never the
# network or a real gh.
export STUB_STATE="${E2E}"
export PATH="${SHIM_DIR}:${PATH}"
: > "${E2E}/gh.log"

# --- The layer under test ----------------------------------------------------

# Source the scanner: main is guarded by the BASH_SOURCE check, so only the
# functions load. shellcheck cannot follow the dynamic path.
# shellcheck source=/dev/null
source "${SCANNER}"

# --- 1. analyze_log: the marker contract --------------------------------------

RATE_LIMITED_MARKERS="${TMP}/markers-rate-limited.json"
analyze_log < "${LOGS}/rate-limited.txt" > "${RATE_LIMITED_MARKERS}"

expect_jq "rate-limited log: every hit is found (two lines, six hits)" \
  'length' "${RATE_LIMITED_MARKERS}" "6"
expect_jq "rate-limited log: hits keep log order across kinds and lines" \
  'map(.marker) | join(",")' "${RATE_LIMITED_MARKERS}" \
  "run-listing-failed,same-error-polls,same-error-polls,same-error-lookups,run-listing-failed,same-error-polls"
expect_jq "rate-limited log: one entry per run-listing failure" \
  '[.[] | select(.marker == "run-listing-failed")] | length' \
  "${RATE_LIMITED_MARKERS}" "2"
expect_jq "rate-limited log: one entry per same-error polls tail" \
  '[.[] | select(.marker == "same-error-polls")] | length' \
  "${RATE_LIMITED_MARKERS}" "3"
expect_jq "rate-limited log: the lookup tail is its own marker kind" \
  '[.[] | select(.marker == "same-error-lookups")] | length' \
  "${RATE_LIMITED_MARKERS}" "1"
expect_jq "rate-limited log: the run-listing snippet carries the matched text" \
  '.[0].snippet | contains("run listing failed:")' "${RATE_LIMITED_MARKERS}" "true"
expect_jq "rate-limited log: the lookup hit's snippet carries its matched text" \
  '.[3].snippet | contains("same error on 6 of 6 lookups during the wait")' \
  "${RATE_LIMITED_MARKERS}" "true"
expect_jq "rate-limited log: a long line's snippet is bounded on both sides" \
  '.[0].snippet | startswith("...") and endswith("...")' "${RATE_LIMITED_MARKERS}" "true"

NEAR_MISS_MARKERS="${TMP}/markers-near-miss.json"
analyze_log < "${LOGS}/near-miss.txt" > "${NEAR_MISS_MARKERS}"
expect_line "near-miss fixture carries the non-marker poll tail" \
  'failed on 3 of 3 polls during the wait; last:' "${LOGS}/near-miss.txt"
expect_line "near-miss fixture carries the non-marker lookup tail" \
  'failed on 3 of 3 lookups during the wait; last:' "${LOGS}/near-miss.txt"
expect_jq "near-miss log: differing poll errors are not markers" \
  'length' "${NEAR_MISS_MARKERS}" "0"

CLEAN_MARKERS="${TMP}/markers-clean.json"
analyze_log < "${LOGS}/clean.txt" > "${CLEAN_MARKERS}"
expect_jq "clean log: no markers" 'length' "${CLEAN_MARKERS}" "0"
expect_eq "empty log: no markers" "$(analyze_log < /dev/null | jq 'length')" "0"

TABBED_MARKERS="${TMP}/markers-tabbed.json"
analyze_log < "${LOGS}/tabbed.txt" > "${TABBED_MARKERS}"
expect_jq "tabbed log: the lookup marker is found" 'length' "${TABBED_MARKERS}" "1"
expect_jq "tabbed log: the marker kind distinguishes lookups from polls" \
  '.[0].marker' "${TABBED_MARKERS}" "same-error-lookups"
expect_jq "tabbed log: a tab in the window does not cut the snippet short" \
  '.[0].snippet | contains("context deadline exceeded; run lookups")' "${TABBED_MARKERS}" "true"
expect_jq "tabbed log: tabs are collapsed" \
  'any(.[]; .snippet | contains("\t"))' "${TABBED_MARKERS}" "false"
expect_jq "tabbed log: carriage returns are stripped" \
  'any(.[]; .snippet | contains("\r"))' "${TABBED_MARKERS}" "false"

# --- 2. build_attempt_record ---------------------------------------------------

REC="${TMP}/rec.json"
build_attempt_record 3 failure ran '[{"marker":"same-error-polls","snippet":"x"}]' > "${REC}"
expect_jq "attempt record: attempt, conclusion, job state and markers pass through" \
  '[.attempt, .conclusion, .behaviour_job, (.markers | length)] | map(tostring) | join(",")' \
  "${REC}" "3,failure,ran,1"
expect_jq "attempt record: no log_error when the log was fetched" \
  'has("log_error")' "${REC}" "false"

build_attempt_record 1 "" skipped '[]' > "${REC}"
expect_jq "attempt record: an empty conclusion becomes null" \
  '.conclusion == null' "${REC}" "true"

build_attempt_record 2 failure ran '[]' "gh: HTTP 502 while fetching the log" > "${REC}"
expect_jq "attempt record: a failed log fetch is recorded as log_error" \
  '.log_error' "${REC}" "gh: HTTP 502 while fetching the log"

# --- 3. The fixture window: three runs composed the way scan_run would --------

# run_row ID ATTEMPT_COUNT CONCLUSION — one gh run-listing row.
run_row() {
  jq -nc --argjson id "$1" --argjson attempts "$2" --arg conclusion "$3" '
    {databaseId: $id, attempt: $attempts, conclusion: $conclusion,
     createdAt: "2026-10-20T12:00:00Z",
     url: ("https://api.example.test/repos/o/r/actions/runs/" + ($id | tostring)),
     displayTitle: ("e2e " + ($id | tostring)), headSha: "0123456789abcdef"}'
}

# evidence ROW RECORD... — one run's evidence entry: attempt records sorted
# by attempt and attached to the listing row, exactly scan_run's pipeline.
evidence() {
  local row="$1" dir n r
  shift
  dir=$(mktemp -d "${TMP}/recs.XXXXXX")
  n=0
  for r in "$@"; do
    n=$((n + 1))
    printf '%s\n' "${r}" > "${dir}/rec-$(printf '%02d' "${n}").json"
  done
  jq -s 'sort_by(.attempt)' "${dir}"/rec-*.json > "${dir}/recs.json"
  jq -n --argjson run "${row}" --slurpfile recs "${dir}/recs.json" \
    '{run: $run, attempts: ($recs[0] // [])}' | build_run_evidence
  rm -rf "${dir}"
}

# Run 401 — clean: one attempt, nothing to report.
ROW_401=$(run_row 401 1 success)
REC_401_1=$(build_attempt_record 1 success ran "$(analyze_log < "${LOGS}/clean.txt")")
EVIDENCE_401=$(evidence "${ROW_401}" "${REC_401_1}")

# Run 402 — retry-masked: the first attempt failed rate-limited, the
# automatic retry passed; the run ultimately passed with markers on
# attempt 1.
ROW_402=$(run_row 402 2 success)
REC_402_1=$(build_attempt_record 1 failure ran "$(analyze_log < "${LOGS}/rate-limited.txt")")
REC_402_2=$(build_attempt_record 2 success ran "$(analyze_log < "${LOGS}/clean.txt")")
EVIDENCE_402=$(evidence "${ROW_402}" "${REC_402_1}" "${REC_402_2}")

# Run 403 — failing with markers: both attempts failed; the first carried
# the markers, the second failed with only the near-miss shape.
ROW_403=$(run_row 403 2 failure)
REC_403_1=$(build_attempt_record 1 failure ran "$(analyze_log < "${LOGS}/rate-limited.txt")")
REC_403_2=$(build_attempt_record 2 failure ran "$(analyze_log < "${LOGS}/near-miss.txt")")
EVIDENCE_403=$(evidence "${ROW_403}" "${REC_403_1}" "${REC_403_2}")

# Most recent first, like main's kept order.
RUNS="${TMP}/runs.json"
printf '%s\n%s\n%s\n' "${EVIDENCE_403}" "${EVIDENCE_402}" "${EVIDENCE_401}" \
  | jq -s . > "${RUNS}"

# --- 4. verdict_from_runs: the two signal levels -------------------------------

VERDICT="${TMP}/verdict.json"
verdict_from_runs < "${RUNS}" > "${VERDICT}"
expect_jq "verdict: every scanned run is counted" '.runs_scanned' "${VERDICT}" "3"
expect_jq "verdict: the strict signal counts every run with markers" \
  '.runs_with_markers' "${VERDICT}" "2"
expect_jq "verdict: the lenient signal counts retry-masked occurrences" \
  '.runs_with_markers_that_passed' "${VERDICT}" "1"

printf '[]' | verdict_from_runs > "${VERDICT}"
expect_jq "verdict: zero occurrences report zero at both levels" \
  '[.runs_scanned, .runs_with_markers, .runs_with_markers_that_passed]
     | map(tostring) | join(",")' \
  "${VERDICT}" "0,0,0"

# --- 5. render_report: per-run evidence and the window statement ----------------

REPORT="${TMP}/report.json"
render_report "o/r" 3 < "${RUNS}" > "${REPORT}"

expect_jq "report: repository, workflow and branch" \
  '[.repository, .workflow, .branch] | join(",")' "${REPORT}" "o/r,e2e.yml,main"
expect_jq "report: states the window and the runs scanned" \
  '[.window.runs, .runs_scanned] | map(tostring) | join(",")' "${REPORT}" "3,3"
expect_jq "report: one entry per scanned run" '.runs | length' "${REPORT}" "3"
expect_jq "report: a complete scan is not incomplete" '.incomplete' "${REPORT}" "false"
expect_jq "report: embeds the verdict" \
  '[.verdict.runs_with_markers, .verdict.runs_with_markers_that_passed]
     | map(tostring) | join(",")' \
  "${REPORT}" "2,1"

# Clean run (spec: clean runs are reported clean).
expect_jq "clean run: reported with no markers" \
  '.runs[] | select(.run_id == 401) | .has_markers' "${REPORT}" "false"
expect_jq "clean run: ultimately passed" \
  '.runs[] | select(.run_id == 401) | .ultimately_passed' "${REPORT}" "true"

# Retry-masked occurrence (spec: named with the attempt that carried it).
expect_jq "masked run: an ultimately-passing run with markers" \
  '.runs[] | select(.run_id == 402)
     | [.has_markers, .ultimately_passed] | map(tostring) | join(",")' \
  "${REPORT}" "true,true"
expect_jq "masked run: the markers are on attempt 1, with their kinds" \
  '.runs[] | select(.run_id == 402) | .attempts[] | select(.attempt == 1)
     | .markers | map(.marker) | join(",")' \
  "${REPORT}" \
  "run-listing-failed,same-error-polls,same-error-polls,same-error-lookups,run-listing-failed,same-error-polls"
expect_jq "masked run: the evidence carries a snippet of the matching line" \
  '.runs[] | select(.run_id == 402) | .attempts[] | select(.attempt == 1)
     | .markers[0].snippet | contains("run listing failed:")' \
  "${REPORT}" "true"
expect_jq "masked run: the retried attempt is clean" \
  '.runs[] | select(.run_id == 402) | .attempts[] | select(.attempt == 2)
     | .markers | length' \
  "${REPORT}" "0"
expect_jq "masked run: conclusions come from metadata, not log parsing" \
  '.runs[] | select(.run_id == 402)
     | [.conclusion, .attempts[0].conclusion, .attempts[1].conclusion] | join(",")' \
  "${REPORT}" "success,failure,success"

# Failing run with markers (spec: named per attempt).
expect_jq "failing run: a failing run with markers" \
  '.runs[] | select(.run_id == 403)
     | [.conclusion, .has_markers, .ultimately_passed] | map(tostring) | join(",")' \
  "${REPORT}" "failure,true,false"
expect_jq "failing run: the markers are on attempt 1" \
  '.runs[] | select(.run_id == 403) | .attempts[] | select(.attempt == 1)
     | .markers | length' \
  "${REPORT}" "6"
expect_jq "failing run: the near-miss attempt stays clean" \
  '.runs[] | select(.run_id == 403) | .attempts[] | select(.attempt == 2)
     | .markers | length' \
  "${REPORT}" "0"

# A thinned window stays visible: fewer runs than requested is reported,
# never silently padded.
REPORT_THIN="${TMP}/report-thin.json"
render_report "o/r" 20 < "${RUNS}" > "${REPORT_THIN}"
expect_jq "thinned window: the requested window is stated" \
  '.window.runs' "${REPORT_THIN}" "20"
expect_jq "thinned window: the actual scan count is stated" \
  '.runs_scanned' "${REPORT_THIN}" "3"

# An unfetchable log is unknown, not empty: the attempt records a log_error
# and the report is flagged incomplete.
REPORT_LOGERR="${TMP}/report-logerr.json"
ROW_501=$(run_row 501 1 failure)
REC_501_1=$(build_attempt_record 1 failure ran '[]' "gh: HTTP 502 while fetching the log")
printf '%s\n' "$(evidence "${ROW_501}" "${REC_501_1}")" | jq -s . \
  | render_report "o/r" 1 > "${REPORT_LOGERR}"
expect_jq "log error: the report is flagged incomplete" \
  '.incomplete' "${REPORT_LOGERR}" "true"
expect_jq "log error: the attempt names what failed" \
  '.runs[0].attempts[0].log_error' "${REPORT_LOGERR}" "gh: HTTP 502 while fetching the log"
expect_jq "log error: unknown markers are not occurrences (the verdict may undercount)" \
  '.verdict.runs_with_markers' "${REPORT_LOGERR}" "0"

# --- 6. render_table: the human-readable summary --------------------------------

TABLE="${TMP}/table.txt"
render_table < "${REPORT}" > "${TABLE}"
expect_line "table: names the workflow, repository and branch" \
  'rate-limit evidence: e2e.yml behaviour runs on o/r \(main\)' "${TABLE}"
expect_line "table: states the window and the scan count" \
  'window: 3 most recent completed runs whose behaviour job ran; 3 scanned' "${TABLE}"
expect_line "table: the clean run shows no markers" \
  '^401 +success +0 +-$' "${TABLE}"
expect_line "table: the masked run names the attempt and marker kinds" \
  '^402 +success +6 +attempt 1: run-listing-failed, same-error-polls' "${TABLE}"
expect_line "table: the failing run is listed with its markers" \
  '^403 +failure +6 +attempt 1: run-listing-failed' "${TABLE}"
expect_line "table: the verdict at the strict level" \
  'verdict: 2 run\(s\) with markers \(strict signal: any occurrence\)' "${TABLE}"
expect_line "table: the verdict at the lenient level" \
  '1 of those ultimately passed \(lenient signal: retry-masked occurrences\)' "${TABLE}"

# --- 7. Hermeticity checkpoint ---------------------------------------------------

# Nothing above may have invoked gh: the analysis layer has no network path,
# and because the whole test runs with the stub in front, an empty log also
# proves the test harness itself made no gh calls.
if [[ -s "${E2E}/gh.log" ]]; then
  fail "hermetic: the analysis layer made gh calls: $(cat "${E2E}/gh.log")"
else
  pass "hermetic: the analysis layer and its tests make no gh calls"
fi

# --- 8. Window bounding end to end (stubbed gh) -----------------------------------

# Four completed candidates on main, oldest first in the listing (main sorts
# most recent first itself); the window asks for 3, so run 4001 — the oldest
# — must never be scanned. No fixtures exist for it: touching it would fail
# the stub loudly.
#   4004  1 attempt   success  clean
#   4003  2 attempts  success  attempt 1 rate-limited (masked), attempt 2 clean
#   4002  2 attempts  failure  attempt 1 rate-limited, attempt 2 near-miss only
#   4001  1 attempt   success  outside the window
jq -nc '
  [ {databaseId: 4001, attempt: 1, conclusion: "success",
     createdAt: "2026-10-18T08:00:00Z",
     url: "https://api.example.test/repos/o/r/actions/runs/4001",
     displayTitle: "e2e 4001", headSha: "1111111111111111"},
    {databaseId: 4002, attempt: 2, conclusion: "failure",
     createdAt: "2026-10-19T08:00:00Z",
     url: "https://api.example.test/repos/o/r/actions/runs/4002",
     displayTitle: "e2e 4002", headSha: "2222222222222222"},
    {databaseId: 4003, attempt: 2, conclusion: "success",
     createdAt: "2026-10-20T08:00:00Z",
     url: "https://api.example.test/repos/o/r/actions/runs/4003",
     displayTitle: "e2e 4003", headSha: "3333333333333333"},
    {databaseId: 4004, attempt: 1, conclusion: "success",
     createdAt: "2026-10-21T08:00:00Z",
     url: "https://api.example.test/repos/o/r/actions/runs/4004",
     displayTitle: "e2e 4004", headSha: "4444444444444444"} ]' > "${E2E}/candidates.json"

# jobs_fixture RUN ATTEMPT BEHAVIOUR_JOB_ID BEHAVIOUR_CONCLUSION — one
# attempt's jobs listing: e2e.yml's three jobs, with the behaviour job named
# as scan_run selects it.
jobs_fixture() {
  mkdir -p "${E2E}/runs/$1"
  jq -nc --argjson job "$3" --arg conclusion "$4" '
    {total_count: 3, jobs: [
      {id: ($job - 1), name: "lint", conclusion: "success"},
      {id: $job, name: "behaviour", conclusion: $conclusion},
      {id: ($job + 1), name: "teardown", conclusion: "success"}]}' \
    > "${E2E}/runs/$1/jobs-$2.json"
}

# attempt_conclusion RUN ATTEMPT CONCLUSION — the attempt metadata main
# fetches for every attempt but the latest.
attempt_conclusion() {
  jq -nc --arg conclusion "$3" '{conclusion: $conclusion}' \
    > "${E2E}/runs/$1/attempt-$2.json"
}

# job_log JOB_ID LOG — the behaviour job's log fixture.
job_log() {
  mkdir -p "${E2E}/jobs/$1"
  cp "$2" "${E2E}/jobs/$1/log.txt"
}

jobs_fixture 4004 1 9101 success
jobs_fixture 4003 1 9201 failure
jobs_fixture 4003 2 9204 success
jobs_fixture 4002 1 9301 failure
jobs_fixture 4002 2 9304 failure
attempt_conclusion 4003 1 failure
attempt_conclusion 4002 1 failure
job_log 9101 "${LOGS}/clean.txt"
job_log 9201 "${LOGS}/rate-limited.txt"
job_log 9204 "${LOGS}/clean.txt"
job_log 9301 "${LOGS}/rate-limited.txt"
job_log 9304 "${LOGS}/near-miss.txt"

E2E_REPORT="${TMP}/e2e-report.json"
E2E_STDERR="${TMP}/e2e-stderr.txt"
RC=0
bash "${SCANNER}" --runs 3 --repo o/r > "${E2E_REPORT}" 2> "${E2E_STDERR}" || RC=$?

if [[ "${RC}" -eq 0 ]]; then
  pass "end to end: the scan completes"
else
  fail "end to end: the scan exited ${RC}"
  sed 's/^/         /' "${E2E_STDERR}" >&2
fi
expect_jq "end to end: only the three most recent runs are scanned" \
  '.runs | map(.run_id | tostring) | join(",")' "${E2E_REPORT}" "4004,4003,4002"
expect_jq "end to end: the report states the window and the count" \
  '[.window.runs, .runs_scanned] | map(tostring) | join(",")' "${E2E_REPORT}" "3,3"
expect_jq "end to end: the verdict at both levels" \
  '[.verdict.runs_with_markers, .verdict.runs_with_markers_that_passed]
     | map(tostring) | join(",")' \
  "${E2E_REPORT}" "2,1"
expect_jq "end to end: the clean run" \
  '.runs[] | select(.run_id == 4004)
     | [.has_markers, .ultimately_passed] | map(tostring) | join(",")' \
  "${E2E_REPORT}" "false,true"
expect_jq "end to end: the retry-masked occurrence names its attempt" \
  '.runs[] | select(.run_id == 4003)
     | [.has_markers, .ultimately_passed, .attempts[0].markers[0].marker,
        .attempts[0].conclusion, .attempts[1].conclusion]
     | map(tostring) | join(",")' \
  "${E2E_REPORT}" "true,true,run-listing-failed,failure,success"
expect_jq "end to end: the failing run with markers" \
  '.runs[] | select(.run_id == 4002)
     | [.conclusion, .has_markers, (.attempts[1].markers | length)]
     | map(tostring) | join(",")' \
  "${E2E_REPORT}" "failure,true,0"

expect_absent "window bounding: the run outside the window is never queried" \
  '/runs/4001/' "${E2E}/gh.log"
LOG_FETCHES=$(grep -cE 'actions/jobs/[0-9]+/logs' "${E2E}/gh.log" || true)
expect_eq "window bounding: exactly the kept runs' logs are fetched" \
  "${LOG_FETCHES}" "5"

expect_line "end to end: stdout carries only the report; the table goes to stderr" \
  'rate-limit evidence: e2e.yml behaviour runs on o/r \(main\)' "${E2E_STDERR}"
expect_line "end to end: the table states the window and the scan count" \
  'window: 3 most recent completed runs whose behaviour job ran; 3 scanned' "${E2E_STDERR}"
expect_line "end to end: progress names each kept run" \
  'scanned run 4004: success, 1 attempt\(s\), 0 marker\(s\) \[1/3\]' "${E2E_STDERR}"
expect_line "end to end: the masked run's progress line" \
  'scanned run 4003: success, 2 attempt\(s\), 6 marker\(s\) \[2/3\]' "${E2E_STDERR}"
expect_line "end to end: the verdict table goes to stderr" \
  'verdict: 2 run\(s\) with markers \(strict signal: any occurrence\)' "${E2E_STDERR}"

echo
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed" >&2
  exit 1
fi
echo "all tests passed"
