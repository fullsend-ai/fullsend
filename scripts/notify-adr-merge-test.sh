#!/usr/bin/env bash
# notify-adr-merge-test.sh — Tests for notify-adr-merge.sh with stubbed gh and curl.
#
# Run from repo root: bash scripts/notify-adr-merge-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NOTIFY="${SCRIPT_DIR}/notify-adr-merge.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

MOCK_BIN="${TMPDIR}/bin"
FIX="${TMPDIR}/fixtures"
GH_LOG="${TMPDIR}/gh.log"
CURL_LOG="${TMPDIR}/curl.log"
mkdir -p "${MOCK_BIN}" "${FIX}"

cat >"${MOCK_BIN}/gh" <<'EOF'
#!/usr/bin/env bash
path=""
for arg in "$@"; do
  case "${arg}" in repos/*) path="${arg}" ;; esac
done
echo "${path}" >>"${GH_LOG}"
case "${path}" in
  */commits/*/pulls)
    sha="${path#*/commits/}"; sha="${sha%/pulls}"
    cat "${FIX}/commit_${sha}.json" 2>/dev/null || echo '[]'
    ;;
  */pulls/*/files)
    n="${path#*/pulls/}"; n="${n%/files}"
    cat "${FIX}/files_${n}.json"
    ;;
  */pulls/*)
    n="${path#*/pulls/}"
    cat "${FIX}/pr_${n}.json"
    ;;
  *)
    echo "unexpected gh call: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "${MOCK_BIN}/gh"

# Each call appends one payload line to CURL_LOG.
cat >"${MOCK_BIN}/curl" <<'EOF'
#!/usr/bin/env bash
if [[ "${CURL_FAIL:-}" == "true" ]]; then
  exit 22
fi
cat >>"${CURL_LOG}"
EOF
chmod +x "${MOCK_BIN}/curl"

export PATH="${MOCK_BIN}:${PATH}"
export GH_LOG CURL_LOG FIX
export GH_TOKEN="test-token"
export SLACK_WEBHOOK_URL="https://hooks.example.invalid/test"

SHA_A="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
SHA_B="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
SHA_C="cccccccccccccccccccccccccccccccccccccccc"
SHA_D="dddddddddddddddddddddddddddddddddddddddd"
SHA_E="eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

# A carries PR 10 (ADRs) and an unmerged PR 99; B carries PR 10 again and PR 11
# (no ADRs); C carries PR 12 (ADRs); D carries no PR; E carries only PR 11.
echo '[{"number":10,"merged_at":"2026-06-01T10:00:00Z"},{"number":99,"merged_at":null}]' >"${FIX}/commit_${SHA_A}.json"
echo '[{"number":10,"merged_at":"2026-06-01T10:00:00Z"},{"number":11,"merged_at":"2026-06-01T10:00:00Z"}]' >"${FIX}/commit_${SHA_B}.json"
echo '[{"number":12,"merged_at":"2026-06-01T10:05:00Z"}]' >"${FIX}/commit_${SHA_C}.json"
echo '[{"number":11,"merged_at":"2026-06-01T10:00:00Z"}]' >"${FIX}/commit_${SHA_E}.json"

echo '[{"filename":"docs/ADRs/0001-first.md"},{"filename":"README.md"}][{"filename":"docs/ADRs/0002-second.md"}]' >"${FIX}/files_10.json"
echo '[{"filename":"internal/foo.go"}]' >"${FIX}/files_11.json"
echo '[{"filename":"docs/ADRs/0003-third.md"}]' >"${FIX}/files_12.json"
for n in 10 11 12; do
  jq -n --argjson n "${n}" '{number: $n, title: ("Title " + ($n | tostring)), html_url: ("https://github.com/o/r/pull/" + ($n | tostring)), merged_by: {login: "merger"}}' >"${FIX}/pr_${n}.json"
done

check() {
  local name="$1"
  shift
  if "$@"; then
    echo "PASS: ${name}"
  else
    echo "FAIL: ${name}"
    FAILURES=$((FAILURES + 1))
  fi
}

reset() { : >"${GH_LOG}"; : >"${CURL_LOG}"; }

