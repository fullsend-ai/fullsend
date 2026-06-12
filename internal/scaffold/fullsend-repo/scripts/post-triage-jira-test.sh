#!/usr/bin/env bash
# post-triage-jira-test.sh — Test post-triage-jira.sh with fixture JSON inputs.
#
# Uses mock curl and python3 commands to capture calls without hitting Jira.
# Run from the repo root: bash internal/scaffold/fullsend-repo/scripts/post-triage-jira-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POST_SCRIPT="${SCRIPT_DIR}/post-triage-jira.sh"
FAILURES=0

# Create a temp directory for test fixtures and mock state.
TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# curl call log — records every invocation made by the post-script.
CURL_LOG="${TMPDIR}/curl-calls.log"

MOCK_BIN="${TMPDIR}/bin"
mkdir -p "${MOCK_BIN}"

# Mock curl:
#  - Records all calls to CURL_LOG
#  - Returns minimal success responses for all Jira endpoints
#  - For comment GET (existing comment search): returns empty list by default
cat > "${MOCK_BIN}/curl" <<MOCKEOF
#!/usr/bin/env bash
# Record the call (flatten multiline for easy grep).
echo "curl \$*" >> "${CURL_LOG}"

URL=""
METHOD="GET"
PREV=""
for arg in "\$@"; do
  case "\${PREV}" in
    -X) METHOD="\${arg}" ;;
  esac
  case "\${arg}" in
    http*) URL="\${arg}" ;;
  esac
  PREV="\${arg}"
done

# Return appropriate fixtures per endpoint.
if [[ "\${URL}" == */comment* ]] && [[ "\${METHOD}" == "GET" ]]; then
  # Return empty comment list so no existing sticky comment is found.
  echo '{"comments":[]}'
elif [[ "\${URL}" == */transitions* ]]; then
  # Return a Done transition for duplicate close tests.
  echo '{"transitions":[{"id":"31","name":"Done"}]}'
elif [[ "\${URL}" == */issueLink* ]]; then
  echo '{}'
elif [[ "\${METHOD}" == "PUT" ]] || [[ "\${METHOD}" == "POST" ]]; then
  echo '{}'
else
  echo '{}'
fi
MOCKEOF
chmod +x "${MOCK_BIN}/curl"

# Mock python3: return a minimal ADF JSON for any input so comment posting works.
# We pass through to real python3 for markdown-to-adf.py, but if python3 is not
# available we fall back to a static ADF stub.
cat > "${MOCK_BIN}/python3" <<'MOCKEOF'
#!/usr/bin/env bash
# If called as "python3 scripts/markdown-to-adf.py" (or any path ending in
# markdown-to-adf.py), output a minimal valid ADF body.
for arg in "$@"; do
  if [[ "${arg}" == *markdown-to-adf.py ]]; then
    # Consume stdin and emit stub ADF.
    cat > /dev/null
    echo '{"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"test comment"}]}]}}'
    exit 0
  fi
done
# Fall through: execute real python3 for anything else.
exec /usr/bin/env python3 "$@"
MOCKEOF
chmod +x "${MOCK_BIN}/python3"

export PATH="${MOCK_BIN}:${PATH}"
export ISSUE_KEY="PROJ-123"
export JIRA_HOST="myorg.atlassian.net"
export JIRA_EMAIL="agent@example.com"
export JIRA_API_TOKEN="fake-token"
export GH_TOKEN="fake-gh-token"

# --- Helper functions (parallel to post-triage-test.sh) ---

