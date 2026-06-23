#!/usr/bin/env bash
# pre-code-auth-test.sh — harness test for pre-code authorization gate
set -euo pipefail

export ISSUE_NUMBER=1
export REPO_FULL_NAME="test-org/test-repo"
export GITHUB_ISSUE_URL="https://github.com/test-org/test-repo/issues/1"
export GH_TOKEN="fake"
export GITHUB_OUTPUT="$(mktemp)"

# Stub fullsend to simulate blocked auth
fullsend() {
  if [[ "$1" == "auth" ]]; then
    return 10
  fi
  return 0
}
export -f fullsend

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_FILE="$(mktemp)"
if bash "${SCRIPT_DIR}/pre-code.sh" >"${OUTPUT_FILE}" 2>&1; then
  if grep -q 'skipped=true' "${GITHUB_OUTPUT}"; then
    echo "OK: pre-code auth gate skipped agent"
    rm -f "${OUTPUT_FILE}" "${GITHUB_OUTPUT}"
    exit 0
  fi
  echo "FAIL: expected skipped=true in output" >&2
  cat "${OUTPUT_FILE}" >&2
  rm -f "${OUTPUT_FILE}"
  exit 1
fi
echo "FAIL: pre-code should exit 0 with skipped=true" >&2
cat "${OUTPUT_FILE}" >&2
rm -f "${OUTPUT_FILE}"
exit 1
