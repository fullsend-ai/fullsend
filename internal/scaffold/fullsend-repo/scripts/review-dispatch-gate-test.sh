#!/usr/bin/env bash
# review-dispatch-gate-test.sh - Regression tests for review-dispatch-gate.sh.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/review-dispatch-gate.sh"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT
FAILURES=0
EXPECTED_SHA="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
OLDER_SHA="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

build_mock() {
  local current_head="$1" runs_json="$2" jobs_json="$3" jobs_exit_code="${4:-0}" pending_runs_json="${5:-}" queued_runs_json="${6:-}" run_failure_status="${7:-}"
  if [[ -z "${pending_runs_json}" ]]; then
    pending_runs_json='{"workflow_runs":[]}'
  fi
  if [[ -z "${queued_runs_json}" ]]; then
    queued_runs_json='{"workflow_runs":[]}'
  fi
  local bin="${TMPDIR}/bin"
  rm -rf "${bin}"
  mkdir -p "${bin}"
  printf '%s' "${current_head}" > "${TMPDIR}/current-head"
  printf '%s' "${runs_json}" > "${TMPDIR}/runs.json"
  printf '%s' "${pending_runs_json}" > "${TMPDIR}/pending-runs.json"
  printf '%s' "${queued_runs_json}" > "${TMPDIR}/queued-runs.json"
  printf '%s' "${run_failure_status}" > "${TMPDIR}/run-failure-status"
  printf '%s' "${jobs_json}" > "${TMPDIR}/jobs.json"
  printf '%s' "${jobs_exit_code}" > "${TMPDIR}/jobs-exit-code"
  cat > "${bin}/gh" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
endpoint=""
for arg in "$@"; do
  if [[ "${arg}" == repos/* ]]; then
    endpoint="${arg}"
  fi
done
run_status="$(<"${MOCK_ROOT}/run-failure-status")"
if [[ "${endpoint}" == *"/actions/runs?status=${run_status}&per_page=100" ]] && [[ -n "${run_status}" ]]; then
  echo "simulated ${run_status} workflow-runs API failure" >&2
  exit 1
fi
case "${endpoint}" in
  repos/test-org/test-repo/pulls/42)
    [[ " $* " == *" --jq .head.sha "* ]] || {
      echo "expected pulls API to select .head.sha" >&2
      exit 1
    }
    cat "${MOCK_ROOT}/current-head"
    ;;
  *'/actions/runs?status=in_progress&per_page=100') cat "${MOCK_ROOT}/runs.json" ;;
  *'/actions/runs?status=pending&per_page=100') cat "${MOCK_ROOT}/pending-runs.json" ;;
  *'/actions/runs?status=queued&per_page=100') cat "${MOCK_ROOT}/queued-runs.json" ;;
  *'/actions/runs/'*'/jobs?per_page=100')
    jobs_exit_code="$(<"${MOCK_ROOT}/jobs-exit-code")"
    if [[ "${jobs_exit_code}" != "0" ]]; then
      echo "simulated jobs API failure" >&2
      exit "${jobs_exit_code}"
    fi
    jq_filter=""
    for ((i = 1; i <= $#; i++)); do
      if [[ "${!i}" == "--jq" ]]; then
        next=$((i + 1))
        jq_filter="${!next}"
        break
      fi
    done
    [[ -n "${jq_filter}" ]] || {
      echo "expected jobs API to select name and status" >&2
      exit 1
    }
    jq -r "${jq_filter}" "${MOCK_ROOT}/jobs.json"
    ;;
  *) echo "unexpected gh invocation: $*" >&2; exit 1 ;;
esac
MOCK
  chmod +x "${bin}/gh"
  printf '%s' "${bin}"
}

run_case() {
  local name="$1" current_head="$2" runs_json="$3" jobs_json="$4" want_skip="$5" jobs_exit_code="${6:-0}" want_success="${7:-true}" want_duplicate_run_id="${8:-}" pending_runs_json="${9:-}" queued_runs_json="${10:-}" run_failure_status="${11:-}"
  local bin output actual exit_code=0
  bin="$(build_mock "${current_head}" "${runs_json}" "${jobs_json}" "${jobs_exit_code}" "${pending_runs_json}" "${queued_runs_json}" "${run_failure_status}")"
  output="${TMPDIR}/output"
  : > "${output}"
  env PATH="${bin}:${PATH}" MOCK_ROOT="${TMPDIR}" GITHUB_OUTPUT="${output}" \
    SOURCE_REPO="test-org/test-repo" PR_NUMBER=42 EXPECTED_HEAD_SHA="${EXPECTED_SHA}" CURRENT_RUN_ID=222 \
    bash "${SCRIPT}" >"${TMPDIR}/stdout" 2>"${TMPDIR}/stderr" || exit_code=$?
  if [[ "${want_success}" != "true" ]]; then
    if [[ "${exit_code}" == "0" ]]; then
      echo "FAIL: ${name} (script exited successfully)"
      FAILURES=$((FAILURES + 1))
      return
    fi
    if grep -Fqx 'skip=false' "${output}"; then
      echo "FAIL: ${name} (script allowed a review after a failed jobs API lookup)"
      FAILURES=$((FAILURES + 1))
      return
    fi
    echo "PASS: ${name}"
    return
  fi
  if [[ "${exit_code}" != "0" ]]; then
    echo "FAIL: ${name} (script exited non-zero)"
    cat "${TMPDIR}/stderr"
    FAILURES=$((FAILURES + 1))
    return
  fi
  actual="$(sed -n 's/^skip=//p' "${output}" | tail -1)"
  if [[ "${actual}" != "${want_skip}" ]]; then
    echo "FAIL: ${name} (skip=${actual}, want ${want_skip})"
    FAILURES=$((FAILURES + 1))
    return
  fi
  if [[ -n "${want_duplicate_run_id}" ]] && ! grep -Fqx "duplicate_run_id=${want_duplicate_run_id}" "${output}"; then
    echo "FAIL: ${name} (missing duplicate workflow run ID ${want_duplicate_run_id})"
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "PASS: ${name}"
}

run_case "same-head-running-review-is-skipped" \
  "${EXPECTED_SHA}" \
  '{"workflow_runs":[{"id":111}]}' \
  "{\"jobs\":[{\"name\":\"Dispatch / Review dispatch gate #42 ${EXPECTED_SHA}\",\"status\":\"completed\"},{\"name\":\"Dispatch / Review\",\"status\":\"in_progress\"}]}" \
  true 0 true 111
run_case "same-head-queued-review-is-skipped" \
  "${EXPECTED_SHA}" \
  '{"workflow_runs":[{"id":111}]}' \
  "{\"jobs\":[{\"name\":\"Dispatch / Lint\",\"status\":\"completed\"},{\"name\":\"Dispatch / Review dispatch gate #42 ${EXPECTED_SHA}\",\"status\":\"completed\"},{\"name\":\"Dispatch / Review\",\"status\":\"queued\"},{\"name\":\"Dispatch / Test\",\"status\":\"completed\"}]}" \
  true 0 true 111
run_case "same-head-pending-review-is-skipped" \
  "${EXPECTED_SHA}" \
  '{"workflow_runs":[]}' \
  "{\"jobs\":[{\"name\":\"Dispatch / Review dispatch gate #42 ${EXPECTED_SHA}\",\"status\":\"completed\"},{\"name\":\"Dispatch / Review\",\"status\":\"pending\"}] }" \
  true 0 true 111 \
  '{"workflow_runs":[{"id":111}]}'
run_case "same-head-queued-workflow-run-is-skipped" \
  "${EXPECTED_SHA}" \
  '{"workflow_runs":[]}' \
  "{\"jobs\":[{\"name\":\"Dispatch / Review dispatch gate #42 ${EXPECTED_SHA}\",\"status\":\"completed\"},{\"name\":\"Dispatch / Review\",\"status\":\"queued\"}]}" \
  true 0 true 111 \
  '' \
  '{"workflow_runs":[{"id":111}]}'
run_case "completed-review-is-not-skipped" \
  "${EXPECTED_SHA}" \
  '{"workflow_runs":[{"id":111}]}' \
  "{\"jobs\":[{\"name\":\"Dispatch / Review dispatch gate #42 ${EXPECTED_SHA}\",\"status\":\"completed\"},{\"name\":\"Dispatch / Review\",\"status\":\"completed\"}]}" \
  false
run_case "older-head-active-review-is-not-skipped" \
  "${EXPECTED_SHA}" \
  '{"workflow_runs":[{"id":111}]}' \
  "{\"jobs\":[{\"name\":\"Dispatch / Review dispatch gate #42 ${OLDER_SHA}\",\"status\":\"completed\"},{\"name\":\"Dispatch / Review\",\"status\":\"in_progress\"}]}" \
  false
run_case "stale-request-is-skipped" \
  "${OLDER_SHA}" \
  '{"workflow_runs":[]}' \
  '{"jobs":[]}' \
  true
run_case "no-active-review-is-allowed" \
  "${EXPECTED_SHA}" \
  '{"workflow_runs":[]}' \
  '{"jobs":[]}' \
  false
run_case "jobs-api-failure-stops-gate" \
  "${EXPECTED_SHA}" \
  '{"workflow_runs":[{"id":111}]}' \
  '{"jobs":[]}' \
  false \
  1 \
  false
run_case "run-list-api-failure-stops-gate" \
  "${EXPECTED_SHA}" \
  '{"workflow_runs":[]}' \
  '{"jobs":[]}' \
  false 0 false '' '' '' in_progress
run_case "malformed-current-head-stops-gate" \
  'not-a-full-sha' \
  '{"workflow_runs":[]}' \
  '{"jobs":[]}' \
  false \
  0 \
  false

if (( FAILURES > 0 )); then
  exit 1
fi
