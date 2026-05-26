#!/usr/bin/env bash
# pre-fetch-prior-review-test.sh — Test pre-fetch-prior-review.sh with mock gh/jq.
#
# Uses mock gh and jq commands to simulate GitHub API responses.
# Run from the repo root:
#   bash internal/scaffold/fullsend-repo/scripts/pre-fetch-prior-review-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT_UNDER_TEST="${SCRIPT_DIR}/pre-fetch-prior-review.sh"
FAILURES=0

# Create a temp directory for test fixtures and mock state.
TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# --- Mock builder ---

# build_mock creates mock gh/jq binaries that return preconfigured API data.
# Arguments:
#   $1 — JSON to return from the gh api call (array of comment objects)
build_mock() {
  local api_response="$1"
  local mock_bin="${TMPDIR}/bin"

  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"

  # Write the API response to a file so the mock can read it.
  printf '%s' "${api_response}" > "${TMPDIR}/api-response.json"

  # Mock gh: intercepts `gh api` calls and returns the fixture data.
  # For paginated calls with --jq '.[]', we unwrap the array elements.
  cat > "${mock_bin}/gh" <<'MOCKGH'
#!/usr/bin/env bash
if [[ "$1" == "api" ]]; then
  # The real script calls: gh api <url> --paginate --jq '.[]'
  # We simulate by outputting each array element as a separate JSON object.
  RESP_FILE="RESP_PLACEHOLDER"
  python3 -c "
import json, sys
data = json.load(open('${RESP_FILE}'))
for item in data:
    print(json.dumps(item))
" 2>/dev/null || cat "${RESP_FILE}"
fi
MOCKGH

  local escaped_resp="${TMPDIR//\//\\/}\/api-response.json"
  perl -pi -e "s/RESP_PLACEHOLDER/${escaped_resp}/g" "${mock_bin}/gh"
  chmod +x "${mock_bin}/gh"

  echo "${mock_bin}"
}

# --- Test runner ---

run_test() {
  local test_name="$1"
  local api_response="$2"       # JSON array of comment objects
  local expected_sha="$3"       # expected prior_sha value (empty string for none)
  local expected_provenance="$4" # expected provenance value
  local expected_stdout="$5"    # substring expected in stdout
  local expect_exit="${6:-0}"   # expected exit code

  local mock_bin
  mock_bin="$(build_mock "${api_response}")"

  # Create workspace for GITHUB_WORKSPACE and GITHUB_OUTPUT.
  local workspace="${TMPDIR}/workspace-${test_name}"
  local output_file="${workspace}/github-output.txt"
  mkdir -p "${workspace}"
  > "${output_file}"

  local exit_code=0
  env \
    PATH="${mock_bin}:${PATH}" \
    GH_TOKEN="fake-token" \
    ORG_NAME="test-org" \
    PR_NUM="76" \
    REVIEW_APP_CLIENT_ID="Iv1.abc123" \
    SOURCE_REPO="test-org/test-repo" \
    GITHUB_WORKSPACE="${workspace}" \
    GITHUB_OUTPUT="${output_file}" \
    bash "${SCRIPT_UNDER_TEST}" > "${workspace}/stdout.log" 2>&1 || exit_code=$?

  # Check exit code.
  if [[ ${exit_code} -ne ${expect_exit} ]]; then
    echo "FAIL: ${test_name} — expected exit ${expect_exit}, got ${exit_code}"
    echo "--- stdout ---"
    cat "${workspace}/stdout.log"
    echo "--- GITHUB_OUTPUT ---"
    cat "${output_file}" 2>/dev/null || echo "(empty)"
    FAILURES=$((FAILURES + 1))
    return
  fi

  # Check prior_sha output.
  local actual_sha
  actual_sha="$(grep '^prior_sha=' "${output_file}" | tail -1 | cut -d= -f2-)"
  if [[ "${actual_sha}" != "${expected_sha}" ]]; then
    echo "FAIL: ${test_name} — prior_sha: expected '${expected_sha}', got '${actual_sha}'"
    echo "--- stdout ---"
    cat "${workspace}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  # Check provenance output.
  local actual_prov
  actual_prov="$(grep '^prior_review_provenance=' "${output_file}" | tail -1 | cut -d= -f2-)"
  if [[ "${actual_prov}" != "${expected_provenance}" ]]; then
    echo "FAIL: ${test_name} — provenance: expected '${expected_provenance}', got '${actual_prov}'"
    echo "--- stdout ---"
    cat "${workspace}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  # Check expected stdout substring.
  if [[ -n "${expected_stdout}" ]]; then
    if ! grep -qF "${expected_stdout}" "${workspace}/stdout.log" 2>/dev/null; then
      echo "FAIL: ${test_name} — expected stdout '${expected_stdout}' not found"
      echo "--- stdout ---"
      cat "${workspace}/stdout.log"
      FAILURES=$((FAILURES + 1))
      return
    fi
  fi

  echo "PASS: ${test_name}"
}

