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
  local assoc="$1"
  local labels_json="$2"
  local updated_at="${3:-2026-06-01T10:00:00Z}"
  jq -n --arg assoc "${assoc}" --argjson labels "${labels_json}" --arg updated_at "${updated_at}" \
    '{author_association: $assoc, labels: $labels, updated_at: $updated_at}' >"${PR_JSON}"
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
if [[ "\${GH_FAIL}" == "permission" && "\$*" == *"/collaborators/"* ]]; then
  echo "simulated permission API failure" >&2
  exit 1
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
    echo "{\"role_name\": \"\${role}\"}"
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

write_pr "MEMBER" '[]'
run_case "trusted member author" "true" "trusted_author" "false"

export PR_AUTHOR_ASSOCIATION="MEMBER"
write_pr "NONE" '[]'
run_case "event payload trusted author overrides API NONE" "true" "trusted_author" "false"
if grep -q '/pulls/' "${GH_LOG}"; then
  echo "FAIL: trusted event payload should not call pulls API"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: trusted event payload skips pulls API"
fi
unset PR_AUTHOR_ASSOCIATION

export PR_AUTHOR_ASSOCIATION="CONTRIBUTOR"
export EVENT_ACTION="synchronize"
export PR_UPDATED_AT="2026-06-01T10:00:00Z"
write_pr "NONE" '[{"name":"ok-to-test"}]'
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "untrusted event payload falls through to ok-to-test label check" "true" "ok_to_test" "false"
if ! grep -q '/pulls/' "${GH_LOG}"; then
  echo "FAIL: untrusted event payload should fetch pulls API for labels"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: untrusted event payload fetches pulls API for labels"
fi
unset PR_AUTHOR_ASSOCIATION EVENT_ACTION PR_UPDATED_AT

write_pr "OWNER" '[]'
run_case "trusted owner author" "true" "trusted_author" "false"

write_pr "COLLABORATOR" '[]'
run_case "trusted collaborator author" "true" "trusted_author" "false"

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

unset PR_AUTHOR_ASSOCIATION PR_AUTHOR_LOGIN

write_pr "MEMBER" '[{"name":"ok-to-test"}]'
run_case "trusted member ignores stale ok-to-test label" "true" "trusted_author" "false"

export EVENT_ACTION="synchronize"
export PR_UPDATED_AT="2026-06-01T10:00:00Z"
write_pr "NONE" '[{"name":"ok-to-test"}]'
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "fresh ok-to-test label after push" "true" "ok_to_test" "false"

export PR_UPDATED_AT="2026-06-01T12:00:00Z"
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "stale ok-to-test label after newer push" "false" "stale_ok_to_test" "true"

export PR_UPDATED_AT="2026-06-01T12:00:00Z"
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T12:00:00Z"}]'
run_case "ok-to-test label at push time is stale" "false" "stale_ok_to_test" "true"

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
write_pr "NONE" '[{"name":"ok-to-test"}]'
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "labeled ok-to-test verifies labeler permission" "true" "ok_to_test" "false"
if grep -q '/issues/42/events' "${GH_LOG}"; then
  echo "PASS: labeled path checks events API"
else
  echo "FAIL: labeled path should check events API"
  FAILURES=$((FAILURES + 1))
fi

# Triage-role users can apply labels but must not authorize a run.
set_role "triager" "triage"
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z","actor":{"login":"triager"}}]'
run_case "ok-to-test labeler without write permission denied" "false" "untrusted_labeler" "true"

# The latest ok-to-test labeler decides, not an earlier one.
write_events '[
  {"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T09:00:00Z"},
  {"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z","actor":{"login":"triager"}}
]'
run_case "latest ok-to-test labeler without write permission denied" "false" "untrusted_labeler" "true"

# Other labels applied later by anyone do not change who applied ok-to-test.
write_events '[
  {"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"},
  {"event":"labeled","label":{"name":"component/cli"},"created_at":"2026-06-01T11:30:00Z","actor":{"login":"triager"}}
]'
run_case "later non-ok-to-test label by another user is ignored" "true" "ok_to_test" "false"

