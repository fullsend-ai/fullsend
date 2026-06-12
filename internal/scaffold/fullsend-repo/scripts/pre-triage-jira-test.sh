#!/usr/bin/env bash
# pre-triage-jira-test.sh — Test pre-triage-jira.sh with mock curl responses.
#
# Uses a mock curl command to capture calls without hitting a real Jira instance.
# Run from the repo root: bash internal/scaffold/fullsend-repo/scripts/pre-triage-jira-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PRE_SCRIPT="${SCRIPT_DIR}/pre-triage-jira.sh"
FAILURES=0

# Create a temp directory for test fixtures and mock state.
TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# Mock bin directory.
MOCK_BIN="${TMPDIR}/bin"
mkdir -p "${MOCK_BIN}"

# curl call log — records every invocation made by the pre-script.
CURL_LOG="${TMPDIR}/curl-calls.log"

# Build a mock curl that:
#  - Records all calls to CURL_LOG (path baked in at heredoc-write time)
#  - Returns fixture Jira API responses based on the URL path
cat > "${MOCK_BIN}/curl" <<MOCKEOF
#!/usr/bin/env bash
echo "curl \$*" >> "${CURL_LOG}"

URL=""
PREV=""
for arg in "\$@"; do
  case "\${arg}" in
    http*) URL="\${arg}" ;;
  esac
  PREV="\${arg}"
done

if [[ "\${URL}" == */issue/PROJ-123* ]] && [[ "\${URL}" != */comment* ]] && [[ "\${URL}" != */transitions* ]] && [[ "\${URL}" != */issueLink* ]]; then
  cat <<'JSON'
{"key":"PROJ-123","fields":{"summary":"Login button does not respond on mobile","description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Steps."}]}]},"status":{"name":"Open"},"priority":{"name":"High"},"issuetype":{"name":"Bug"},"reporter":{"emailAddress":"reporter@example.com"},"labels":[],"created":"2024-01-01T00:00:00.000Z","updated":"2024-01-02T00:00:00.000Z","parent":null,"issuelinks":[],"project":{"key":"PROJ","name":"My Project"}}}
JSON
elif [[ "\${URL}" == */search* ]]; then
  echo '{"issues":[]}'
elif [[ "\${URL}" == */comment* ]]; then
  echo '{"comments":[]}'
elif [[ "\${URL}" == */project/PROJ* ]]; then
  echo '{"issueTypes":[{"name":"Bug","subtask":false,"hierarchyLevel":0,"description":"A bug"}]}'
else
  echo '{}'
fi
MOCKEOF
chmod +x "${MOCK_BIN}/curl"

export PATH="${MOCK_BIN}:${PATH}"

# --- Helper functions ---

run_test() {
  local test_name="$1"
  local expect_failure="${2:-false}"
  local extra_check="${3:-}"   # optional: function name to call for extra assertions

  # Reset curl log.
  : > "${CURL_LOG}"

  # Run the pre-script.
  local exit_code=0
  (
    export PATH="${MOCK_BIN}:${PATH}"
    bash "${PRE_SCRIPT}"
  ) > "${TMPDIR}/stdout-${test_name}.log" 2>&1 || exit_code=$?

  if [[ "${expect_failure}" == "true" ]]; then
    if [[ ${exit_code} -eq 0 ]]; then
      echo "FAIL: ${test_name} — expected failure but got success"
      cat "${TMPDIR}/stdout-${test_name}.log"
      FAILURES=$((FAILURES + 1))
      return
    fi
    echo "PASS: ${test_name} (expected failure, got exit code ${exit_code})"
    return
  fi

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TMPDIR}/stdout-${test_name}.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ -n "${extra_check}" ]]; then
    if ! "${extra_check}" "${test_name}"; then
      FAILURES=$((FAILURES + 1))
      return
    fi
  fi

  echo "PASS: ${test_name}"
}

