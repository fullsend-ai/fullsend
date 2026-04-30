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

  local test_dir="${TMPDIR}/${test_name}"
  mkdir -p "${test_dir}/output"
  echo "${json_content}" > "${test_dir}/output/agent-result.json"

  local exit_code=0
  FULLSEND_OUTPUT_SCHEMA="${SCHEMA}" \
    bash -c "cd '${test_dir}' && bash '${VALIDATOR}'" > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  if [[ "${expect_pass}" == "true" && ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — expected PASS but got exit ${exit_code}"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
  elif [[ "${expect_pass}" == "false" && ${exit_code} -eq 0 ]]; then
    echo "FAIL: ${test_name} — expected FAIL but got PASS"
    FAILURES=$((FAILURES + 1))
  else
    echo "PASS: ${test_name}"
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

# --- FULLSEND_OUTPUT_FILE override ---

run_test_custom_filename() {
  local test_name="$1"
  local json_content="$2"
  local output_file="$3"
  local schema="$4"
  local expect_pass="$5"

  local test_dir="${TMPDIR}/${test_name}"
  mkdir -p "${test_dir}/output"
  echo "${json_content}" > "${test_dir}/output/$(basename "${output_file}")"

  local exit_code=0
  FULLSEND_OUTPUT_SCHEMA="${schema}" FULLSEND_OUTPUT_FILE="${output_file}" \
    bash -c "cd '${test_dir}' && bash '${VALIDATOR}'" > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  if [[ "${expect_pass}" == "true" && ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — expected PASS but got exit ${exit_code}"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
  elif [[ "${expect_pass}" == "false" && ${exit_code} -eq 0 ]]; then
    echo "FAIL: ${test_name} — expected FAIL but got PASS"
    FAILURES=$((FAILURES + 1))
  else
    echo "PASS: ${test_name}"
  fi
}

FIX_SCHEMA="${SCRIPT_DIR}/../schemas/fix-result.schema.json"

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

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
