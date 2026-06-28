#!/usr/bin/env bash
# post-prioritize-test.sh — Test post-prioritize.sh with fixture JSON and mock gh/fullsend.
#
# Run from the repo root: bash internal/scaffold/fullsend-repo/scripts/post-prioritize-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POST_SCRIPT="${SCRIPT_DIR}/post-prioritize.sh"
FAILURES=0

TEST_TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TEST_TMPDIR}"' EXIT

GH_LOG="${TEST_TMPDIR}/gh-calls.log"
GH_FAIL_COUNT="${TEST_TMPDIR}/gh-fail-count"
MOCK_BIN="${TEST_TMPDIR}/bin"
mkdir -p "${MOCK_BIN}"

MOCK_PROJECT_ID="PVT_test_project"
MOCK_ITEM_ID="PVTI_test_item"
MOCK_ISSUE_NODE_ID="I_test_issue_node"

cat > "${MOCK_BIN}/gh" <<MOCKEOF
#!/usr/bin/env bash
GH_LOG_FILE="${GH_LOG}"
GH_FAIL_COUNT_FILE="${GH_FAIL_COUNT}"

echo "gh \$*" >> "\${GH_LOG_FILE}"

if [[ "\$1" == "api" && "\$2" == "rate_limit" ]]; then
  now=\$(date +%s)
  reset=\$(( now + 3600 ))
  printf '{"resources":{"core":{"limit":5000,"remaining":4999,"reset":%s},"graphql":{"limit":5000,"remaining":4999,"reset":%s}}}\n' "\${reset}" "\${reset}"
  exit 0
fi

fail_count=0
if [[ -f "\${GH_FAIL_COUNT_FILE}" ]]; then
  fail_count=\$(cat "\${GH_FAIL_COUNT_FILE}")
fi
fail_count=\$(( fail_count + 1 ))
echo "\${fail_count}" > "\${GH_FAIL_COUNT_FILE}"

if [[ "\${GH_CSMA_FAIL_MODE:-}" == "auth" ]] && [[ "\$1" != "api" || "\$2" != "rate_limit" ]]; then
  echo "ERROR: Resource not accessible by integration" >&2
  exit 1
fi

# Simulate gh exiting 0 but printing a rate limit error to stdout.
# This is how "gh project view" behaves on GraphQL rate limits.
if [[ "\${GH_CSMA_FAIL_MODE:-}" == "exit0-ratelimit" ]] && [[ "\$1" != "api" || "\$2" != "rate_limit" ]]; then
  if (( fail_count <= GH_CSMA_FAIL_UNTIL )); then
    echo "GraphQL: API rate limit exceeded for installation ID 131739396." >&2
    exit 0
  fi
fi

if [[ -n "\${GH_CSMA_FAIL_UNTIL:-}" ]] && (( fail_count <= GH_CSMA_FAIL_UNTIL )); then
  echo "You have exceeded a secondary rate limit. Please retry again later." >&2
  exit 1
fi

case "\$*" in
  *"project view"*)
    printf '{"id":"%s"}\n' "${MOCK_PROJECT_ID}"
    exit 0
    ;;
  *"repos/"*"/issues/"*)
    if [[ "\$*" == *"--jq"* ]]; then
      printf '%s\n' "${MOCK_ISSUE_NODE_ID}"
    else
      printf '{"node_id":"%s"}\n' "${MOCK_ISSUE_NODE_ID}"
    fi
    exit 0
    ;;
  *"project field-list"*)
    cat <<'FIELDS'
{"fields":[
  {"id":"PVTF_reach","name":"RICE Reach"},
  {"id":"PVTF_impact","name":"RICE Impact"},
  {"id":"PVTF_confidence","name":"RICE Confidence"},
  {"id":"PVTF_effort","name":"RICE Effort"},
  {"id":"PVTF_score","name":"RICE Score"}
]}
FIELDS
    exit 0
    ;;
esac

if [[ "\$1" == "api" && "\$2" == "graphql" ]]; then
  if [[ "\$*" == *"--input"* ]]; then
    input=\$(cat)
    if echo "\${input}" | grep -q 'updateProjectV2ItemFieldValue'; then
      echo '{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"PVTI_test_item"}}}}'
      exit 0
    fi
  fi
  printf '{"data":{"node":{"projectItems":{"nodes":[{"id":"%s","project":{"id":"%s"}}]}}}}\n' \
    "${MOCK_ITEM_ID}" "${MOCK_PROJECT_ID}"
  exit 0
