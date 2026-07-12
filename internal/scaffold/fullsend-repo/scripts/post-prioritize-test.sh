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
# --- Inlined from lib/github-api-csma.sh ---
# CSMA/CD-style resilience for GitHub API calls via gh/fullsend.
# Carrier sense: check rate_limit before transmitting.
# Slot time: random jitter between calls to desynchronize parallel runners.
# Collision detection: retry on 429 / secondary rate limit errors with exponential backoff.
#
# Environment (all optional):
#   GITHUB_CSMA_MAX_ATTEMPTS          — default 8
#   GITHUB_CSMA_MIN_REMAINING_CORE    — default 100
#   GITHUB_CSMA_MIN_REMAINING_GRAPHQL — default 100
#   GITHUB_CSMA_SLOT_MIN_MS           — default 250
#   GITHUB_CSMA_SLOT_MAX_MS           — default 750 (0 disables jitter)
#   GITHUB_CSMA_SPREAD_MAX_SEC        — default 60 (post-reset desync spread)
#   GITHUB_CSMA_BACKOFF_CAP_SEC       — default 120

# shellcheck shell=bash

_github_csma_max_attempts() {
  echo "${GITHUB_CSMA_MAX_ATTEMPTS:-8}"
}

_github_csma_min_remaining() {
  local resource="$1"
  case "${resource}" in
    graphql) echo "${GITHUB_CSMA_MIN_REMAINING_GRAPHQL:-100}" ;;
    *) echo "${GITHUB_CSMA_MIN_REMAINING_CORE:-100}" ;;
  esac
}

_github_csma_slot_min_ms() {
  echo "${GITHUB_CSMA_SLOT_MIN_MS:-250}"
}

_github_csma_slot_max_ms() {
  echo "${GITHUB_CSMA_SLOT_MAX_MS:-750}"
}

_github_csma_spread_max_sec() {
  echo "${GITHUB_CSMA_SPREAD_MAX_SEC:-60}"
}

_github_csma_backoff_cap_sec() {
  echo "${GITHUB_CSMA_BACKOFF_CAP_SEC:-120}"
}

# Add a random spread delay after a rate-limit sleep to desynchronize runners.
# Called from both github_csma_sense and _github_csma_sleep_after_rate_limit.
_github_csma_post_reset_spread() {
  local spread_max
  spread_max=$(_github_csma_spread_max_sec)
  if (( spread_max > 0 )); then
    local spread_secs=$(( RANDOM % spread_max ))
    echo "Rate limit reset — spreading ${spread_secs}s to desync from other runners..." >&2
    sleep "${spread_secs}"
  fi
}

_github_csma_emit_failure() {
  printf '%s\n' "$1" >&2
}

# Wait until the named rate_limit resource has enough quota (carrier sense).
# Usage: github_csma_sense [core|graphql] [min_remaining]
github_csma_sense() {
  local resource="${1:-core}"
  local min_remaining="${2:-$(_github_csma_min_remaining "${resource}")}"

  local info remaining reset now wait_secs
  if ! info=$(gh api rate_limit 2>/dev/null); then
    echo "WARNING: github_csma_sense: could not read rate_limit; proceeding" >&2
    return 0
  fi

  remaining=$(echo "${info}" | jq -r --arg r "${resource}" '.resources[$r].remaining // empty')
  reset=$(echo "${info}" | jq -r --arg r "${resource}" '.resources[$r].reset // empty')

  if [[ -z "${remaining}" || "${remaining}" == "null" ]]; then
    echo "WARNING: github_csma_sense: no .resources.${resource} in rate_limit; proceeding" >&2
    return 0
  fi

  if (( remaining >= min_remaining )); then
    return 0
  fi

  now=$(date +%s)
  wait_secs=$(( reset - now + 1 ))
  if (( wait_secs < 1 )); then
    wait_secs=1
  fi
  cap=$(_github_csma_backoff_cap_sec)
  if (( wait_secs > cap )); then
    wait_secs="${cap}"
  fi

  echo "Rate limit sense: ${resource} remaining=${remaining} (min=${min_remaining}); waiting ${wait_secs}s until reset..." >&2
  sleep "${wait_secs}"

  # After a rate-limit sleep, all runners wake at the same reset timestamp.
  # Spread them over a wide window to avoid a thundering herd.
  _github_csma_post_reset_spread
}

# Random inter-call delay (slot time) to reduce synchronized collisions.
github_csma_slot() {
  local max_ms min_ms span_ms delay_ms
  max_ms=$(_github_csma_slot_max_ms)
  if (( max_ms <= 0 )); then
    return 0
  fi
  min_ms=$(_github_csma_slot_min_ms)
  if (( min_ms > max_ms )); then
    min_ms="${max_ms}"
  fi
  span_ms=$(( max_ms - min_ms + 1 ))
  delay_ms=$(( min_ms + RANDOM % span_ms ))
  sleep "$(awk -v ms="${delay_ms}" 'BEGIN { printf "%.3f", ms / 1000 }')"
}

# Return 0 if combined output looks like a retryable GitHub rate limit error.
github_csma_is_rate_limit() {
  local text="$1"
  local lower
  lower=$(echo "${text}" | tr '[:upper:]' '[:lower:]')

  if echo "${lower}" | grep -qE 'http 429|status: 429'; then
    return 0
  fi
  if echo "${lower}" | grep -qE 'secondary rate limit|rate limit exceeded|api rate limit'; then
    return 0
  fi
  if echo "${lower}" | grep -qE 'http 403|status: 403'; then
    if echo "${lower}" | grep -qE 'secondary|rate limit|abuse|retry.after'; then
      return 0
    fi
  fi
  return 1
}

