#!/usr/bin/env bash
# check-e2e-authorization-test.sh — Tests for check-e2e-authorization.sh with mock gh.
#
# Run from repo root: bash scripts/check-e2e-authorization-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AUTH_SCRIPT="${SCRIPT_DIR}/check-e2e-authorization.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

MOCK_BIN="${TMPDIR}/bin"
mkdir -p "${MOCK_BIN}"

PR_JSON="${TMPDIR}/pr.json"
EVENTS_JSON="${TMPDIR}/events.json"
COMMENT_JSON="${TMPDIR}/comment.json"
COLLAB_ROLE="${TMPDIR}/collab_role"
ROLES_DIR="${TMPDIR}/roles"
GH_LOG="${TMPDIR}/gh.log"
GH_FAIL="false"

# Default: no collaborator role configured (API returns failure)
echo "" >"${COLLAB_ROLE}"

# Per-login role; overrides COLLAB_ROLE so a case can give the PR author and
# the labeler different permissions.
mkdir -p "${ROLES_DIR}"
set_role() {
  echo "$2" >"${ROLES_DIR}/$1"
}
set_role "labeler" "write"

write_pr() {
  local assoc="${1:-NONE}"
  local labels_json="${2:-[]}"
  local updated_at="${3:-2026-06-01T10:00:00Z}"
  local author_login="${4-author}"
  if [[ -n "${author_login}" ]]; then
    jq -n --arg assoc "${assoc}" --argjson labels "${labels_json}" --arg updated_at "${updated_at}" --arg login "${author_login}" \
      '{author_association: $assoc, labels: $labels, updated_at: $updated_at, user: {login: $login}}' >"${PR_JSON}"
  else
    jq -n --arg assoc "${assoc}" --argjson labels "${labels_json}" --arg updated_at "${updated_at}" \
      '{author_association: $assoc, labels: $labels, updated_at: $updated_at}' >"${PR_JSON}"
  fi
}

write_events() {
  local events_json="$1"
  # Labeled events without an explicit actor default to "labeler" (write role).
  jq 'map(if .event == "labeled" and (has("actor") | not) then .actor = {login: "labeler"} else . end)' \
    <<<"${events_json}" >"${EVENTS_JSON}"
}

cat >"${MOCK_BIN}/gh" <<EOF
#!/usr/bin/env bash
echo "gh \$*" >> "${GH_LOG}"
if [[ "\${GH_FAIL}" == "true" ]]; then
  echo "simulated gh failure" >&2
  exit 1
fi
if [[ "\${GH_FAIL}" == "events" && "\$*" == *"/issues/"*"/events"* ]]; then
  echo "simulated events API failure" >&2
  exit 1
fi
if [[ "\${GH_FAIL}" == "delete" && "\$*" == *DELETE* ]]; then
  echo "simulated label DELETE failure" >&2
  exit 1
fi
if [[ "\${GH_FAIL}" == "delete_404" && "\$*" == *DELETE* ]]; then
  echo "gh: Label does not exist (HTTP 404)" >&2
  exit 1
fi
if [[ "\${GH_FAIL}" == "pulls" && "\$*" == *"/pulls/"* ]]; then
  echo "simulated pull request API failure" >&2
  exit 1
fi
if [[ "\${GH_FAIL}" == "comment" && "\$*" == *"/issues/comments/"* ]]; then
  echo "simulated comment API failure" >&2
  exit 1
fi
if [[ "\${GH_FAIL}" == "permission" && "\$*" == *"/collaborators/"* ]]; then
  echo "simulated permission API failure" >&2
  exit 1
fi
if [[ "\${GH_FAIL}" == "labeler_permission" && "\$*" == *"/collaborators/"* ]]; then
  login="\$*"
  login="\${login#*/collaborators/}"
  login="\${login%%/permission*}"
  if [[ "\${login}" != "author" && "\${login}" != "maruiz93" ]]; then
    echo "simulated labeler permission API failure" >&2
    exit 1
  fi
