#!/usr/bin/env bash
# post-review-test.sh — Test the outcome-label logic in post-review.sh.
#
# Extracts and tests the label-application logic in isolation using shell
# functions. This avoids needing a live GitHub API or fullsend CLI.
#
# Run from the repo root:
#   bash internal/scaffold/fullsend-repo/scripts/post-review-test.sh

set -euo pipefail

FAILURES=0

# ---------------------------------------------------------------------------
# Test helper — reimplements the outcome-label logic from post-review.sh
# so we can test it without network access.
#
# Arguments:
#   $1 — ACTION (the original action from agent-result.json)
#   $2 — DOWNGRADED ("true" or "false")
#
# Prints the label that would be applied, or "none" if no label.
# ---------------------------------------------------------------------------
determine_outcome_label() {
  local action="$1"
  local downgraded="$2"
  local is_draft="${3:-false}"

  if [ "${action}" = "approve" ] && [ "${downgraded}" = "false" ] && [ "${is_draft}" != "true" ]; then
    echo "ready-for-merge"
  elif [ "${action}" = "approve" ] && { [ "${downgraded}" = "true" ] || [ "${is_draft}" = "true" ]; }; then
    echo "requires-manual-review"
  elif [ "${action}" = "comment" ]; then
    echo "requires-manual-review"
  elif [ "${action}" = "request_changes" ]; then
    echo "none"
  elif [ "${action}" = "reject" ]; then
    echo "rejected"
  else
    echo "none"
  fi
}

run_test() {
  local test_name="$1"
  local action="$2"
  local downgraded="$3"
  local expected="$4"
  local is_draft="${5:-false}"

  local actual
  actual="$(determine_outcome_label "${action}" "${downgraded}" "${is_draft}")"

  if [ "${actual}" != "${expected}" ]; then
    echo "FAIL: ${test_name}"
    echo "  action:     '${action}'"
    echo "  downgraded: '${downgraded}'"
    echo "  expected:   '${expected}'"
    echo "  actual:     '${actual}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Test cases ---

# Approve without protected-path downgrade → ready-for-merge
run_test "approve-no-downgrade" \
  "approve" "false" "ready-for-merge"

# Approve with protected-path downgrade → requires-manual-review
run_test "approve-with-downgrade" \
  "approve" "true" "requires-manual-review"

# Comment (split/conflicting review) → requires-manual-review
run_test "comment-split-review" \
  "comment" "false" "requires-manual-review"

# request_changes → no outcome label
run_test "request-changes-no-label" \
  "request_changes" "false" "none"

# reject → rejected
run_test "reject-label" \
  "reject" "false" "rejected"

# Defensive: comment + downgraded=true can't occur in production (DOWNGRADED is
# only set inside the approve branch), but verify the label logic handles it.
run_test "comment-with-downgrade-flag" \
  "comment" "true" "requires-manual-review"

# Edge cases: ensure unknown/empty actions produce no label
run_test "empty-action-no-label" \
  "" "false" "none"

run_test "failure-action-no-label" \
  "failure" "false" "none"

run_test "unknown-action-no-label" \
  "banana" "false" "none"

# Draft PR: approve without downgrade on a draft → requires-manual-review
run_test "approve-no-downgrade-draft" \
  "approve" "false" "requires-manual-review" "true"

# Draft PR: approve with downgrade on a draft → requires-manual-review
run_test "approve-with-downgrade-draft" \
  "approve" "true" "requires-manual-review" "true"

# Non-draft: explicit false behaves like omitting the argument
run_test "approve-no-downgrade-not-draft" \
  "approve" "false" "ready-for-merge" "false"

# ---------------------------------------------------------------------------
# Severity-threshold filtering logic
# Mirrors severity_rank() in post-review.sh — keep in sync
# ---------------------------------------------------------------------------

severity_rank() {
  case "$1" in
    info)     echo 0 ;;
    low)      echo 1 ;;
    medium)   echo 2 ;;
    high)     echo 3 ;;
    critical) echo 4 ;;
    *)        echo 1 ;;
  esac
}

