#!/usr/bin/env bash
# post-explore-test.sh — Test post-explore.sh with fixture JSON and mock gh.
#
# Run from the repo root: bash internal/scaffold/fullsend-repo/scripts/post-explore-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POST_SCRIPT="${SCRIPT_DIR}/post-explore.sh"
FAILURES=0

TEST_TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TEST_TMPDIR}"' EXIT

GH_LOG="${TEST_TMPDIR}/gh-calls.log"
MOCK_BIN="${TEST_TMPDIR}/bin"
mkdir -p "${MOCK_BIN}"

cat > "${MOCK_BIN}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
echo "gh $*" >> "$GH_LOG"

case "$*" in
  *"workflow run"*)
    exit 0
    ;;
  *"issue comment"*)
    cat > /dev/null  # consume stdin
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
  # Handle _pe_now_ms calls from pipeline-events.sh
  if [[ "${2:-}" == *"time.time"* ]]; then
    echo "1000000"
    exit 0
  fi
  # Handle get_traceparent json.load
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
export GITHUB_RUN_ID="12345"
export GITHUB_ISSUE_NUMBER="42"
export GITHUB_WORKSPACE="${TEST_TMPDIR}"

EXPLORE_FIXTURE='{
  "confidence": {"overall": 85},
  "gaps": ["gap1"],
  "related_work": [{"title": "PR #1"}, {"title": "PR #2"}],
  "summary": "Test exploration summary.",
  "technical_landscape": {"languages": ["Go"]}
}'

run_test() {
  local test_name="$1"
  local fixture="${2:-$EXPLORE_FIXTURE}"
  local expect_failure="${3:-false}"

  local run_dir="${TEST_TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${fixture}" > "${run_dir}/iteration-1/output/agent-result.json"

  : > "${GH_LOG}"

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

# --- Tests ---

# Happy path: exploration completes and chains refine
run_test "happy-path"
assert_gh_called "happy-path" "workflow run refine.yml"
assert_gh_called "happy-path" "issue_key=42"
assert_gh_called "happy-path" "explore_run_id=12345"
if [[ -f "/tmp/workspace/exploration_context.json" ]]; then
  echo "PASS: happy-path exploration_context.json saved"
  rm -f "/tmp/workspace/exploration_context.json"
else
  echo "FAIL: happy-path — exploration_context.json not saved to /tmp/workspace/"
  FAILURES=$((FAILURES + 1))
fi

# Auto-create propagation
export AUTO_CREATE="true"
run_test "auto-create-propagation"
assert_gh_called "auto-create-propagation" "auto_create=true"
unset AUTO_CREATE

# Missing agent result — run from empty dir with no iteration-*/output/
test_name="missing-result"
run_dir="${TEST_TMPDIR}/run-${test_name}"
mkdir -p "${run_dir}"
: > "${GH_LOG}"
exit_code=0
(cd "${run_dir}" && bash "${POST_SCRIPT}") > "${TEST_TMPDIR}/stdout-${test_name}.log" 2>&1 || exit_code=$?
if [[ ${exit_code} -ne 0 ]]; then
  echo "PASS: ${test_name} (expected failure)"
else
  echo "FAIL: ${test_name} — expected failure but got success"
  FAILURES=$((FAILURES + 1))
fi

# Invalid JSON result
run_test "invalid-json" "not valid json" "true"

if [[ ${FAILURES} -gt 0 ]]; then
  echo ""
  echo "${FAILURES} test(s) failed."
  exit 1
fi

echo ""
echo "All post-explore tests passed."
