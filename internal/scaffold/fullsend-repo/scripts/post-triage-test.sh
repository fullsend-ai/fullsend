#!/usr/bin/env bash
# post-triage-test.sh — Test post-triage.sh with fixture JSON inputs.
#
# Uses a mock gh command to capture calls without hitting GitHub.
# Run from the repo root: bash internal/scaffold/fullsend-repo/scripts/post-triage-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POST_SCRIPT="${SCRIPT_DIR}/post-triage.sh"
FAILURES=0

# Create a temp directory for test fixtures and mock state.
TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# Mock gh: record all calls to a log file.
GH_LOG="${TMPDIR}/gh-calls.log"
MOCK_BIN="${TMPDIR}/bin"
mkdir -p "${MOCK_BIN}"
cat > "${MOCK_BIN}/gh" <<MOCKEOF
#!/usr/bin/env bash
# When querying the repo labels list, return a set of known test labels so that
# the label-existence guard in post-triage.sh allows them through.
if [[ "\$1" == "api" ]] && [[ "\$2" == *"/labels" ]] && [[ "\$*" == *"--paginate"* ]] && [[ "\$*" != *"-f "* ]] && [[ "\$*" != *"-X "* ]]; then
  # Return labels used by the test fixtures, one per line (--jq '.[].name').
  printf '%s\n' "area/api" "area/cli" "priority/high" "component/parser"
  exit 0
fi
# For issue create, return a fake URL on stdout so callers can capture it.
if [[ "\$1" == "issue" ]] && [[ "\$2" == "create" ]]; then
  echo "gh \$*" >> "${GH_LOG}"
  echo "https://github.com/mock-org/mock-repo/issues/999"
  exit 0
fi
echo "gh \$*" >> "${GH_LOG}"
MOCKEOF
chmod +x "${MOCK_BIN}/gh"

cat > "${MOCK_BIN}/fullsend" <<MOCKEOF
#!/usr/bin/env bash
BODY=""
PREV=""
for arg in "\$@"; do
  if [[ "\${arg}" == "-" ]] && [[ "\${PREV}" == "--result" ]]; then
    BODY=\$(cat)
  fi
  PREV="\${arg}"
done
if [[ -n "\${BODY}" ]]; then
  echo "fullsend \$* <<BODY:\${BODY}:BODY>>" >> "${GH_LOG}"
else
  echo "fullsend \$*" >> "${GH_LOG}"
fi
MOCKEOF
chmod +x "${MOCK_BIN}/fullsend"

export PATH="${MOCK_BIN}:${PATH}"
export GITHUB_ISSUE_URL="https://github.com/test-org/test-repo/issues/42"
export GH_TOKEN="fake-token"

# prerequisites handler reads config.yaml from GITHUB_WORKSPACE.
# Create a minimal workspace with an allowlist so the test can exercise
# both the allowed and disallowed paths.
WORKSPACE="${TMPDIR}/workspace"
mkdir -p "${WORKSPACE}"
cat > "${WORKSPACE}/config.yaml" <<CFGEOF
version: "1"
create_issues:
  allow_targets:
    orgs:
      - test-org
    repos:
      - allowed-org/allowed-repo
CFGEOF
export GITHUB_WORKSPACE="${WORKSPACE}"

