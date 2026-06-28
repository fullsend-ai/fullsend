#!/usr/bin/env bash
# pre-review-test.sh — Test pre-review.sh with mock gh to verify PR state check.
#
# Uses a mock gh command to capture calls without hitting GitHub.
# Run from the repo root: bash internal/scaffold/fullsend-repo/scripts/pre-review-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PRE_SCRIPT="${SCRIPT_DIR}/pre-review.sh"
FAILURES=0

# Create a temp directory for mock state.
TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# --- Helpers ---

# build_mock creates a mock gh binary that returns preconfigured responses.
# Arguments:
#   $1 — PR state to return for "gh pr view" calls (e.g. "OPEN", "MERGED", "CLOSED").
build_mock() {
  local pr_state="$1"
  local mock_bin="${TMPDIR}/bin"
  local gh_log="${TMPDIR}/gh-calls.log"

  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"
  : > "${gh_log}"

  cat > "${mock_bin}/gh" <<MOCKEOF
#!/usr/bin/env bash
CALL_LOG="${gh_log}"

echo "gh \$*" >> "\${CALL_LOG}"

# Route by subcommand
if [[ "\$1" == "pr" && "\$2" == "view" ]]; then
  echo "${pr_state}"
elif [[ "\$1" == "issue" && "\$2" == "comment" ]]; then
  # Consume stdin (body-file reads from stdin)
  cat > /dev/null
  exit 0
fi
MOCKEOF

  chmod +x "${mock_bin}/gh"

  echo "${mock_bin}"
}

run_test() {
  local test_name="$1"
  local pr_state="$2"
  local expected_pattern="$3"
  local expect_exit="$4"
  local extra_env="${5:-}"

  local mock_bin
  mock_bin="$(build_mock "${pr_state}")"
  local gh_log="${TMPDIR}/gh-calls.log"

  # Set base env vars for the script.
  local env_cmd=(
    env
    PATH="${mock_bin}:${PATH}"
    PR_NUMBER="42"
    REPO_FULL_NAME="test-org/test-repo"
    GITHUB_PR_URL="https://github.com/test-org/test-repo/pull/42"
    REVIEW_TOKEN="fake-token"
  )

  # Add extra env vars if provided.
  if [[ -n "${extra_env}" ]]; then
    while IFS= read -r kv; do
      [[ -n "${kv}" ]] && env_cmd+=("${kv}")
    done <<< "${extra_env}"
  fi

  local exit_code=0
  "${env_cmd[@]}" bash "${PRE_SCRIPT}" > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  # Check exit code.
  if [[ ${exit_code} -ne ${expect_exit} ]]; then
    echo "FAIL: ${test_name} — expected exit ${expect_exit}, got ${exit_code}"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  # Check expected pattern in gh calls (if provided).
  if [[ -n "${expected_pattern}" ]]; then
    if ! grep -qF "${expected_pattern}" "${gh_log}" 2>/dev/null; then
      echo "FAIL: ${test_name} — expected gh call pattern '${expected_pattern}' not found"
      echo "Actual calls:"
      cat "${gh_log}" 2>/dev/null || echo "(no calls)"
      FAILURES=$((FAILURES + 1))
      return
    fi
  fi

  echo "PASS: ${test_name}"
}

# Check stdout contains a specific string.
run_test_stdout() {
  local test_name="$1"
  local pr_state="$2"
  local expected_stdout="$3"
  local expect_exit="$4"
  local extra_env="${5:-}"

  local mock_bin
  mock_bin="$(build_mock "${pr_state}")"

  local env_cmd=(
    env
    PATH="${mock_bin}:${PATH}"
    PR_NUMBER="42"
    REPO_FULL_NAME="test-org/test-repo"
    GITHUB_PR_URL="https://github.com/test-org/test-repo/pull/42"
    REVIEW_TOKEN="fake-token"
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

  if ! grep -qF "${expected_stdout}" "${TMPDIR}/stdout.log" 2>/dev/null; then
    echo "FAIL: ${test_name} — expected stdout '${expected_stdout}' not found"
    echo "Actual stdout:"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# Check stdout does NOT contain a specific string.
run_test_stdout_absent() {
  local test_name="$1"
  local pr_state="$2"
  local absent_stdout="$3"
  local expect_exit="$4"
  local extra_env="${5:-}"

  local mock_bin
  mock_bin="$(build_mock "${pr_state}")"

  local env_cmd=(
    env
    PATH="${mock_bin}:${PATH}"
    PR_NUMBER="42"
    REPO_FULL_NAME="test-org/test-repo"
    GITHUB_PR_URL="https://github.com/test-org/test-repo/pull/42"
    REVIEW_TOKEN="fake-token"
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

  if grep -qF "${absent_stdout}" "${TMPDIR}/stdout.log" 2>/dev/null; then
    echo "FAIL: ${test_name} — stdout should NOT contain '${absent_stdout}'"
    echo "Actual stdout:"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Test cases ---

# OPEN PR → agent proceeds normally, no skip comment.
run_test_stdout "open-pr-proceeds" \
  "OPEN" \
  "proceeding with review agent" \
  0

run_test_stdout_absent "open-pr-no-skip-comment" \
  "OPEN" \
  "Review skipped" \
  0

# MERGED PR → posts comment and exits 0.
run_test_stdout "merged-pr-skips" \
  "MERGED" \
  "PR #42 is MERGED" \
  0

run_test "merged-pr-posts-comment" \
  "MERGED" \
  "gh issue comment 42 --repo test-org/test-repo --body-file -" \
  0

# CLOSED PR → posts comment and exits 0.
run_test_stdout "closed-pr-skips" \
  "CLOSED" \
  "PR #42 is CLOSED" \
  0

run_test "closed-pr-posts-comment" \
  "CLOSED" \
  "gh issue comment 42 --repo test-org/test-repo --body-file -" \
  0

# No token → skips PR state check entirely, exits 0.
run_test_stdout "no-token-skips-check" \
  "MERGED" \
  "No token available" \
  0 \
  "$(printf '%s\n%s' 'REVIEW_TOKEN=' 'GH_TOKEN=')"

# REVIEW_TOKEN not set but GH_TOKEN is → uses GH_TOKEN, still works.
run_test_stdout "gh-token-fallback-proceeds" \
  "OPEN" \
  "proceeding with review agent" \
  0 \
  "$(printf '%s\n%s' 'REVIEW_TOKEN=' 'GH_TOKEN=fake-token')"

run_test_stdout "gh-token-fallback-skips-merged" \
  "MERGED" \
  "PR #42 is MERGED" \
  0 \
  "$(printf '%s\n%s' 'REVIEW_TOKEN=' 'GH_TOKEN=fake-token')"

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