fi

echo "unexpected gh invocation: \$*" >&2
exit 99
MOCKEOF
chmod +x "${MOCK_BIN}/gh"

cat > "${MOCK_BIN}/fullsend" <<MOCKEOF
#!/usr/bin/env bash
BODY=""
PREV=""
for arg in "\$@"; do
  if [[ "\${arg}" == "-" ]] && [[ "\${PREV}" == "--result" ]]; then
    BODY=\$(cat)
  fi
  PREV="\${arg}"
done
if [[ -n "\${BODY}" ]]; then
  echo "fullsend \$* <<BODY:\${BODY}:BODY>>" >> "${GH_LOG}"
else
  echo "fullsend \$*" >> "${GH_LOG}"
fi
MOCKEOF
chmod +x "${MOCK_BIN}/fullsend"

export PATH="${MOCK_BIN}:${PATH}"
export GH_LOG="${GH_LOG}"
export GH_FAIL_COUNT="${GH_FAIL_COUNT}"
export MOCK_PROJECT_ID="${MOCK_PROJECT_ID}"
export MOCK_ITEM_ID="${MOCK_ITEM_ID}"
export MOCK_ISSUE_NODE_ID="${MOCK_ISSUE_NODE_ID}"
export GITHUB_ISSUE_URL="https://github.com/test-org/test-repo/issues/42"
export GH_TOKEN="fake-token"
export ORG="test-org"
export PROJECT_NUMBER="1"
export GITHUB_CSMA_SLOT_MAX_MS=0
export GITHUB_CSMA_BACKOFF_CAP_SEC=1
export GITHUB_CSMA_SPREAD_MAX_SEC=0

FIXTURE_JSON='{
  "reach": 3,
  "impact": 2,
  "confidence": 0.8,
  "effort": 2,
  "reasoning": {
    "reach": "Many users affected.",
    "impact": "Moderate workflow improvement.",
    "confidence": "Some customer signal.",
    "effort": "Small scoped change."
  }
}'

run_test() {
  local test_name="$1"
  local fail_until="${2:-}"
  local min_gh_calls="${3:-1}"
  local expect_failure="${4:-false}"
  local fail_mode="${5:-}"

  local run_dir="${TEST_TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${FIXTURE_JSON}" > "${run_dir}/iteration-1/output/agent-result.json"

  : > "${GH_LOG}"
  rm -f "${GH_FAIL_COUNT}"
  unset GH_CSMA_FAIL_UNTIL GH_CSMA_FAIL_MODE
  if [[ -n "${fail_until}" ]]; then
    export GH_CSMA_FAIL_UNTIL="${fail_until}"
  fi
  if [[ -n "${fail_mode}" ]]; then
    export GH_CSMA_FAIL_MODE="${fail_mode}"
  fi

  local exit_code=0
  (cd "${run_dir}" && bash "${POST_SCRIPT}") > "${TEST_TMPDIR}/stdout-${test_name}.log" 2>&1 || exit_code=$?

  if [[ "${expect_failure}" == "true" ]]; then
    if [[ ${exit_code} -eq 0 ]]; then
      echo "FAIL: ${test_name} — expected failure but got success"
      FAILURES=$((FAILURES + 1))
      return
    fi
    echo "PASS: ${test_name} (expected failure)"
    return
  fi

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TEST_TMPDIR}/stdout-${test_name}.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  local gh_calls
  gh_calls=$(wc -l < "${GH_LOG}")
  if (( gh_calls < min_gh_calls )); then
    echo "FAIL: ${test_name} — expected at least ${min_gh_calls} gh calls, got ${gh_calls}"
    cat "${GH_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if ! grep -qF 'project view' "${GH_LOG}"; then
    echo "FAIL: ${test_name} — missing project view call"
    cat "${GH_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if ! grep -qF 'fullsend post-comment' "${GH_LOG}"; then
    echo "FAIL: ${test_name} — missing fullsend post-comment call"
    cat "${GH_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if ! grep -qF 'fullsend:prioritize-agent' "${GH_LOG}"; then
    echo "FAIL: ${test_name} — comment marker not posted"
    cat "${GH_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

