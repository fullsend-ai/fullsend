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
#   $1 — output for timeline API calls (JSON lines matching cross-ref events)
#   $2 — output for comments API calls (jq-filtered count, default "0")
#   $3 — exit code for timeline API calls (default 0)
build_mock() {
  local timeline_output="$1"
  local comments_output="${2:-0}"
  local timeline_exit="${3:-0}"
  local mock_bin="${TMPDIR}/bin"
  local gh_log="${TMPDIR}/gh-calls.log"

  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"
  > "${gh_log}"

  printf '%s' "${timeline_output}" > "${TMPDIR}/timeline-output.txt"
  printf '%s' "${comments_output}" > "${TMPDIR}/comments-output.txt"
  printf '%s' "${timeline_exit}" > "${TMPDIR}/timeline-exit.txt"

  cat > "${mock_bin}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
CALL_LOG="__LOGFILE__"
TIMELINE_OUTPUT="__TIMELINE__"
COMMENTS_OUTPUT="__COMMENTS__"
TIMELINE_EXIT="__TIMELINE_EXIT__"

echo "gh $*" >> "${CALL_LOG}"

if [[ "$1" == "api" ]]; then
  URL="$2"
  if [[ "${URL}" == *"/timeline"* ]]; then
    ECODE="$(cat "${TIMELINE_EXIT}")"
    if [[ "${ECODE}" -ne 0 ]]; then
      echo "API error" >&2
      exit "${ECODE}"
    fi
    # Simulate --paginate --jq by just outputting the pre-filtered content.
    cat "${TIMELINE_OUTPUT}"
    exit 0
  elif [[ "${URL}" == *"/comments"* ]] && [[ "$*" == *"--jq"* ]]; then
    cat "${COMMENTS_OUTPUT}"
    exit 0
  elif [[ "${URL}" == *"/labels"* ]]; then
    exit 0
  fi
elif [[ "$1" == "label" ]]; then
  exit 0
elif [[ "$1" == "issue" && "$2" == "comment" ]]; then
  cat > /dev/null
  exit 0
fi
MOCKEOF

  # Patch placeholders.
  local escaped_log="${gh_log//\//\\/}"
  local escaped_timeline="${TMPDIR//\//\\/}\/timeline-output.txt"
  local escaped_comments="${TMPDIR//\//\\/}\/comments-output.txt"
  local escaped_exit="${TMPDIR//\//\\/}\/timeline-exit.txt"
  perl -pi -e "s/__LOGFILE__/${escaped_log}/g" "${mock_bin}/gh"
  perl -pi -e "s/__TIMELINE__/${escaped_timeline}/g" "${mock_bin}/gh"
  perl -pi -e "s/__COMMENTS__/${escaped_comments}/g" "${mock_bin}/gh"
  perl -pi -e "s/__TIMELINE_EXIT__/${escaped_exit}/g" "${mock_bin}/gh"

  chmod +x "${mock_bin}/gh"

  echo "${mock_bin}"
}

run_test() {
  local test_name="$1"
  local timeline_output="$2"
  local expected_pattern="$3"
  local expect_exit="$4"
  local extra_env="${5:-}"
  local comments_output="${6:-0}"
  local timeline_exit="${7:-0}"

  local mock_bin
  mock_bin="$(build_mock "${timeline_output}" "${comments_output}" "${timeline_exit}")"
  local gh_log="${TMPDIR}/gh-calls.log"

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

run_test_stdout() {
  local test_name="$1"
  local timeline_output="$2"
  local expected_stdout="$3"
  local expect_exit="$4"
  local extra_env="${5:-}"
  local comments_output="${6:-0}"
  local timeline_exit="${7:-0}"

  local mock_bin
  mock_bin="$(build_mock "${timeline_output}" "${comments_output}" "${timeline_exit}")"

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

TAB=$'\t'

# No existing PRs → agent proceeds (exit 0).
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

# Bot PR only → timeline returns empty → proceeds.
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

# Timeline API uses correct endpoint.
run_test "timeline-api-called" \
  "" \
  "gh api repos/test-org/test-repo/issues/42/timeline --paginate --jq" \
  0

# gh API failure → warns and proceeds (fail-open with warning).
run_test_stdout "api-failure-warns-and-proceeds" \
  "" \
  "Failed to check for existing PRs" \
  0 \
  "" \
  "[]" \
  "1"

run_test_stdout "api-failure-proceeds-with-agent" \
  "" \
  "No existing human PRs found" \
  0 \
  "" \
  "[]" \
  "1"

# Invalid FULLSEND_BOT_LOGIN → exits with error.
run_test_stdout "invalid-bot-login-rejected" \
  "" \
  "FULLSEND_BOT_LOGIN contains invalid characters" \
  1 \
  "FULLSEND_BOT_LOGIN=evil\$(whoami)"

# Valid custom FULLSEND_BOT_LOGIN → accepted.
run_test_stdout "valid-custom-bot-login" \
  "" \
  "No existing human PRs found" \
  0 \
  "FULLSEND_BOT_LOGIN=my-bot[bot]"

# Comment idempotency — existing comment → should skip posting.
run_test_stdout "idempotent-comment-skips-duplicate" \
  "99${TAB}human-dev${TAB}https://github.com/test-org/test-repo/pull/99" \
  "Skipping duplicate comment" \
  0 \
  "" \
  "1"

# Comment idempotency — no existing comment → should post.
run_test "idempotent-comment-posts-new" \
  "99${TAB}human-dev${TAB}https://github.com/test-org/test-repo/pull/99" \
  "gh issue comment 42 --repo test-org/test-repo --body-file -" \
  0 \
  "" \
  "0"

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
