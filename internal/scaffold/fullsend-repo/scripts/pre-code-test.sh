#!/usr/bin/env bash
# pre-code-test.sh — Test pre-code.sh with mock gh to verify existing-PR check.
#
# Uses a mock gh command to capture calls without hitting GitHub.
# Run from the repo root: bash internal/scaffold/fullsend-repo/scripts/pre-code-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PRE_SCRIPT="${SCRIPT_DIR}/pre-code.sh"
FAILURES=0

# Create a temp directory for mock state.
TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# --- Helpers ---

# build_mock creates a mock gh binary that returns preconfigured responses.
# Arguments:
#   $1 — output to return for "gh pr list" calls (tab-separated lines
#         matching what gh --jq would produce, or empty for no PRs)
build_mock() {
  local pr_list_output="$1"
  local mock_bin="${TMPDIR}/bin"
  local gh_log="${TMPDIR}/gh-calls.log"

  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"
  > "${gh_log}"

  # Write the pr list output to a file so the mock can read it.
  printf '%s' "${pr_list_output}" > "${TMPDIR}/pr-list-output.txt"

  cat > "${mock_bin}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
CALL_LOG="LOGFILE_PLACEHOLDER"
PR_OUTPUT="OUTPUT_PLACEHOLDER"

echo "gh $*" >> "${CALL_LOG}"

# Route by subcommand
if [[ "$1" == "pr" && "$2" == "list" ]]; then
  cat "${PR_OUTPUT}"
elif [[ "$1" == "label" ]]; then
  exit 0
elif [[ "$1" == "api" ]]; then
  exit 0
elif [[ "$1" == "issue" && "$2" == "comment" ]]; then
  # Consume stdin (body-file reads from stdin)
  cat > /dev/null
  exit 0
fi
MOCKEOF

  # Patch placeholders with actual paths (avoid sed on source files,
  # but this is a generated mock — not repo source code).
  local escaped_log="${gh_log//\//\\/}"
  local escaped_out="${TMPDIR//\//\\/}\/pr-list-output.txt"
  perl -pi -e "s/LOGFILE_PLACEHOLDER/${escaped_log}/g" "${mock_bin}/gh"
  perl -pi -e "s/OUTPUT_PLACEHOLDER/${escaped_out}/g" "${mock_bin}/gh"

  chmod +x "${mock_bin}/gh"

  echo "${mock_bin}"
}

run_test() {
  local test_name="$1"
  local pr_list_output="$2"
  local expected_pattern="$3"
  local expect_exit="$4"         # 0 = success, 1 = failure
  local extra_env="${5:-}"       # additional env vars (KEY=VAL KEY2=VAL2)

  local mock_bin
  mock_bin="$(build_mock "${pr_list_output}")"
  local gh_log="${TMPDIR}/gh-calls.log"

  # Set base env vars for the script.
  local env_cmd=(
    env
    PATH="${mock_bin}:${PATH}"
    ISSUE_NUMBER="42"
    REPO_FULL_NAME="test-org/test-repo"
    GITHUB_ISSUE_URL="https://github.com/test-org/test-repo/issues/42"
    GH_TOKEN="fake-token"
  )

  # Add extra env vars if provided.
  if [[ -n "${extra_env}" ]]; then
    for kv in ${extra_env}; do
      env_cmd+=("${kv}")
    done
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
  local pr_list_output="$2"
  local expected_stdout="$3"
  local expect_exit="$4"
  local extra_env="${5:-}"

  local mock_bin
  mock_bin="$(build_mock "${pr_list_output}")"

  local env_cmd=(
    env
    PATH="${mock_bin}:${PATH}"
    ISSUE_NUMBER="42"
    REPO_FULL_NAME="test-org/test-repo"
    GITHUB_ISSUE_URL="https://github.com/test-org/test-repo/issues/42"
    GH_TOKEN="fake-token"
  )

  if [[ -n "${extra_env}" ]]; then
    for kv in ${extra_env}; do
      env_cmd+=("${kv}")
    done
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

# --- Test cases ---

# Tab character for readability.
TAB=$'\t'

# No existing PRs → agent proceeds (exit 0, no label/comment).
run_test_stdout "no-existing-prs-proceeds" \
  "" \
  "No existing human PRs found" \
  0

# Human PR exists → should apply label and comment, then exit 0.
run_test "human-pr-applies-label" \
  "99${TAB}human-dev${TAB}https://github.com/test-org/test-repo/pull/99" \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=pr-open --silent" \
  0

run_test "human-pr-posts-comment" \
  "99${TAB}human-dev${TAB}https://github.com/test-org/test-repo/pull/99" \
  "gh issue comment 42 --repo test-org/test-repo --body-file -" \
  0

run_test_stdout "human-pr-skips-agent" \
  "99${TAB}human-dev${TAB}https://github.com/test-org/test-repo/pull/99" \
  "Skipping code agent" \
  0

# Bot PR only → gh --jq filters it out, so pr list returns empty → proceeds.
run_test_stdout "bot-pr-does-not-block" \
  "" \
  "No existing human PRs found" \
  0

# CODE_FORCE=true → should skip check even with human PR.
run_test_stdout "force-override-skips-check" \
  "99${TAB}human-dev${TAB}https://github.com/test-org/test-repo/pull/99" \
  "CODE_FORCE=true" \
  0 \
  "CODE_FORCE=true"

# No GH_TOKEN → skips check entirely, exits 0.
run_test_stdout "no-gh-token-skips-check" \
  "" \
  "GH_TOKEN not set" \
  0 \
  "GH_TOKEN="

# Multiple human PRs → should block and apply label.
run_test "multiple-human-prs-block" \
  "50${TAB}dev-a${TAB}https://github.com/test-org/test-repo/pull/50
51${TAB}dev-b${TAB}https://github.com/test-org/test-repo/pull/51" \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=pr-open --silent" \
  0

run_test_stdout "multiple-human-prs-notice" \
  "50${TAB}dev-a${TAB}https://github.com/test-org/test-repo/pull/50
51${TAB}dev-b${TAB}https://github.com/test-org/test-repo/pull/51" \
  "Found existing human PR #50 by @dev-a" \
  0

# PR label gets created.
run_test "pr-label-created" \
  "99${TAB}human-dev${TAB}https://github.com/test-org/test-repo/pull/99" \
  "gh label create pr-open --repo test-org/test-repo" \
  0

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
