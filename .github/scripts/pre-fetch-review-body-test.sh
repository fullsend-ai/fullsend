#!/usr/bin/env bash
# pre-fetch-review-body-test.sh — Tests for pre-fetch-review-body.sh
#
# Run from the repo root:
#   bash .github/scripts/pre-fetch-review-body-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/pre-fetch-review-body.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

ORG_BOT="acme-review[bot]"
SHARED_BOT="fullsend-ai-review[bot]"
CUSTOM_BOT="custom-prefix-review[bot]"

# review emits one review object.
#   $1 — id, $2 — login, $3 — state, $4 — body
review() {
  jq -cn --argjson id "$1" --arg login "$2" --arg state "$3" --arg body "$4" \
    '{id: $id, user: {login: $login}, state: $state, body: $body}'
}

# filler emits N COMMENTED reviews from a human reviewer starting at id $2.
filler() {
  local n="$1" start="$2" i
  for ((i = 0; i < n; i++)); do
    review "$((start + i))" "human-reviewer" "COMMENTED" "comment $((start + i))"
  done
}

# write_page writes the reviews read from stdin as a JSON array page file.
#   $1 — page number
write_page() {
  jq -s '.' > "${TMPDIR}/page-$1.json"
}

reset_pages() {
  rm -f "${TMPDIR}"/page-*.json
}

# The mock gh mimics `gh api`: with --paginate it emits every page as a
# separate JSON array (the shape gh produces); without it, only page 1.
# A sentinel file makes the API call fail.
MOCK_BIN="${TMPDIR}/bin"
mkdir -p "${MOCK_BIN}"
cat > "${MOCK_BIN}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
MOCK_DIR="$(cd "$(dirname "$0")/.." && pwd)"
if [[ "$1" != "api" ]]; then
  echo "unexpected gh call: $*" >&2
  exit 1
fi
shift
paginate=false endpoint=""
for arg in "$@"; do
  case "${arg}" in
    --paginate) paginate=true ;;
    -*) echo "mock gh: unexpected flag: ${arg}" >&2; exit 1 ;;
    *) endpoint="${arg}" ;;
  esac
done
if [[ "${endpoint}" != "repos/org/repo/pulls/123/reviews"* ]]; then
  echo "mock gh: unexpected endpoint: ${endpoint}" >&2
  exit 1
fi
if [[ -f "${MOCK_DIR}/fail" ]]; then
  echo "HTTP 502: Bad Gateway" >&2
  exit 1
fi
shopt -s nullglob
pages=("${MOCK_DIR}"/page-*.json)
if [[ ${#pages[@]} -eq 0 ]]; then
  echo "[]"
  exit 0
fi
if [[ "${paginate}" == "true" ]]; then
  for p in $(printf '%s\n' "${pages[@]}" | sort -V); do
    cat "${p}"
  done
else
  cat "${MOCK_DIR}/page-1.json"
fi
MOCKEOF
chmod +x "${MOCK_BIN}/gh"

# run_test runs the script against the current mock pages.
#   $1 — test name
#   $2 — expected exit code
#   $3 — trigger source
#   $4 — expected review body (exact; ignored when expected exit is non-zero)
#   $5 — expected output substring (optional)
#   $6 — FULLSEND_APP_SET override (optional; default custom-prefix)
run_test() {
  local name="$1" expected_exit="$2" trigger="$3" expected_body="$4"
  local expected_output="${5:-}" app_set="${6-custom-prefix}"
  local workspace="${TMPDIR}/workspace"
  rm -rf "${workspace}"
  mkdir -p "${workspace}"
  : > "${TMPDIR}/github-output"

  local actual_exit=0 output
  output=$(PATH="${MOCK_BIN}:${PATH}" \
    GH_TOKEN="fake" \
    SOURCE_REPO="org/repo" \
    PR_NUM="123" \
    TRIGGER_SOURCE="${trigger}" \
    ORG_NAME="acme" \
    FULLSEND_APP_SET="${app_set}" \
    GITHUB_WORKSPACE="${workspace}" \
    GITHUB_OUTPUT="${TMPDIR}/github-output" \
    bash "${SCRIPT}" 2>&1) || actual_exit=$?

  if [[ "${actual_exit}" -ne "${expected_exit}" ]]; then
    echo "FAIL: ${name} — expected exit ${expected_exit}, got ${actual_exit}"
    echo "${output}" | sed 's/^/    /'
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ -n "${expected_output}" ]] && [[ "${output}" != *"${expected_output}"* ]]; then
    echo "FAIL: ${name} — expected output '${expected_output}' not found"
    echo "${output}" | sed 's/^/    /'
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ "${expected_exit}" -eq 0 ]]; then
    local actual_body
    actual_body="$(cat "${workspace}/review-body.txt")"
    if [[ "${actual_body}" != "${expected_body}" ]]; then
      echo "FAIL: ${name} — expected body '${expected_body}', got '${actual_body}'"
      FAILURES=$((FAILURES + 1))
      return
    fi
    if ! grep -qx "review_file=${workspace}/review-body.txt" "${TMPDIR}/github-output"; then
      echo "FAIL: ${name} — review_file output not written"
      FAILURES=$((FAILURES + 1))
      return
    fi
  fi

  echo "PASS: ${name}"
}