fi
case "\$*" in
  *"/collaborators/"*"/permission"*)
    login="\$*"
    login="\${login#*/collaborators/}"
    login="\${login%%/permission*}"
    if [[ -f "${ROLES_DIR}/\${login}" ]]; then
      role=\$(cat "${ROLES_DIR}/\${login}")
    else
      role=\$(cat "${COLLAB_ROLE}")
    fi
    if [[ -z "\${role}" ]]; then
      echo "not a collaborator" >&2
      exit 1
    fi
    if [[ "\${role}" == raw:* ]]; then
      echo "\${role#raw:}"
    elif [[ "\${role}" == mismatch:* ]]; then
      mismatch_data="\${role#mismatch:}"
      role_part="\${mismatch_data%%:*}"
      perm_part="\${mismatch_data#*:}"
      echo "{\"role_name\": \"\${role_part}\", \"permission\": \"\${perm_part}\"}"
    elif [[ "\${role}" == custom:* ]]; then
      custom_name="\${role#custom:}"
      custom_perm="\${custom_name#*:}"
      custom_name="\${custom_name%%:*}"
      echo "{\"role_name\": \"\${custom_name}\", \"permission\": \"\${custom_perm}\"}"
    elif [[ "\${role}" == "maintain" ]]; then
      echo "{\"role_name\": \"maintain\", \"permission\": \"write\"}"
    elif [[ "\${role}" == "triage" ]]; then
      echo "{\"role_name\": \"triage\", \"permission\": \"read\"}"
    else
      echo "{\"role_name\": \"\${role}\", \"permission\": \"\${role}\"}"
    fi
    ;;
  *"/issues/comments/"*)
    cat "${COMMENT_JSON}"
    ;;
  *"/issues/"*"/events"*)
    cat "${EVENTS_JSON}"
    ;;
  *"/pulls/"*)
    cat "${PR_JSON}"
    ;;
  *DELETE*)
    echo "deleted" >> "${GH_LOG}"
    ;;
  *)
    echo "unexpected gh call: \$*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "${MOCK_BIN}/gh"

export PATH="${MOCK_BIN}:${PATH}"
export GH_TOKEN="test-token"
export CHECK_E2E_AUTH_DRY_RUN="true"
export GH_FAIL="false"
unset EVENT_ACTION PR_UPDATED_AT PR_AUTHOR_LOGIN LABEL_ACTOR_LOGIN

run_case() {
  local name="$1"
  local expected_auth="$2"
  local expected_reason="$3"
  local expected_removed="$4"

  : >"${GH_LOG}"

  local output
  output="$("${AUTH_SCRIPT}" 42 "fullsend-ai/fullsend")"
  local auth reason removed
  auth="$(grep -o 'authorized=[^ ]*' <<<"${output}" | cut -d= -f2)"
  reason="$(grep -o 'reason=[^ ]*' <<<"${output}" | cut -d= -f2)"
  removed="$(grep -o 'label_removed=[^ ]*' <<<"${output}" | cut -d= -f2)"

  if [[ "${auth}" != "${expected_auth}" || "${reason}" != "${expected_reason}" || "${removed}" != "${expected_removed}" ]]; then
    echo "FAIL: ${name}"
    echo "  expected authorized=${expected_auth} reason=${expected_reason} label_removed=${expected_removed}"
    echo "  got      authorized=${auth} reason=${reason} label_removed=${removed}"
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "PASS: ${name}"
}

# --- Author permission tests (trust based on collaborator API, not author_association) ---

export PR_AUTHOR_LOGIN="author"

set_role "author" "write"
write_pr "MEMBER" '[]'
run_case "author with write permission is authorized" "true" "trusted_author" "false"

set_role "author" "maintain"
write_pr "MEMBER" '[]'
run_case "author with maintain permission is authorized" "true" "trusted_author" "false"

