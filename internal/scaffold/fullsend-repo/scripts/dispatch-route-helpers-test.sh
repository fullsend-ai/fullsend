#!/usr/bin/env bash
# dispatch-route-helpers-test.sh — Tests for dispatch-route-helpers.sh
#
# Run from the repo root:
#   bash internal/scaffold/fullsend-repo/scripts/dispatch-route-helpers-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HELPERS="${SCRIPT_DIR}/dispatch-route-helpers.sh"
FAILURES=0

# --- Helpers ---

run_test() {
  local test_name="$1"
  local expected_rc="$2"  # expected return code
  local func_call="$3"    # function name + args
  local env_overrides="${4:-}"

  # Build env array
  local env_cmd=(
    env
    GITHUB_REPOSITORY="test-org/test-repo"
    ORG_NAME="test-org"
    GH_TOKEN="fake-token"
    COMMENT_USER_LOGIN=""
    ISSUE_USER_LOGIN=""
    ISSUE_LABELS=""
  )

  if [[ -n "${env_overrides}" ]]; then
    while IFS= read -r kv; do
      [[ -n "${kv}" ]] && env_cmd+=("${kv}")
    done <<< "${env_overrides}"
  fi

  local actual_rc=0
  # Source the helpers and call the function in a subshell
  "${env_cmd[@]}" bash -c "
    source '${HELPERS}'
    ${func_call}
  " > /dev/null 2>&1 || actual_rc=$?

  if [[ "${actual_rc}" -ne "${expected_rc}" ]]; then
    echo "FAIL: ${test_name} — expected rc=${expected_rc}, got rc=${actual_rc}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- is_org_bot tests ---

# Path 1: fullsend's own shared, vendor-owned Apps match unconditionally,
# regardless of ORG_NAME — this is the default deployment model
# (ADR 0029/0059/0068), where every adopting org installs the same
# fullsend-ai-<role>[bot] Apps rather than org-named ones.
run_test "is_org_bot: shared fullsend-ai coder bot matches regardless of ORG_NAME" 0 \
  'is_org_bot "fullsend-ai-coder[bot]"' \
  "ORG_NAME=some-other-customer-org"

run_test "is_org_bot: shared fullsend-ai review bot matches" 0 \
  'is_org_bot "fullsend-ai-review[bot]"'

run_test "is_org_bot: shared fullsend-ai triage bot matches" 0 \
  'is_org_bot "fullsend-ai-triage[bot]"'

run_test "is_org_bot: shared fullsend-ai retro bot matches" 0 \
  'is_org_bot "fullsend-ai-retro[bot]"'

run_test "is_org_bot: shared fullsend-ai prioritize bot matches" 0 \
  'is_org_bot "fullsend-ai-prioritize[bot]"'

# Path 1 doesn't need ORG_NAME at all
run_test "is_org_bot: shared fullsend-ai bot matches with ORG_NAME unset" 0 \
  'is_org_bot "fullsend-ai-coder[bot]"' \
  "ORG_NAME="

# Path 1 is exact-role-match only — not a wildcard on the fullsend-ai- prefix
run_test "is_org_bot: fullsend-ai prefix with unknown role rejected" 1 \
  'is_org_bot "fullsend-ai-exploit[bot]"'

# Path 2: a self-managed org's own exact ${ORG_NAME}-<role>[bot] identity
# (ADR 0029/0033) — for orgs running their own private Apps.
run_test "is_org_bot: self-managed org coder bot matches" 0 \
  'is_org_bot "test-org-coder[bot]"'

run_test "is_org_bot: self-managed org review bot matches" 0 \
  'is_org_bot "test-org-review[bot]"'

run_test "is_org_bot: self-managed org triage bot matches" 0 \
  'is_org_bot "test-org-triage[bot]"'

run_test "is_org_bot: self-managed org retro bot matches" 0 \
  'is_org_bot "test-org-retro[bot]"'

run_test "is_org_bot: self-managed org prioritize bot matches" 0 \
  'is_org_bot "test-org-prioritize[bot]"'

# Path 2 requires ORG_NAME
run_test "is_org_bot: self-managed match rejected with empty ORG_NAME" 1 \
  'is_org_bot "test-org-coder[bot]"' \
  "ORG_NAME="

# Path 2 is exact-role-match only — not a wildcard on the ${ORG_NAME}- prefix.
# This is the spoofing vector the wildcard design allowed: a third party
# registering "${ORG_NAME}-<anything>[bot]" must NOT match.
run_test "is_org_bot: org-prefixed non-role bot rejected (no wildcard)" 1 \
  'is_org_bot "test-org-exploit[bot]"'

# A different self-managed org's bot does not match this org's identity
run_test "is_org_bot: different org's self-managed bot rejected" 1 \
  'is_org_bot "other-org-coder[bot]"'

# Third-party bots matching neither path are rejected
run_test "is_org_bot: third-party bot rejected" 1 \
  'is_org_bot "renovate[bot]"'

# Human user does not match
run_test "is_org_bot: human user rejected" 1 \
  'is_org_bot "human-dev"'

# Empty username / no argument returns 1
run_test "is_org_bot: empty username rejected" 1 \
  'is_org_bot ""'

run_test "is_org_bot: no argument rejected" 1 \
  'is_org_bot'

# ORG_NAME is matched literally (plain string equality in the role loop,
# not a glob/regex), so special characters in an org name can't be
# misinterpreted as a pattern
run_test "is_org_bot: org name with special characters matches literally" 0 \
  'is_org_bot "a+b.c-coder[bot]"' \
  "ORG_NAME=a+b.c"

# --- is_org_bot role-filter tests (used by the pull_request_review ->
# fix dispatch gate, which must match specifically the review bot, not
# any fullsend agent bot) ---

run_test "is_org_bot: role filter matches shared bot with matching role" 0 \
  'is_org_bot "fullsend-ai-review[bot]" review'

run_test "is_org_bot: role filter rejects shared bot with different role" 1 \
  'is_org_bot "fullsend-ai-coder[bot]" review'

run_test "is_org_bot: role filter matches self-managed bot with matching role" 0 \
  'is_org_bot "test-org-review[bot]" review'

run_test "is_org_bot: role filter rejects self-managed bot with different role" 1 \
  'is_org_bot "test-org-coder[bot]" review'

# --- has_label tests ---

run_test "has_label: label present" 0 \
  'has_label "bug"' \
  "ISSUE_LABELS=bug,enhancement,ready-to-code"

run_test "has_label: label absent" 1 \
  'has_label "feature"' \
  "ISSUE_LABELS=bug,enhancement"

run_test "has_label: empty labels" 1 \
  'has_label "bug"' \
  "ISSUE_LABELS="

run_test "has_label: custom csv" 0 \
  'has_label "ready-for-review" "ready-for-review,bug"'

run_test "has_label: custom csv miss" 1 \
  'has_label "ready-for-review" "bug,enhancement"'

# has_label must not hard-fail under `set -u` when ISSUE_LABELS is truly
# unset (not just empty) — regression test, since the caller documents
# ISSUE_LABELS as optional but workflows source this under set -euo pipefail.
actual_rc=0
stderr_output=$(env -u ISSUE_LABELS GITHUB_REPOSITORY="test-org/test-repo" ORG_NAME="test-org" GH_TOKEN="fake-token" \
  bash -c "set -u; source '${HELPERS}'; has_label 'bug'" 2>&1 >/dev/null) || actual_rc=$?
if [[ "${actual_rc}" -eq 1 && "${stderr_output}" != *"unbound variable"* ]]; then
  echo "PASS: has_label: unset ISSUE_LABELS under set -u returns 1, not unbound-variable error"
else
  echo "FAIL: has_label: unset ISSUE_LABELS under set -u — expected clean rc=1, got rc=${actual_rc} stderr=${stderr_output}"
  FAILURES=$((FAILURES + 1))
fi

# --- is_issue_author tests ---

run_test "is_issue_author: matching" 0 \
  'is_issue_author' \
  "$(printf '%s\n%s' 'COMMENT_USER_LOGIN=alice' 'ISSUE_USER_LOGIN=alice')"

run_test "is_issue_author: not matching" 1 \
  'is_issue_author' \
  "$(printf '%s\n%s' 'COMMENT_USER_LOGIN=alice' 'ISSUE_USER_LOGIN=bob')"

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
