#!/usr/bin/env bash
# post-critique-test.sh — Test post-critique.sh verdict routing and iteration control.
#
# Run from the repo root: bash internal/scaffold/fullsend-repo/scripts/post-critique-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POST_SCRIPT="${SCRIPT_DIR}/post-critique.sh"
FAILURES=0

TEST_TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TEST_TMPDIR}" /tmp/workspace/critique-history.json /tmp/workspace/critique-feedback.json /tmp/workspace/refine-result.json' EXIT

GH_LOG="${TEST_TMPDIR}/gh-calls.log"
MOCK_BIN="${TEST_TMPDIR}/bin"
mkdir -p "${MOCK_BIN}"

cat > "${MOCK_BIN}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
echo "gh $*" >> "$GH_LOG"

case "$*" in
  *"issue comment"*)
    cat > /dev/null
    exit 0
    ;;
  *"workflow run"*)
    exit 0
    ;;
  *"api"*)
    exit 0
    ;;
esac
exit 0
MOCKEOF
chmod +x "${MOCK_BIN}/gh"

cat > "${MOCK_BIN}/python3" <<'MOCKEOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "-c" ]]; then
  if [[ "${2:-}" == *"time.time"* ]]; then
    echo "1000000"
    exit 0
  fi
  echo ""
  exit 0
fi
exec /usr/bin/python3 "$@"
MOCKEOF
chmod +x "${MOCK_BIN}/python3"

export PATH="${MOCK_BIN}:${PATH}"
export GH_LOG="${GH_LOG}"
export GH_TOKEN="fake-token"
export ISSUE_KEY="42"
export ISSUE_SOURCE="github"
export REPO_FULL_NAME="test-org/test-repo"
export GITHUB_REPOSITORY="test-org/.fullsend"
export GITHUB_RUN_ID="99999"
export GITHUB_ISSUE_NUMBER="42"
export GITHUB_WORKSPACE="${TEST_TMPDIR}"
export REFINE_RUN_ID="88888"

APPROVED_FIXTURE='{
  "verdict": "approved",
  "comment": "Plan looks good.",
  "assessment": {"overall": 90},
  "revisions": []
}'

REVISE_FIXTURE='{
  "verdict": "revise",
  "comment": "Needs adjustments.",
  "assessment": {"overall": 55},
  "revisions": [{"type": "revise", "target": "Child 1", "reason": "Missing AC"}]
}'

NEEDS_INPUT_FIXTURE='{
  "verdict": "needs_input",
  "comment": "Cannot evaluate without clarification.",
  "assessment": {"overall": 40},
  "question": {"dimension": "scope", "text": "What is the target platform?", "impact": "Determines child decomposition"}
}'

UNKNOWN_FIXTURE='{
  "verdict": "banana",
  "comment": "This should fail.",
  "assessment": {"overall": 0}
}'

# Pre-populate refine result for approved path
mkdir -p /tmp/workspace
echo '{"children": [{"title": "c1"}, {"title": "c2"}]}' > /tmp/workspace/refine-result.json

run_test() {
  local test_name="$1"
  local fixture="$2"
  local extra_env="$3"
  local expect_failure="${4:-false}"

  local run_dir="${TEST_TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${fixture}" > "${run_dir}/iteration-1/output/agent-result.json"

  # Clean up workspace state between tests
  rm -f /tmp/workspace/critique-history.json
  rm -f /tmp/workspace/critique-feedback.json

  : > "${GH_LOG}"

  local exit_code=0
  (cd "${run_dir}" && eval "${extra_env}" bash "${POST_SCRIPT}") > "${TEST_TMPDIR}/stdout-${test_name}.log" 2>&1 || exit_code=$?

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

  echo "PASS: ${test_name}"
}

assert_gh_called() {
  local test_name="$1" pattern="$2"
  if ! grep -qF "${pattern}" "${GH_LOG}"; then
    echo "FAIL: ${test_name} — expected gh call matching '${pattern}'"
    cat "${GH_LOG}"
    FAILURES=$((FAILURES + 1))
  fi
}

