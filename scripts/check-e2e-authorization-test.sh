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
GH_LOG="${TMPDIR}/gh.log"
GH_FAIL="false"

write_pr() {
  local assoc="$1"
  local labels_json="$2"
  local updated_at="${3:-2026-06-01T10:00:00Z}"
  jq -n --arg assoc "${assoc}" --argjson labels "${labels_json}" --arg updated_at "${updated_at}" \
    '{author_association: $assoc, labels: $labels, updated_at: $updated_at}' >"${PR_JSON}"
}

write_events() {
  local events_json="$1"
  echo "${events_json}" >"${EVENTS_JSON}"
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
case "\$*" in
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
unset EVENT_ACTION PR_UPDATED_AT

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
write_events '[]'
run_case "labeled ok-to-test authorizes without events lookup" "true" "ok_to_test" "false"
if grep -q '/events' "${GH_LOG}"; then
  echo "FAIL: labeled path should not call events API"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: labeled path skips events API"
fi

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

if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi

echo "All check-e2e-authorization tests passed."