run_test() {
  local test_name="$1"
  local json_content="$2"
  local expected_pattern="$3"
  local expect_failure="${4:-false}"

  # Create iteration output structure.
  local run_dir="${TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${json_content}" > "${run_dir}/iteration-1/output/agent-result.json"

  # Clear gh call log.
  : > "${GH_LOG}"

  # Run the post-script.
  local exit_code=0
  (cd "${run_dir}" && bash "${POST_SCRIPT}") > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  if [[ "${expect_failure}" == "true" ]]; then
    if [[ ${exit_code} -eq 0 ]]; then
      echo "FAIL: ${test_name} — expected failure but got success"
      FAILURES=$((FAILURES + 1))
      return
    fi
    echo "PASS: ${test_name} (expected failure, got exit code ${exit_code})"
    return
  fi

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if ! grep -qF "${expected_pattern}" "${GH_LOG}"; then
    echo "FAIL: ${test_name} — expected gh call pattern '${expected_pattern}' not found"
    echo "Actual calls:"
    cat "${GH_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

run_test_stdout() {
  local test_name="$1"
  local json_content="$2"
  local expected_stdout="$3"

  local run_dir="${TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${json_content}" > "${run_dir}/iteration-1/output/agent-result.json"
  : > "${GH_LOG}"

  local exit_code=0
  (cd "${run_dir}" && bash "${POST_SCRIPT}") > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if ! grep -qF "${expected_stdout}" "${TMPDIR}/stdout.log"; then
    echo "FAIL: ${test_name} — expected stdout pattern '${expected_stdout}' not found"
    echo "Actual stdout:"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Test cases ---

run_test "insufficient-uses-plain-comment" \
  '{"action":"insufficient","reasoning":"missing repro","clarity_scores":{"symptom":0.6,"cause":0.3,"reproduction":0.1,"impact":0.5,"overall":0.39},"comment":"Could you share the exact steps to reproduce this?"}' \
  "gh issue comment 42 --repo test-org/test-repo --body-file -"

run_test "insufficient-posts-comment-and-labels" \
  '{"action":"insufficient","reasoning":"missing repro","clarity_scores":{"symptom":0.6,"cause":0.3,"reproduction":0.1,"impact":0.5,"overall":0.39},"comment":"Could you share the exact steps to reproduce this?"}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=needs-info --silent"

run_test "insufficient-removes-blocked-label" \
  '{"action":"insufficient","reasoning":"missing repro","clarity_scores":{"symptom":0.6,"cause":0.3,"reproduction":0.1,"impact":0.5,"overall":0.39},"comment":"Could you share the exact steps to reproduce this?"}' \
  "gh api repos/test-org/test-repo/issues/42/labels/blocked -X DELETE --silent"

run_test "sufficient-posts-summary-and-labels" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash on save","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_save_crash"},"comment":"## Triage Summary\n\nThis is ready."}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=ready-to-code --silent"

run_test "sufficient-bug-gets-ready-to-code" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash on save","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_save_crash"},"comment":"## Triage Summary\n\nThis is ready."}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=ready-to-code --silent"

run_test "sufficient-feature-gets-triaged" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Add dark mode","severity":"medium","category":"feature","problem":"No dark mode","root_cause_hypothesis":"Not implemented","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Add theme toggle","proposed_test_case":"test_dark_mode"},"comment":"## Triage Summary\n\nThis is a feature."}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=triaged --silent"

run_test "sufficient-feature-gets-feature-label" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Add dark mode","severity":"medium","category":"feature","problem":"No dark mode","root_cause_hypothesis":"Not implemented","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Add theme toggle","proposed_test_case":"test_dark_mode"},"comment":"## Triage Summary\n\nThis is a feature."}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=feature --silent"

run_test "sufficient-other-gets-triaged" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Misc","severity":"low","category":"other","problem":"Misc","root_cause_hypothesis":"Unclear","reproduction_steps":["step 1"],"environment":"Linux","impact":"Some","recommended_fix":"Investigate","proposed_test_case":"test_misc"},"comment":"## Triage Summary\n\nMisc."}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=triaged --silent"

run_test "sufficient-performance-gets-ready-to-code" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Slow query","severity":"medium","category":"performance","problem":"Slow","root_cause_hypothesis":"Missing index","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Add index","proposed_test_case":"test_query_speed"},"comment":"## Triage Summary\n\nThis is a performance issue."}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=ready-to-code --silent"

run_test "sufficient-documentation-gets-ready-to-code" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Update docs","severity":"low","category":"documentation","problem":"Outdated docs","root_cause_hypothesis":"Not updated","reproduction_steps":["step 1"],"environment":"Linux","impact":"Contributors","recommended_fix":"Update README","proposed_test_case":"test_docs"},"comment":"## Triage Summary\n\nThis is a documentation issue."}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=ready-to-code --silent"

run_test "sufficient-with-empty-info-gaps-passes" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash on save","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_save_crash","information_gaps":[]},"comment":"## Triage Summary\n\nThis is ready."}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=ready-to-code --silent"

run_test "sufficient-with-info-gaps-fails" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash on save","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_save_crash","information_gaps":["What label naming convention to use?"]},"comment":"## Triage Summary\n\nThis is ready."}' \
  "" \
  "true"

run_test "sufficient-removes-blocked-label" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash on save","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_save_crash","information_gaps":[]},"comment":"## Triage Summary\n\nThis is ready."}' \
  "gh api repos/test-org/test-repo/issues/42/labels/blocked -X DELETE --silent"

run_test "sufficient-removes-needs-info-label" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash on save","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_save_crash","information_gaps":[]},"comment":"## Triage Summary\n\nThis is ready."}' \
  "gh api repos/test-org/test-repo/issues/42/labels/needs-info -X DELETE --silent"

run_test "duplicate-labels" \
  '{"action":"duplicate","reasoning":"same as #10","duplicate_of":10,"comment":"This appears to be a duplicate of #10."}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=duplicate --silent"

run_test "duplicate-removes-blocked-label" \
  '{"action":"duplicate","reasoning":"same as #10","duplicate_of":10,"comment":"This appears to be a duplicate of #10."}' \
  "gh api repos/test-org/test-repo/issues/42/labels/blocked -X DELETE --silent"

run_test "duplicate-closes-issue" \
  '{"action":"duplicate","reasoning":"same as #10","duplicate_of":10,"comment":"This appears to be a duplicate of #10."}' \
  "gh issue close 42 --repo test-org/test-repo --reason duplicate"

run_test "duplicate-self-reference-fails" \
  '{"action":"duplicate","reasoning":"same issue","duplicate_of":42,"comment":"Duplicate of itself."}' \
  "" \
  "true"

run_test "prerequisites-posts-comment-and-labels" \
  '{"action":"prerequisites","reasoning":"needs upstream fix","prerequisites":{"existing":[{"url":"https://github.com/other-org/other-repo/issues/99"}],"create":[]},"comment":"This issue is blocked on an upstream dependency."}' \
  "gh issue comment 42 --repo test-org/test-repo --body-file -"

run_test "prerequisites-applies-blocked-label" \
  '{"action":"prerequisites","reasoning":"needs upstream fix","prerequisites":{"existing":[{"url":"https://github.com/other-org/other-repo/issues/99"}],"create":[]},"comment":"This issue is blocked on an upstream dependency."}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=blocked --silent"

run_test "prerequisites-missing-comment-fails" \
  '{"action":"prerequisites","reasoning":"needs upstream fix","prerequisites":{"existing":[{"url":"https://github.com/other-org/other-repo/issues/99"}],"create":[]}}' \
  "" \
  "true"

run_test "prerequisites-creates-allowed-issue" \
  '{"action":"prerequisites","reasoning":"needs upstream fix","prerequisites":{"existing":[],"create":[{"repo":"allowed-org/allowed-repo","title":"Need X","body":"We need X for downstream."}]},"comment":"Blocked on upstream work."}' \
  "gh issue create --repo allowed-org/allowed-repo --title Need X --body We need X for downstream."

run_test_stdout "prerequisites-skips-disallowed-target" \
  '{"action":"prerequisites","reasoning":"needs upstream fix","prerequisites":{"existing":[],"create":[{"repo":"disallowed-org/other-repo","title":"Need Y","body":"We need Y."}]},"comment":"Blocked on upstream work."}' \
  "::warning::Skipping issue creation in 'disallowed-org/other-repo'"

run_test "question-posts-comment" \
  '{"action":"question","reasoning":"issue is asking a question","comment":"Based on the repository docs, Python 4 is not currently supported.\n\nDid this answer your question, or would you like to open a feature request for Python 4 support?"}' \
  "gh issue comment 42 --repo test-org/test-repo --body-file -"

run_test "question-applies-question-label" \
  '{"action":"question","reasoning":"issue is asking a question","comment":"Based on the repository docs, Python 4 is not currently supported.\n\nDid this answer your question, or would you like to open a feature request for Python 4 support?"}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=question --silent"

run_test "question-removes-blocked-label" \
  '{"action":"question","reasoning":"issue is asking a question","comment":"Based on the repository docs, Python 4 is not currently supported."}' \
  "gh api repos/test-org/test-repo/issues/42/labels/blocked -X DELETE --silent"

run_test "question-removes-needs-info-label" \
  '{"action":"question","reasoning":"issue is asking a question","comment":"Based on the repository docs, Python 4 is not currently supported."}' \
  "gh api repos/test-org/test-repo/issues/42/labels/needs-info -X DELETE --silent"

run_test "question-missing-comment-fails" \
  '{"action":"question","reasoning":"issue is asking a question"}' \
  "" \
  "true"

run_test_stdout "question-control-label-refused" \
  '{"action":"question","reasoning":"issue is asking a question","comment":"Answer here.","label_actions":{"reason":"Tried to set question label.","actions":[{"action":"add","label":"question"}]}}' \
  "::warning::Refused to add control label 'question' -- control labels are managed by the triage pipeline"

run_test "unknown-action-fails" \
  '{"action":"not_a_bug","reasoning":"working as intended","comment":"This is working as intended."}' \
  "" \
  "true"

run_test "missing-json-fails" \
  "" \
  "" \
  "true"

run_test "invalid-json-fails" \
  "this is not json" \
  "" \
  "true"

run_test "label-actions-applied" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"API crash matches area/api label.","actions":[{"action":"add","label":"area/api"}]}}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=area/api --silent"