set_role "author" "admin"
write_pr "MEMBER" '[]'
run_case "author with admin permission is authorized" "true" "trusted_author" "false"

set_role "author" "triage"
write_pr "MEMBER" '[]'
run_case "author with triage permission denied (even if MEMBER)" "false" "unauthorized" "false"

set_role "author" "read"
write_pr "COLLABORATOR" '[]'
run_case "author with read permission denied (even if COLLABORATOR)" "false" "unauthorized" "false"

set_role "author" "custom:Developer:write"
write_pr "MEMBER" '[]'
run_case "author with write-backed custom role is authorized" "true" "trusted_author" "false"

set_role "author" "custom:TriageHelper:read"
write_pr "MEMBER" '[]'
run_case "author with read-backed custom role is denied" "false" "unauthorized" "false"

# Mismatched role_name and permission fields rejected
set_role "author" "mismatch:write:read"
write_pr "MEMBER" '[]'
run_case "author with mismatched role and permission is denied" "false" "unauthorized" "false"

# Malformed / non-JSON permission API response returns error
set_role "author" "raw:not-a-json-response"
write_pr "MEMBER" '[]'
run_case "malformed permission response returns error" "false" "error" "false"

# Permission response missing permission field (e.g. {}) returns error
set_role "author" "raw:{}"
write_pr "MEMBER" '[]'
run_case "permission response missing permission field returns error" "false" "error" "false"

# Permission response with permission: none (e.g. apps/bots) falls through to label check
set_role "author" "raw:{\"role_name\":\"\",\"permission\":\"none\"}"
write_pr "NONE" '[]'
run_case "bot or app author with permission none falls through to unauthorized" "false" "unauthorized" "false"

export EVENT_ACTION="labeled"
export LABEL_ACTOR_LOGIN="labeler"
write_pr "NONE" '[{"name":"ok-to-test"}]'
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "bot or app author with ok-to-test can be approved by maintainer" "true" "ok_to_test" "false"
unset EVENT_ACTION LABEL_ACTOR_LOGIN

echo "" >"${ROLES_DIR}/author"
write_pr "CONTRIBUTOR" '[]'
run_case "contributor author denied" "false" "unauthorized" "false"

# --- Trusted bot tests ---

export PR_AUTHOR_ASSOCIATION="CONTRIBUTOR"
export PR_AUTHOR_LOGIN="renovate-fullsend[bot]"
echo "" >"${COLLAB_ROLE}"
write_pr "NONE" '[]'
run_case "renovate bot authorized as trusted bot" "true" "trusted_bot" "false"

export PR_AUTHOR_LOGIN="fullsend-ai-coder[bot]"
write_pr "NONE" '[]'
run_case "fullsend-ai-coder bot authorized as trusted bot" "true" "trusted_bot" "false"

export PR_AUTHOR_LOGIN="some-other-bot[bot]"
write_pr "NONE" '[]'
run_case "unknown bot not authorized" "false" "unauthorized" "false"

export PR_AUTHOR_LOGIN="author"
set_role "author" "write"
write_pr "MEMBER" '[{"name":"ok-to-test"}]'
run_case "author with write permission ignores stale ok-to-test label" "true" "trusted_author" "false"
echo "" >"${ROLES_DIR}/author"
unset PR_AUTHOR_LOGIN

# --- ok-to-test lifecycle ---
# Only a labeled event approves. A label that postdates this event stays for
# that workflow; an older label is removed.

export EVENT_ACTION="synchronize"
export PR_UPDATED_AT="2026-06-01T10:00:00Z"
write_pr "NONE" '[{"name":"ok-to-test"}]'
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "synchronize keeps a label applied after the push" "false" "fresh_ok_to_test" "false"

export PR_UPDATED_AT="2026-06-01T12:00:00Z"
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "synchronize removes ok-to-test applied before the push" "false" "stale_ok_to_test" "true"
unset EVENT_ACTION PR_UPDATED_AT