filter_findings_json() {
  local result_json="$1"
  local threshold="$2"
  local threshold_rank
  threshold_rank=$(severity_rank "$threshold")

  echo "$result_json" | jq --argjson rank "$threshold_rank" '
    if .findings then
      .findings |= [.[] | select(
        (if .severity == "info" then 0
         elif .severity == "low" then 1
         elif .severity == "medium" then 2
         elif .severity == "high" then 3
         elif .severity == "critical" then 4
         else 1 end) >= $rank
      )]
    else . end
  '
}

run_filter_test() {
  local test_name="$1"
  local input_json="$2"
  local threshold="$3"
  local expected_count="$4"

  local filtered
  filtered="$(filter_findings_json "$input_json" "$threshold")"
  local actual_count
  actual_count="$(echo "$filtered" | jq 'if .findings then (.findings | length) else -1 end')"

  if [ "${actual_count}" != "${expected_count}" ]; then
    echo "FAIL: ${test_name}"
    echo "  threshold:      '${threshold}'"
    echo "  expected count: '${expected_count}'"
    echo "  actual count:   '${actual_count}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Severity filter test cases ---

MIXED_FINDINGS='{"action":"request-changes","findings":[
  {"severity":"info","category":"style","file":"a.go","description":"x"},
  {"severity":"low","category":"style","file":"b.go","description":"y"},
  {"severity":"medium","category":"bug","file":"c.go","description":"z"},
  {"severity":"high","category":"security","file":"d.go","description":"w"},
  {"severity":"critical","category":"security","file":"e.go","description":"v"}
]}'

run_filter_test "threshold-low-drops-info" \
  "$MIXED_FINDINGS" "low" "4"

run_filter_test "threshold-medium-drops-low-and-info" \
  "$MIXED_FINDINGS" "medium" "3"

run_filter_test "threshold-high" \
  "$MIXED_FINDINGS" "high" "2"

run_filter_test "threshold-critical" \
  "$MIXED_FINDINGS" "critical" "1"

run_filter_test "threshold-info-keeps-all" \
  "$MIXED_FINDINGS" "info" "5"

NO_FINDINGS='{"action":"approve"}'
run_filter_test "no-findings-key-passthrough" \
  "$NO_FINDINGS" "low" "-1"

# ---------------------------------------------------------------------------
# Verdict-downgrade tests: when filtering empties all findings, the action
# must be downgraded from request-changes/reject to comment with findings
# key removed.
# Mirrors filter + downgrade logic in post-review.sh — keep in sync
# ---------------------------------------------------------------------------

filter_and_downgrade() {
  local result_json="$1"
  local threshold="$2"

  local filtered
  filtered="$(filter_findings_json "$result_json" "$threshold")"
  local count
  count="$(echo "$filtered" | jq 'if .findings then (.findings | length) else -1 end')"

  if [ "$count" -eq 0 ]; then
    local action
    action="$(echo "$filtered" | jq -r '.action')"
    if [ "$action" = "request-changes" ] || [ "$action" = "reject" ]; then
      echo "$filtered" | jq 'del(.findings) | .action = "comment"'
      return
    fi
    # For approve/comment, just remove the empty findings array
    echo "$filtered" | jq 'del(.findings)'
    return
  fi
  echo "$filtered"
}

run_downgrade_test() {
  local test_name="$1"
  local input_json="$2"
  local threshold="$3"
  local expected_action="$4"
  local expected_has_findings="$5"

  local result
  result="$(filter_and_downgrade "$input_json" "$threshold")"
  local actual_action
  actual_action="$(echo "$result" | jq -r '.action')"
  local has_findings
  has_findings="$(echo "$result" | jq 'has("findings")')"

  if [ "$actual_action" != "$expected_action" ] || [ "$has_findings" != "$expected_has_findings" ]; then
    echo "FAIL: ${test_name}"
    echo "  expected action:       '${expected_action}'"
    echo "  actual action:         '${actual_action}'"
    echo "  expected has_findings: '${expected_has_findings}'"
    echo "  actual has_findings:   '${has_findings}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# All findings are info-level; threshold=low removes them all → downgrade
ALL_INFO='{"action":"request-changes","findings":[
  {"severity":"info","category":"style","file":"a.go","description":"x"},
  {"severity":"info","category":"style","file":"b.go","description":"y"}
]}'