# Compute backoff seconds for attempt (0-based). Writes to stdout.
github_csma_backoff() {
  local attempt="$1"
  local cap base delay
  cap=$(_github_csma_backoff_cap_sec)
  base=$(( 1 << attempt ))
  if (( base > cap )); then
    base="${cap}"
  fi
  delay=$(( RANDOM % (base + 1) ))
  if (( delay < 1 )); then
    delay=1
  fi
  echo "${delay}"
}

_github_csma_sleep_after_rate_limit() {
  local attempt="$1"
  local resource="${2:-core}"
  local delay wait_secs now reset info cap

  delay=$(github_csma_backoff "${attempt}")
  if info=$(gh api rate_limit 2>/dev/null); then
    now=$(date +%s)
    reset=$(echo "${info}" | jq -r --arg r "${resource}" '.resources[$r].reset // empty')
    if [[ -n "${reset}" && "${reset}" != "null" ]]; then
      wait_secs=$(( reset - now + 1 ))
      cap=$(_github_csma_backoff_cap_sec)
      if (( wait_secs > cap )); then
        wait_secs="${cap}"
      fi
      if (( wait_secs > delay && wait_secs > 0 )); then
        delay="${wait_secs}"
      fi
    fi
  fi
  echo "GitHub API rate limit (attempt $(( attempt + 1 ))); backing off ${delay}s..." >&2
  sleep "${delay}"

  # After backing off, spread runners to avoid thundering herd on wake.
  _github_csma_post_reset_spread
}

# Run gh with CSMA/CD. First argument: rate_limit resource (core|graphql).
# Remaining arguments are passed to gh. Prints gh stdout on success.
github_csma_run() {
  local resource="${1:-core}"
  shift

  local max_attempts attempt outfile errfile combined
  max_attempts=$(_github_csma_max_attempts)
  outfile=$(mktemp)
  errfile=$(mktemp)
  # shellcheck disable=SC2064
  trap "rm -f '${outfile}' '${errfile}'" RETURN

  for (( attempt = 0; attempt < max_attempts; attempt++ )); do
    github_csma_sense "${resource}"
    github_csma_slot

    : >"${outfile}"
    : >"${errfile}"
    local rc=0
    gh "$@" >"${outfile}" 2>"${errfile}" || rc=$?

    combined=$(cat "${outfile}" "${errfile}")
    if github_csma_is_rate_limit "${combined}"; then
      if (( attempt < max_attempts - 1 )); then
        _github_csma_sleep_after_rate_limit "${attempt}" "${resource}"
        continue
      fi
      _github_csma_emit_failure "${combined}"
      return 1
    fi

    if (( rc != 0 )); then
      _github_csma_emit_failure "${combined}"
      return 1
    fi
    cat "${outfile}"
    return 0
  done

  return 1
}

# Run producer | gh with CSMA/CD. First argument: resource; rest are gh args.
# Reads producer output from stdin (save once for retries).
github_csma_run_pipe() {
  local resource="${1:-graphql}"
  shift

  local max_attempts attempt infile outfile errfile combined
  max_attempts=$(_github_csma_max_attempts)
  infile=$(mktemp)
  outfile=$(mktemp)
  errfile=$(mktemp)
  cat >"${infile}"
  # shellcheck disable=SC2064
  trap "rm -f '${infile}' '${outfile}' '${errfile}'" RETURN

  for (( attempt = 0; attempt < max_attempts; attempt++ )); do
    github_csma_sense "${resource}"
    github_csma_slot

    : >"${outfile}"
    : >"${errfile}"
    local rc=0
    gh "$@" <"${infile}" >"${outfile}" 2>"${errfile}" || rc=$?

    combined=$(cat "${outfile}" "${errfile}")
    if github_csma_is_rate_limit "${combined}"; then
      if (( attempt < max_attempts - 1 )); then
        _github_csma_sleep_after_rate_limit "${attempt}" "${resource}"
        continue
      fi
      _github_csma_emit_failure "${combined}"
      return 1
    fi

    if (( rc != 0 )); then
      _github_csma_emit_failure "${combined}"
      return 1
    fi
    cat "${outfile}"
    return 0
  done

  return 1
}

# Run an arbitrary command with stdin from caller; retries on rate-limit errors in output.
# First argument: rate_limit resource (core|graphql); remaining args are the command.
github_csma_run_cmd() {
  local resource="${1:-core}"
  shift

  local max_attempts attempt infile outfile errfile combined
  max_attempts=$(_github_csma_max_attempts)
  infile=$(mktemp)
  outfile=$(mktemp)
  errfile=$(mktemp)
  cat >"${infile}"
  # shellcheck disable=SC2064
  trap "rm -f '${infile}' '${outfile}' '${errfile}'" RETURN

  for (( attempt = 0; attempt < max_attempts; attempt++ )); do
    github_csma_sense "${resource}"
    github_csma_slot

    : >"${outfile}"
    : >"${errfile}"
    local rc=0
    "$@" <"${infile}" >"${outfile}" 2>"${errfile}" || rc=$?

    combined=$(cat "${outfile}" "${errfile}")
    if github_csma_is_rate_limit "${combined}"; then
      if (( attempt < max_attempts - 1 )); then
        _github_csma_sleep_after_rate_limit "${attempt}" "${resource}"
        continue
      fi
      _github_csma_emit_failure "${combined}"
      return 1
    fi

    if (( rc != 0 )); then
      _github_csma_emit_failure "${combined}"
      return 1
    fi
    cat "${outfile}"
    return 0
  done

  return 1
}
# --- End inlined CSMA ---
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