assert_gh_not_called() {
  local test_name="$1" pattern="$2"
  if grep -qF "${pattern}" "${GH_LOG}"; then
    echo "FAIL: ${test_name} — unexpected gh call matching '${pattern}'"
    cat "${GH_LOG}"
    FAILURES=$((FAILURES + 1))
  fi
}

assert_stdout_contains() {
  local test_name="$1" pattern="$2"
  if ! grep -qF "${pattern}" "${TEST_TMPDIR}/stdout-${test_name}.log"; then
    echo "FAIL: ${test_name} — expected stdout containing '${pattern}'"
    FAILURES=$((FAILURES + 1))
  fi
}

# --- Tests ---

# 1. Approved verdict with auto-create disabled (default) — adds label
run_test "approved-no-auto-create" "$APPROVED_FIXTURE" "REVIEW_ROUND=1 MAX_REVIEW_ROUNDS=3 AUTO_CREATE=false"
assert_gh_called "approved-no-auto-create" "issue comment"
assert_gh_called "approved-no-auto-create" "refine-approved"
assert_gh_not_called "approved-no-auto-create" "workflow run"
assert_stdout_contains "approved-no-auto-create" "Post-critique complete"

# 2. Revise verdict under limit — chains back to refine
run_test "revise-under-limit" "$REVISE_FIXTURE" "REVIEW_ROUND=1 MAX_REVIEW_ROUNDS=3"
assert_gh_called "revise-under-limit" "workflow run refine.yml"
assert_gh_called "revise-under-limit" "review_round=2"
assert_gh_called "revise-under-limit" "issue comment"
assert_stdout_contains "revise-under-limit" "Post-critique complete"

# 3. Revise at max rounds — escalates to human
run_test "revise-max-rounds" "$REVISE_FIXTURE" "REVIEW_ROUND=3 MAX_REVIEW_ROUNDS=3"
assert_gh_called "revise-max-rounds" "refine-needs-human"
assert_gh_called "revise-max-rounds" "refine-approved"
assert_gh_not_called "revise-max-rounds" "workflow run refine.yml"
assert_stdout_contains "revise-max-rounds" "Post-critique complete"

# 4. Needs input — posts question and adds label
run_test "needs-input" "$NEEDS_INPUT_FIXTURE" "REVIEW_ROUND=1 MAX_REVIEW_ROUNDS=3"
assert_gh_called "needs-input" "refine-needs-input"
assert_gh_called "needs-input" "issue comment"
assert_gh_not_called "needs-input" "workflow run"
assert_stdout_contains "needs-input" "Post-critique complete"

# 5. Unknown verdict — should fail
run_test "unknown-verdict" "$UNKNOWN_FIXTURE" "REVIEW_ROUND=1 MAX_REVIEW_ROUNDS=3" "true"

# 6. Critique history accumulates across rounds
run_test "history-round-1" "$REVISE_FIXTURE" "REVIEW_ROUND=1 MAX_REVIEW_ROUNDS=3"
if [[ -f /tmp/workspace/critique-history.json ]]; then
  HISTORY_ROUNDS=$(jq '.rounds | length' /tmp/workspace/critique-history.json)
  if [[ "$HISTORY_ROUNDS" == "1" ]]; then
    echo "PASS: history-accumulation"
  else
    echo "FAIL: history-accumulation — expected 1 round in history, got ${HISTORY_ROUNDS}"
    FAILURES=$((FAILURES + 1))
  fi
else
  echo "FAIL: history-accumulation — critique-history.json not found"
  FAILURES=$((FAILURES + 1))
fi

if [[ ${FAILURES} -gt 0 ]]; then
  echo ""
  echo "${FAILURES} test(s) failed."
  exit 1
fi

echo ""
echo "All post-critique tests passed."