run_downgrade_test "request-changes-all-filtered-downgrade" \
  "$ALL_INFO" "low" "comment" "false"

# Same scenario with reject action
ALL_INFO_REJECT='{"action":"reject","findings":[
  {"severity":"info","category":"style","file":"a.go","description":"x"}
]}'

run_downgrade_test "reject-all-filtered-downgrade" \
  "$ALL_INFO_REJECT" "low" "comment" "false"

# Partial filtering: some findings remain → no downgrade
run_downgrade_test "request-changes-partial-filter-no-downgrade" \
  "$MIXED_FINDINGS" "medium" "request-changes" "true"

# comment with all findings filtered → action stays comment, findings removed
COMMENT_ALL_INFO='{"action":"comment","body":"text","head_sha":"abc123","findings":[
  {"severity":"info","category":"style","file":"a.go","description":"x"}
]}'
run_downgrade_test "comment-all-filtered-removes-findings" \
  "$COMMENT_ALL_INFO" "low" "comment" "false"

# approve with all findings filtered → action stays approve, findings removed
APPROVE_ALL_INFO='{"action":"approve","body":"LGTM","head_sha":"abc123","findings":[
  {"severity":"info","category":"style","file":"a.go","description":"x"}
]}'
run_downgrade_test "approve-all-filtered-removes-findings" \
  "$APPROVE_ALL_INFO" "low" "approve" "false"

# ---------------------------------------------------------------------------
# Control-label guard tests
# ---------------------------------------------------------------------------

REVIEW_CONTROL_LABELS=(
  "ready-for-merge" "requires-manual-review" "rejected"
  "ready-for-review" "fullsend-no-fix" "fullsend-fix"
)

is_control_label() {
  local label="$1"
  for cl in "${REVIEW_CONTROL_LABELS[@]}"; do
    if [[ "${cl}" == "${label}" ]]; then
      return 0
    fi
  done
  return 1
}

