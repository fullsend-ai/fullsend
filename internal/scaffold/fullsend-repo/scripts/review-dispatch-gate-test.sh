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
  local current_head="$1" runs_json="$2" jobs_json="$3"
  local bin="${TMPDIR}/bin"
  rm -rf "${bin}"
  mkdir -p "${bin}"
  printf '%s' "${current_head}" > "${TMPDIR}/current-head"
  printf '%s' "${runs_json}" > "${TMPDIR}/runs.json"
  printf '%s' "${jobs_json}" > "${TMPDIR}/jobs.json"
  cat > "${bin}/gh" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
endpoint=""
for arg in "$@"; do
  if [[ "${arg}" == repos/* ]]; then
    endpoint="${arg}"
  fi
done
case "${endpoint}" in
  repos/test-org/test-repo/pulls/42) cat "${MOCK_ROOT}/current-head" ;;
  *'/actions/runs?status=in_progress&per_page=100') cat "${MOCK_ROOT}/runs.json" ;;
  *'/actions/runs/'*'/jobs?per_page=100') jq -r '.jobs[].name' "${MOCK_ROOT}/jobs.json" ;;
  *) echo "unexpected gh invocation: $*" >&2; exit 1 ;;
esac
MOCK
  chmod +x "${bin}/gh"
  printf '%s' "${bin}"
}

run_case() {
  local name="$1" current_head="$2" runs_json="$3" jobs_json="$4" want_skip="$5"
  local bin output actual
  bin="$(build_mock "${current_head}" "${runs_json}" "${jobs_json}")"
  output="${TMPDIR}/output"
  : > "${output}"
  if ! env PATH="${bin}:${PATH}" MOCK_ROOT="${TMPDIR}" GITHUB_OUTPUT="${output}" \
    SOURCE_REPO="test-org/test-repo" PR_NUMBER=42 EXPECTED_HEAD_SHA="${EXPECTED_SHA}" CURRENT_RUN_ID=222 \
    bash "${SCRIPT}" >"${TMPDIR}/stdout" 2>"${TMPDIR}/stderr"; then
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
  echo "PASS: ${name}"
}

run_case "same-head-active-review-is-skipped" \
  "${EXPECTED_SHA}" \
  '{"workflow_runs":[{"id":111}]}' \
  "{\"jobs\":[{\"name\":\"Dispatch / Review dispatch gate #42 ${EXPECTED_SHA}\"}]}" \
  true
run_case "same-head-review-is-found-among-unrelated-jobs" \
  "${EXPECTED_SHA}" \
  '{"workflow_runs":[{"id":111}]}' \
  "{\"jobs\":[{\"name\":\"Dispatch / Lint\"},{\"name\":\"Dispatch / Review dispatch gate #42 ${EXPECTED_SHA}\"},{\"name\":\"Dispatch / Test\"}]}" \
  true
run_case "older-head-active-review-is-not-skipped" \
  "${EXPECTED_SHA}" \
  '{"workflow_runs":[{"id":111}]}' \
  "{\"jobs\":[{\"name\":\"Dispatch / Review dispatch gate #42 ${OLDER_SHA}\"}]}" \
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

if (( FAILURES > 0 )); then
  exit 1
fi
