#!/usr/bin/env bash
# pre-fix-test.sh — Test pre-fix.sh input validation, iteration cap, and
# the FIX_SKIP_TOOL_INSTALL flag (issue #4718).
#
# Run from the repo root: bash internal/scaffold/fullsend-repo/scripts/pre-fix-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PRE_SCRIPT="${SCRIPT_DIR}/pre-fix.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# run_test invokes pre-fix.sh with a base set of valid env vars, overridden/
# extended by extra_env, and checks the exit code plus an optional stdout
# substring.
run_test() {
  local test_name="$1"
  local expect_exit="$2"
  local expected_stdout="${3:-}"
  local extra_env="${4:-}"

  local env_cmd=(
    env
    PR_NUMBER="42"
    REPO_FULL_NAME="test-org/test-repo"
    TRIGGER_SOURCE="human-dev"
  )

  if [[ -n "${extra_env}" ]]; then
    while IFS= read -r kv; do
      [[ -n "${kv}" ]] && env_cmd+=("${kv}")
    done <<< "${extra_env}"
  fi

  local exit_code=0
  "${env_cmd[@]}" bash "${PRE_SCRIPT}" > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne ${expect_exit} ]]; then
    echo "FAIL: ${test_name} — expected exit ${expect_exit}, got ${exit_code}"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ -n "${expected_stdout}" ]] && ! grep -qF "${expected_stdout}" "${TMPDIR}/stdout.log" 2>/dev/null; then
    echo "FAIL: ${test_name} — expected stdout '${expected_stdout}' not found"
    echo "Actual stdout:"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

run_test_excludes() {
  local test_name="$1"
  local expect_exit="$2"
  local excluded_stdout="$3"
  local extra_env="${4:-}"

  local env_cmd=(
    env
    PR_NUMBER="42"
    REPO_FULL_NAME="test-org/test-repo"
    TRIGGER_SOURCE="human-dev"
  )

  if [[ -n "${extra_env}" ]]; then
    while IFS= read -r kv; do
      [[ -n "${kv}" ]] && env_cmd+=("${kv}")
    done <<< "${extra_env}"
  fi

  local exit_code=0
  "${env_cmd[@]}" bash "${PRE_SCRIPT}" > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne ${expect_exit} ]]; then
    echo "FAIL: ${test_name} — expected exit ${expect_exit}, got ${exit_code}"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if grep -qF "${excluded_stdout}" "${TMPDIR}/stdout.log" 2>/dev/null; then
    echo "FAIL: ${test_name} — excluded stdout '${excluded_stdout}' was found"
    echo "Actual stdout:"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Input validation ---

run_test "valid-input-passes" 0 "Input validation passed"
run_test "invalid-pr-number-fails" 1 "" "PR_NUMBER=abc"
run_test "invalid-repo-full-name-fails" 1 "" "REPO_FULL_NAME=not-a-repo"
run_test "missing-trigger-source-fails" 1 "" "TRIGGER_SOURCE="

# --- Instruction length cap ---

run_test "instruction-within-cap-passes" 0 "Input validation passed" \
  "HUMAN_INSTRUCTION=short instruction"

LONG_INSTRUCTION="$(printf 'a%.0s' {1..10001})"
run_test "instruction-over-cap-fails" 1 "" \
  "HUMAN_INSTRUCTION=${LONG_INSTRUCTION}"

# --- Iteration cap ---

run_test "human-iteration-within-cap-passes" 0 "Input validation passed" \
  "FIX_ITERATION=10"
run_test "human-iteration-over-cap-fails" 1 "" \
  "FIX_ITERATION=11"

run_test "bot-iteration-within-cap-passes" 0 "Input validation passed" \
  "TRIGGER_SOURCE=fullsend-ai-coder[bot]
FIX_ITERATION=5"
run_test "bot-iteration-over-cap-fails" 1 "" \
  "TRIGGER_SOURCE=fullsend-ai-coder[bot]
FIX_ITERATION=6"

# --- FIX_SKIP_TOOL_INSTALL (issue #4718) ---
#
# reusable-fix.yml's inline "Validate inputs" step sets this so it can fail
# fast on bad input/iteration cap without also running the tool auto-install
# — that only needs to happen once, in the harness pre_script invocation
# inside "Run fix agent".

run_test "skip-tool-install-flag-defers" 0 \
  "Tool auto-install deferred to the harness pre_script invocation — skipping here" \
  "FIX_SKIP_TOOL_INSTALL=true"

run_test_excludes "skip-tool-install-flag-unset-does-not-defer" 0 \
  "deferred to the harness pre_script invocation"

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
