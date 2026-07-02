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

# build_mock creates a mock gh binary that returns preconfigured JSON
# from "gh api repos/.../issues/.../comments" and
# "gh api repos/.../pulls/.../reviews".
#   $1 — comment JSON to return (empty string = no prior review)
#   $2 — review JSON stream to return (empty string = no reviews)
build_mock() {
  local comment_json="$1"
  local review_json="${2:-}"
  local mock_bin="${TMPDIR}/bin"

  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"

  printf '%s' "${comment_json}" > "${TMPDIR}/comment-json.txt"
  printf '%s' "${review_json}" > "${TMPDIR}/review-json.txt"

  cat > "${mock_bin}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
# Mock gh — returns preconfigured comment JSON for api calls.
COMMENT_FILE="COMMENT_PLACEHOLDER"
REVIEW_FILE="REVIEW_PLACEHOLDER"

if [[ "$1" == "api" ]]; then
  case "${2:-}" in
    repos/*/issues/*/comments)
      cat "${COMMENT_FILE}"
      ;;
    repos/*/pulls/*/reviews)
      cat "${REVIEW_FILE}"
      ;;
    *)
      cat "${COMMENT_FILE}"
      ;;
  esac
  exit 0
fi

exit 0
MOCKEOF

  local escaped="${TMPDIR//\//\\/}\/comment-json.txt"
  perl -pi -e "s/COMMENT_PLACEHOLDER/${escaped}/g" "${mock_bin}/gh"
  escaped="${TMPDIR//\//\\/}\/review-json.txt"
  perl -pi -e "s/REVIEW_PLACEHOLDER/${escaped}/g" "${mock_bin}/gh"
  chmod +x "${mock_bin}/gh"

  echo "${mock_bin}"
}

# make_comment_json builds the JSON that gh api would return after
# jq filtering. The body field is set to $1.
make_comment_json() {
  local body="$1"
  # Escape the body for JSON embedding.
  local escaped_body
  escaped_body="$(printf '%s' "${body}" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')"
  cat <<ENDJSON
{
  "id": 12345,
  "user": {"login": "test-org-review[bot]"},
  "body": ${escaped_body},
  "performed_via_github_app": {"client_id": "Iv1.abc123"}
}
ENDJSON
}

make_review_json() {
  local user="$1"
  local state="$2"
  cat <<ENDJSON
{
  "id": 999,
  "user": {"login": "${user}"},
  "state": "${state}"
}
ENDJSON
}

run_test() {
  local test_name="$1"
  local comment_json="$2"
  local expected_sha="$3"   # expected value in prior_sha= output line
  local expect_exit="$4"    # 0 = success
  local review_json="${5:-}"
  local expected_active="${6:-false}"

  local mock_bin
  mock_bin="$(build_mock "${comment_json}" "${review_json}")"

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
  actual_sha="$(grep '^prior_sha=' "${github_output}" | head -1 | cut -d= -f2 || true)"

  if [[ "${actual_sha}" != "${expected_sha}" ]]; then
    echo "FAIL: ${test_name} — expected prior_sha='${expected_sha}', got '${actual_sha}'"
    echo "--- GITHUB_OUTPUT ---"
    cat "${github_output}"
    echo "--- stdout ---"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  local actual_active
  actual_active="$(grep '^prior_active_changes_requested=' "${github_output}" | head -1 | cut -d= -f2 || true)"

  if [[ "${actual_active}" != "${expected_active}" ]]; then
    echo "FAIL: ${test_name} — expected prior_active_changes_requested='${expected_active}', got '${actual_active}'"
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

# 6. Existing bot CHANGES_REQUESTED review is active and visible.
run_test "active-changes-requested-review" \
  "$(make_comment_json "${BODY_WITH_SHA}")" \
  "abc1234" \
  0 \
  "$(make_review_json "test-org-review[bot]" "CHANGES_REQUESTED")" \
  "true"

# 7. Prior bot review was dismissed, so old findings are not visible
#    through the formal review UI.
run_test "dismissed-changes-requested-review" \
  "$(make_comment_json "${BODY_WITH_SHA}")" \
  "abc1234" \
  0 \
  "$(make_review_json "test-org-review[bot]" "DISMISSED")" \
  "false"

# 8. A human CHANGES_REQUESTED review does not make the bot's prior
#    findings visible.
run_test "human-changes-requested-review" \
  "$(make_comment_json "${BODY_WITH_SHA}")" \
  "abc1234" \
  0 \
  "$(make_review_json "human-reviewer" "CHANGES_REQUESTED")" \
  "false"

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
