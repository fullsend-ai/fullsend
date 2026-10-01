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

# Fixed "now" so recency assertions do not depend on wall clock.
NOW_EPOCH=1700000000
WINDOW_SECONDS=1800

iso_from_epoch() {
  date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ
}

write_commit_fixtures() {
  local author_type="$1" author_login="$2" committed_at="$3"
  printf '%s' "abc123def456" > "${TMPDIR}/head-sha.txt"
  jq -n \
    --arg type "${author_type}" \
    --arg login "${author_login}" \
    --arg committed_at "${committed_at}" \
    '{type:$type,login:$login,committer_type:"",committer_login:"",committed_at:$committed_at}' \
    > "${TMPDIR}/commit-json.txt"
}

# build_mock creates a mock gh binary that returns preconfigured PR JSON.
#   $1 — is_bot value (true/false/null)
#   $2 — login value
#   $3 — comma-separated labels (optional)
build_mock() {
  local is_bot="$1" login="$2" labels="${3:-}"
  local mock_bin="${TMPDIR}/bin"

  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"

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

  # Default HEAD is an old bot commit so existing proceed-path tests
  # still pass the recency check without extra arguments.
  write_commit_fixtures "Bot" "fullsend-ai-coder[bot]" "2020-01-01T00:00:00Z"

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
  shift
  path=""
  jq_arg=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --jq) shift; jq_arg="$1" ;;
      -f|-F|--repo) shift ;;
      *)
        if [[ "$1" != -* && -z "${path}" ]]; then
          path="$1"
        fi
        ;;
    esac
    shift
  done
  case "${path}" in
    repos/*/pulls/*)
      if [[ "${jq_arg}" != '.head.sha' ]]; then
        echo "mock gh: unexpected pulls --jq arg: ${jq_arg}" >&2
        exit 1
      fi
      cat "${MOCK_DIR}/head-sha.txt"
      exit 0
      ;;
    repos/*/commits/*)
      if [[ "${jq_arg}" != '{type:(.author.type//""),login:(.author.login//""),committer_type:(.committer.type//""),committer_login:(.committer.login//""),committed_at:(.commit.committer.date//"")}' ]]; then
        echo "mock gh: unexpected commits --jq arg: ${jq_arg}" >&2
        exit 1
      fi
      cat "${MOCK_DIR}/commit-json.txt"
      exit 0
      ;;
  esac
  echo "unexpected gh api call: path=${path} jq=${jq_arg}" >&2
  exit 1
fi
echo "unexpected gh call: $*" >&2
exit 1
MOCKEOF

  chmod +x "${mock_bin}/gh"

  echo "${mock_bin}"
}

# run_test runs the eligibility script with mocked PR data and asserts exit code
# and (optionally) annotation text.
#   $1 — test name
#   $2 — expected exit code
#   $3 — is_bot value
#   $4 — login value
#   $5 — trigger source
#   $6 — labels (optional, comma-separated)
#   $7 — expected annotation substring (optional)
run_test() {
  local name="$1" expected_exit="$2" is_bot="$3" login="$4" trigger="$5" labels="${6:-}" expected_annotation="${7:-}"
  local mock_bin
  mock_bin=$(build_mock "${is_bot}" "${login}" "${labels}")

  local actual_exit=0 output
  output=$(PATH="${mock_bin}:${PATH}" \
    TRIGGER_SOURCE="${trigger}" \
    PR_NUM="123" \
    SOURCE_REPO="org/repo" \
    GH_TOKEN="fake" \
    HUMAN_PUSH_NOW_EPOCH="${NOW_EPOCH}" \
    HUMAN_PUSH_RECENCY_SECONDS="${WINDOW_SECONDS}" \
    bash "${SCRIPT}" 2>&1) || actual_exit=$?

  if [[ "${actual_exit}" -ne "${expected_exit}" ]]; then
    echo "FAIL: ${name} — expected exit ${expected_exit}, got ${actual_exit}"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ -n "${expected_annotation}" ]] && [[ "${output}" != *"${expected_annotation}"* ]]; then
    echo "FAIL: ${name} — expected annotation '${expected_annotation}' not found in output"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${name}"
}

# run_commit_test is run_test plus a custom HEAD commit fixture.
#   $1-$7 — same as run_test
#   $8  — GitHub author type (User/Bot/empty)
#   $9  — GitHub author login
#   $10 — committed_at ISO timestamp
run_commit_test() {
  local name="$1" expected_exit="$2" is_bot="$3" login="$4" trigger="$5" labels="${6:-}" expected_annotation="${7:-}"
  local author_type="$8" author_login="$9" committed_at="${10}"
  local mock_bin
  mock_bin=$(build_mock "${is_bot}" "${login}" "${labels}")
  write_commit_fixtures "${author_type}" "${author_login}" "${committed_at}"

  local actual_exit=0 output
  output=$(PATH="${mock_bin}:${PATH}" \
    TRIGGER_SOURCE="${trigger}" \
    PR_NUM="123" \
    SOURCE_REPO="org/repo" \
    GH_TOKEN="fake" \
    HUMAN_PUSH_NOW_EPOCH="${NOW_EPOCH}" \
    HUMAN_PUSH_RECENCY_SECONDS="${WINDOW_SECONDS}" \
    bash "${SCRIPT}" 2>&1) || actual_exit=$?

  if [[ "${actual_exit}" -ne "${expected_exit}" ]]; then
    echo "FAIL: ${name} — expected exit ${expected_exit}, got ${actual_exit}"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ -n "${expected_annotation}" ]] && [[ "${output}" != *"${expected_annotation}"* ]]; then
    echo "FAIL: ${name} — expected annotation '${expected_annotation}' not found in output"
    echo "  output: ${output}"
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

RECENT_TS="$(iso_from_epoch $((NOW_EPOCH - 300)))"
OLD_TS="$(iso_from_epoch $((NOW_EPOCH - 3600)))"

# Human HEAD commit within the recency window — skip bot-triggered fix.
run_commit_test "human HEAD within window skipped" 1 \
  "true" "app/fullsend-ai-coder" "review-bot[bot]" "" \
  "skipped: recent human push" \
  "User" "waynesun09" "${RECENT_TS}"

# Human HEAD commit older than the window — existing behavior preserved.
run_commit_test "human HEAD older than window proceeds" 0 \
  "true" "app/fullsend-ai-coder" "review-bot[bot]" "" \
  "" \
  "User" "waynesun09" "${OLD_TS}"

# Bot HEAD commit (prior fix/code run) does not block bot-to-bot follow-up,
# even when the commit is recent.
run_commit_test "bot HEAD within window proceeds" 0 \
  "true" "app/fullsend-ai-coder" "review-bot[bot]" "" \
  "" \
  "Bot" "fullsend-ai-coder[bot]" "${RECENT_TS}"

# Login ending in [bot] is treated as a bot even if type is missing.
run_commit_test "bot login suffix within window proceeds" 0 \
  "true" "app/fullsend-ai-coder" "review-bot[bot]" "" \
  "" \
  "" "renovate[bot]" "${RECENT_TS}"

# Unlinked git author falls back to GitHub committer identity.
run_test_committer_fallback() {
  local mock_bin
  mock_bin=$(build_mock "true" "app/fullsend-ai-coder" "")
  printf '%s' "abc123def456" > "${TMPDIR}/head-sha.txt"
  jq -n --arg committed_at "${RECENT_TS}" \
    '{type:"",login:"",committer_type:"User",committer_login:"waynesun09",committed_at:$committed_at}' \
    > "${TMPDIR}/commit-json.txt"

  local actual_exit=0 output
  output=$(PATH="${mock_bin}:${PATH}" \
    TRIGGER_SOURCE="review-bot[bot]" \
    PR_NUM="123" \
    SOURCE_REPO="org/repo" \
    GH_TOKEN="fake" \
    HUMAN_PUSH_NOW_EPOCH="${NOW_EPOCH}" \
    HUMAN_PUSH_RECENCY_SECONDS="${WINDOW_SECONDS}" \
    bash "${SCRIPT}" 2>&1) || actual_exit=$?

  if [[ "${actual_exit}" -ne 1 ]]; then
    echo "FAIL: committer fallback recent human skipped — expected exit 1, got ${actual_exit}"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi
  if [[ "${output}" != *"skipped: recent human push"* ]]; then
    echo "FAIL: committer fallback recent human skipped — expected skip annotation"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "PASS: committer fallback recent human skipped"
}
run_test_committer_fallback

# Malformed committer date fails open (nothing else to fall back to).
run_commit_test "malformed committer date fails open" 0 \
  "true" "app/fullsend-ai-coder" "review-bot[bot]" "" \
  "Could not parse head commit timestamp" \
  "User" "waynesun09" "not-a-date"

# Explicit /fs-fix still proceeds even if HEAD is a recent human commit.
run_commit_test "human trigger ignores recent human HEAD" 0 \
  "true" "app/fullsend-ai-coder" "human-user" "" \
  "" \
  "User" "waynesun09" "${RECENT_TS}"

# Recency lookup failure is fail-open (proceed), unlike the label/authorship
# fetch which fails closed.
run_test_recency_api_failure() {
  local mock_bin
  mock_bin=$(build_mock "true" "app/fullsend-ai-coder" "")
  cat > "${mock_bin}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
MOCK_DIR="$(cd "$(dirname "$0")/.." && pwd)"
PR_JSON_FILE="$(cat "${MOCK_DIR}/pr-json-path")"
if [[ "$1" == "pr" && "$2" == "view" ]]; then
  cat "${PR_JSON_FILE}"
  exit 0
fi
if [[ "$1" == "api" ]]; then
  echo "gh: API rate limit exceeded" >&2
  exit 1
fi
echo "unexpected gh call: $*" >&2
exit 1
MOCKEOF
  chmod +x "${mock_bin}/gh"

  local actual_exit=0 output
  output=$(PATH="${mock_bin}:${PATH}" \
    TRIGGER_SOURCE="review-bot[bot]" \
    PR_NUM="123" \
    SOURCE_REPO="org/repo" \
    GH_TOKEN="fake" \
    HUMAN_PUSH_NOW_EPOCH="${NOW_EPOCH}" \
    HUMAN_PUSH_RECENCY_SECONDS="${WINDOW_SECONDS}" \
    bash "${SCRIPT}" 2>&1) || actual_exit=$?

  if [[ "${actual_exit}" -ne 0 ]]; then
    echo "FAIL: recency API failure fail-open — expected exit 0, got ${actual_exit}"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi
  if [[ "${output}" != *"Could not fetch PR head SHA for human-activity check"* ]]; then
    echo "FAIL: recency API failure fail-open — expected proceeding warning not found"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "PASS: recency API failure fail-open"
}
run_test_recency_api_failure

# WINDOW=0 disables the recency check even for a fresh human HEAD.
run_test_window_disabled() {
  local mock_bin
  mock_bin=$(build_mock "true" "app/fullsend-ai-coder" "")
  write_commit_fixtures "User" "waynesun09" "${RECENT_TS}" ""

  local actual_exit=0 output
  output=$(PATH="${mock_bin}:${PATH}" \
    TRIGGER_SOURCE="review-bot[bot]" \
    PR_NUM="123" \
    SOURCE_REPO="org/repo" \
    GH_TOKEN="fake" \
    HUMAN_PUSH_NOW_EPOCH="${NOW_EPOCH}" \
    HUMAN_PUSH_RECENCY_SECONDS="0" \
    bash "${SCRIPT}" 2>&1) || actual_exit=$?

  if [[ "${actual_exit}" -ne 0 ]]; then
    echo "FAIL: recency window disabled — expected exit 0, got ${actual_exit}"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "PASS: recency window disabled"
}
run_test_window_disabled

echo ""
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) FAILED"
  exit 1
else
  echo "All tests passed"
fi
