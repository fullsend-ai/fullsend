#!/usr/bin/env bash
# pre-code-test.sh — Test pre-code.sh input validation.
#
# Run from the repo root: bash internal/scaffold/fullsend-repo/scripts/pre-code-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PRE_SCRIPT="${SCRIPT_DIR}/pre-code.sh"
FAILURES=0

run_test() {
  local test_name="$1"
  local issue_number="$2"
  local repo_full_name="$3"
  local github_issue_url="$4"
  local expect_failure="${5:-false}"

  local exit_code=0
  ISSUE_NUMBER="${issue_number}" \
  REPO_FULL_NAME="${repo_full_name}" \
  GITHUB_ISSUE_URL="${github_issue_url}" \
    bash "${PRE_SCRIPT}" > /dev/null 2>&1 || exit_code=$?

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
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Valid inputs ---

run_test "issue-url-passes" \
  "42" "org/repo" "https://github.com/org/repo/issues/42"

run_test "pull-request-url-passes" \
  "839" "konflux-ci/konflux-ui" "https://github.com/konflux-ci/konflux-ui/pull/839"

run_test "large-issue-number" \
  "99999" "org/repo" "https://github.com/org/repo/issues/99999"

run_test "repo-with-dots-and-hyphens" \
  "1" "my-org/my.repo-name" "https://github.com/my-org/my.repo-name/issues/1"

# --- Invalid URL format ---

run_test "rejects-commits-url" \
  "42" "org/repo" "https://github.com/org/repo/commits/42" "true"

run_test "rejects-discussions-url" \
  "42" "org/repo" "https://github.com/org/repo/discussions/42" "true"

run_test "rejects-pulls-plural" \
  "42" "org/repo" "https://github.com/org/repo/pulls/42" "true"

run_test "rejects-trailing-path" \
  "42" "org/repo" "https://github.com/org/repo/issues/42/files" "true"

run_test "rejects-empty-url" \
  "42" "org/repo" "" "true"

run_test "rejects-non-github-host" \
  "42" "org/repo" "https://gitlab.com/org/repo/issues/42" "true"

# --- Cross-validation ---

run_test "rejects-repo-mismatch" \
  "42" "org/other-repo" "https://github.com/org/repo/issues/42" "true"

run_test "rejects-number-mismatch" \
  "99" "org/repo" "https://github.com/org/repo/issues/42" "true"

run_test "rejects-number-mismatch-pr" \
  "99" "org/repo" "https://github.com/org/repo/pull/42" "true"

# --- Invalid ISSUE_NUMBER ---

run_test "rejects-zero-issue-number" \
  "0" "org/repo" "https://github.com/org/repo/issues/0" "true"

run_test "rejects-non-numeric-issue-number" \
  "abc" "org/repo" "https://github.com/org/repo/issues/42" "true"

run_test "rejects-empty-issue-number" \
  "" "org/repo" "https://github.com/org/repo/issues/42" "true"

# --- Invalid REPO_FULL_NAME ---

run_test "rejects-empty-repo" \
  "42" "" "https://github.com/org/repo/issues/42" "true"

run_test "rejects-repo-no-slash" \
  "42" "noslash" "https://github.com/org/repo/issues/42" "true"

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
