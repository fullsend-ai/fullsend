#!/usr/bin/env bash
# sanitize-artifacts-test.sh — Test sanitize-artifacts.sh redaction logic.
#
# Run from the repo root: bash internal/scaffold/fullsend-repo/scripts/sanitize-artifacts-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SANITIZE_SCRIPT="${SCRIPT_DIR}/sanitize-artifacts.sh"
FAILURES=0

TEST_TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TEST_TMPDIR}"' EXIT

# --- Fixtures ---

# Transcript JSONL with private data
TRANSCRIPT_FIXTURE='{"message":{"content":[{"type":"tool_result","content":"{\"source\":\"jira\",\"key\":\"PROJ-123\",\"summary\":\"Add login\",\"description\":\"Private issue body with secrets\",\"reporter\":\"alice@example.com\",\"host\":\"redhat.atlassian.net\",\"comments\":[{\"author\":\"bob@corp.com\",\"body\":\"internal comment\"}],\"linked_issues\":[{\"key\":\"PROJ-456\",\"description\":\"linked desc\"}],\"parent\":{\"key\":\"PROJ-100\",\"description\":\"parent body\"}}"},{"type":"text","text":"User alice@example.com reported this on redhat.atlassian.net"},{"type":"thinking","thinking":"I see https://redhat-internal.slack.com/archives/C123 is related"},{"type":"tool_use","input":{"query":"check https://docs.google.com/document/d/secret123"}}]}}'

# agent-result.json with emails
RESULT_FIXTURE='{
  "summary": "Found issue at staging.atlassian.net reported by dev@company.com",
  "confidence": {"overall": 85},
  "technical_landscape": {
    "overview": "Check https://redhat-internal.slack.com/foo for details"
  }
}'

run_test() {
  local test_name="$1"

  local input_dir="${TEST_TMPDIR}/input-${test_name}"
  local output_dir="${TEST_TMPDIR}/output-${test_name}"
  mkdir -p "${input_dir}/iteration-1/output"

  echo "${TRANSCRIPT_FIXTURE}" > "${input_dir}/iteration-1/transcript.jsonl"
  echo "${RESULT_FIXTURE}" > "${input_dir}/iteration-1/output/agent-result.json"

  local exit_code=0
  bash "${SANITIZE_SCRIPT}" "${input_dir}" "${output_dir}" > "${TEST_TMPDIR}/stdout-${test_name}.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — exit code ${exit_code}"
    cat "${TEST_TMPDIR}/stdout-${test_name}.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

assert_file_contains() {
  local test_name="$1" file="$2" pattern="$3"
  if ! grep -qF "${pattern}" "${file}"; then
    echo "FAIL: ${test_name} — expected '${pattern}' in $(basename "${file}")"
    FAILURES=$((FAILURES + 1))
  fi
}

assert_file_not_contains() {
  local test_name="$1" file="$2" pattern="$3"
  if grep -qF "${pattern}" "${file}"; then
    echo "FAIL: ${test_name} — unexpected '${pattern}' in $(basename "${file}")"
    FAILURES=$((FAILURES + 1))
  fi
}

# --- Tests ---

# 1. Transcript redaction
run_test "transcript-redaction"
SANITIZED_TRANSCRIPT="${TEST_TMPDIR}/output-transcript-redaction/iteration-1/transcript.jsonl"

# Emails should be redacted
assert_file_not_contains "transcript-emails" "$SANITIZED_TRANSCRIPT" "alice@example.com"
assert_file_not_contains "transcript-emails" "$SANITIZED_TRANSCRIPT" "bob@corp.com"
assert_file_contains "transcript-emails" "$SANITIZED_TRANSCRIPT" "[redacted-email]"

# Atlassian hosts should be redacted
assert_file_not_contains "transcript-hosts" "$SANITIZED_TRANSCRIPT" "redhat.atlassian.net"
assert_file_contains "transcript-hosts" "$SANITIZED_TRANSCRIPT" "[redacted-host]"

# Issue context fields should be redacted
assert_file_not_contains "transcript-body" "$SANITIZED_TRANSCRIPT" "Private issue body with secrets"
assert_file_contains "transcript-body" "$SANITIZED_TRANSCRIPT" "[redacted"

# Internal URLs should be redacted
assert_file_not_contains "transcript-slack" "$SANITIZED_TRANSCRIPT" "redhat-internal.slack.com"
assert_file_not_contains "transcript-gdocs" "$SANITIZED_TRANSCRIPT" "docs.google.com"

# Structural metadata should survive
assert_file_contains "transcript-metadata" "$SANITIZED_TRANSCRIPT" "PROJ-123"
assert_file_contains "transcript-metadata" "$SANITIZED_TRANSCRIPT" "Add login"

# 2. agent-result.json redaction (text scrub only)
SANITIZED_RESULT="${TEST_TMPDIR}/output-transcript-redaction/iteration-1/output/agent-result.json"

assert_file_not_contains "result-emails" "$SANITIZED_RESULT" "dev@company.com"
assert_file_not_contains "result-hosts" "$SANITIZED_RESULT" "staging.atlassian.net"
assert_file_not_contains "result-slack" "$SANITIZED_RESULT" "redhat-internal.slack.com"
assert_file_contains "result-structure" "$SANITIZED_RESULT" "confidence"
assert_file_contains "result-structure" "$SANITIZED_RESULT" "overall"

# 3. Missing arguments should fail
if bash "${SANITIZE_SCRIPT}" 2>/dev/null; then
  echo "FAIL: missing-args — expected failure"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: missing-args (expected failure)"
fi

if [[ ${FAILURES} -gt 0 ]]; then
  echo ""
  echo "${FAILURES} test(s) failed."
  exit 1
fi

echo ""
echo "All sanitize-artifacts tests passed."
