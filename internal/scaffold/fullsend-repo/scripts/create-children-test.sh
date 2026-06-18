#!/usr/bin/env bash
# create-children-test.sh — Test create-children.sh issue creation logic.
#
# Run from the repo root: bash internal/scaffold/fullsend-repo/scripts/create-children-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CREATE_SCRIPT="${SCRIPT_DIR}/create-children.sh"
FAILURES=0

TEST_TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TEST_TMPDIR}"' EXIT

GH_LOG="${TEST_TMPDIR}/gh-calls.log"
MOCK_BIN="${TEST_TMPDIR}/bin"
ISSUE_COUNTER_FILE="${TEST_TMPDIR}/issue-counter"
mkdir -p "${MOCK_BIN}"

echo "100" > "${ISSUE_COUNTER_FILE}"

cat > "${MOCK_BIN}/gh" <<MOCKEOF
#!/usr/bin/env bash
echo "gh \$*" >> "${GH_LOG}"

case "\$*" in
  *"label create"*)
    exit 0
    ;;
  *"issue create"*)
    cat > /dev/null  # consume --body-file stdin
    counter=\$(cat "${ISSUE_COUNTER_FILE}")
    counter=\$((counter + 1))
    echo "\$counter" > "${ISSUE_COUNTER_FILE}"
    echo "https://github.com/test-org/test-repo/issues/\${counter}"
    exit 0
    ;;
  *"repos/"*"/issues/"*"--jq"*)
    echo "I_node_id_123"
    exit 0
    ;;
  *"repos/"*"/issues/"*)
    echo '{"id": "I_node_id_123"}'
    exit 0
    ;;
  *"sub_issues"*)
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
export GITHUB_ISSUE_NUMBER="42"

FLAT_CHILDREN_FIXTURE='{
  "status": "complete",
  "children": [
    {"title": "Child A", "type": "story", "description": "First child", "acceptance_criteria": ["AC1"], "priority": "high", "estimated_scope": "S"},
    {"title": "Child B", "type": "task", "description": "Second child", "acceptance_criteria": ["AC2"], "priority": "medium", "estimated_scope": "M"}
  ]
}'

HIERARCHICAL_FIXTURE='{
  "status": "complete",
  "children": [
    {"title": "Epic Parent", "type": "epic", "description": "Parent epic", "acceptance_criteria": ["AC-E1"]},
    {"title": "Story under Epic", "type": "story", "parent_title": "Epic Parent", "description": "Child story", "acceptance_criteria": ["AC-S1"]},
    {"title": "Task under Story", "type": "task", "parent_title": "Story under Epic", "description": "Grandchild task", "acceptance_criteria": ["AC-T1"]}
  ]
}'

ORPHAN_FIXTURE='{
  "status": "complete",
  "children": [
    {"title": "Good Child", "type": "story", "description": "Has no parent ref", "acceptance_criteria": ["AC1"]},
    {"title": "Orphan", "type": "task", "parent_title": "Nonexistent Parent", "description": "Bad ref", "acceptance_criteria": ["AC2"]}
  ]
}'

run_test() {
  local test_name="$1"
  local fixture="$2"
  local expect_failure="${3:-false}"

  local result_file="${TEST_TMPDIR}/result-${test_name}.json"
  echo "${fixture}" > "${result_file}"

  echo "100" > "${ISSUE_COUNTER_FILE}"
  : > "${GH_LOG}"

  local exit_code=0
  RESULT_FILE="${result_file}" bash "${CREATE_SCRIPT}" > "${TEST_TMPDIR}/stdout-${test_name}.log" 2>&1 || exit_code=$?

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

assert_stdout_contains() {
  local test_name="$1" pattern="$2"
  if ! grep -qF "${pattern}" "${TEST_TMPDIR}/stdout-${test_name}.log"; then
    echo "FAIL: ${test_name} — expected stdout containing '${pattern}'"
    FAILURES=$((FAILURES + 1))
  fi
}

count_gh_calls() {
  local pattern="$1"
  grep -cF "${pattern}" "${GH_LOG}" 2>/dev/null || echo "0"
}

# --- Tests ---

# 1. Flat children — 2 issues created under root parent
run_test "flat-children" "$FLAT_CHILDREN_FIXTURE"
ISSUE_CREATES=$(count_gh_calls "issue create")
if [[ "$ISSUE_CREATES" == "2" ]]; then
  echo "PASS: flat-children created 2 issues"
else
  echo "FAIL: flat-children — expected 2 issue creates, got ${ISSUE_CREATES}"
  FAILURES=$((FAILURES + 1))
fi

# 2. Hierarchical children — topological ordering
run_test "hierarchical" "$HIERARCHICAL_FIXTURE"
assert_stdout_contains "hierarchical" "Created"
ISSUE_CREATES=$(count_gh_calls "issue create")
if [[ "$ISSUE_CREATES" == "3" ]]; then
  echo "PASS: hierarchical created 3 issues"
else
  echo "FAIL: hierarchical — expected 3 issue creates, got ${ISSUE_CREATES}"
  FAILURES=$((FAILURES + 1))
fi

# 3. Orphan fallback — unresolvable parent_title falls back to root
run_test "orphan-fallback" "$ORPHAN_FIXTURE"
assert_stdout_contains "orphan-fallback" "orphan"
ISSUE_CREATES=$(count_gh_calls "issue create")
if [[ "$ISSUE_CREATES" == "2" ]]; then
  echo "PASS: orphan-fallback created 2 issues"
else
  echo "FAIL: orphan-fallback — expected 2 issue creates, got ${ISSUE_CREATES}"
  FAILURES=$((FAILURES + 1))
fi

# 4. Missing RESULT_FILE
unset RESULT_FILE
run_test "missing-result-file" "unused" "true"
export RESULT_FILE="/nonexistent"
run_test "nonexistent-result-file" "unused" "true"

# 5. Invalid JSON
run_test "invalid-json" "not valid json" "true"

if [[ ${FAILURES} -gt 0 ]]; then
  echo ""
  echo "${FAILURES} test(s) failed."
  exit 1
fi

echo ""
echo "All create-children tests passed."
