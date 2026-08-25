#!/usr/bin/env bash
# check-fix-eligibility-test.sh — Tests for check-fix-eligibility.sh
#
# Run from the repo root:
#   bash .github/scripts/check-fix-eligibility-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/check-fix-eligibility.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# build_mock creates a mock gh binary that returns preconfigured PR JSON.
#   $1 — is_bot value (true/false/null)
#   $2 — login value
#   $3 — comma-separated labels (optional)
#   $4 — reviews API response body (optional, default "[]"); the literal
#        string "FAIL" makes the mock reviews call exit 1, simulating an
#        API error
#   $5 — issues/comments API response body, for marker search (optional,
#        default "[]")
build_mock() {
  local is_bot="$1" login="$2" labels="${3:-}"
  local reviews_json="${4:-[]}" comments_json="${5:-[]}"
  local mock_bin="${TMPDIR}/bin"

  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"
  rm -f "${TMPDIR}/posted-comment-body.txt" "${TMPDIR}/reviews-fail"

  local labels_json="[]"
  if [[ -n "${labels}" ]]; then
    labels_json=$(printf '%s' "${labels}" | jq -R 'split(",")')
  fi

  local json
  json=$(jq -n \
    --argjson is_bot "${is_bot}" \
    --arg login "${login}" \
    --argjson labels "${labels_json}" \
    '{labels: $labels, is_bot: $is_bot, login: $login}')

  printf '%s' "${json}" > "${TMPDIR}/pr-json.txt"
  printf '%s' "${TMPDIR}/pr-json.txt" > "${TMPDIR}/pr-json-path"

  if [[ "${reviews_json}" == "FAIL" ]]; then
    : > "${TMPDIR}/reviews-fail"
  else
    printf '%s' "${reviews_json}" > "${TMPDIR}/reviews-json.txt"
  fi
  printf '%s' "${comments_json}" > "${TMPDIR}/comments-json.txt"

  cat > "${mock_bin}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
MOCK_DIR="$(cd "$(dirname "$0")/.." && pwd)"
PR_JSON_FILE="$(cat "${MOCK_DIR}/pr-json-path")"
if [[ "$1" == "pr" && "$2" == "view" ]]; then
  shift 2
  json_arg="" jq_arg=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --repo) shift ;;
      --json) shift; json_arg="$1" ;;
      --jq)   shift; jq_arg="$1" ;;
      *)      ;;
    esac
    shift
  done
  if [[ "${json_arg}" != "labels,author" ]]; then
    echo "mock gh: unexpected --json arg: ${json_arg}" >&2
    exit 1
  fi
  if [[ "${jq_arg}" != '{labels: [.labels[].name], is_bot: .author.is_bot, login: .author.login}' ]]; then
    echo "mock gh: unexpected --jq arg: ${jq_arg}" >&2
    exit 1
  fi
  cat "${PR_JSON_FILE}"
  exit 0
fi
if [[ "$1" == "api" ]]; then
  if [[ "$2" == "-X" && "$3" == "POST" && "$4" == *"/comments" ]]; then
    cat > "${MOCK_DIR}/posted-comment-body.txt"
    echo '{"id": 999}'
    exit 0
  fi
  if [[ "$2" == *"/reviews" ]]; then
    if [[ -f "${MOCK_DIR}/reviews-fail" ]]; then
      echo "mock gh: simulated reviews API failure" >&2
      exit 1
    fi
    cat "${MOCK_DIR}/reviews-json.txt"
    exit 0
  fi
  if [[ "$2" == *"/comments" ]]; then
    cat "${MOCK_DIR}/comments-json.txt"
    exit 0
  fi
fi
echo "unexpected gh call: $*" >&2
exit 1
MOCKEOF

  chmod +x "${mock_bin}/gh"

  echo "${mock_bin}"
}

