#!/usr/bin/env bash
# pre-fetch-prior-review-test.sh — Test SHA extraction in pre-fetch-prior-review.sh
#
# Verifies that the grep pipeline for extracting Head SHA does not crash
# under set -euo pipefail when the prior review body lacks a SHA line.
#
# Run from the repo root:
#   bash internal/scaffold/fullsend-repo/scripts/pre-fetch-prior-review-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/pre-fetch-prior-review.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# --- Helpers ---

# build_mock creates a mock gh binary that returns a preconfigured
# comment JSON from "gh api repos/.../issues/.../comments".
#   $1 — the JSON to return (empty string = no prior review)
build_mock() {
  local comment_json="$1"
  local mock_bin="${TMPDIR}/bin"

  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"

  printf '%s' "${comment_json}" > "${TMPDIR}/comment-json.txt"

  cat > "${mock_bin}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
# Mock gh — returns preconfigured comment JSON for api calls.
COMMENT_FILE="COMMENT_PLACEHOLDER"

if [[ "$1" == "api" ]]; then
  cat "${COMMENT_FILE}"
  exit 0
fi

exit 0
MOCKEOF

  local escaped="${TMPDIR//\//\\/}\/comment-json.txt"
  perl -pi -e "s/COMMENT_PLACEHOLDER/${escaped}/g" "${mock_bin}/gh"
  chmod +x "${mock_bin}/gh"

  echo "${mock_bin}"
}

# make_comment_json builds the JSON that gh api would return after
# jq filtering. The body field is set to $1. Optional $2 overrides
# the user login (defaults to the org-specific bot). Optional $3
# overrides the app client_id (defaults to Iv1.abc123).
make_comment_json() {
  local body="$1"
  local login="${2:-test-org-review[bot]}"
  local client_id="${3:-Iv1.abc123}"
  # Escape the body for JSON embedding.
  local escaped_body
  escaped_body="$(printf '%s' "${body}" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')"
  cat <<ENDJSON
{
  "id": 12345,
  "user": {"login": "${login}"},
  "body": ${escaped_body},
  "performed_via_github_app": {"client_id": "${client_id}"}
}
ENDJSON
}

run_test() {
  local test_name="$1"
  local comment_json="$2"
  local expected_sha="$3"   # expected value in prior_sha= output line
  local expect_exit="$4"    # 0 = success

  local mock_bin
  mock_bin="$(build_mock "${comment_json}")"

  local github_output="${TMPDIR}/github-output.txt"
  local workspace="${TMPDIR}/workspace"
  mkdir -p "${workspace}"
  : > "${github_output}"

  local exit_code=0
  env \
    PATH="${mock_bin}:${PATH}" \
    GH_TOKEN="fake-token" \
    ORG_NAME="test-org" \
    PR_NUM="100" \
    REVIEW_APP_CLIENT_ID="Iv1.abc123" \
    SOURCE_REPO="test-org/test-repo" \
    GITHUB_OUTPUT="${github_output}" \
    GITHUB_WORKSPACE="${workspace}" \
    bash "${SCRIPT}" > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne ${expect_exit} ]]; then
    echo "FAIL: ${test_name} — expected exit ${expect_exit}, got ${exit_code}"
    echo "--- stdout ---"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  # Check prior_sha output value.
  local actual_sha
  actual_sha="$(grep '^prior_sha=' "${github_output}" | head -1 | cut -d= -f2)"

  if [[ "${actual_sha}" != "${expected_sha}" ]]; then
    echo "FAIL: ${test_name} — expected prior_sha='${expected_sha}', got '${actual_sha}'"
    echo "--- GITHUB_OUTPUT ---"
    cat "${github_output}"
    echo "--- stdout ---"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Test cases ---

# 1. No prior review comment — script exits early with empty SHA.
run_test "no-prior-review" \
  "" \
  "" \
  0

# 2. Prior review body has a valid Head SHA line.
BODY_WITH_SHA="<!-- fullsend:review-agent -->
## Review

**Head SHA:** abc1234

Some review content here."

run_test "body-with-valid-sha" \
  "$(make_comment_json "${BODY_WITH_SHA}")" \
  "abc1234" \
  0

# 3. Prior review body WITHOUT Head SHA line — the bug this fixes.
#    Before the fix, grep would exit 1 under pipefail and crash.
BODY_NO_SHA="<!-- fullsend:review-agent -->
## Review

Some review content without any SHA line."

run_test "body-without-sha-no-crash" \
  "$(make_comment_json "${BODY_NO_SHA}")" \
  "" \
  0

# 4. Full 40-char SHA.
BODY_FULL_SHA="<!-- fullsend:review-agent -->
**Head SHA:** deadbeefdeadbeefdeadbeefdeadbeefdeadbeef

Details here."

run_test "body-with-full-sha" \
  "$(make_comment_json "${BODY_FULL_SHA}")" \
  "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" \
  0

# 5. SHA in sticky history section should NOT be extracted —
#    only the current section (before <!-- sticky:history-start -->) matters.
BODY_SHA_IN_HISTORY="<!-- fullsend:review-agent -->
## Current Review

No SHA in this section.

<!-- sticky:history-start -->
**Head SHA:** aaa1111
<!-- sticky:history-end -->"

run_test "sha-only-in-history-section" \
  "$(make_comment_json "${BODY_SHA_IN_HISTORY}")" \
  "" \
  0

# 6. Shared vendor bot identity (fullsend-ai-review[bot]) is recognized
#    when ORG_NAME differs. This is the core regression covered by #5550.
BODY_SHARED_BOT="<!-- fullsend:review-agent -->
## Review

**Head SHA:** beef123

Review from the shared vendor App."

run_test "shared-vendor-bot-identity" \
  "$(make_comment_json "${BODY_SHARED_BOT}" "fullsend-ai-review[bot]")" \
  "beef123" \
  0

# 7. Unrelated bot login should NOT match — only the org-specific or
#    shared vendor identity should be recognized.
BODY_WRONG_BOT="<!-- fullsend:review-agent -->
## Review

**Head SHA:** bad0bad

This review is from the wrong bot."

run_test "unrelated-bot-no-match" \
  "$(make_comment_json "${BODY_WRONG_BOT}" "other-org-review[bot]")" \
  "" \
  0

# 8. Shared vendor bot with a mismatched client ID should be discarded
#    by provenance validation (wrong-app path). This verifies that the
#    dual-identity matching does not bypass the provenance check.
BODY_MISMATCHED_APP="<!-- fullsend:review-agent -->
## Review

**Head SHA:** cafe456

Review from a bot with the wrong app client_id."

run_test "shared-vendor-bot-wrong-app" \
  "$(make_comment_json "${BODY_MISMATCHED_APP}" "fullsend-ai-review[bot]" "Iv1.WRONG")" \
  "" \
  0

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
