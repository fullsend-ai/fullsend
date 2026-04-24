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

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