# run_test runs the eligibility script with mocked PR data and asserts exit code
# and (optionally) annotation text.
#   $1  — test name
#   $2  — expected exit code
#   $3  — is_bot value
#   $4  — login value
#   $5  — trigger source
#   $6  — labels (optional, comma-separated)
#   $7  — expected annotation substring (optional)
#   $8  — reviews API response body (optional, default "[]"; "FAIL"
#         simulates an API error)
#   $9  — issues/comments API response body (optional, default "[]")
#   $10 — REVIEW_MAX_FIX_CYCLES value to set (optional; empty leaves the
#         script's own default in effect)
#   $11 — "yes"/"no" to assert whether a fix-cycle-cap comment was (not)
#         posted (optional)
run_test() {
  local name="$1" expected_exit="$2" is_bot="$3" login="$4" trigger="$5" labels="${6:-}" expected_annotation="${7:-}"
  local reviews_json="${8:-[]}" comments_json="${9:-[]}" cap_env="${10:-}" expect_post="${11:-}"
  local mock_bin
  mock_bin=$(build_mock "${is_bot}" "${login}" "${labels}" "${reviews_json}" "${comments_json}")

  local actual_exit=0 output
  output=$(PATH="${mock_bin}:${PATH}" \
    TRIGGER_SOURCE="${trigger}" \
    PR_NUM="123" \
    SOURCE_REPO="org/repo" \
    GH_TOKEN="fake" \
    REVIEW_MAX_FIX_CYCLES="${cap_env}" \
    bash "${SCRIPT}" 2>&1) || actual_exit=$?

  if [[ "${actual_exit}" -ne "${expected_exit}" ]]; then
    echo "FAIL: ${name} — expected exit ${expected_exit}, got ${actual_exit}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ -n "${expected_annotation}" ]] && [[ "${output}" != *"${expected_annotation}"* ]]; then
    echo "FAIL: ${name} — expected annotation '${expected_annotation}' not found in output"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ "${expect_post}" == "yes" && ! -f "${TMPDIR}/posted-comment-body.txt" ]]; then
    echo "FAIL: ${name} — expected a fix-cycle-cap comment to be posted, but none was"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ "${expect_post}" == "no" && -f "${TMPDIR}/posted-comment-body.txt" ]]; then
    echo "FAIL: ${name} — expected no fix-cycle-cap comment, but one was posted"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${name}"
}

echo "=== check-fix-eligibility tests ==="

# Human trigger always proceeds (exit 0)
run_test "human trigger skips gate" 0 "false" "some-user" "human-user"

# Human trigger ignores fullsend-no-fix label (exit 0)
run_test "human trigger ignores no-fix label" 0 "false" "some-user" "human-user" "fullsend-no-fix"

# Human trigger ignores both labels (exit 0)
run_test "human trigger ignores both labels" 0 "false" "some-user" "human-user" "fullsend-no-fix,fullsend-fix"

# Coder bot PR auto-fixes (exit 0)
run_test "coder bot auto-fixes" 0 "true" "app/fullsend-ai-coder" "review-bot[bot]"

# Human-authored PR without label is skipped (exit 1)
run_test "human PR without label skipped" 1 "false" "some-user" "review-bot[bot]" "" "lacks 'fullsend-fix' label"

# Human-authored PR with fullsend-fix label proceeds (exit 0)
run_test "human PR with label proceeds" 0 "false" "some-user" "review-bot[bot]" "fullsend-fix"

# Other bot (renovate) without label is skipped (exit 1)
run_test "renovate bot without label skipped" 1 "true" "app/renovate-fullsend" "review-bot[bot]" "" "lacks 'fullsend-fix' label"

# Other bot (renovate) with label proceeds (exit 0)
run_test "renovate bot with label proceeds" 0 "true" "app/renovate-fullsend" "review-bot[bot]" "fullsend-fix"

# fullsend-no-fix label blocks even coder bot (exit 1)
run_test "no-fix label blocks coder bot" 1 "true" "app/fullsend-ai-coder" "review-bot[bot]" "fullsend-no-fix" "has 'fullsend-no-fix' label"

# fullsend-no-fix takes priority over fullsend-fix (exit 1)
run_test "no-fix priority over fix label" 1 "true" "app/fullsend-ai-coder" "review-bot[bot]" "fullsend-no-fix,fullsend-fix" "has 'fullsend-no-fix' label"

# null is_bot (old gh CLI) without label is skipped (exit 1)
run_test "null is_bot without label skipped" 1 "null" "app/fullsend-ai-coder" "review-bot[bot]" "" "lacks 'fullsend-fix' label"

# null is_bot (old gh CLI) with fullsend-fix label proceeds (exit 0)
run_test "null is_bot with label proceeds" 0 "null" "app/fullsend-ai-coder" "review-bot[bot]" "fullsend-fix" "did not return is_bot field"