export EVENT_ACTION="reopened"
export PR_UPDATED_AT="2026-06-01T10:00:00Z"
write_pr "NONE" '[{"name":"ok-to-test"}]'
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "reopened run does not approve a newer ok-to-test label" "false" "fresh_ok_to_test" "false"

export PR_UPDATED_AT="2026-06-01T12:00:00Z"
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "reopened run removes an older ok-to-test label" "false" "stale_ok_to_test" "true"

export PR_UPDATED_AT="2026-06-01T12:00:00Z"
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T12:00:00Z"}]'
run_case "ok-to-test label at event time is stale" "false" "stale_ok_to_test" "true"

unset CHECK_E2E_AUTH_DRY_RUN
run_case "stale ok-to-test removes label" "false" "stale_ok_to_test" "true"
if ! grep -q DELETE "${GH_LOG}"; then
  echo "FAIL: stale ok-to-test removes label (expected gh DELETE call)"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: stale ok-to-test removes label (DELETE exercised)"
fi
export CHECK_E2E_AUTH_DRY_RUN="true"

unset EVENT_ACTION PR_UPDATED_AT
write_pr "NONE" '[]'
run_case "untrusted author without label" "false" "unauthorized" "false"

export EVENT_ACTION="labeled"
export LABEL_ACTOR_LOGIN="labeler"
write_pr "NONE" '[{"name":"ok-to-test"}]'
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "labeled ok-to-test verifies labeler permission" "true" "ok_to_test" "false"

# Missing LABEL_ACTOR_LOGIN on labeled event fails closed without deleting label
unset LABEL_ACTOR_LOGIN
run_case "labeled ok-to-test without sender login is denied" "false" "untrusted_labeler" "false"
export LABEL_ACTOR_LOGIN="labeler"

# Triage-role users can apply labels but must not authorize a run.
set_role "triager" "triage"
export LABEL_ACTOR_LOGIN="triager"
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z","actor":{"login":"triager"}}]'
run_case "ok-to-test labeler without write permission denied" "false" "untrusted_labeler" "true"

# The latest ok-to-test labeler decides, not an earlier one.
export EVENT_ACTION="labeled"
export LABEL_ACTOR_LOGIN="triager"
write_events '[
  {"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T09:00:00Z"},
  {"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z","actor":{"login":"triager"}}
]'
run_case "latest ok-to-test labeler without write permission denied" "false" "untrusted_labeler" "true"

# Other labels applied later by anyone do not change who applied ok-to-test.
export LABEL_ACTOR_LOGIN="labeler"
write_events '[
  {"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"},
  {"event":"labeled","label":{"name":"component/cli"},"created_at":"2026-06-01T11:30:00Z","actor":{"login":"triager"}}
]'
run_case "later non-ok-to-test label by another user is ignored" "true" "ok_to_test" "false"
unset LABEL_ACTOR_LOGIN

export EVENT_ACTION="labeled"
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z","actor":null}]'
run_case "ok-to-test label event without actor denied" "false" "untrusted_labeler" "false"

# On labeled events the frozen sender is the labeler: a later re-label by a
# write user must not authorize the run the triage-role user started.
export LABEL_ACTOR_LOGIN="triager"
write_events '[
  {"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z","actor":{"login":"triager"}},
  {"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:05:00Z"}
]'
run_case "frozen untrusted sender denied, newer label kept" "false" "untrusted_labeler" "false"

write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z","actor":{"login":"triager"}}]'
run_case "frozen untrusted sender denied, own label removed" "false" "untrusted_labeler" "true"

export LABEL_ACTOR_LOGIN="labeler"
write_events '[]'
run_case "frozen trusted sender authorizes without events lookup" "true" "ok_to_test" "false"
if grep -q '/events' "${GH_LOG}"; then
  echo "FAIL: frozen sender path should not call events API"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: frozen sender path skips events API"
