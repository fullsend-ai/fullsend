#!/usr/bin/env bash
# validate-output-schema-test.sh — Test validate-output-schema.sh with fixtures.
#
# Run from the repo root:
#   bash internal/scaffold/fullsend-repo/scripts/validate-output-schema-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VALIDATOR="${SCRIPT_DIR}/validate-output-schema.sh"
SCHEMA="${SCRIPT_DIR}/../schemas/triage-result.schema.json"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

run_test() {
  local test_name="$1"
  local json_content="$2"
  local expect_pass="$3"  # "true" or "false"
  local expect_output="${4:-}"  # optional: substring that must appear in stdout

  local test_dir="${TMPDIR}/${test_name}"
  mkdir -p "${test_dir}/output"
  echo "${json_content}" > "${test_dir}/output/agent-result.json"

  local exit_code=0
  FULLSEND_OUTPUT_SCHEMA="${SCHEMA}" \
    bash -c "cd '${test_dir}' && bash '${VALIDATOR}'" > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  local passed=true
  if [[ "${expect_pass}" == "true" && ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — expected PASS but got exit ${exit_code}"
    head -10 "${TMPDIR}/stdout.log"
    passed=false
  elif [[ "${expect_pass}" == "false" && ${exit_code} -eq 0 ]]; then
    echo "FAIL: ${test_name} — expected FAIL but got PASS"
    passed=false
  fi

  if [[ -n "${expect_output}" ]] && ! grep -qF "${expect_output}" "${TMPDIR}/stdout.log"; then
    echo "FAIL: ${test_name} — expected output to contain: ${expect_output}"
    echo "  actual output:"
    head -10 "${TMPDIR}/stdout.log"
    passed=false
  fi

  if [[ "${passed}" == "true" ]]; then
    echo "PASS: ${test_name}"
  else
    FAILURES=$((FAILURES + 1))
  fi
}

# --- Valid inputs ---

run_test "valid-insufficient" \
  '{"action":"insufficient","reasoning":"missing repro","clarity_scores":{"symptom":0.6,"cause":0.3,"reproduction":0.1,"impact":0.5,"overall":0.39},"comment":"Can you share repro steps?"}' \
  "true"

run_test "valid-sufficient" \
  '{"action":"sufficient","reasoning":"clear","clarity_scores":{"symptom":0.9,"cause":0.8,"reproduction":0.9,"impact":0.7,"overall":0.85},"triage_summary":{"title":"Bug","severity":"high","category":"bug","problem":"crash","root_cause_hypothesis":"null ptr","reproduction_steps":["step 1"],"impact":"all users","recommended_fix":"fix ptr","proposed_test_case":"test_fix"},"comment":"Triage complete."}' \
  "true"

run_test "valid-duplicate" \
  '{"action":"duplicate","reasoning":"same as #10","duplicate_of":10,"comment":"Duplicate of #10."}' \
  "true"

run_test "valid-blocked-issue" \
  '{"action":"blocked","reasoning":"upstream dependency","blocked_by":"https://github.com/org/repo/issues/99","comment":"Blocked on upstream."}' \
  "true"

run_test "valid-blocked-pr" \
  '{"action":"blocked","reasoning":"waiting on PR","blocked_by":"https://github.com/org/repo/pull/55","comment":"Blocked on a PR."}' \
  "true"

# --- Conditional requirement failures ---

run_test "insufficient-missing-clarity-scores" \
  '{"action":"insufficient","reasoning":"missing info","comment":"Need more info."}' \
  "false"

run_test "duplicate-missing-duplicate-of" \
  '{"action":"duplicate","reasoning":"dupe","comment":"Duplicate."}' \
  "false"

run_test "sufficient-missing-triage-summary" \
  '{"action":"sufficient","reasoning":"ok","clarity_scores":{"symptom":0.9,"cause":0.8,"reproduction":0.9,"impact":0.7,"overall":0.85},"comment":"Done."}' \
  "false"

run_test "blocked-missing-blocked-by" \
  '{"action":"blocked","reasoning":"upstream dependency","comment":"Blocked."}' \
  "false"

run_test "blocked-malformed-url" \
  '{"action":"blocked","reasoning":"upstream dependency","blocked_by":"not-a-url","comment":"Blocked."}' \
  "false"

# --- FULLSEND_OUTPUT_FILE override ---

run_test_custom_filename() {
  local test_name="$1"
  local json_content="$2"
  local output_file="$3"
  local schema="$4"
  local expect_pass="$5"
  local expect_output="${6:-}"  # optional: substring that must appear in stdout

  local test_dir="${TMPDIR}/${test_name}"
  mkdir -p "${test_dir}/output"
  echo "${json_content}" > "${test_dir}/output/$(basename "${output_file}")"

  local exit_code=0
  FULLSEND_OUTPUT_SCHEMA="${schema}" FULLSEND_OUTPUT_FILE="${output_file}" \
    bash -c "cd '${test_dir}' && bash '${VALIDATOR}'" > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  local passed=true
  if [[ "${expect_pass}" == "true" && ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — expected PASS but got exit ${exit_code}"
    head -10 "${TMPDIR}/stdout.log"
    passed=false
  elif [[ "${expect_pass}" == "false" && ${exit_code} -eq 0 ]]; then
    echo "FAIL: ${test_name} — expected FAIL but got PASS"
    passed=false
  fi

  if [[ -n "${expect_output}" ]] && ! grep -qF "${expect_output}" "${TMPDIR}/stdout.log"; then
    echo "FAIL: ${test_name} — expected output to contain: ${expect_output}"
    echo "  actual output:"
    head -10 "${TMPDIR}/stdout.log"
    passed=false
  fi

  if [[ "${passed}" == "true" ]]; then
    echo "PASS: ${test_name}"
  else
    FAILURES=$((FAILURES + 1))
  fi
}

FIX_SCHEMA="${SCRIPT_DIR}/../schemas/fix-result.schema.json"
REVIEW_SCHEMA="${SCRIPT_DIR}/../schemas/review-result.schema.json"

run_test_custom_filename "custom-output-file-valid" \
  '{"pr_number":42,"summary":"Fixed 1 issue.","trigger_source":"bot","iteration":1,"tests_passed":true,"actions":[{"type":"fix","finding":"nil check","description":"Added nil check","path":"pkg/handler.go"}],"files_changed":["pkg/handler.go"]}' \
  "fix-result.json" \
  "${FIX_SCHEMA}" \
  "true"

run_test_custom_filename "custom-output-file-invalid" \
  '{"summary":"Bad."}' \
  "fix-result.json" \
  "${FIX_SCHEMA}" \
  "false"

run_test_custom_filename "review-approve-actionable-finding-valid" \
  '{"action":"approve","pr_number":42,"repo":"owner/repo","head_sha":"abcdef0123456789abcdef0123456789abcdef01","body":"Approved with follow-ups.","findings":[{"severity":"low","category":"docs","file":"README.md","line":3,"description":"Document the flag.","remediation":"Add a short usage note.","actionable":true}]}' \
  "agent-result.json" \
  "${REVIEW_SCHEMA}" \
  "true"

run_test_custom_filename "review-finding-additional-property-rejected" \
  '{"action":"approve","pr_number":42,"repo":"owner/repo","head_sha":"abcdef0123456789abcdef0123456789abcdef01","body":"Approved.","findings":[{"severity":"low","category":"docs","file":"README.md","description":"Document the flag.","unexpected":true}]}' \
  "agent-result.json" \
  "${REVIEW_SCHEMA}" \
  "false"

# Helper for custom-filename tests that also assert output content.
run_test_custom_filename_output() {
  local test_name="$1"
  local json_content="$2"
  local output_file="$3"
  local schema="$4"
  local expect_pass="$5"
  local expect_output="$6"

  local test_dir="${TMPDIR}/${test_name}"
  mkdir -p "${test_dir}/output"
  echo "${json_content}" > "${test_dir}/output/$(basename "${output_file}")"

  local exit_code=0
  FULLSEND_OUTPUT_SCHEMA="${schema}" FULLSEND_OUTPUT_FILE="${output_file}" \
    bash -c "cd '${test_dir}' && bash '${VALIDATOR}'" > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  local passed=true
  if [[ "${expect_pass}" == "true" && ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — expected PASS but got exit ${exit_code}"
    cat "${TMPDIR}/stdout.log"
    passed=false
  elif [[ "${expect_pass}" == "false" && ${exit_code} -eq 0 ]]; then
    echo "FAIL: ${test_name} — expected FAIL but got PASS"
    passed=false
  fi

  if [[ -n "${expect_output}" ]] && ! grep -qF "${expect_output}" "${TMPDIR}/stdout.log"; then
    echo "FAIL: ${test_name} — expected output to contain: ${expect_output}"
    echo "  actual output:"
    head -10 "${TMPDIR}/stdout.log"
    passed=false
  fi

  if [[ "${passed}" == "true" ]]; then
    echo "PASS: ${test_name}"
  else
    FAILURES=$((FAILURES + 1))
  fi
}

run_test_custom_filename_output "nested-additional-property-shows-allowed" \
  '{"action":"approve","pr_number":42,"repo":"owner/repo","head_sha":"abcdef0123456789abcdef0123456789abcdef01","body":"Approved.","findings":[{"severity":"low","category":"docs","file":"README.md","description":"Document the flag.","unexpected":true}]}' \
  "agent-result.json" \
  "${REVIEW_SCHEMA}" \
  "false" \
  "allowed properties: actionable, category, description, file, line, remediation, severity"

# --- Structural failures ---

run_test "missing-action" \
  '{"reasoning":"test","comment":"test"}' \
  "false"

run_test "missing-comment" \
  '{"action":"sufficient","reasoning":"test"}' \
  "false"

run_test "invalid-action-value" \
  '{"action":"not_a_bug","reasoning":"test","comment":"test"}' \
  "false"

run_test "invalid-json" \
  'not json at all' \
  "false"

run_test "additional-properties-rejected" \
  '{"action":"sufficient","reasoning":"ok","clarity_scores":{"symptom":0.9,"cause":0.8,"reproduction":0.9,"impact":0.7,"overall":0.85},"triage_summary":{"title":"Bug","severity":"high","category":"bug","problem":"crash","root_cause_hypothesis":"null ptr","reproduction_steps":["step 1"],"impact":"all users","recommended_fix":"fix","proposed_test_case":"test"},"comment":"Done.","injected_field":"malicious"}' \
  "false"

# --- Allowed-properties output tests ---

# Helper that asserts both exit code and that stdout contains a required string.
run_test_output() {
  local test_name="$1"
  local json_content="$2"
  local expect_pass="$3"  # "true" or "false"
  local expect_output="$4"  # substring that must appear in stdout

  local test_dir="${TMPDIR}/${test_name}"
  mkdir -p "${test_dir}/output"
  echo "${json_content}" > "${test_dir}/output/agent-result.json"

  local exit_code=0
  FULLSEND_OUTPUT_SCHEMA="${SCHEMA}" \
    bash -c "cd '${test_dir}' && bash '${VALIDATOR}'" > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  local passed=true
  if [[ "${expect_pass}" == "true" && ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — expected PASS but got exit ${exit_code}"
    cat "${TMPDIR}/stdout.log"
    passed=false
  elif [[ "${expect_pass}" == "false" && ${exit_code} -eq 0 ]]; then
    echo "FAIL: ${test_name} — expected FAIL but got PASS"
    passed=false
  fi

  if [[ -n "${expect_output}" ]] && ! grep -qF "${expect_output}" "${TMPDIR}/stdout.log"; then
    echo "FAIL: ${test_name} — expected output to contain: ${expect_output}"
    echo "  actual output:"
    cat "${TMPDIR}/stdout.log" | head -10
    passed=false
  fi

  if [[ "${passed}" == "true" ]]; then
    echo "PASS: ${test_name}"
  else
    FAILURES=$((FAILURES + 1))
  fi
}

run_test_output "additional-properties-shows-allowed" \
  '{"action":"sufficient","reasoning":"ok","clarity_scores":{"symptom":0.9,"cause":0.8,"reproduction":0.9,"impact":0.7,"overall":0.85},"triage_summary":{"title":"Bug","severity":"high","category":"bug","problem":"crash","root_cause_hypothesis":"null ptr","reproduction_steps":["step 1"],"impact":"all users","recommended_fix":"fix","proposed_test_case":"test"},"comment":"Done.","injected_field":"malicious"}' \
  "false" \
  "allowed properties:"

run_test_output "additional-properties-lists-known-keys" \
  '{"action":"sufficient","reasoning":"ok","clarity_scores":{"symptom":0.9,"cause":0.8,"reproduction":0.9,"impact":0.7,"overall":0.85},"triage_summary":{"title":"Bug","severity":"high","category":"bug","problem":"crash","root_cause_hypothesis":"null ptr","reproduction_steps":["step 1"],"impact":"all users","recommended_fix":"fix","proposed_test_case":"test"},"comment":"Done.","injected_field":"malicious"}' \
  "false" \
  "action, blocked_by, clarity_scores, comment, duplicate_of, label_actions, reasoning, triage_summary"

run_test_output "valid-output-no-allowed-line" \
  '{"action":"insufficient","reasoning":"missing repro","clarity_scores":{"symptom":0.6,"cause":0.3,"reproduction":0.1,"impact":0.5,"overall":0.39},"comment":"Can you share repro steps?"}' \
  "true" \
  ""

run_test "invalid-category-rejected" \
  '{"action":"sufficient","reasoning":"ok","clarity_scores":{"symptom":0.9,"cause":0.8,"reproduction":0.9,"impact":0.7,"overall":0.85},"triage_summary":{"title":"Bug","severity":"high","category":"invented-category","problem":"crash","root_cause_hypothesis":"null ptr","reproduction_steps":["step 1"],"impact":"all users","recommended_fix":"fix","proposed_test_case":"test"},"comment":"Done."}' \
  "false"

# --- fix-result.schema.json conditional allOf/if/then rules ---

run_test_custom_filename "fix-missing-description" \
  '{"pr_number":42,"summary":"s","trigger_source":"bot","iteration":1,"tests_passed":true,"actions":[{"type":"fix","finding":"nil check"}],"files_changed":["f.go"]}' \
  "fix-result.json" \
  "${FIX_SCHEMA}" \
  "false"

run_test_custom_filename "disagree-missing-reason" \
  '{"pr_number":42,"summary":"s","trigger_source":"bot","iteration":1,"tests_passed":true,"actions":[{"type":"disagree","finding":"nil check"}],"files_changed":["f.go"]}' \
  "fix-result.json" \
  "${FIX_SCHEMA}" \
  "false"

run_test_custom_filename "fix-with-description-valid" \
  '{"pr_number":42,"summary":"s","trigger_source":"bot","iteration":1,"tests_passed":true,"actions":[{"type":"fix","finding":"nil check","description":"Added nil check"}],"files_changed":["f.go"]}' \
  "fix-result.json" \
  "${FIX_SCHEMA}" \
  "true"

run_test_custom_filename "disagree-with-reason-valid" \
  '{"pr_number":42,"summary":"s","trigger_source":"bot","iteration":1,"tests_passed":true,"actions":[{"type":"disagree","finding":"nil check","reason":"Already guarded upstream"}],"files_changed":["f.go"]}' \
  "fix-result.json" \
  "${FIX_SCHEMA}" \
  "true"

run_test_custom_filename "empty-actions-rejected" \
  '{"pr_number":42,"summary":"s","trigger_source":"bot","iteration":1,"tests_passed":true,"actions":[],"files_changed":["f.go"]}' \
  "fix-result.json" \
  "${FIX_SCHEMA}" \
  "false"

# --- FULLSEND_OUTPUT_FILE path traversal guard ---
run_test_custom_filename "path-traversal-stripped" \
  '{"pr_number":42,"summary":"Fixed 1 issue.","trigger_source":"bot","iteration":1,"tests_passed":true,"actions":[{"type":"fix","finding":"nil check","description":"Added nil check","path":"pkg/handler.go"}],"files_changed":["pkg/handler.go"]}' \
  "../../etc/fix-result.json" \
  "${FIX_SCHEMA}" \
  "true"

# --- review-result.schema.json tests ---

REVIEW_SCHEMA="${SCRIPT_DIR}/../schemas/review-result.schema.json"

run_test_custom_filename "review-reject-valid" \
  '{"action":"reject","pr_number":1,"repo":"org/repo","head_sha":"abc1234","body":"Wrong approach.","findings":[{"severity":"high","category":"intent-alignment","file":"main.go","description":"Wrong design."}]}' \
  "agent-result.json" \
  "${REVIEW_SCHEMA}" \
  "true"

run_test_custom_filename "review-reject-missing-findings" \
  '{"action":"reject","pr_number":1,"repo":"org/repo","head_sha":"abc1234","body":"Wrong approach."}' \
  "agent-result.json" \
  "${REVIEW_SCHEMA}" \
  "false"

run_test_custom_filename "review-reject-missing-body" \
  '{"action":"reject","pr_number":1,"repo":"org/repo","head_sha":"abc1234","findings":[{"severity":"high","category":"intent-alignment","file":"main.go","description":"Wrong design."}]}' \
  "agent-result.json" \
  "${REVIEW_SCHEMA}" \
  "false"

run_test_custom_filename "review-approve-valid" \
  '{"action":"approve","pr_number":1,"repo":"org/repo","head_sha":"abc1234","body":"Looks good, only minor nits."}' \
  "agent-result.json" \
  "${REVIEW_SCHEMA}" \
  "true"

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