run_control_label_test() {
  local test_name="$1"
  local label="$2"
  local expected_control="$3"

  if is_control_label "${label}"; then
    local actual="true"
  else
    local actual="false"
  fi

  if [ "${actual}" != "${expected_control}" ]; then
    echo "FAIL: ${test_name}"
    echo "  label:    '${label}'"
    echo "  expected: '${expected_control}'"
    echo "  actual:   '${actual}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# Control labels should be recognized
run_control_label_test "ready-for-merge-is-control" "ready-for-merge" "true"
run_control_label_test "requires-manual-review-is-control" "requires-manual-review" "true"
run_control_label_test "rejected-is-control" "rejected" "true"
run_control_label_test "ready-for-review-is-control" "ready-for-review" "true"
run_control_label_test "fullsend-no-fix-is-control" "fullsend-no-fix" "true"
run_control_label_test "fullsend-fix-is-control" "fullsend-fix" "true"

# Non-control labels should NOT be recognized
run_control_label_test "area-api-not-control" "area/api" "false"
run_control_label_test "priority-high-not-control" "priority/high" "false"
run_control_label_test "bug-not-control" "bug" "false"
run_control_label_test "empty-not-control" "" "false"

# ---------------------------------------------------------------------------
# Integration tests for label_actions processing
# ---------------------------------------------------------------------------
# These tests run the full post-review.sh with mock gh/fullsend binaries
# to verify label_actions validation, body modification, and API calls.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POST_SCRIPT="${SCRIPT_DIR}/post-review.sh"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

GH_LOG="${TMPDIR}/gh-calls.log"
MOCK_BIN="${TMPDIR}/bin"
mkdir -p "${MOCK_BIN}"

cat > "${MOCK_BIN}/gh" <<MOCKEOF
#!/usr/bin/env bash
# Mock gh: handle specific subcommands, log everything else.

# gh pr view ... --json state,isDraft ... → JSON with OPEN + isDraft
if [[ "\$1" == "pr" ]] && [[ "\$2" == "view" ]] && [[ "\$*" == *"--json state"* ]]; then
  echo "{\"state\":\"OPEN\",\"isDraft\":\${MOCK_IS_DRAFT:-false}}"
  exit 0
fi

# gh pr view ... --json files ... → no protected files
if [[ "\$1" == "pr" ]] && [[ "\$2" == "view" ]] && [[ "\$*" == *"--json files"* ]]; then
  echo "src/main.go"
  exit 0
fi

# gh api repos/.../labels --paginate (list repo labels)
if [[ "\$1" == "api" ]] && [[ "\$2" == *"/labels" ]] && [[ "\$*" == *"--paginate"* ]] && [[ "\$*" != *"-f "* ]] && [[ "\$*" != *"-X "* ]]; then
  printf '%s\n' "area/api" "area/cli" "priority/high" "component/parser"
  exit 0
fi

# Log all other calls
echo "gh \$*" >> "${GH_LOG}"
MOCKEOF
chmod +x "${MOCK_BIN}/gh"

cat > "${MOCK_BIN}/fullsend" <<MOCKEOF
#!/usr/bin/env bash
# Mock fullsend: log the call, consume stdin if --result - is used.
BODY=""
PREV=""
for arg in "\$@"; do
  if [[ "\${arg}" == "-" ]] && [[ "\${PREV}" == "--result" ]]; then
    BODY=\$(cat)
  fi
  PREV="\${arg}"
done
echo "fullsend \$*" >> "${GH_LOG}"
MOCKEOF
chmod +x "${MOCK_BIN}/fullsend"

run_label_test() {
  local test_name="$1"
  local json_content="$2"
  local expected_pattern="$3"

  local run_dir="${TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${json_content}" > "${run_dir}/iteration-1/output/agent-result.json"
  : > "${GH_LOG}"

  local exit_code=0
  # shellcheck disable=SC2030
  (
    cd "${run_dir}"
    export PATH="${MOCK_BIN}:${PATH}"
    export REVIEW_TOKEN="fake-token"
    export PR_NUMBER="99"
    export REPO_FULL_NAME="test-org/test-repo"
    bash "${POST_SCRIPT}"
  ) > "${TMPDIR}/stdout-${test_name}.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TMPDIR}/stdout-${test_name}.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if ! grep -qF -- "${expected_pattern}" "${GH_LOG}"; then
    echo "FAIL: ${test_name} — expected pattern '${expected_pattern}' not found in gh calls"
    echo "Actual calls:"
    cat "${GH_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

run_label_test_stdout() {
  local test_name="$1"
  local json_content="$2"
  local expected_stdout="$3"

  local run_dir="${TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${json_content}" > "${run_dir}/iteration-1/output/agent-result.json"
  : > "${GH_LOG}"

  local exit_code=0
  # shellcheck disable=SC2030,SC2031
  (
    cd "${run_dir}"
    export PATH="${MOCK_BIN}:${PATH}"
    export REVIEW_TOKEN="fake-token"
    export PR_NUMBER="99"
    export REPO_FULL_NAME="test-org/test-repo"
    bash "${POST_SCRIPT}"
  ) > "${TMPDIR}/stdout-${test_name}.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TMPDIR}/stdout-${test_name}.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if ! grep -qF -- "${expected_stdout}" "${TMPDIR}/stdout-${test_name}.log"; then
    echo "FAIL: ${test_name} — expected stdout '${expected_stdout}' not found"
    echo "Actual stdout:"
    cat "${TMPDIR}/stdout-${test_name}.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

run_label_test_no_pattern() {
  local test_name="$1"
  local json_content="$2"
  local forbidden_pattern="$3"

  local run_dir="${TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${json_content}" > "${run_dir}/iteration-1/output/agent-result.json"
  : > "${GH_LOG}"

  local exit_code=0
  # shellcheck disable=SC2030,SC2031
  (
    cd "${run_dir}"
    export PATH="${MOCK_BIN}:${PATH}"
    export REVIEW_TOKEN="fake-token"
    export PR_NUMBER="99"
    export REPO_FULL_NAME="test-org/test-repo"
    bash "${POST_SCRIPT}"
  ) > "${TMPDIR}/stdout-${test_name}.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TMPDIR}/stdout-${test_name}.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if grep -qF -- "${forbidden_pattern}" "${GH_LOG}"; then
    echo "FAIL: ${test_name} — forbidden pattern '${forbidden_pattern}' was found in gh calls"
    echo "Actual calls:"
    cat "${GH_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Label actions integration tests ---

# Approve with label_actions — label should be added via API
run_label_test "label-actions-applied" \
  '{"action":"approve","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"LGTM","label_actions":{"reason":"PR modifies API surface.","actions":[{"action":"add","label":"area/api"}]}}' \
  "gh api repos/test-org/test-repo/issues/99/labels -f labels[]=area/api --silent"

# Control label refused — should NOT call the labels API for it
run_label_test_stdout "label-actions-control-label-refused" \
  '{"action":"approve","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"LGTM","label_actions":{"reason":"Tried to set control label.","actions":[{"action":"add","label":"ready-for-merge"}]}}' \
  "::warning::Refused to add control label 'ready-for-merge'"

# Non-existent label skipped — label "bug" is not in mock label list
run_label_test_stdout "label-actions-nonexistent-label-skipped" \
  '{"action":"approve","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"LGTM","label_actions":{"reason":"Agent recommended a label that does not exist.","actions":[{"action":"add","label":"bug"}]}}' \
  "::warning::Skipping label 'bug'"

# Invalid characters refused
run_label_test_stdout "label-actions-invalid-characters-refused" \
  '{"action":"approve","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"LGTM","label_actions":{"reason":"Injection attempt.","actions":[{"action":"add","label":"label;injection"}]}}' \
  "::warning::Refused label 'label;injection'"

# Remove label — should call DELETE
run_label_test "label-actions-remove" \
  '{"action":"approve","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"LGTM","label_actions":{"reason":"Stale area label removed.","actions":[{"action":"remove","label":"area/cli"}]}}' \
  "gh api repos/test-org/test-repo/issues/99/labels/area%2Fcli -X DELETE --silent"

# Multiple adds — both should be applied
run_label_test "label-actions-multiple-add" \
  '{"action":"approve","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"LGTM","label_actions":{"reason":"Multiple labels apply.","actions":[{"action":"add","label":"area/api"},{"action":"add","label":"priority/high"}]}}' \
  "gh api repos/test-org/test-repo/issues/99/labels -f labels[]=area/api --silent"

run_label_test "label-actions-multiple-second-label" \
  '{"action":"approve","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"LGTM","label_actions":{"reason":"Multiple labels apply.","actions":[{"action":"add","label":"area/api"},{"action":"add","label":"priority/high"}]}}' \
  "gh api repos/test-org/test-repo/issues/99/labels -f labels[]=priority/high --silent"

# When all label actions are refused, reason should NOT appear in the review body
run_label_test_no_pattern "label-actions-all-refused-no-body-append" \
  '{"action":"approve","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"LGTM","label_actions":{"reason":"Should not appear.","actions":[{"action":"add","label":"ready-for-merge"}]}}' \
  "labels[]=ready-for-merge"

# No label_actions field — should still post review without errors
run_label_test "label-actions-absent-still-posts" \
  '{"action":"approve","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"LGTM"}' \
  "fullsend post-review"

# request-changes with label_actions — labels should still be applied
run_label_test "label-actions-with-request-changes" \
  '{"action":"request-changes","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"Issues found","findings":[{"severity":"high","category":"bug","file":"main.go","description":"nil deref"}],"label_actions":{"reason":"Touches CI config.","actions":[{"action":"add","label":"area/api"}]}}' \
  "gh api repos/test-org/test-repo/issues/99/labels -f labels[]=area/api --silent"

# Label with embedded newline (GHA command injection attempt) — should be refused
run_label_test_stdout "label-actions-newline-injection-refused" \
  '{"action":"approve","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"LGTM","label_actions":{"reason":"Injection.","actions":[{"action":"add","label":"ok\n::set-output name=x::pwned"}]}}' \
  "::warning::Refused label"

# Label with :: delimiter (GHA command injection attempt) — :: is sanitized to :,
# so the label becomes ":warning:injected" which passes the character regex but
# does not exist in the repo. The important thing is the :: is stripped.
run_label_test_stdout "label-actions-gha-delimiter-sanitized" \
  '{"action":"approve","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"LGTM","label_actions":{"reason":"Injection.","actions":[{"action":"add","label":"::warning::injected"}]}}' \
  "::warning::Skipping label ':warning:injected'"

# --- Severity filtering integration tests ---
# These invoke the real post-review.sh with REVIEW_FINDING_SEVERITY_THRESHOLD
# set to a non-default value, exercising the production severity_rank() and jq
# filter rather than the mirrored copies above.

run_label_test_with_env() {
  local test_name="$1"
  local json_content="$2"
  local expected_pattern="$3"
  local env_var="$4"
  local env_val="$5"

  local run_dir="${TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${json_content}" > "${run_dir}/iteration-1/output/agent-result.json"
  : > "${GH_LOG}"

  local exit_code=0
  # shellcheck disable=SC2030,SC2031
  (
    cd "${run_dir}"
    export PATH="${MOCK_BIN}:${PATH}"
    export REVIEW_TOKEN="fake-token"
    export PR_NUMBER="99"
    export REPO_FULL_NAME="test-org/test-repo"
    export "${env_var}=${env_val}"
    bash "${POST_SCRIPT}"
  ) > "${TMPDIR}/stdout-${test_name}.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TMPDIR}/stdout-${test_name}.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if ! grep -qF -- "${expected_pattern}" "${GH_LOG}"; then
    echo "FAIL: ${test_name} — expected pattern '${expected_pattern}' not found in gh calls"
    echo "Actual calls:"
    cat "${GH_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

run_label_test_with_env "severity-filter-downgrade-integration" \
  '{"action":"request-changes","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"Issues found","findings":[{"severity":"low","category":"style","file":"a.go","description":"minor"}]}' \
  "requires-manual-review" \
  "REVIEW_FINDING_SEVERITY_THRESHOLD" "medium"

# Verify stdout mentions the downgrade
run_label_test_with_env_stdout() {
  local test_name="$1"
  local json_content="$2"
  local expected_stdout="$3"
  local env_var="$4"
  local env_val="$5"

  local run_dir="${TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${json_content}" > "${run_dir}/iteration-1/output/agent-result.json"
  : > "${GH_LOG}"

  local exit_code=0
  # shellcheck disable=SC2030,SC2031
  (
    cd "${run_dir}"
    export PATH="${MOCK_BIN}:${PATH}"
    export REVIEW_TOKEN="fake-token"
    export PR_NUMBER="99"
    export REPO_FULL_NAME="test-org/test-repo"
    export "${env_var}=${env_val}"
    bash "${POST_SCRIPT}"
  ) > "${TMPDIR}/stdout-${test_name}.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TMPDIR}/stdout-${test_name}.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if ! grep -qF -- "${expected_stdout}" "${TMPDIR}/stdout-${test_name}.log"; then
    echo "FAIL: ${test_name} — expected stdout '${expected_stdout}' not found"
    echo "Actual stdout:"
    cat "${TMPDIR}/stdout-${test_name}.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

run_label_test_with_env_stdout "severity-filter-downgrade-log-message" \
  '{"action":"request-changes","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"Issues found","findings":[{"severity":"low","category":"style","file":"a.go","description":"minor"}]}' \
  "All findings removed by severity filter" \
  "REVIEW_FINDING_SEVERITY_THRESHOLD" "medium"

# --- Draft PR integration tests ---
# These invoke the real post-review.sh with MOCK_IS_DRAFT=true to verify that
# approve verdicts on draft PRs receive requires-manual-review instead of
# ready-for-merge.

# Draft PR with approve verdict → requires-manual-review (not ready-for-merge)
run_label_test_with_env "draft-pr-approve-gets-manual-review" \
  '{"action":"approve","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"LGTM"}' \
  "--add-label requires-manual-review" \
  "MOCK_IS_DRAFT" "true"

# Draft PR with approve verdict — verify stdout mentions draft suppression
run_label_test_with_env_stdout "draft-pr-approve-log-message" \
  '{"action":"approve","pr_number":99,"repo":"test-org/test-repo","head_sha":"abc123","body":"LGTM"}' \
  "Draft PR" \
  "MOCK_IS_DRAFT" "true"

# --- Summary ---

echo ""
if [ "${FAILURES}" -gt 0 ]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
