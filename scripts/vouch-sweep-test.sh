#!/usr/bin/env bash
# vouch-sweep-test.sh — Tests for vouch-sweep.sh with a stubbed gh.
#
# Run from repo root: bash scripts/vouch-sweep-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SWEEP="${SCRIPT_DIR}/vouch-sweep.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

MOCK_BIN="${TMPDIR}/bin"
FIX="${TMPDIR}/fixtures"
GH_LOG="${TMPDIR}/gh.log"
mkdir -p "${MOCK_BIN}" "${FIX}/roles"

cat >"${MOCK_BIN}/gh" <<'EOF'
#!/usr/bin/env bash
method=GET
path=""
for arg in "$@"; do
  case "${arg}" in
    PATCH|POST) method="${arg}" ;;
    repos/*) path="${arg}" ;;
  esac
done
echo "${method} ${path}" >>"${GH_LOG}"
case "${method}:${path}" in
  GET:*/contents/.github/VOUCHED.td*)
    if [[ "${VOUCHED_FAIL:-}" == "true" ]]; then
      echo "simulated contents API failure" >&2
      exit 1
    fi
    cat "${FIX}/vouched.json"
    ;;
  GET:*/pulls\?state=open*)
    cat "${FIX}/prs.json"
    ;;
  GET:*/collaborators/*/permission)
    login="${path#*/collaborators/}"
    login="${login%/permission}"
    if [[ ! -f "${FIX}/roles/${login}" ]]; then
      echo "gh: Not Found (HTTP 404)" >&2
      exit 1
    fi
    role="$(cat "${FIX}/roles/${login}")"
    if [[ "${role}" == "error" ]]; then
      echo "gh: Server Error (HTTP 500)" >&2
      exit 1
    fi
    echo "{\"role_name\": \"${role}\", \"permission\": \"${role}\"}"
    ;;
  GET:*/issues/*/events)
    n="${path#*/issues/}"; n="${n%/events}"
    cat "${FIX}/events_${n}.json" 2>/dev/null || echo '[]'
    ;;
  GET:*/issues/*/comments)
    n="${path#*/issues/}"; n="${n%/comments}"
    cat "${FIX}/comments_${n}.json" 2>/dev/null || echo '[]'
    ;;
  PATCH:*/pulls/*|POST:*/issues/*/comments)
    echo '{}'
    ;;
  *)
    echo "unexpected gh call: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "${MOCK_BIN}/gh"

export PATH="${MOCK_BIN}:${PATH}"
export GH_LOG FIX
export GH_TOKEN="test-token"
# 2026-06-01T12:00:00Z; the default 60-minute window starts at 11:00:00Z.
VOUCH_SWEEP_NOW="$(jq -n '"2026-06-01T12:00:00Z" | fromdate')"
export VOUCH_SWEEP_NOW

vouched_text='# Vouched Contributors
#
# One GitHub username per line, no @ prefix.
  Vouched-User
-denounced
'
jq -n --arg c "$(printf '%s' "${vouched_text}" | base64)" '{content: $c}' >"${FIX}/vouched.json"

pr() {
  local number="$1" login="$2" created="$3" updated="$4" type="${5:-User}"
  jq -n --argjson n "${number}" --arg l "${login}" --arg c "${created}" --arg u "${updated}" --arg t "${type}" \
    '{number: $n, user: {login: $l, type: $t}, created_at: $c, updated_at: $u}'
}

{
  pr 1 newbie 2026-06-01T11:30:00Z 2026-06-01T11:30:00Z
  pr 2 vouched-user 2026-06-01T11:30:00Z 2026-06-01T11:30:00Z
  pr 3 'renovate-fullsend[bot]' 2026-06-01T11:30:00Z 2026-06-01T11:30:00Z Bot
  pr 4 writer 2026-06-01T11:30:00Z 2026-06-01T11:30:00Z
  pr 5 returner 2026-06-01T09:00:00Z 2026-06-01T11:40:00Z
  pr 6 chatter 2026-06-01T09:00:00Z 2026-06-01T11:40:00Z
  pr 7 oldtimer 2026-06-01T09:00:00Z 2026-06-01T09:30:00Z
  pr 8 repeat 2026-06-01T11:30:00Z 2026-06-01T11:30:00Z
  pr 9 denounced 2026-06-01T11:30:00Z 2026-06-01T11:30:00Z
  pr 10 maintainer 2026-06-01T11:00:00Z 2026-06-01T11:00:00Z
  pr 11 reader 2026-06-01T11:30:00Z 2026-06-01T11:30:00Z
} | jq -s . >"${FIX}/prs.json"

echo "write" >"${FIX}/roles/writer"
echo "maintain" >"${FIX}/roles/maintainer"
echo "read" >"${FIX}/roles/reader"
echo '[{"event":"reopened","created_at":"2026-06-01T11:40:00Z"}]' >"${FIX}/events_5.json"
echo '[{"event":"reopened","created_at":"2026-06-01T08:00:00Z"},{"event":"commented","created_at":"2026-06-01T11:40:00Z"}]' >"${FIX}/events_6.json"
jq -n '[{user: {login: "github-actions[bot]"}, body: "<!-- vouch-check -->\nThank you"}]' >"${FIX}/comments_8.json"
# A copy of the marker from someone else does not suppress the comment.
jq -n '[{user: {login: "newbie"}, body: "<!-- vouch-check -->"}]' >"${FIX}/comments_1.json"

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
logged() { grep -qxF -- "$1" "${GH_LOG}"; }
not_logged() { ! grep -qF -- "$1" "${GH_LOG}"; }
absent() { ! grep -qxF -- "$1" "${GH_LOG}"; }

R="repos/fullsend-ai/fullsend"

# --- Full sweep ---
: >"${GH_LOG}"
rc=0
out="$(bash "${SWEEP}" fullsend-ai/fullsend 2>"${TMPDIR}/err")" || rc=$?
check "sweep exits 0" test "${rc}" -eq 0
check "unvouched new PR closed" logged "PATCH ${R}/pulls/1"
check "unvouched new PR commented" logged "POST ${R}/issues/1/comments"
check "vouched author (case-insensitive) left open" not_logged "${R}/pulls/2"
check "bot author left open" not_logged "${R}/pulls/3"
check "write collaborator left open" not_logged "${R}/pulls/4"
check "maintain collaborator at window start left open" not_logged "${R}/pulls/10"
check "PR reopened in window closed" logged "PATCH ${R}/pulls/5"
check "PR reopened in window commented" logged "POST ${R}/issues/5/comments"
check "older PR without reopen in window left open" not_logged "${R}/pulls/6"
check "older PR without reopen in window: no permission lookup" not_logged "/collaborators/chatter/"
check "PR not updated in window: no lookups" not_logged "oldtimer"
check "PR not updated in window: no events fetch" not_logged "${R}/issues/7/events"
check "already-commented PR closed again" logged "PATCH ${R}/pulls/8"
check "already-commented PR gets no second comment" absent "POST ${R}/issues/8/comments"
check "denounced author closed" logged "PATCH ${R}/pulls/9"
check "read collaborator closed" logged "PATCH ${R}/pulls/11"
check "summary names closed PR" grep -q "^#1: closed" <<<"${out}"

# Close happens before the comment.
close_line="$(grep -n "PATCH ${R}/pulls/1$" "${GH_LOG}" | cut -d: -f1)"
comment_line="$(grep -n "POST ${R}/issues/1/comments$" "${GH_LOG}" | cut -d: -f1)"
check "close precedes comment" test "${close_line}" -lt "${comment_line}"

# --- Dry run changes nothing ---
: >"${GH_LOG}"
out="$(bash "${SWEEP}" --dry-run fullsend-ai/fullsend 2>/dev/null)"
check "dry run makes no PATCH" not_logged "PATCH"
check "dry run makes no POST" not_logged "POST"
check "dry run reports would close" grep -q "^#1: would close" <<<"${out}"
check "dry run reports would comment" grep -q "^#1: would comment" <<<"${out}"
check "dry run skips comment for already-commented PR" grep -q "^#8: vouch comment already posted" <<<"${out}"

# --- Lookback window ---
: >"${GH_LOG}"
bash "${SWEEP}" --lookback-minutes 10 fullsend-ai/fullsend >/dev/null 2>&1
check "10-minute window excludes PR opened 30 minutes ago" absent "PATCH ${R}/pulls/1"
check "10-minute window excludes PR reopened 20 minutes ago" absent "PATCH ${R}/pulls/5"

: >"${GH_LOG}"
bash "${SWEEP}" --lookback-minutes 300 fullsend-ai/fullsend >/dev/null 2>&1
check "300-minute window includes older opened PR" logged "PATCH ${R}/pulls/6"

# --- Errors never close PRs ---
: >"${GH_LOG}"
rc=0
VOUCHED_FAIL=true bash "${SWEEP}" fullsend-ai/fullsend >/dev/null 2>&1 || rc=$?
check "VOUCHED.td read failure exits non-zero" test "${rc}" -ne 0
check "VOUCHED.td read failure closes nothing" not_logged "PATCH"

echo "error" >"${FIX}/roles/newbie"
: >"${GH_LOG}"
rc=0
bash "${SWEEP}" fullsend-ai/fullsend >/dev/null 2>&1 || rc=$?
check "permission API error exits 1" test "${rc}" -eq 1
check "permission API error leaves that PR open" absent "PATCH ${R}/pulls/1"
check "permission API error does not stop other PRs" logged "PATCH ${R}/pulls/9"
rm -f "${FIX}/roles/newbie"

# --- Usage ---
rc=0
bash "${SWEEP}" >/dev/null 2>&1 || rc=$?
check "missing repository is a usage error" test "${rc}" -eq 2
rc=0
bash "${SWEEP}" --lookback-minutes abc fullsend-ai/fullsend >/dev/null 2>&1 || rc=$?
check "non-numeric lookback is a usage error" test "${rc}" -eq 2

if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi

echo "All vouch-sweep tests passed."