run_test_stdout "label-actions-control-label-refused" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"Tried to set control label.","actions":[{"action":"add","label":"ready-to-code"}]}}' \
  "::warning::Refused to add control label 'ready-to-code' -- control labels are managed by the triage pipeline"

run_test_stdout "label-actions-feature-control-label-refused" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"Tried to set feature label.","actions":[{"action":"add","label":"feature"}]}}' \
  "::warning::Refused to add control label 'feature' -- control labels are managed by the triage pipeline"

run_test "label-actions-absent-still-posts-comment" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady."}' \
  "fullsend post-comment --repo test-org/test-repo --number 42"

run_test "label-actions-with-insufficient" \
  '{"action":"insufficient","reasoning":"missing repro","clarity_scores":{"symptom":0.6,"cause":0.3,"reproduction":0.1,"impact":0.5,"overall":0.39},"comment":"Could you share the exact steps to reproduce this?","label_actions":{"reason":"Component label applies regardless of triage outcome.","actions":[{"action":"add","label":"component/parser"}]}}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=component/parser --silent"

run_test "label-actions-reason-appended-to-comment" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"API crash matches area/api label.","actions":[{"action":"add","label":"area/api"}]}}' \
  "API crash matches area/api label."

run_test "label-actions-remove" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"Stale area label removed.","actions":[{"action":"remove","label":"area/cli"}]}}' \
  "gh api repos/test-org/test-repo/issues/42/labels/area%2Fcli -X DELETE --silent"