echo "=== pre-fetch-review-body tests ==="

# #8353: 45 reviews across two pages. The first 30 contain no change
# request; the review-bot change requests are on page 2.
reset_pages
filler 30 1 | write_page 1
{
  filler 5 31
  review 36 "${ORG_BOT}" "CHANGES_REQUESTED" "first change request"
  filler 4 37
  review 41 "${ORG_BOT}" "CHANGES_REQUESTED" "latest change request"
  review 42 "${ORG_BOT}" "COMMENTED" "bot comment after change request"
  review 43 "human-reviewer" "CHANGES_REQUESTED" "human change request"
  filler 2 44
} | write_page 2
run_test "change request on second page is found" 0 "${ORG_BOT}" "latest change request" \
  "Pre-fetched review body: 22 bytes"

# Single page with a match.
reset_pages
{
  review 1 "human-reviewer" "COMMENTED" "looks fine"
  review 2 "${ORG_BOT}" "CHANGES_REQUESTED" "single page change request"
} | write_page 1
run_test "single page match" 0 "${ORG_BOT}" "single page change request"

# Latest match wins when change requests span pages.
reset_pages
{
  review 1 "${ORG_BOT}" "CHANGES_REQUESTED" "page one change request"
  filler 29 2
} | write_page 1
{
  filler 3 31
  review 34 "${ORG_BOT}" "CHANGES_REQUESTED" "page two change request"
} | write_page 2
run_test "latest change request across pages wins" 0 "${ORG_BOT}" "page two change request"

# Human change requests and other bots are ignored.
reset_pages
{
  review 1 "${ORG_BOT}" "CHANGES_REQUESTED" "bot change request"
  review 2 "human-reviewer" "CHANGES_REQUESTED" "human change request"
  review 3 "other-review[bot]" "CHANGES_REQUESTED" "other bot change request"
} | write_page 1
run_test "non-review-bot reviewers ignored" 0 "${ORG_BOT}" "bot change request"

# All three accepted review-bot identities are recognized.
for bot in "${ORG_BOT}" "${SHARED_BOT}" "${CUSTOM_BOT}"; do
  reset_pages
  {
    filler 30 1
  } | write_page 1
  review 31 "${bot}" "CHANGES_REQUESTED" "from ${bot}" | write_page 2
  run_test "accepted identity ${bot}" 0 "${bot}" "from ${bot}"
done

# Without FULLSEND_APP_SET, the custom identity is not recognized.
reset_pages
review 1 "${CUSTOM_BOT}" "CHANGES_REQUESTED" "custom" | write_page 1
run_test "custom identity ignored without app set" 0 "human-user" "" "" ""

# No change request: bot-triggered runs fail, human-triggered runs proceed.
reset_pages
filler 30 1 | write_page 1
filler 15 31 | write_page 2
run_test "no match fails bot-triggered run" 1 "${ORG_BOT}" "" \
  "Bot-triggered run but review body is empty"
run_test "no match proceeds for human trigger" 0 "human-user" "" \
  "Pre-fetched review body: 1 bytes"

# No reviews at all.
reset_pages
run_test "no reviews proceeds for human trigger" 0 "human-user" ""

# API failure is surfaced, then handled as an empty body.
reset_pages
review 1 "${ORG_BOT}" "CHANGES_REQUESTED" "unreachable" | write_page 1
touch "${TMPDIR}/fail"
run_test "API failure warns and fails bot-triggered run" 1 "${ORG_BOT}" "" \
  "::warning::Failed to fetch reviews for PR #123: HTTP 502: Bad Gateway"
run_test "API failure warns and proceeds for human trigger" 0 "human-user" "" \
  "::warning::Failed to fetch reviews for PR #123"
rm -f "${TMPDIR}/fail"

# Oversized body is rejected.
reset_pages
jq -n --arg login "${ORG_BOT}" \
  '[{id: 1, user: {login: $login}, state: "CHANGES_REQUESTED", body: ("x" * 1048577)}]' \
  > "${TMPDIR}/page-1.json"
run_test "oversized body rejected" 1 "${ORG_BOT}" "" "::error::Review body is"

echo ""
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