run_test_failure_stderr() {
  local test_name="$1"
  local fail_until="$2"
  local expected_stderr="$3"
  local fail_mode="${4:-}"

  local run_dir="${TEST_TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${FIXTURE_JSON}" > "${run_dir}/iteration-1/output/agent-result.json"

  : > "${GH_LOG}"
  rm -f "${GH_FAIL_COUNT}"
  unset GH_CSMA_FAIL_UNTIL GH_CSMA_FAIL_MODE
  export GITHUB_CSMA_MAX_ATTEMPTS="${GITHUB_CSMA_MAX_ATTEMPTS:-8}"
  if [[ -n "${fail_until}" ]]; then
    export GH_CSMA_FAIL_UNTIL="${fail_until}"
  fi
  if [[ -n "${fail_mode}" ]]; then
    export GH_CSMA_FAIL_MODE="${fail_mode}"
  fi

  local exit_code=0
  (cd "${run_dir}" && bash "${POST_SCRIPT}") > "${TEST_TMPDIR}/stdout-${test_name}.log" 2> "${TEST_TMPDIR}/stderr-${test_name}.log" || exit_code=$?

  if [[ ${exit_code} -eq 0 ]]; then
    echo "FAIL: ${test_name} — expected failure but got success"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if ! grep -qF "${expected_stderr}" "${TEST_TMPDIR}/stderr-${test_name}.log"; then
    echo "FAIL: ${test_name} — expected stderr containing '${expected_stderr}'"
    cat "${TEST_TMPDIR}/stderr-${test_name}.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# Unit tests for shared CSMA helpers.
# shellcheck source=lib/github-api-csma.sh
source "${SCRIPT_DIR}/lib/github-api-csma.sh"
if github_csma_is_rate_limit "HTTP 429: Too Many Requests"; then
  :
else
  echo "FAIL: github_csma_is_rate_limit — expected HTTP 429 match"
  FAILURES=$((FAILURES + 1))
fi
if github_csma_is_rate_limit "You have exceeded a secondary rate limit"; then
  :
else
  echo "FAIL: github_csma_is_rate_limit — expected secondary limit match"
  FAILURES=$((FAILURES + 1))
fi
if ! github_csma_is_rate_limit '{"totalCount":429}'; then
  :
else
  echo "FAIL: github_csma_is_rate_limit — bare 429 must not match"
  FAILURES=$((FAILURES + 1))
fi
delay=$(github_csma_backoff 0)
if (( delay >= 1 && delay <= 120 )); then
  echo "PASS: github_csma_backoff"
else
  echo "FAIL: github_csma_backoff — delay out of range: ${delay}"
  FAILURES=$((FAILURES + 1))
fi

# Happy path: no injected failures.
run_test "happy-path" "" 8

# Retry path: first two non-rate-limit gh calls fail with secondary limit, then succeed.
run_test "rate-limit-retry" "2" 10

# Non-retryable errors must surface to stderr without retry loops.
run_test_failure_stderr "auth-error" "" "Resource not accessible by integration" "auth"

# Exhausted retries on persistent rate limits.
export GITHUB_CSMA_MAX_ATTEMPTS=3
run_test_failure_stderr "exhausted-retries" "100" "secondary rate limit"
unset GITHUB_CSMA_MAX_ATTEMPTS

# gh exits 0 but output contains rate limit error (gh project view behavior).
# First 2 calls fail with exit-0 rate limit, then succeed.
run_test "exit0-rate-limit-retry" "2" 10 "false" "exit0-ratelimit"

# Exhausted retries when gh keeps exiting 0 with rate limit errors.
export GITHUB_CSMA_MAX_ATTEMPTS=3
run_test_failure_stderr "exit0-rate-limit-exhausted" "100" "rate limit exceeded" "exit0-ratelimit"
unset GITHUB_CSMA_MAX_ATTEMPTS

if [[ ${FAILURES} -gt 0 ]]; then
  echo ""
  echo "${FAILURES} test(s) failed."
  exit 1
fi

echo ""
echo "All post-prioritize tests passed."