# --- Dry run prints payloads, sends nothing ---
reset
out="$(bash "${NOTIFY}" --dry-run fullsend-ai/fullsend "${SHA_A}" "${SHA_B}" "${SHA_C}" "${SHA_D}" 2>/dev/null)"
check "dry run sends nothing" test ! -s "${CURL_LOG}"
check "one payload per ADR PR" test "$(wc -l <<<"${out}")" -eq 2
first="$(sed -n 1p <<<"${out}")"
second="$(sed -n 2p <<<"${out}")"
expected_head='📋 *ADR updated* — <https://github.com/o/r/pull/10|#10 Title 10>
Merged by *merger*'
check "header matches former format" test "$(jq -r '.blocks[0].text.text' <<<"${first}")" == "${expected_head}"
check "header is mrkdwn section" test "$(jq -r '.blocks[0] | .type + "/" + .text.type' <<<"${first}")" == "section/mrkdwn"
expected_files='*Affected ADRs:*
• 0001-first
• 0002-second
'
check "ADR list spans pages and drops non-ADR files" test "$(jq -r '.blocks[1].text.text' <<<"${first}"; printf x)" == "${expected_files}"$'\n'x
check "second payload is PR 12" grep -q '#12 Title 12' <<<"${second}"
check "PR without ADR files is skipped" test "$(grep -c '#11' <<<"${out}" || true)" -eq 0
check "unmerged PR is not fetched" test "$(grep -c '/pulls/99' "${GH_LOG}" || true)" -eq 0
check "PR 10 deduped across commits" test "$(grep -cx 'repos/fullsend-ai/fullsend/pulls/10/files' "${GH_LOG}")" -eq 1

# --- Send ---
reset
rc=0
bash "${NOTIFY}" fullsend-ai/fullsend "${SHA_A}" "${SHA_B}" "${SHA_C}" >/dev/null 2>&1 || rc=$?
check "send exits 0" test "${rc}" -eq 0
check "send posts one message per ADR PR" test "$(wc -l <"${CURL_LOG}")" -eq 2
sent_ok() { jq -e '.blocks | length == 2' <(head -n1 "${CURL_LOG}") >/dev/null; }
check "sent payload is valid JSON" sent_ok

# --- Push with no merged PR ---
reset
rc=0
bash "${NOTIFY}" fullsend-ai/fullsend "${SHA_D}" >/dev/null 2>&1 || rc=$?
check "push without merged PR exits 0" test "${rc}" -eq 0
check "push without merged PR sends nothing" test ! -s "${CURL_LOG}"

# --- Missing webhook / failed send ---
reset
rc=0
SLACK_WEBHOOK_URL="" bash "${NOTIFY}" fullsend-ai/fullsend "${SHA_C}" >/dev/null 2>&1 || rc=$?
check "missing webhook with ADR PR exits 1" test "${rc}" -eq 1
rc=0
SLACK_WEBHOOK_URL="" bash "${NOTIFY}" --dry-run fullsend-ai/fullsend "${SHA_C}" >/dev/null 2>&1 || rc=$?
check "dry run does not need the webhook" test "${rc}" -eq 0
rc=0
SLACK_WEBHOOK_URL="" bash "${NOTIFY}" fullsend-ai/fullsend "${SHA_E}" >/dev/null 2>&1 || rc=$?
check "missing webhook without ADR PR exits 0" test "${rc}" -eq 0
rc=0
CURL_FAIL=true bash "${NOTIFY}" fullsend-ai/fullsend "${SHA_C}" >/dev/null 2>&1 || rc=$?
check "failed send exits 1" test "${rc}" -eq 1

# --- Usage ---
rc=0
bash "${NOTIFY}" fullsend-ai/fullsend >/dev/null 2>&1 || rc=$?
check "missing sha is a usage error" test "${rc}" -eq 2
rc=0
bash "${NOTIFY}" fullsend-ai/fullsend 'not-a-sha;rm' >/dev/null 2>&1 || rc=$?
check "invalid sha is rejected" test "${rc}" -eq 2

if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi

echo "All notify-adr-merge tests passed."