run_test_curl_pattern() {
  local test_name="$1"
  local expected_pattern="$2"

  : > "${CURL_LOG}"

  local exit_code=0
  (
    export PATH="${MOCK_BIN}:${PATH}"
    bash "${PRE_SCRIPT}"
  ) > "${TMPDIR}/stdout-${test_name}.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TMPDIR}/stdout-${test_name}.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if ! grep -qF -- "${expected_pattern}" "${CURL_LOG}"; then
    echo "FAIL: ${test_name} — expected curl call pattern '${expected_pattern}' not found"
    echo "Actual curl calls:"
    cat "${CURL_LOG}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Set up base env ---

export JIRA_HOST="myorg.atlassian.net"
export JIRA_EMAIL="agent@example.com"
export JIRA_API_TOKEN="fake-token"

# --- Test: valid ISSUE_KEY accepted ---

export ISSUE_KEY="PROJ-123"
run_test "valid-issue-key-accepted"

# --- Test: invalid ISSUE_KEY formats rejected ---

ORIG_KEY="${ISSUE_KEY}"

export ISSUE_KEY="abc123"
run_test "invalid-key-all-lowercase" "true"

export ISSUE_KEY="proj-123"
run_test "invalid-key-lowercase-project" "true"

export ISSUE_KEY="123"
run_test "invalid-key-numeric-only" "true"

export ISSUE_KEY="PROJ-"
run_test "invalid-key-no-number" "true"

export ISSUE_KEY="-123"
run_test "invalid-key-no-project" "true"

export ISSUE_KEY="${ORIG_KEY}"

# --- Test: missing required env vars rejected ---

export ISSUE_KEY="PROJ-123"

ORIG_HOST="${JIRA_HOST}"
unset JIRA_HOST
run_test "missing-jira-host-fails" "true"
export JIRA_HOST="${ORIG_HOST}"

ORIG_EMAIL="${JIRA_EMAIL}"
unset JIRA_EMAIL
run_test "missing-jira-email-fails" "true"
export JIRA_EMAIL="${ORIG_EMAIL}"

ORIG_TOKEN="${JIRA_API_TOKEN}"
unset JIRA_API_TOKEN
run_test "missing-jira-api-token-fails" "true"
export JIRA_API_TOKEN="${ORIG_TOKEN}"

# --- Test: curl called with correct Jira API endpoint for issue fetch ---

export ISSUE_KEY="PROJ-123"
run_test_curl_pattern "curl-called-with-issue-endpoint" \
  "${JIRA_HOST}/rest/api/3/issue/PROJ-123"

# --- Test: curl called with PUT for label stripping ---

export ISSUE_KEY="PROJ-123"
run_test_curl_pattern "curl-called-with-put-for-label-strip" \
  "-X PUT"

# --- Test: label strip targets fullsend: namespace labels ---

export ISSUE_KEY="PROJ-123"
: > "${CURL_LOG}"
(
  export PATH="${MOCK_BIN}:${PATH}"
  bash "${PRE_SCRIPT}"
) > /dev/null 2>&1 || true

if grep -q "fullsend:needs-info" "${CURL_LOG}"; then
  echo "PASS: label-strip-targets-fullsend-namespace"
else
  echo "FAIL: label-strip-targets-fullsend-namespace — 'fullsend:needs-info' not found in curl calls"
  echo "Actual curl calls:"
  cat "${CURL_LOG}"
  FAILURES=$((FAILURES + 1))
fi

# --- Test: issue-context.json is written to the host temp path ---

export ISSUE_KEY="PROJ-123"

(
  export PATH="${MOCK_BIN}:${PATH}"
  bash "${PRE_SCRIPT}"
) > /dev/null 2>&1

CONTEXT_FILE="/tmp/fullsend-triage-context-${ISSUE_KEY}.json"
if [[ -f "${CONTEXT_FILE}" ]]; then
  echo "PASS: issue-context-json-written"
else
  echo "FAIL: issue-context-json-written — ${CONTEXT_FILE} not found"
  FAILURES=$((FAILURES + 1))
fi

# --- Test: issue-context.json contains required fields ---

if [[ -f "${CONTEXT_FILE}" ]] && jq empty "${CONTEXT_FILE}" 2>/dev/null; then
  SOURCE=$(jq -r '.source' "${CONTEXT_FILE}")
  KEY=$(jq -r '.key' "${CONTEXT_FILE}")
  HAS_RELATED=$(jq 'has("related_issues")' "${CONTEXT_FILE}")

  if [[ "${SOURCE}" == "jira" && "${KEY}" == "PROJ-123" && "${HAS_RELATED}" == "true" ]]; then
    echo "PASS: issue-context-json-has-required-fields"
  else
    echo "FAIL: issue-context-json-has-required-fields — source=${SOURCE} key=${KEY} has_related=${HAS_RELATED}"
    FAILURES=$((FAILURES + 1))
  fi
else
  echo "FAIL: issue-context-json-has-required-fields — file missing or invalid JSON"
  FAILURES=$((FAILURES + 1))
fi

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