fi

# A write sender on reopen is not an approval, even when the label is newer.
export EVENT_ACTION="reopened"
export PR_UPDATED_AT="2026-06-01T10:00:00Z"
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z","actor":{"login":"triager"}}]'
run_case "reopened run does not treat sender as approval" "false" "fresh_ok_to_test" "false"
unset LABEL_ACTOR_LOGIN PR_UPDATED_AT
export EVENT_ACTION="labeled"

# A failed labeler lookup is an API error, not a denial.
export LABEL_ACTOR_LOGIN="labeler"
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
export GH_FAIL="labeler_permission"
run_case "labeler permission API failure returns error" "false" "error" "false"
export GH_FAIL="false"

# An API error while checking the latest labeler during untrusted cleanup returns error
export LABEL_ACTOR_LOGIN="triager"
export GH_FAIL="events"
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z","actor":{"login":"triager"}}]'
run_case "events API failure during untrusted labeler cleanup returns error" "false" "error" "false"
export GH_FAIL="false"

unset CHECK_E2E_AUTH_DRY_RUN
run_case "untrusted labeler removes label" "false" "untrusted_labeler" "true"
if ! grep -q DELETE "${GH_LOG}"; then
  echo "FAIL: untrusted labeler removes label (expected gh DELETE call)"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: untrusted labeler removes label (DELETE exercised)"
fi
export GH_FAIL="delete"
run_case "label removal failure returns error" "false" "error" "false"
export GH_FAIL="false"

# Label removal when concurrent workflow already deleted label (HTTP 404) succeeds
export GH_FAIL="delete_404"
run_case "label removal race with 404 treated as already removed" "false" "untrusted_labeler" "true"
export GH_FAIL="false"
export CHECK_E2E_AUTH_DRY_RUN="true"
unset LABEL_ACTOR_LOGIN

export EVENT_ACTION="reopened"
unset PR_UPDATED_AT
write_pr "NONE" '[{"name":"ok-to-test"}]' "2026-06-01T10:00:00Z"
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "falls back to pull updated_at when PR_UPDATED_AT unset" "false" "fresh_ok_to_test" "false"

export PR_AUTHOR_LOGIN="author"
set_role "author" "write"
write_pr "MEMBER" '[]'
export GH_FAIL="events"
run_case "trusted author not blocked by events API failure" "true" "trusted_author" "false"
echo "" >"${ROLES_DIR}/author"
unset PR_AUTHOR_LOGIN

export EVENT_ACTION="reopened"
export PR_UPDATED_AT="2026-06-01T10:00:00Z"
write_pr "NONE" '[{"name":"ok-to-test"}]'
write_events '[]'
export GH_FAIL="events"
run_case "events API failure for untrusted ok-to-test returns error" "false" "error" "false"
export GH_FAIL="false"

export EVENT_ACTION="synchronize"
export PR_UPDATED_AT="2026-06-01T10:00:00Z"
write_pr "NONE" '[{"name":"ok-to-test"}]'
write_events '[]'
export GH_FAIL="true"
run_case "gh api failure on pull fetch returns error reason" "false" "error" "false"
export GH_FAIL="false"

# --- Collaborator permission API tests ---

# Contributor with collaborator API write+ permissions
unset EVENT_ACTION PR_UPDATED_AT
export PR_AUTHOR_LOGIN="maruiz93"
echo "admin" >"${COLLAB_ROLE}"
write_pr "NONE" '[]'
: >"${GH_LOG}"
run_case "collaborator API check authorizes admin" "true" "trusted_author" "false"
if ! grep -q '/collaborators/' "${GH_LOG}"; then
  echo "FAIL: should have called collaborator permission API"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: called collaborator permission API"
fi

echo "write" >"${COLLAB_ROLE}"
: >"${GH_LOG}"
run_case "collaborator API check authorizes write" "true" "trusted_author" "false"

