#!/usr/bin/env bash
# post-fix-test.sh — Test the push retry logic from post-fix.sh.
#
# Extracts and tests the push-retry decision logic in isolation using shell
# functions. This avoids needing a full git repo or GitHub API access.
#
# Run from the repo root:
#   bash internal/scaffold/fullsend-repo/scripts/post-fix-test.sh

set -euo pipefail

FAILURES=0

# ---------------------------------------------------------------------------
# Test helper — reimplements the push retry logic from post-fix.sh section 5.
# Given a push exit code and output, returns the action.
# ---------------------------------------------------------------------------
decide_push_retry() {
  local push_rc="$1"
  local push_output="$2"

  if [ "${push_rc}" -eq 0 ]; then
    echo "success"
    return 0
  fi

  if echo "${push_output}" | grep -qi "non-fast-forward\|rejected\|fetch first"; then
    echo "retry:force-with-lease"
    return 0
  fi

  echo "fail:unexpected-error"
  return 0
}

run_push_retry_test() {
  local test_name="$1"
  local push_rc="$2"
  local push_output="$3"
  local expected_prefix="$4"

  local actual
  actual="$(decide_push_retry "${push_rc}" "${push_output}")"

  if [[ "${actual}" != ${expected_prefix}* ]]; then
    echo "FAIL: ${test_name}"
    echo "  push_rc:         '${push_rc}'"
    echo "  push_output:     '${push_output}'"
    echo "  expected prefix: '${expected_prefix}'"
    echo "  actual:          '${actual}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Push retry test cases ---

# Successful push → no retry needed
run_push_retry_test "push-success" \
  "0" "Everything up-to-date" "success"

# Non-fast-forward error → retry with --force-with-lease
run_push_retry_test "push-non-fast-forward" \
  "1" "error: failed to push some refs: non-fast-forward" "retry:force-with-lease"

# Rejected error → retry with --force-with-lease
run_push_retry_test "push-rejected" \
  "1" "! [rejected] agent/42 -> agent/42 (fetch first)" "retry:force-with-lease"

# Unknown error → fail
run_push_retry_test "push-unexpected-error" \
  "1" "fatal: repository not found" "fail:unexpected-error"

# ---------------------------------------------------------------------------
# Test helper — reimplements the pre-commit auto-fix retry decision logic
# from post-fix.sh section 3. Given a pre-commit exit code and whether
# unstaged changes exist, returns the action the script would take.
# ---------------------------------------------------------------------------
decide_precommit_retry() {
  local precommit_rc="$1"          # 0 = passed, 1 = failed
  local has_unstaged="$2"          # "yes" or "no"
  local retry_precommit_rc="$3"    # 0 = passed on retry, 1 = still fails (ignored if no retry)
  local retry_has_unstaged="${4:-no}"  # "yes" if retry left unstaged changes

  if [ "${precommit_rc}" -eq 0 ]; then
    echo "pass:clean"
    return 0
  fi

  # Pre-commit failed — check for auto-fixed files
  if [ "${has_unstaged}" = "yes" ]; then
    if [ "${retry_precommit_rc}" -eq 0 ]; then
      if [ "${retry_has_unstaged}" = "yes" ]; then
        echo "blocked:retry-left-unstaged"
      else
        echo "pass:auto-fixed"
      fi
    else
      echo "blocked:retry-failed"
    fi
  else
    echo "blocked:no-auto-fix"
  fi
}

run_precommit_retry_test() {
  local test_name="$1"
  local precommit_rc="$2"
  local has_unstaged="$3"
  local retry_precommit_rc="$4"
  local expected="$5"
  local retry_has_unstaged="${6:-no}"

  local actual
  actual="$(decide_precommit_retry "${precommit_rc}" "${has_unstaged}" "${retry_precommit_rc}" "${retry_has_unstaged}")"

  if [ "${actual}" != "${expected}" ]; then
    echo "FAIL: ${test_name}"
    echo "  precommit_rc:         '${precommit_rc}'"
    echo "  has_unstaged:         '${has_unstaged}'"
    echo "  retry_precommit_rc:   '${retry_precommit_rc}'"
    echo "  retry_has_unstaged:   '${retry_has_unstaged}'"
    echo "  expected:             '${expected}'"
    echo "  actual:               '${actual}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Pre-commit auto-fix retry test cases ---

# Pre-commit passes on first run → no retry needed
run_precommit_retry_test "precommit-passes-first-run" \
  "0" "no" "0" "pass:clean"

# Pre-commit fails, hooks auto-fixed files, retry succeeds
run_precommit_retry_test "precommit-auto-fix-retry-succeeds" \
  "1" "yes" "0" "pass:auto-fixed"

# Pre-commit fails, hooks auto-fixed files, retry still fails
run_precommit_retry_test "precommit-auto-fix-retry-fails" \
  "1" "yes" "1" "blocked:retry-failed"

# Pre-commit fails, no unstaged changes (genuine failure)
run_precommit_retry_test "precommit-genuine-failure" \
  "1" "no" "0" "blocked:no-auto-fix"

# Pre-commit passes but unstaged changes exist (e.g. hook wrote a log file)
run_precommit_retry_test "precommit-passes-with-unstaged" \
  "0" "yes" "0" "pass:clean"

# Pre-commit fails, auto-fix retry passes, but retry left unstaged changes
run_precommit_retry_test "precommit-retry-passes-but-left-unstaged" \
  "1" "yes" "0" "blocked:retry-left-unstaged" "yes"

# ---------------------------------------------------------------------------
# Test helper — reimplements the RESULT_FILE selection logic from post-fix.sh
# (the FULLSEND_VALIDATED_ITERATION_DIR / fix-result.json / result.json
# fallback / rescan block) so we can test it without git, gitleaks, or a
# real PR. Verifies the fix for a fail-open regression (#5395 follow-up):
# the prefer-validated-dir logic must not silently fall back to rescanning
# RUN_DIR/iteration-*/output for fix-result.json when
# FULLSEND_VALIDATED_ITERATION_DIR is set but the expected filename is
# missing there (e.g. the agent wrote "result.json", which
# validate-output-schema.sh accepts as a fallback without renaming). Falling
# back to a rescan would let a later, unvalidated iteration's output win —
# exactly the bug #5393/#5395 fixed.
# ---------------------------------------------------------------------------
select_fix_result_file() {
  local run_dir="$1"
  local validated_dir="${2:-}"

  local result_file
  if [ -n "${validated_dir}" ]; then
    result_file="${validated_dir}/fix-result.json"
    if [ ! -f "${result_file}" ]; then
      local fallback_result="${validated_dir}/result.json"
      [ -f "${fallback_result}" ] && result_file="${fallback_result}"
    fi
  else
    result_file=""
    for dir in "${run_dir}"/iteration-*/output; do
      if [ -f "${dir}/fix-result.json" ]; then
        result_file="${dir}/fix-result.json"
      fi
    done
  fi
  echo "${result_file}"
}

run_select_fix_result_test() {
  local test_name="$1"
  local validated_filename="$2" # "" to leave the validated dir empty
  local expect_wrong_iteration="$3" # "true" if a wrong-iteration fallback is acceptable (no FULLSEND_VALIDATED_ITERATION_DIR)

  local run_dir
  run_dir="$(mktemp -d)"
  mkdir -p "${run_dir}/iteration-1/output"
  if [ -n "${validated_filename}" ]; then
    echo '{"marker":"validated"}' > "${run_dir}/iteration-1/output/${validated_filename}"
  fi
  # A later iteration — must never be selected when FULLSEND_VALIDATED_ITERATION_DIR
  # is set, whether or not the validated dir's file is found.
  mkdir -p "${run_dir}/iteration-2/output"
  echo '{"marker":"wrong-iteration"}' > "${run_dir}/iteration-2/output/fix-result.json"

  local result_file
  result_file="$(select_fix_result_file "${run_dir}" "${run_dir}/iteration-1/output")"

  rm -rf "${run_dir}"

  if [ "${expect_wrong_iteration}" = "true" ]; then
    if [ "${result_file}" != "${run_dir}/iteration-1/output/${validated_filename}" ]; then
      echo "FAIL: ${test_name} — expected '${run_dir}/iteration-1/output/${validated_filename}', got '${result_file}'"
      FAILURES=$((FAILURES + 1))
      return
    fi
    echo "PASS: ${test_name}"
    return
  fi

  # Missing-file case: must fail closed (empty result), never silently
  # resolve to iteration-2's fix-result.json.
  if [ "${result_file}" = "${run_dir}/iteration-2/output/fix-result.json" ]; then
    echo "FAIL: ${test_name} — silently fell back to iteration-2 instead of failing closed"
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "PASS: ${test_name} (result_file='${result_file}', did not use wrong iteration)"
}

run_select_fix_result_test "validated-dir-fix-result-json-used" "fix-result.json" "true"
run_select_fix_result_test "validated-dir-result-json-fallback-used" "result.json" "true"
run_select_fix_result_test "validated-dir-missing-file-fails-closed" "" "false"

# Sanity check: with no FULLSEND_VALIDATED_ITERATION_DIR (legacy behavior),
# the rescan picks the last iteration by glob order.
run_test_rescan_last_iteration() {
  local run_dir
  run_dir="$(mktemp -d)"
  mkdir -p "${run_dir}/iteration-1/output" "${run_dir}/iteration-2/output"
  echo '{"marker":"iter1"}' > "${run_dir}/iteration-1/output/fix-result.json"
  echo '{"marker":"iter2"}' > "${run_dir}/iteration-2/output/fix-result.json"

  local result_file
  result_file="$(select_fix_result_file "${run_dir}" "")"
  rm -rf "${run_dir}"

  if [ "${result_file}" != "${run_dir}/iteration-2/output/fix-result.json" ]; then
    echo "FAIL: rescan-picks-last-iteration — expected iteration-2, got '${result_file}'"
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "PASS: rescan-picks-last-iteration"
}
run_test_rescan_last_iteration

# --- Summary ---

echo ""
if [ ${FAILURES} -gt 0 ]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
