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
#   $1 — JSON to return for "gh pr list" calls (must be valid JSON array)
#   $2 — (optional) triage comment body to return for "gh api .../comments"
build_mock() {
  local pr_json="$1"
  local triage_body="${2:-}"
  local mock_bin="${TMPDIR}/bin"
  local gh_log="${TMPDIR}/gh-calls.log"

  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"
  > "${gh_log}"

  # Write the pr list JSON to a file so the mock can read it.
  printf '%s' "${pr_json}" > "${TMPDIR}/pr-list-output.txt"
  # Write the triage body (post-jq output: raw body text or empty).
  printf '%s' "${triage_body}" > "${TMPDIR}/triage-output.txt"

  cat > "${mock_bin}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
CALL_LOG="LOGFILE_PLACEHOLDER"
PR_OUTPUT="OUTPUT_PLACEHOLDER"
TRIAGE_OUTPUT="TRIAGE_PLACEHOLDER"

echo "gh $*" >> "${CALL_LOG}"

# Route by subcommand
if [[ "$1" == "pr" && "$2" == "list" ]]; then
  cat "${PR_OUTPUT}"
elif [[ "$1" == "api" && "$2" == *"/comments"* ]]; then
  cat "${TRIAGE_OUTPUT}"
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
  local escaped_triage="${TMPDIR//\//\\/}\/triage-output.txt"
  perl -pi -e "s/LOGFILE_PLACEHOLDER/${escaped_log}/g" "${mock_bin}/gh"
  perl -pi -e "s/OUTPUT_PLACEHOLDER/${escaped_out}/g" "${mock_bin}/gh"
  perl -pi -e "s/TRIAGE_PLACEHOLDER/${escaped_triage}/g" "${mock_bin}/gh"

  chmod +x "${mock_bin}/gh"

  echo "${mock_bin}"
}