echo "maintain" >"${COLLAB_ROLE}"
: >"${GH_LOG}"
run_case "collaborator API check authorizes maintain" "true" "trusted_author" "false"

# Collaborator API says read — should NOT authorize
echo "read" >"${COLLAB_ROLE}"
write_pr "NONE" '[]'
: >"${GH_LOG}"
run_case "collaborator API read permission denied" "false" "unauthorized" "false"

# Missing author login in both env and PR payload fails closed
unset PR_AUTHOR_LOGIN EVENT_ACTION PR_UPDATED_AT
echo "admin" >"${COLLAB_ROLE}"
write_pr "NONE" '[]' "2026-06-01T10:00:00Z" ""
: >"${GH_LOG}"
run_case "missing author identity fails closed with error" "false" "error" "false"

# Collaborator API fails for the PR author — returns error, does not silently deny or clear labels
export PR_AUTHOR_LOGIN="author"
export GH_FAIL="permission"
write_pr "NONE" '[{"name":"ok-to-test"}]'
run_case "author permission lookup API failure returns error" "false" "error" "false"
export GH_FAIL="false"
unset PR_AUTHOR_LOGIN

# --- /ok-to-test command mode (EVENT_NAME=issue_comment) ---
# The comment names the reviewed sha; only a PR head that starts with it runs.

HEAD_SHA="1111111111111111111111111111111111111111"
# Shares the 7-character prefix of HEAD_SHA but is a different commit.
OTHER_SHA="1111111222222222222222222222222222222222"

write_cmd_pr() {
  jq -n --arg sha "${HEAD_SHA}" '{
    user: {login: "forker"}, labels: [], updated_at: "2026-06-01T10:00:00Z",
    head: {sha: $sha, ref: "feature", repo: {full_name: "forker/fullsend"}}
  }' >"${PR_JSON}"
}

# write_comment BODY [LOGIN] [ISSUE]
write_comment() {
  local body="$1" login="${2:-maint}" issue="${3:-42}"
  jq -n --arg body "${body}" --arg login "${login}" --arg issue "${issue}" '{
    id: 900, user: {login: $login}, body: $body, created_at: "2026-06-01T12:00:00Z",
    issue_url: ("https://api.github.com/repos/fullsend-ai/fullsend/issues/" + $issue)
  }' >"${COMMENT_JSON}"
}

run_cmd_case() {
  local name="$1" expected_auth="$2" expected_reason="$3" expected_sha="$4"
  : >"${GH_LOG}"
  local output auth reason sha
  output="$("${AUTH_SCRIPT}" 42 "fullsend-ai/fullsend")"
  auth="$(grep -o 'authorized=[^ ]*' <<<"${output}" | cut -d= -f2)"
  reason="$(grep -o 'reason=[^ ]*' <<<"${output}" | cut -d= -f2)"
  sha="$(grep -o 'head_sha=[^ ]*' <<<"${output}" | cut -d= -f2)"
  if [[ "${auth}" != "${expected_auth}" || "${reason}" != "${expected_reason}" || "${sha}" != "${expected_sha}" ]]; then
    echo "FAIL: ${name}"
    echo "  expected authorized=${expected_auth} reason=${expected_reason} head_sha=${expected_sha}"
    echo "  got      authorized=${auth} reason=${reason} head_sha=${sha}"
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "PASS: ${name}"
}

unset PR_AUTHOR_LOGIN EVENT_ACTION PR_UPDATED_AT LABEL_ACTOR_LOGIN
echo "" >"${COLLAB_ROLE}"
export EVENT_NAME="issue_comment"
export COMMENTER_LOGIN="maint"
export COMMENT_ID="900"
set_role "maint" "write"
set_role "forker" "read"
write_cmd_pr
write_comment "/ok-to-test ${HEAD_SHA}"