run_test() {
  local test_name="$1"
  local json_content="$2"
  local expected_pattern="$3"
  local expect_failure="${4:-false}"

  # Create iteration output structure.
  local run_dir="${TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${json_content}" > "${run_dir}/iteration-1/output/agent-result.json"

  # Clear curl call log.
  : > "${CURL_LOG}"

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

  if ! grep -qF "${expected_pattern}" "${CURL_LOG}"; then
    echo "FAIL: ${test_name} — expected curl call pattern '${expected_pattern}' not found"
    echo "Actual calls:"
    cat "${CURL_LOG}"
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
  : > "${CURL_LOG}"

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

run_test_no_pattern() {
  local test_name="$1"
  local json_content="$2"
  local forbidden_pattern="$3"

  local run_dir="${TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${json_content}" > "${run_dir}/iteration-1/output/agent-result.json"
  : > "${CURL_LOG}"

  local exit_code=0
  (cd "${run_dir}" && bash "${POST_SCRIPT}") > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if grep -qF "${forbidden_pattern}" "${CURL_LOG}"; then
    echo "FAIL: ${test_name} — forbidden pattern '${forbidden_pattern}' was found"
    echo "Actual calls:"
    cat "${CURL_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

run_test_label_order() {
  local test_name="$1"
  local json_content="$2"
  local before_pattern="$3"
  local after_pattern="$4"

  local run_dir="${TMPDIR}/run-${test_name}"
  mkdir -p "${run_dir}/iteration-1/output"
  echo "${json_content}" > "${run_dir}/iteration-1/output/agent-result.json"
  : > "${CURL_LOG}"

  local exit_code=0
  (cd "${run_dir}" && bash "${POST_SCRIPT}") > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  local before_line after_line
  before_line=$(grep -nF "${before_pattern}" "${CURL_LOG}" | head -1 | cut -d: -f1)
  after_line=$(grep -nF "${after_pattern}" "${CURL_LOG}" | head -1 | cut -d: -f1)

  if [[ -z "${before_line}" ]]; then
    echo "FAIL: ${test_name} — before pattern '${before_pattern}' not found"
    echo "Actual calls:"
    cat "${CURL_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ -z "${after_line}" ]]; then
    echo "FAIL: ${test_name} — after pattern '${after_pattern}' not found"
    echo "Actual calls:"
    cat "${CURL_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ "${before_line}" -ge "${after_line}" ]]; then
    echo "FAIL: ${test_name} — '${before_pattern}' (line ${before_line}) should appear before '${after_pattern}' (line ${after_line})"
    echo "Actual calls:"
    cat "${CURL_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Test cases ---

# insufficient → adds fullsend:needs-info, removes fullsend:blocked, posts comment
run_test "insufficient-adds-needs-info" \
  '{"action":"insufficient","reasoning":"missing repro","comment":"Could you share the exact steps to reproduce this?"}' \
  '"add":"fullsend:needs-info"'

run_test "insufficient-removes-blocked" \
  '{"action":"insufficient","reasoning":"missing repro","comment":"Could you share the exact steps to reproduce this?"}' \
  '"remove":"fullsend:blocked"'

run_test "insufficient-posts-comment" \
  '{"action":"insufficient","reasoning":"missing repro","comment":"Could you share the exact steps to reproduce this?"}' \
  "/rest/api/3/issue/PROJ-123/comment"

run_test "insufficient-missing-comment-fails" \
  '{"action":"insufficient","reasoning":"missing repro"}' \
  "" \
  "true"

# sufficient bug → deferred fullsend:ready-to-code, sticky comment, removes blocked+needs-info
run_test "sufficient-bug-gets-ready-to-code" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix it","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nThis is ready."}' \
  '"add":"fullsend:ready-to-code"'

run_test "sufficient-bug-removes-blocked" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix it","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nThis is ready."}' \
  '"remove":"fullsend:blocked"'

run_test "sufficient-bug-removes-needs-info" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix it","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nThis is ready."}' \
  '"remove":"fullsend:needs-info"'

run_test "sufficient-bug-posts-comment" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix it","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nThis is ready."}' \
  "/rest/api/3/issue/PROJ-123/comment"

# sufficient feature → fullsend:triaged + fullsend:feature
run_test "sufficient-feature-gets-triaged" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Add dark mode","severity":"medium","category":"feature","problem":"No dark mode","root_cause_hypothesis":"Not implemented","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Add theme toggle","proposed_test_case":"test_dark_mode"},"comment":"## Triage Summary\n\nThis is a feature."}' \
  '"add":"fullsend:triaged"'

run_test "sufficient-feature-gets-feature-label" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Add dark mode","severity":"medium","category":"feature","problem":"No dark mode","root_cause_hypothesis":"Not implemented","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Add theme toggle","proposed_test_case":"test_dark_mode"},"comment":"## Triage Summary\n\nThis is a feature."}' \
  '"add":"fullsend:feature"'

run_test "sufficient-performance-gets-ready-to-code" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Slow query","severity":"medium","category":"performance","problem":"Slow","root_cause_hypothesis":"Missing index","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Add index","proposed_test_case":"test_speed"},"comment":"## Triage Summary\n\nThis is a performance issue."}' \
  '"add":"fullsend:ready-to-code"'

run_test "sufficient-documentation-gets-ready-to-code" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Update docs","severity":"low","category":"documentation","problem":"Outdated","root_cause_hypothesis":"Not updated","reproduction_steps":["step 1"],"environment":"Linux","impact":"Contributors","recommended_fix":"Update README","proposed_test_case":"test_docs"},"comment":"## Triage Summary\n\nThis is a documentation issue."}' \
  '"add":"fullsend:ready-to-code"'

run_test "sufficient-other-gets-triaged" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Misc","severity":"low","category":"other","problem":"Misc","root_cause_hypothesis":"Unclear","reproduction_steps":["step 1"],"environment":"Linux","impact":"Some","recommended_fix":"Investigate","proposed_test_case":"test_misc"},"comment":"## Triage Summary\n\nMisc."}' \
  '"add":"fullsend:triaged"'

run_test "sufficient-with-empty-info-gaps-passes" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix it","proposed_test_case":"test_crash","information_gaps":[]},"comment":"## Triage Summary\n\nReady."}' \
  '"add":"fullsend:ready-to-code"'

run_test "sufficient-with-info-gaps-fails" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix it","proposed_test_case":"test_crash","information_gaps":["Still unclear."]},"comment":"## Triage Summary\n\nReady."}' \
  "" \
  "true"

run_test "sufficient-missing-comment-fails" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix it","proposed_test_case":"test_crash"}}' \
  "" \
  "true"

# duplicate → adds fullsend:duplicate, creates Jira issueLink, closes issue
run_test "duplicate-adds-label" \
  '{"action":"duplicate","reasoning":"same as PROJ-10","duplicate_of":"PROJ-10","comment":"This appears to be a duplicate of PROJ-10."}' \
  '"add":"fullsend:duplicate"'

run_test "duplicate-removes-blocked" \
  '{"action":"duplicate","reasoning":"same as PROJ-10","duplicate_of":"PROJ-10","comment":"This appears to be a duplicate of PROJ-10."}' \
  '"remove":"fullsend:blocked"'

run_test "duplicate-creates-jira-link" \
  '{"action":"duplicate","reasoning":"same as PROJ-10","duplicate_of":"PROJ-10","comment":"This appears to be a duplicate of PROJ-10."}' \
  "/rest/api/3/issueLink"

run_test "duplicate-closes-issue" \
  '{"action":"duplicate","reasoning":"same as PROJ-10","duplicate_of":"PROJ-10","comment":"This appears to be a duplicate of PROJ-10."}' \
  "/rest/api/3/issue/PROJ-123/transitions"

run_test "duplicate-self-reference-fails" \
  '{"action":"duplicate","reasoning":"same issue","duplicate_of":"PROJ-123","comment":"Duplicate of itself."}' \
  "" \
  "true"

run_test "duplicate-missing-comment-fails" \
  '{"action":"duplicate","reasoning":"same as PROJ-10","duplicate_of":"PROJ-10"}' \
  "" \
  "true"

run_test "duplicate-missing-duplicate-of-fails" \
  '{"action":"duplicate","reasoning":"same as PROJ-10","comment":"Duplicate."}' \
  "" \
  "true"

# blocked → adds fullsend:blocked, validates blocked_by is a URL or Jira key
run_test "blocked-adds-label" \
  '{"action":"blocked","reasoning":"upstream dep","blocked_by":"https://jira.example.com/browse/OTHER-99","comment":"This issue is blocked on an upstream dependency."}' \
  '"add":"fullsend:blocked"'

run_test "blocked-removes-ready-to-code" \
  '{"action":"blocked","reasoning":"upstream dep","blocked_by":"https://jira.example.com/browse/OTHER-99","comment":"This issue is blocked."}' \
  '"remove":"fullsend:ready-to-code"'

run_test "blocked-removes-needs-info" \
  '{"action":"blocked","reasoning":"upstream dep","blocked_by":"https://jira.example.com/browse/OTHER-99","comment":"This issue is blocked."}' \
  '"remove":"fullsend:needs-info"'

run_test "blocked-accepts-jira-key-as-blocked-by" \
  '{"action":"blocked","reasoning":"upstream dep","blocked_by":"OTHER-99","comment":"This issue is blocked."}' \
  '"add":"fullsend:blocked"'

run_test "blocked-missing-blocked-by-fails" \
  '{"action":"blocked","reasoning":"upstream dep","comment":"Blocked on upstream."}' \
  "" \
  "true"

run_test "blocked-missing-comment-fails" \
  '{"action":"blocked","reasoning":"upstream dep","blocked_by":"https://jira.example.com/browse/OTHER-99"}' \
  "" \
  "true"

run_test "blocked-invalid-blocked-by-fails" \
  '{"action":"blocked","reasoning":"upstream dep","blocked_by":"not-a-url-or-key","comment":"Blocked."}' \
  "" \
  "true"

# question → adds fullsend:question, removes blocked + needs-info
run_test "question-adds-label" \
  '{"action":"question","reasoning":"issue is asking a question","comment":"Based on the docs, Python 4 is not supported."}' \
  '"add":"fullsend:question"'

run_test "question-removes-blocked" \
  '{"action":"question","reasoning":"issue is asking a question","comment":"Based on the docs, Python 4 is not supported."}' \
  '"remove":"fullsend:blocked"'

run_test "question-removes-needs-info" \
  '{"action":"question","reasoning":"issue is asking a question","comment":"Based on the docs, Python 4 is not supported."}' \
  '"remove":"fullsend:needs-info"'

run_test "question-posts-comment" \
  '{"action":"question","reasoning":"issue is asking a question","comment":"Based on the docs, Python 4 is not supported."}' \
  "/rest/api/3/issue/PROJ-123/comment"

run_test "question-missing-comment-fails" \
  '{"action":"question","reasoning":"issue is asking a question"}' \
  "" \
  "true"

# Control label guard: fullsend:needs-info in label_actions refused
run_test_stdout "control-label-needs-info-refused" \
  '{"action":"question","reasoning":"issue is asking a question","comment":"Answer.","label_actions":{"reason":"Tried to set control label.","actions":[{"action":"add","label":"fullsend:needs-info"}]}}' \
  "::warning::Refused to add control label 'fullsend:needs-info' -- control labels are managed by the triage pipeline"

run_test_stdout "control-label-ready-to-code-refused" \
  '{"action":"question","reasoning":"issue is asking a question","comment":"Answer.","label_actions":{"reason":"Tried to set control label.","actions":[{"action":"add","label":"fullsend:ready-to-code"}]}}' \
  "::warning::Refused to add control label 'fullsend:ready-to-code' -- control labels are managed by the triage pipeline"

run_test_stdout "control-label-triaged-refused" \
  '{"action":"question","reasoning":"issue is asking a question","comment":"Answer.","label_actions":{"reason":"Tried to set triaged.","actions":[{"action":"add","label":"fullsend:triaged"}]}}' \
  "::warning::Refused to add control label 'fullsend:triaged' -- control labels are managed by the triage pipeline"

# Unknown action → fails
run_test "unknown-action-fails" \
  '{"action":"not_a_real_action","reasoning":"working as intended","comment":"This is working as intended."}' \
  "" \
  "true"

# Invalid / missing JSON → fails
run_test "invalid-json-fails" \
  "this is not json" \
  "" \
  "true"

run_test "missing-json-fails" \
  "" \
  "" \
  "true"

# label_actions applied for non-control labels
run_test "label-actions-add-custom-label" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix it","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"Area label applies.","actions":[{"action":"add","label":"area/backend"}]}}' \
  '"add":"area/backend"'

run_test "label-actions-remove-custom-label" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix it","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"Stale label removed.","actions":[{"action":"remove","label":"area/old"}]}}' \
  '"remove":"area/old"'