# --- Test cases ---

# 1. No prior comment exists → empty prior_sha, provenance=none.
run_test "no-prior-comment" \
  "[]" \
  "" \
  "none" \
  "No prior review comment found"

# 2. Valid comment with Head SHA marker → correct SHA extracted.
run_test "valid-comment-with-sha" \
  '[{
    "id": 1001,
    "user": {"login": "test-org-review[bot]"},
    "body": "<!-- fullsend:review-agent -->\n\n**Head SHA:** abc1234def5678\n\nReview looks good.",
    "performed_via_github_app": {"client_id": "Iv1.abc123"}
  }]' \
  "abc1234def5678" \
  "app-verified" \
  "Prior review SHA: abc1234def5678"

# 3. Comment with failed provenance (no app) → empty prior_sha, warning.
run_test "no-app-provenance" \
  '[{
    "id": 2001,
    "user": {"login": "test-org-review[bot]"},
    "body": "<!-- fullsend:review-agent -->\n\n**Head SHA:** abc1234\n\nReview.",
    "performed_via_github_app": null
  }]' \
  "" \
  "unverifiable-no-app" \
  "has no GitHub App provenance"

# 4. Comment with wrong app client_id → empty prior_sha, error.
run_test "wrong-app-provenance" \
  '[{
    "id": 3001,
    "user": {"login": "test-org-review[bot]"},
    "body": "<!-- fullsend:review-agent -->\n\n**Head SHA:** abc1234\n\nReview.",
    "performed_via_github_app": {"client_id": "Iv1.wrong999"}
  }]' \
  "" \
  "unverifiable-wrong-app" \
  "wrong app"

# 5. Comment with missing Head SHA marker → empty prior_sha, warning logged.
#    This is the bug scenario from issue #1447: script should not crash.
run_test "missing-head-sha-marker" \
  '[{
    "id": 4001,
    "user": {"login": "test-org-review[bot]"},
    "body": "<!-- fullsend:review-agent -->\n\nReview looks good but no SHA marker here.",
    "performed_via_github_app": {"client_id": "Iv1.abc123"}
  }]' \
  "" \
  "app-verified" \
  "has no Head SHA marker"

# 6. Head SHA only inside sticky history section → should not be extracted.
run_test "sha-only-in-sticky-history" \
  '[{
    "id": 5001,
    "user": {"login": "test-org-review[bot]"},
    "body": "<!-- fullsend:review-agent -->\n\nNo SHA here.\n<!-- sticky:history-start -->\n**Head SHA:** aaa0123456789\n<!-- sticky:history-end -->",
    "performed_via_github_app": {"client_id": "Iv1.abc123"}
  }]' \
  "" \
  "app-verified" \
  "has no Head SHA marker"

# 7. Multiple comments from bot → last one used.
run_test "multiple-comments-uses-last" \
  '[{
    "id": 6001,
    "user": {"login": "test-org-review[bot]"},
    "body": "<!-- fullsend:review-agent -->\n\n**Head SHA:** aaa1234bbb5678\n\nFirst review.",
    "performed_via_github_app": {"client_id": "Iv1.abc123"}
  },
  {
    "id": 6002,
    "user": {"login": "test-org-review[bot]"},
    "body": "<!-- fullsend:review-agent -->\n\n**Head SHA:** ccc789abcdef12\n\nSecond review.",
    "performed_via_github_app": {"client_id": "Iv1.abc123"}
  }]' \
  "ccc789abcdef12" \
  "app-verified" \
  "Prior review SHA: ccc789abcdef12"

# 8. Comment from wrong bot user → no prior review found.
run_test "wrong-bot-user" \
  '[{
    "id": 7001,
    "user": {"login": "other-org-review[bot]"},
    "body": "<!-- fullsend:review-agent -->\n\n**Head SHA:** abc1234\n\nReview.",
    "performed_via_github_app": {"client_id": "Iv1.abc123"}
  }]' \
  "" \
  "none" \
  "No prior review comment found"

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