# Every comment-mode call goes through this log check: no timing lookups.
CMD_GH_LOG="${TMPDIR}/gh-cmd.log"
: >"${CMD_GH_LOG}"
run_logged_cmd_case() {
  run_cmd_case "$@"
  cat "${GH_LOG}" >>"${CMD_GH_LOG}"
}

run_logged_cmd_case "/ok-to-test <full sha> from write commenter authorizes" "true" "ok_to_test" "${HEAD_SHA}"
if grep -q DELETE "${GH_LOG}"; then
  echo "FAIL: /ok-to-test must not touch labels"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: /ok-to-test does not touch labels"
fi

set_role "maint" "maintain"
run_logged_cmd_case "/ok-to-test <sha> from maintain commenter authorizes" "true" "ok_to_test" "${HEAD_SHA}"
set_role "maint" "admin"
run_logged_cmd_case "/ok-to-test <sha> from admin commenter authorizes" "true" "ok_to_test" "${HEAD_SHA}"
set_role "maint" "write"

write_comment "/ok-to-test ${HEAD_SHA:0:7}"
run_logged_cmd_case "7-character sha prefix is missing_sha" "false" "missing_sha" "${HEAD_SHA}"
write_comment "/ok-to-test ${HEAD_SHA:0:39}"
run_logged_cmd_case "39-character sha prefix is missing_sha" "false" "missing_sha" "${HEAD_SHA}"
write_comment "/ok-to-test ABCDEF0123456789ABCDEF0123456789ABCDEF01"
HEAD_SHA="abcdef0123456789abcdef0123456789abcdef01" write_cmd_pr
run_logged_cmd_case "uppercase sha authorizes" "true" "ok_to_test" "abcdef0123456789abcdef0123456789abcdef01"
write_cmd_pr
write_comment "/ok-to-test   ${HEAD_SHA}   "
run_logged_cmd_case "trailing spaces after sha authorize" "true" "ok_to_test" "${HEAD_SHA}"
write_comment "/ok-to-test ${HEAD_SHA}"$'\r\n'"Reviewed the diff."
run_logged_cmd_case "CRLF line ending after sha authorizes" "true" "ok_to_test" "${HEAD_SHA}"
write_comment "/ok-to-test ${HEAD_SHA}"

# Untrusted commenters never get the head sha.
set_role "maint" "triage"
run_logged_cmd_case "/ok-to-test from triage commenter denied" "false" "untrusted_commenter" ""
set_role "maint" "read"
run_logged_cmd_case "/ok-to-test from read commenter denied" "false" "untrusted_commenter" ""
set_role "maint" "mismatch:write:read"
run_logged_cmd_case "/ok-to-test from mismatched role denied" "false" "untrusted_commenter" ""
echo "" >"${ROLES_DIR}/maint"
run_logged_cmd_case "/ok-to-test from non-collaborator denied" "false" "untrusted_commenter" ""
set_role "maint" "write"

# The PR author's own permission decides only as a commenter.
set_role "forker" "write"
export COMMENTER_LOGIN="forker"
write_comment "/ok-to-test ${HEAD_SHA}" "forker"
run_logged_cmd_case "/ok-to-test <sha> from write fork author authorizes own head" "true" "ok_to_test" "${HEAD_SHA}"
set_role "forker" "read"
run_logged_cmd_case "/ok-to-test <sha> from read fork author denied" "false" "untrusted_commenter" ""
export COMMENTER_LOGIN="maint"