run_test_stdout "label-actions-invalid-characters-refused" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix it","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"Injection.","actions":[{"action":"add","label":"label;injection"}]}}' \
  "::warning::Refused label 'label;injection' -- contains invalid characters"

# All label_actions refused → label reason NOT appended to comment
run_test_no_pattern "label-actions-all-refused-no-reason-in-log" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix it","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"Should not appear.","actions":[{"action":"add","label":"fullsend:ready-to-code"}]}}' \
  "Should not appear."

# Deferred label ordering: fullsend:ready-to-code appears after label_actions
run_test_label_order "ready-to-code-applied-after-label-actions" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix it","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady.","label_actions":{"reason":"Area label.","actions":[{"action":"add","label":"area/backend"}]}}' \
  '"add":"area/backend"' \
  '"add":"fullsend:ready-to-code"'

# fullsend:ready-to-code still applied when no label_actions present
run_test "ready-to-code-applied-without-label-actions" \
  '{"action":"sufficient","reasoning":"all clear","triage_summary":{"title":"Fix crash","severity":"high","category":"bug","problem":"Crash","root_cause_hypothesis":"Overflow","reproduction_steps":["step 1"],"environment":"Linux","impact":"All users","recommended_fix":"Fix it","proposed_test_case":"test_crash"},"comment":"## Triage Summary\n\nReady."}' \
  '"add":"fullsend:ready-to-code"'

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
