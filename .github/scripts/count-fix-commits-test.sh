#!/usr/bin/env bash
# count-fix-commits-test.sh — Tests for count-fix-commits.jq
#
# Run from the repo root:
#   bash .github/scripts/count-fix-commits-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FILTER="${SCRIPT_DIR}/count-fix-commits.jq"
BOT_EMAIL='1+app[bot]@users.noreply.github.com'
FAILURES=0

# commit NAME EMAIL MESSAGE_JSON — one commit as the API returns it.
commit() { printf '{"commit":{"author":{"name":"%s","email":"%s"},"message":%s}}' "$1" "$2" "$3"; }
# bot MESSAGE_JSON — a commit the API published as the App's bot user.
bot() { commit 'app[bot]' "${BOT_EMAIL}" "$1"; }

# run_test NAME EXPECTED PAGE... — PAGE is a JSON array of commits.
run_test() {
  local name="$1" expected="$2"
  shift 2
  local actual
  actual="$(printf '%s\n' "$@" | jq -s --arg bot_email "${BOT_EMAIL}" -f "${FILTER}" 2>&1)" || true
  if [[ "${actual}" == "${expected}" ]]; then
    echo "PASS: ${name}"
  else
    echo "FAIL: ${name} — expected ${expected}, got '${actual}'"
    FAILURES=$((FAILURES + 1))
  fi
}

run_test "no-commits" 0 '[]'
run_test "author-name" 1 "[$(commit fullsend-fix f@x '"fix: a"')]"
run_test "trailer-in-last-paragraph" 1 "[$(bot '"fix: a\n\nAuthored-by: fullsend-fix <b@x>"')]"
run_test "trailer-with-trailing-whitespace" 1 "[$(bot '"fix: a\n\nAuthored-by: fullsend-fix <b@x>\n\n  \n"')]"
run_test "trailer-quoted-in-body" 0 "[$(bot '"docs: q\n\nAuthored-by: fullsend-fix <b@x>\n\nquoted above."')]"
run_test "code-agent-trailer" 0 "[$(bot '"feat: c\n\nAuthored-by: fullsend-code <b@x>"')]"
run_test "human-with-trailer" 0 "[$(commit human h@x '"fix: a\n\nAuthored-by: fullsend-fix <b@x>"')]"
run_test "other-bot-with-trailer" 0 "[$(commit 'github-actions[bot]' '41898282+github-actions[bot]@users.noreply.github.com' '"fix: a\n\nAuthored-by: fullsend-fix <b@x>"')]"
run_test "empty-message" 0 "[$(bot '""')]"
run_test "null-message" 0 "[$(bot 'null')]"
run_test "pages-are-summed" 3 "[$(commit fullsend-fix f@x '"fix: a"'),$(commit human h@x '"x"')]" "[$(bot '"fix: b\n\nAuthored-by: fullsend-fix <b@x>"'),$(commit fullsend-fix f@x '""')]"

echo
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