# is_bot=false with coder login (login alone doesn't bypass label gate)
run_test "false is_bot coder login without label skipped" 1 "false" "app/fullsend-ai-coder" "review-bot[bot]" "" "lacks 'fullsend-fix' label"

# is_bot=false with coder login and label proceeds (exit 0)
run_test "false is_bot coder login with label proceeds" 0 "false" "app/fullsend-ai-coder" "review-bot[bot]" "fullsend-fix"

echo ""
echo "=== fix-cycle cap tests ==="

REVIEWS_UNDER_CAP='[{"state":"CHANGES_REQUESTED","user":{"login":"org-review[bot]"}},{"state":"CHANGES_REQUESTED","user":{"login":"org-review[bot]"}}]'
REVIEWS_AT_CAP='[{"state":"CHANGES_REQUESTED","user":{"login":"org-review[bot]"}},{"state":"CHANGES_REQUESTED","user":{"login":"org-review[bot]"}},{"state":"CHANGES_REQUESTED","user":{"login":"org-review[bot]"}}]'
COMMENTS_WITH_MARKER='[{"id":55,"body":"<!-- fullsend-fix-cycle-cap -->\nOld message"}]'

# Under the default cap (3): proceeds, no comment
run_test "under cap proceeds" 0 "true" "app/fullsend-ai-coder" "review-bot[bot]" "" "" \
  "${REVIEWS_UNDER_CAP}" "[]" "" "no"

# At the default cap: blocked, warning emitted, comment posted
run_test "at cap exits 1 and posts comment" 1 "true" "app/fullsend-ai-coder" "review-bot[bot]" "" \
  "automated fix cycles" "${REVIEWS_AT_CAP}" "[]" "" "yes"

# Marker already present: still blocked, but no second comment
run_test "marker present skips second comment" 1 "true" "app/fullsend-ai-coder" "review-bot[bot]" "" \
  "" "${REVIEWS_AT_CAP}" "${COMMENTS_WITH_MARKER}" "" "no"

# Human trigger bypasses the cap entirely (existing :16 early-exit)
run_test "human trigger ignores cap" 0 "false" "some-user" "human-user" "" \
  "" "${REVIEWS_AT_CAP}" "[]" "" "no"

# 0 disables the cap even when the count would otherwise trip it
run_test "cap 0 disables" 0 "true" "app/fullsend-ai-coder" "review-bot[bot]" "" \
  "" "${REVIEWS_AT_CAP}" "[]" "0" "no"

# Reviews API failure: proceed, warn, do not block
run_test "reviews API failure proceeds with warning" 0 "true" "app/fullsend-ai-coder" "review-bot[bot]" "" \
  "Could not count review cycles" "FAIL" "[]" "" "no"

# Non-numeric cap value: warns and falls back to the default (3), which the
# at-cap review count above should still trip
run_test "non-numeric cap warns and uses default" 1 "true" "app/fullsend-ai-coder" "review-bot[bot]" "" \
  "REVIEW_MAX_FIX_CYCLES is not a number" "${REVIEWS_AT_CAP}" "[]" "abc" "yes"

# gh pr view failure (network error / invalid token) emits ::error:: and exits 1
run_test_gh_failure() {
  local mock_bin="${TMPDIR}/bin-fail"
  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"

  cat > "${mock_bin}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
if [[ "$1" == "pr" && "$2" == "view" ]]; then
  echo "gh: Could not resolve to a PullRequest" >&2
  exit 1
fi
echo "unexpected gh call: $*" >&2
exit 1
MOCKEOF
  chmod +x "${mock_bin}/gh"

  local actual_exit=0 output
  output=$(PATH="${mock_bin}:${PATH}" \
    TRIGGER_SOURCE="review-bot[bot]" \
    PR_NUM="999" \
    SOURCE_REPO="org/repo" \
    GH_TOKEN="fake" \
    bash "${SCRIPT}" 2>&1) || actual_exit=$?

  if [[ "${actual_exit}" -ne 1 ]]; then
    echo "FAIL: gh pr view failure — expected exit 1, got ${actual_exit}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ "${output}" != *"::error::Failed to fetch PR info"* ]]; then
    echo "FAIL: gh pr view failure — expected ::error:: annotation not found in output"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: gh pr view failure"
}
run_test_gh_failure

echo ""
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) FAILED"
  exit 1
else
  echo "All tests passed"
fi