# No sha: refused, with the current head shown for the reply.
write_comment "/ok-to-test"
run_logged_cmd_case "bare /ok-to-test is missing_sha" "false" "missing_sha" "${HEAD_SHA}"
write_comment "/ok-to-test please"
run_logged_cmd_case "/ok-to-test with words is missing_sha" "false" "missing_sha" "${HEAD_SHA}"
write_comment "/ok-to-test ${HEAD_SHA:0:6}"
run_logged_cmd_case "6-character sha is missing_sha" "false" "missing_sha" "${HEAD_SHA}"
write_comment "/ok-to-test${HEAD_SHA:0:7}"
run_logged_cmd_case "sha without a space is missing_sha" "false" "missing_sha" "${HEAD_SHA}"
write_comment "/ok-to-test ${HEAD_SHA}0"
run_logged_cmd_case "41-character sha is missing_sha" "false" "missing_sha" "${HEAD_SHA}"
write_comment "/ok-to-test"$'\n'"${HEAD_SHA}"
run_logged_cmd_case "sha on the second line is missing_sha" "false" "missing_sha" "${HEAD_SHA}"
write_comment "/ok-to-test please"$'\n'"/ok-to-test ${HEAD_SHA}"
run_logged_cmd_case "/ok-to-test <sha> on the second line is missing_sha" "false" "missing_sha" "${HEAD_SHA}"

# The named sha is not the PR head: the PR changed since the review.
write_comment "/ok-to-test 2222222222222222222222222222222222222222"
run_logged_cmd_case "sha of another commit is stale" "false" "stale_ok_to_test" "${HEAD_SHA}"
write_comment "/ok-to-test ${OTHER_SHA}"
run_logged_cmd_case "full sha sharing a prefix with the head is stale" "false" "stale_ok_to_test" "${HEAD_SHA}"
write_comment "/ok-to-test ${HEAD_SHA}"
HEAD_SHA="${OTHER_SHA}" write_cmd_pr
run_logged_cmd_case "old head sha after a push to a look-alike commit is stale" "false" "stale_ok_to_test" "${OTHER_SHA}"
write_cmd_pr
write_comment "/ok-to-test ${HEAD_SHA}"

# Comment identity comes from the API by id.
write_comment "/ok-to-test ${HEAD_SHA}" "someone-else"
run_logged_cmd_case "comment by another user fails closed" "false" "error" ""
write_comment "/ok-to-test ${HEAD_SHA}" "maint" "43"
run_logged_cmd_case "comment on another PR fails closed" "false" "error" ""
write_comment "/ok-to-test ${HEAD_SHA}"
unset COMMENTER_LOGIN
run_logged_cmd_case "comment id supplies the commenter" "true" "ok_to_test" "${HEAD_SHA}"
export COMMENTER_LOGIN="maint"
export COMMENT_ID="900/../../pulls/1"
run_logged_cmd_case "non-numeric comment id fails closed" "false" "error" ""
unset COMMENT_ID
run_logged_cmd_case "missing comment id fails closed" "false" "error" ""
if grep -q . "${GH_LOG}"; then
  echo "FAIL: missing comment id must not call the API"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: missing comment id makes no API call"
fi
export COMMENT_ID="900"

export GH_FAIL="permission"
run_logged_cmd_case "commenter permission API failure returns error" "false" "error" ""
export GH_FAIL="pulls"
run_logged_cmd_case "PR API failure returns error" "false" "error" ""
export GH_FAIL="comment"
run_logged_cmd_case "comment API failure returns error" "false" "error" ""
export GH_FAIL="false"

if grep -qE '/actions/runs|/events' "${CMD_GH_LOG}"; then
  echo "FAIL: comment mode must not call actions/runs or issue events"
  grep -E '/actions/runs|/events' "${CMD_GH_LOG}" | sed 's/^/  /'
  FAILURES=$((FAILURES + 1))
elif ! grep -q '/issues/comments/900' "${CMD_GH_LOG}"; then
  echo "FAIL: comment mode call log is empty or missed the comment fetch"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: comment mode never calls actions/runs or issue events"
fi

unset EVENT_NAME COMMENTER_LOGIN COMMENT_ID

# Clean up
unset PR_AUTHOR_ASSOCIATION PR_AUTHOR_LOGIN
echo "" >"${COLLAB_ROLE}"
export GH_FAIL="false"

if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi

echo "All check-e2e-authorization tests passed."