run_test "label-actions-multiple-add" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"Multiple labels apply.","actions":[{"action":"add","label":"area/api"},{"action":"add","label":"priority/high"}]}}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=area/api --silent"

run_test "label-actions-multiple-second-label" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"Multiple labels apply.","actions":[{"action":"add","label":"area/api"},{"action":"add","label":"priority/high"}]}}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=priority/high --silent"

run_test_stdout "label-actions-nonexistent-label-skipped" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"Agent recommended a label that does not exist.","actions":[{"action":"add","label":"bug"}]}}' \
  "::warning::Skipping label 'bug' -- does not exist in repo (will not auto-create)"

run_test_stdout "label-actions-invalid-characters-refused" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"Injection attempt.","actions":[{"action":"add","label":"label;injection"}]}}' \
  "::warning::Refused label 'label;injection' -- contains invalid characters"

# Verify that when all label actions are refused, the reason is NOT appended to the comment.
# We check that the fullsend call does NOT contain "Labels:" in the body.
run_test_no_pattern() {
  local test_name="$1"
  local json_content="$2"
  local forbidden_pattern="$3"

  local run_dir="${TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${json_content}" > "${run_dir}/iteration-1/output/agent-result.json"
  : > "${GH_LOG}"

  local exit_code=0
  (cd "${run_dir}" && bash "${POST_SCRIPT}") > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if grep -qF "${forbidden_pattern}" "${GH_LOG}"; then
    echo "FAIL: ${test_name} — forbidden pattern '${forbidden_pattern}' was found"
    echo "Actual calls:"
    cat "${GH_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

run_test_no_pattern "label-actions-all-refused-no-reason" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"Should not appear.","actions":[{"action":"add","label":"ready-to-code"}]}}' \
  "Should not appear."

# run_test_label_order verifies that a pattern appears AFTER another pattern
# in the gh call log (i.e., ordering of API calls).
run_test_label_order() {
  local test_name="$1"
  local json_content="$2"
  local before_pattern="$3"
  local after_pattern="$4"

  local run_dir="${TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${json_content}" > "${run_dir}/iteration-1/output/agent-result.json"
  : > "${GH_LOG}"

  local exit_code=0
  (cd "${run_dir}" && bash "${POST_SCRIPT}") > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  local before_line after_line
  before_line=$(grep -nF "${before_pattern}" "${GH_LOG}" | head -1 | cut -d: -f1)
  after_line=$(grep -nF "${after_pattern}" "${GH_LOG}" | head -1 | cut -d: -f1)

  if [[ -z "${before_line}" ]]; then
    echo "FAIL: ${test_name} — before pattern '${before_pattern}' not found"
    echo "Actual calls:"
    cat "${GH_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ -z "${after_line}" ]]; then
    echo "FAIL: ${test_name} — after pattern '${after_pattern}' not found"
    echo "Actual calls:"
    cat "${GH_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ "${before_line}" -ge "${after_line}" ]]; then
    echo "FAIL: ${test_name} — '${before_pattern}' (line ${before_line}) should appear before '${after_pattern}' (line ${after_line})"
    echo "Actual calls:"
    cat "${GH_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# Verify ready-to-code is applied AFTER informational labels from label_actions
# to prevent the ready-to-code webhook event from being superseded (#1752).
run_test_label_order "ready-to-code-applied-after-label-actions" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"Component label.","actions":[{"action":"add","label":"area/api"},{"action":"add","label":"priority/high"}]}}' \
  "labels[]=priority/high" \
  "labels[]=ready-to-code"

# Verify ready-to-code is still applied when there are no label_actions.
run_test "ready-to-code-applied-without-label-actions" \
  '{"action":"sufficient","reasoning":"all clear","clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.9,"impact":0.8,"overall":0.87},"triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Buffer overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix buffer","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady."}' \
  "gh api repos/test-org/test-repo/issues/42/labels -f labels[]=ready-to-code --silent"

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