write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z","actor":null}]'
run_case "ok-to-test label event without actor denied" "false" "untrusted_labeler" "true"

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

# The sender of a non-labeled event (e.g. the pusher) is not the labeler.
export EVENT_ACTION="synchronize"
export PR_UPDATED_AT="2026-06-01T10:00:00Z"
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z","actor":{"login":"triager"}}]'
run_case "sender ignored outside labeled events" "false" "untrusted_labeler" "true"
unset LABEL_ACTOR_LOGIN PR_UPDATED_AT
export EVENT_ACTION="labeled"

# A failed labeler lookup is an API error, not a denial.
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
export GH_FAIL="permission"
run_case "labeler permission API failure returns error" "false" "error" "false"
export GH_FAIL="false"

unset CHECK_E2E_AUTH_DRY_RUN
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z","actor":{"login":"triager"}}]'
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
export CHECK_E2E_AUTH_DRY_RUN="true"

export EVENT_ACTION="synchronize"
unset PR_UPDATED_AT
write_pr "NONE" '[{"name":"ok-to-test"}]' "2026-06-01T10:00:00Z"
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "falls back to pull updated_at when PR_UPDATED_AT unset" "true" "ok_to_test" "false"

write_pr "MEMBER" '[]'
export GH_FAIL="events"
run_case "trusted author not blocked by events API failure" "true" "trusted_author" "false"

export EVENT_ACTION="synchronize"
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

# --- Collaborator permission API fallback tests ---

# Private org member: event payload says CONTRIBUTOR, but collaborator API says write+
unset EVENT_ACTION PR_UPDATED_AT
export PR_AUTHOR_ASSOCIATION="CONTRIBUTOR"
export PR_AUTHOR_LOGIN="maruiz93"
echo "admin" >"${COLLAB_ROLE}"
write_pr "NONE" '[]'
: >"${GH_LOG}"
run_case "private org member: collaborator API fallback authorizes admin" "true" "trusted_author" "false"
if ! grep -q '/collaborators/' "${GH_LOG}"; then
  echo "FAIL: should have called collaborator permission API"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: called collaborator permission API for fallback"
fi

echo "write" >"${COLLAB_ROLE}"
: >"${GH_LOG}"
run_case "private org member: collaborator API fallback authorizes write" "true" "trusted_author" "false"

echo "maintain" >"${COLLAB_ROLE}"
: >"${GH_LOG}"
run_case "private org member: collaborator API fallback authorizes maintain" "true" "trusted_author" "false"

# Collaborator API says read — should NOT authorize
echo "read" >"${COLLAB_ROLE}"
write_pr "NONE" '[]'
: >"${GH_LOG}"
run_case "collaborator API read permission denied" "false" "unauthorized" "false"

# Collaborator API fails for the PR author — falls through to the ok-to-test
# path, where the labeler's own permission is checked.
echo "" >"${COLLAB_ROLE}"
export EVENT_ACTION="synchronize"
export PR_UPDATED_AT="2026-06-01T10:00:00Z"
write_pr "NONE" '[{"name":"ok-to-test"}]'
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
: >"${GH_LOG}"
run_case "collaborator API failure falls through to ok-to-test" "true" "ok_to_test" "false"

# No PR_AUTHOR_LOGIN set — should skip collaborator API entirely
unset PR_AUTHOR_LOGIN EVENT_ACTION PR_UPDATED_AT
export PR_AUTHOR_ASSOCIATION="CONTRIBUTOR"
echo "admin" >"${COLLAB_ROLE}"
write_pr "NONE" '[]'
: >"${GH_LOG}"
run_case "no PR_AUTHOR_LOGIN skips collaborator API fallback" "false" "unauthorized" "false"
if grep -q '/collaborators/' "${GH_LOG}"; then
  echo "FAIL: should NOT have called collaborator API without PR_AUTHOR_LOGIN"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: skipped collaborator API without PR_AUTHOR_LOGIN"
fi

# Clean up
unset PR_AUTHOR_ASSOCIATION PR_AUTHOR_LOGIN
echo "" >"${COLLAB_ROLE}"
export GH_FAIL="false"

if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi

echo "All check-e2e-authorization tests passed."
