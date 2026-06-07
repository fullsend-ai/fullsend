#!/usr/bin/env bash
# check-e2e-authorization_test.sh — Tests for check-e2e-authorization.sh with mock gh.
#
# Run from repo root: bash scripts/check-e2e-authorization_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AUTH_SCRIPT="${SCRIPT_DIR}/check-e2e-authorization.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

MOCK_BIN="${TMPDIR}/bin"
mkdir -p "${MOCK_BIN}"

PR_JSON="${TMPDIR}/pr.json"
COMMITS_JSON="${TMPDIR}/commits.json"
TIMELINE_JSON="${TMPDIR}/timeline.json"
EVENTS_JSON="${TMPDIR}/events.json"
GH_LOG="${TMPDIR}/gh.log"

write_pr() {
  local assoc="$1"
  local labels_json="$2"
  jq -n --arg assoc "${assoc}" --argjson labels "${labels_json}" \
    '{author_association: $assoc, labels: $labels}' >"${PR_JSON}"
}

write_commits() {
  local dates_json="$1"
  jq -n --argjson dates "${dates_json}" \
    '[ $dates[] | {commit: {committer: {date: .}}}]' >"${COMMITS_JSON}"
}

write_timeline() {
  local events_json="$1"
  echo "${events_json}" >"${TIMELINE_JSON}"
}

write_events() {
  local events_json="$1"
  echo "${events_json}" >"${EVENTS_JSON}"
}

cat >"${MOCK_BIN}/gh" <<EOF
#!/usr/bin/env bash
echo "gh \$*" >> "${GH_LOG}"
case "\$*" in
  *"/issues/"*"/timeline"*)
    cat "${TIMELINE_JSON}"
    ;;
  *"/pulls/"*"/commits"*)
    cat "${COMMITS_JSON}"
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
write_timeline '[{"event":"committed","created_at":"2026-06-01T10:00:00Z"}]'
write_events '[]'
run_case "trusted member author" "true" "trusted_author" "false"

write_pr "OWNER" '[]'
write_timeline '[{"event":"committed","created_at":"2026-06-01T10:00:00Z"}]'
write_events '[]'
run_case "trusted owner author" "true" "trusted_author" "false"

write_pr "COLLABORATOR" '[]'
write_timeline '[{"event":"committed","created_at":"2026-06-01T10:00:00Z"}]'
write_events '[]'
run_case "trusted collaborator author" "true" "trusted_author" "false"

write_pr "CONTRIBUTOR" '[]'
write_timeline '[{"event":"committed","created_at":"2026-06-01T10:00:00Z"}]'
write_events '[]'
run_case "contributor author denied" "false" "unauthorized" "false"

write_pr "MEMBER" '[{"name":"ok-to-test"}]'
write_timeline '[{"event":"committed","created_at":"2026-06-01T12:00:00Z"}]'
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "trusted member ignores stale ok-to-test label" "true" "trusted_author" "false"

write_pr "NONE" '[{"name":"ok-to-test"}]'
write_timeline '[{"event":"committed","created_at":"2026-06-01T10:00:00Z"}]'
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "fresh ok-to-test label" "true" "ok_to_test" "false"

write_pr "NONE" '[{"name":"ok-to-test"}]'
write_timeline '[{"event":"committed","created_at":"2026-06-01T12:00:00Z"}]'
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "stale ok-to-test label" "false" "stale_ok_to_test" "true"

write_pr "NONE" '[{"name":"ok-to-test"}]'
write_timeline '[{"event":"committed","created_at":"2026-06-01T12:00:00Z"}]'
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T12:00:00Z"}]'
run_case "ok-to-test label at push time is stale" "false" "stale_ok_to_test" "true"

write_pr "NONE" '[{"name":"ok-to-test"}]'
write_timeline '[{"event":"committed","created_at":"2026-06-01T12:00:00Z"}]'
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
unset CHECK_E2E_AUTH_DRY_RUN
run_case "stale ok-to-test removes label" "false" "stale_ok_to_test" "true"
if ! grep -q DELETE "${GH_LOG}"; then
  echo "FAIL: stale ok-to-test removes label (expected gh DELETE call)"
  FAILURES=$((FAILURES + 1))
else
  echo "PASS: stale ok-to-test removes label (DELETE exercised)"
fi
export CHECK_E2E_AUTH_DRY_RUN="true"

write_pr "NONE" '[]'
write_timeline '[{"event":"committed","created_at":"2026-06-01T10:00:00Z"}]'
write_events '[]'
run_case "untrusted author without label" "false" "unauthorized" "false"

write_pr "NONE" '[{"name":"ok-to-test"}]'
write_commits '["2020-01-01T00:00:00Z"]'
write_timeline '[{"event":"committed","created_at":"2026-06-01T12:00:00Z"}]'
write_events '[{"event":"labeled","label":{"name":"ok-to-test"},"created_at":"2026-06-01T11:00:00Z"}]'
run_case "backdated committer date does not bypass timeline push time" "false" "stale_ok_to_test" "true"

if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi

echo "All check-e2e-authorization tests passed."
