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
#   $1 — JSON array to return for "gh pr list" calls. When the caller
#        passes --jq, the mock pipes this JSON through jq so the real
#        filter expression is exercised.  Pass an empty string for no PRs.
build_mock() {
  local pr_list_output="$1"
  local mock_bin="${TMPDIR}/bin"
  local gh_log="${TMPDIR}/gh-calls.log"

  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"
  : > "${gh_log}"

  # Write the pr list output to a file so the mock can read it.
  printf '%s' "${pr_list_output}" > "${TMPDIR}/pr-list-output.txt"

  cat > "${mock_bin}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
CALL_LOG="LOGFILE_PLACEHOLDER"
PR_OUTPUT="OUTPUT_PLACEHOLDER"

echo "gh $*" >> "${CALL_LOG}"

# Route by subcommand
if [[ "$1" == "pr" && "$2" == "list" ]]; then
  # Parse --jq flag from arguments, just like the real gh CLI.
  JQ_EXPR=""
  shift 2
  while [[ $# -gt 0 ]]; do
    if [[ "$1" == "--jq" ]]; then
      JQ_EXPR="$2"
      break
    fi
    shift
  done
  if [[ -n "${JQ_EXPR}" ]] && [[ -s "${PR_OUTPUT}" ]]; then
    jq -r "${JQ_EXPR}" "${PR_OUTPUT}"
  else
    cat "${PR_OUTPUT}"
  fi
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
  local gh_output="${TMPDIR}/github-output.txt"
  : > "${gh_output}"

  # Set base env vars for the script.
  local env_cmd=(
    env
    PATH="${mock_bin}:${PATH}"
    ISSUE_NUMBER="42"
    REPO_FULL_NAME="test-org/test-repo"
    GITHUB_ISSUE_URL="https://github.com/test-org/test-repo/issues/42"
    GH_TOKEN="fake-token"
    GITHUB_OUTPUT="${gh_output}"
  )

  # Add extra env vars if provided (read line-by-line to support values with spaces).
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
  local pr_list_output="$2"
  local expected_stdout="$3"
  local expect_exit="$4"
  local extra_env="${5:-}"

  local mock_bin
  mock_bin="$(build_mock "${pr_list_output}")"
  local gh_output="${TMPDIR}/github-output.txt"
  : > "${gh_output}"

  local env_cmd=(
    env
    PATH="${mock_bin}:${PATH}"
    ISSUE_NUMBER="42"
    REPO_FULL_NAME="test-org/test-repo"
    GITHUB_ISSUE_URL="https://github.com/test-org/test-repo/issues/42"
    GH_TOKEN="fake-token"
    GITHUB_OUTPUT="${gh_output}"
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

# Check stdout contains one string and does NOT contain another.
run_test_stdout_excludes() {
  local test_name="$1"
  local pr_list_output="$2"
  local expected_stdout="$3"
  local excluded_stdout="$4"
  local expect_exit="$5"
  local extra_env="${6:-}"

  local mock_bin
  mock_bin="$(build_mock "${pr_list_output}")"
  local gh_output="${TMPDIR}/github-output.txt"
  : > "${gh_output}"

  local env_cmd=(
    env
    PATH="${mock_bin}:${PATH}"
    ISSUE_NUMBER="42"
    REPO_FULL_NAME="test-org/test-repo"
    GITHUB_ISSUE_URL="https://github.com/test-org/test-repo/issues/42"
    GH_TOKEN="fake-token"
    GITHUB_OUTPUT="${gh_output}"
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

  if grep -qF "${excluded_stdout}" "${TMPDIR}/stdout.log" 2>/dev/null; then
    echo "FAIL: ${test_name} — excluded stdout '${excluded_stdout}' was found"
    echo "Actual stdout:"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Test cases ---

# JSON helpers — build unfiltered PR JSON that the mock returns to the
# script.  The mock pipes this through jq using the real --jq expression
# from pre-code.sh, so the filter is exercised end-to-end.

# Single human PR.
HUMAN_PR_JSON='[{"number":99,"author":{"login":"human-dev"},"url":"https://github.com/test-org/test-repo/pull/99"}]'

# Single fullsend-ai[bot] PR.
BOT_PR_JSON='[{"number":10,"author":{"login":"fullsend-ai[bot]"},"url":"https://github.com/test-org/test-repo/pull/10"}]'

# Single fullsend-ai-coder[bot] PR.
CODER_BOT_PR_JSON='[{"number":11,"author":{"login":"fullsend-ai-coder[bot]"},"url":"https://github.com/test-org/test-repo/pull/11"}]'

# Both bot PRs plus a human PR.
MIXED_PR_JSON='[{"number":10,"author":{"login":"fullsend-ai[bot]"},"url":"https://github.com/test-org/test-repo/pull/10"},{"number":11,"author":{"login":"fullsend-ai-coder[bot]"},"url":"https://github.com/test-org/test-repo/pull/11"},{"number":99,"author":{"login":"human-dev"},"url":"https://github.com/test-org/test-repo/pull/99"}]'

# Multiple human PRs.
MULTI_HUMAN_PR_JSON='[{"number":50,"author":{"login":"dev-a"},"url":"https://github.com/test-org/test-repo/pull/50"},{"number":51,"author":{"login":"dev-b"},"url":"https://github.com/test-org/test-repo/pull/51"}]'

# No existing PRs → agent proceeds (exit 0, no label/comment).
run_test_stdout "no-existing-prs-proceeds" \
  "" \
  "No existing human PRs found" \
  0

# Human PR exists → should apply label and comment, then exit 0.
run_test "human-pr-applies-label" \
  "${HUMAN_PR_JSON}" \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=pr-open --silent" \
  0

run_test "human-pr-posts-comment" \
  "${HUMAN_PR_JSON}" \
  "gh issue comment 42 --repo test-org/test-repo --body-file -" \
  0

run_test_stdout "human-pr-skips-agent" \
  "${HUMAN_PR_JSON}" \
  "Skipping code agent" \
  0

# Bot PR only → jq filter removes it → script sees empty output → proceeds.
run_test_stdout "bot-pr-does-not-block" \
  "${BOT_PR_JSON}" \
  "No existing human PRs found" \
  0

# CODE_FORCE=true → should skip check even with human PR.
run_test_stdout "force-override-code-force" \
  "${HUMAN_PR_JSON}" \
  "Force override" \
  0 \
  "CODE_FORCE=true"

# COMMENT_BODY contains --force → should also skip check.
run_test_stdout "force-override-comment-body" \
  "${HUMAN_PR_JSON}" \
  "Force override" \
  0 \
  "COMMENT_BODY=/fs-code --force"

# No GH_TOKEN → skips check entirely, exits 0.
run_test_stdout "no-gh-token-skips-check" \
  "" \
  "GH_TOKEN not set" \
  0 \
  "GH_TOKEN="

# Coder bot PR only → jq filter removes it → script proceeds.
run_test_stdout "coder-bot-pr-does-not-block" \
  "${CODER_BOT_PR_JSON}" \
  "No existing human PRs found" \
  0

# Both bots + human PR → jq filter removes bots, human PR blocks.
run_test_stdout "coder-bot-pr-plus-human-pr-blocks" \
  "${MIXED_PR_JSON}" \
  "Skipping code agent" \
  0

# Both bots only → jq filter removes all → script proceeds.
run_test_stdout "both-bots-do-not-block" \
  '[{"number":10,"author":{"login":"fullsend-ai[bot]"},"url":"https://github.com/test-org/test-repo/pull/10"},{"number":11,"author":{"login":"fullsend-ai-coder[bot]"},"url":"https://github.com/test-org/test-repo/pull/11"}]' \
  "No existing human PRs found" \
  0

# Multiple human PRs → should block and apply label.
run_test "multiple-human-prs-block" \
  "${MULTI_HUMAN_PR_JSON}" \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=pr-open --silent" \
  0

run_test_stdout "multiple-human-prs-notice" \
  "${MULTI_HUMAN_PR_JSON}" \
  "Found existing human PR #50 by @dev-a" \
  0

# PR label gets created.
run_test "pr-label-created" \
  "${HUMAN_PR_JSON}" \
  "gh label create pr-open --repo test-org/test-repo" \
  0

# --- Regression tests: --force bypasses PR search (issue #1697) ---
TAB=$'\t'

# COMMENT_BODY with --force must exit before PR search is reached.
run_test_stdout_excludes "force-comment-body-no-pr-search" \
  "99${TAB}human-dev${TAB}https://github.com/test-org/test-repo/pull/99" \
  "Force override" \
  "Checking for existing open PRs" \
  0 \
  "COMMENT_BODY=/fs-code --force"

# CODE_FORCE=true must exit before PR search is reached.
run_test_stdout_excludes "force-code-force-no-pr-search" \
  "99${TAB}human-dev${TAB}https://github.com/test-org/test-repo/pull/99" \
  "Force override" \
  "Checking for existing open PRs" \
  0 \
  "CODE_FORCE=true"

# Force check logs COMMENT_BODY value for debuggability.
run_test_stdout "force-check-logs-comment-body" \
  "" \
  "Evaluating force override:" \
  0 \
  "COMMENT_BODY=/fs-code --force"

# Without --force, PR search IS reached (no false bypass).
run_test_stdout "no-force-reaches-pr-search" \
  "" \
  "Checking for existing open PRs" \
  0 \
  "COMMENT_BODY=/fs-code"

# --- GITHUB_OUTPUT skip signal tests (issue #1312) ---

# Helper: run pre-code.sh and check GITHUB_OUTPUT contains expected key=value.
run_test_github_output() {
  local test_name="$1"
  local pr_list_output="$2"
  local expected_output="$3"    # e.g. "skipped=true"
  local expect_exit="$4"
  local extra_env="${5:-}"

  local mock_bin
  mock_bin="$(build_mock "${pr_list_output}")"
  local gh_output="${TMPDIR}/github-output.txt"
  : > "${gh_output}"

  local env_cmd=(
    env
    PATH="${mock_bin}:${PATH}"
    ISSUE_NUMBER="42"
    REPO_FULL_NAME="test-org/test-repo"
    GITHUB_ISSUE_URL="https://github.com/test-org/test-repo/issues/42"
    GH_TOKEN="fake-token"
    GITHUB_OUTPUT="${gh_output}"
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

  if ! grep -qF "${expected_output}" "${gh_output}" 2>/dev/null; then
    echo "FAIL: ${test_name} — expected GITHUB_OUTPUT to contain '${expected_output}'"
    echo "Actual GITHUB_OUTPUT:"
    cat "${gh_output}" 2>/dev/null || echo "(empty)"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# Existing human PR → GITHUB_OUTPUT must contain skip=true.
run_test_github_output "skip-output-set-on-existing-pr" \
  "${HUMAN_PR_JSON}" \
  "skipped=true" \
  0

# No existing PRs → GITHUB_OUTPUT must contain skip=false.
run_test_github_output "skip-output-false-on-no-prs" \
  "" \
  "skipped=false" \
  0

# Force override → GITHUB_OUTPUT must NOT contain skip=true (force exits before PR check).
run_test_github_output "skip-output-not-set-on-force" \
  "${HUMAN_PR_JSON}" \
  "skipped=false" \
  0 \
  "CODE_FORCE=true"

# No GH_TOKEN → GITHUB_OUTPUT must contain skip=false (proceeds without PR check).
run_test_github_output "skip-output-false-on-no-token" \
  "" \
  "skipped=false" \
  0 \
  "GH_TOKEN="

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