run_test() {
  local test_name="$1"
  local pr_json="$2"
  local expected_pattern="$3"
  local expect_exit="$4"         # 0 = success, 1 = failure
  local extra_env="${5:-}"       # additional env vars (KEY=VAL KEY2=VAL2)
  local triage_body="${6:-}"     # optional triage comment body

  local mock_bin
  mock_bin="$(build_mock "${pr_json}" "${triage_body}")"
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
  local pr_json="$2"
  local expected_stdout="$3"
  local expect_exit="$4"
  local extra_env="${5:-}"
  local triage_body="${6:-}"

  local mock_bin
  mock_bin="$(build_mock "${pr_json}" "${triage_body}")"

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

# Helper: build a JSON array of PR objects.
# Usage: pr_object <number> <author> <url> <body> <title> <branch>
pr_object() {
  local num="$1" author="$2" url="$3" body="$4" title="$5" branch="$6"
  # Escape body and title for JSON (handle double quotes and newlines).
  body="$(printf '%s' "${body}" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read())[1:-1])')"
  title="$(printf '%s' "${title}" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read())[1:-1])')"
  printf '{"number":%d,"url":"%s","author":{"login":"%s"},"body":"%s","title":"%s","headRefName":"%s"}' \
    "${num}" "${url}" "${author}" "${body}" "${title}" "${branch}"
}

# ---- Existing behavior (updated to use JSON) ----

# No existing PRs → agent proceeds (exit 0, no label/comment).
run_test_stdout "no-existing-prs-proceeds" \
  "[]" \
  "No existing human PRs found" \
  0

# Human PR with closing keyword → should apply label and comment, then exit 0.
PR_CLOSES='[{"number":99,"url":"https://github.com/test-org/test-repo/pull/99","author":{"login":"human-dev"},"body":"Closes #42","title":"Fix stuff","headRefName":"fix/stuff"}]'

run_test "human-pr-applies-label" \
  "${PR_CLOSES}" \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=pr-open --silent" \
  0

run_test "human-pr-posts-comment" \
  "${PR_CLOSES}" \
  "gh issue comment 42 --repo test-org/test-repo --body-file -" \
  0

run_test_stdout "human-pr-skips-agent" \
  "${PR_CLOSES}" \
  "Skipping code agent" \
  0

# Bot PR only → jq filters it out, so output is empty → proceeds.
run_test_stdout "bot-pr-does-not-block" \
  "[]" \
  "No existing human PRs found" \
  0

# CODE_FORCE=true → should skip check even with human PR.
run_test_stdout "force-override-skips-check" \
  "${PR_CLOSES}" \
  "CODE_FORCE=true" \
  0 \
  "CODE_FORCE=true"

# No GH_TOKEN → skips check entirely, exits 0.
run_test_stdout "no-gh-token-skips-check" \
  "[]" \
  "GH_TOKEN not set" \
  0 \
  "GH_TOKEN="

# Multiple human PRs with closing keywords → should block and apply label.
PR_MULTI='[{"number":50,"url":"https://github.com/test-org/test-repo/pull/50","author":{"login":"dev-a"},"body":"Fixes #42","title":"Fix A","headRefName":"fix/a"},{"number":51,"url":"https://github.com/test-org/test-repo/pull/51","author":{"login":"dev-b"},"body":"Resolves #42","title":"Fix B","headRefName":"fix/b"}]'

run_test "multiple-human-prs-block" \
  "${PR_MULTI}" \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=pr-open --silent" \
  0

run_test_stdout "multiple-human-prs-notice" \
  "${PR_MULTI}" \
  "Found existing human PR #50 by @dev-a" \
  0

# PR label gets created.
run_test "pr-label-created" \
  "${PR_CLOSES}" \
  "gh label create pr-open --repo test-org/test-repo" \
  0

# ---- New: tightened matching ----

# PR mentions issue number casually (no closing keyword) → should NOT block.
PR_CASUAL='[{"number":894,"url":"https://github.com/test-org/test-repo/pull/894","author":{"login":"human-dev"},"body":"Refactored table components, see also #42 for context","title":"PatternFly 5 table refactor","headRefName":"refactor/pf5-tables"}]'

run_test_stdout "casual-mention-does-not-block" \
  "${PR_CASUAL}" \
  "No existing human PRs found" \
  0

# PR with "Fixes #N" in body → should block.
PR_FIXES='[{"number":100,"url":"https://github.com/test-org/test-repo/pull/100","author":{"login":"human-dev"},"body":"This PR fixes #42 by adding the missing component","title":"Add component","headRefName":"feat/component"}]'

run_test_stdout "closing-keyword-fixes-blocks" \
  "${PR_FIXES}" \
  "Skipping code agent" \
  0

# PR with "Resolves #N" in title → should block.
PR_RESOLVES_TITLE='[{"number":101,"url":"https://github.com/test-org/test-repo/pull/101","author":{"login":"human-dev"},"body":"Some unrelated body text","title":"Resolves #42: add truncated list","headRefName":"feat/truncated-list"}]'

run_test_stdout "closing-keyword-in-title-blocks" \
  "${PR_RESOLVES_TITLE}" \
  "Skipping code agent" \
  0

# PR with agent branch convention → should block even without closing keyword.
PR_AGENT_BRANCH='[{"number":200,"url":"https://github.com/test-org/test-repo/pull/200","author":{"login":"human-dev"},"body":"Automated fix","title":"Fix thing","headRefName":"agent/42-fix-thing"}]'

run_test_stdout "agent-branch-convention-blocks" \
  "${PR_AGENT_BRANCH}" \
  "Skipping code agent" \
  0

# PR matches but triage comment marks it as unrelated → should NOT block.
TRIAGE_UNRELATED="<!-- fullsend:triage-agent -->
## Triage Summary

PR #99 is unrelated to this issue — it is a PatternFly refactor.

The actual fix requires adding a new component."

run_test_stdout "triage-excludes-unrelated-pr" \
  "${PR_CLOSES}" \
  "No existing human PRs found" \
  0 \
  "" \
  "${TRIAGE_UNRELATED}"

# PR matches with closing keyword, triage marks a DIFFERENT PR as unrelated
# → should still block (only the mentioned PR is excluded).
PR_TWO='[{"number":99,"url":"https://github.com/test-org/test-repo/pull/99","author":{"login":"human-dev"},"body":"Closes #42","title":"Fix stuff","headRefName":"fix/stuff"},{"number":200,"url":"https://github.com/test-org/test-repo/pull/200","author":{"login":"other-dev"},"body":"Fixes #42","title":"Other fix","headRefName":"fix/other"}]'
TRIAGE_EXCLUDE_200="<!-- fullsend:triage-agent -->
## Triage Summary

PR #200 is unrelated to this issue."

run_test_stdout "triage-excludes-only-mentioned-pr" \
  "${PR_TWO}" \
  "Skipping code agent" \
  0 \
  "" \
  "${TRIAGE_EXCLUDE_200}"

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
