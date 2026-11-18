#!/usr/bin/env bash
# prepend-release-highlights-test.sh — Tests for prepend-release-highlights.sh
#
# Run from the repo root:
#   bash skills/cutting-releases/scripts/prepend-release-highlights-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/prepend-release-highlights.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# The mock gh stores the release body byte for byte and prints it unchanged,
# which is what GitHub and `gh release view --template '{{.body}}'` do.
#   MOCK_STATE         directory holding body, calls.log and edit count
#   MOCK_MANGLE_FIRST  when set, the first edit stores an altered body
#   MOCK_EDIT_FAIL     when set, edits fail without storing anything
#   MOCK_EDIT_LATE_FAIL when set, edits store the body and then fail
#   MOCK_CHANGE_ON_VIEW N: before read N, another editor stores MOCK_OTHER_BODY
#   MOCK_VIEW_FAIL_ON  N: read N fails
# Reads by the script: 1 = initial, 2 = just before the edit, 3 = after it.
MOCK_BIN="${TMPDIR}/bin"
mkdir -p "${MOCK_BIN}"
cat > "${MOCK_BIN}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
echo "$*" >> "${MOCK_STATE}/calls.log"
case "$*" in
  "release view v9.9.9 --repo fullsend-ai/fullsend --json body --template {{.body}}")
    views=$(( $(cat "${MOCK_STATE}/views" 2>/dev/null || echo 0) + 1 ))
    echo "${views}" > "${MOCK_STATE}/views"
    if [[ "${views}" == "${MOCK_CHANGE_ON_VIEW:-}" ]]; then
      printf '%s' "${MOCK_OTHER_BODY}" > "${MOCK_STATE}/body"
    fi
    [[ "${views}" != "${MOCK_VIEW_FAIL_ON:-}" ]] || { echo "gh: HTTP 502" >&2; exit 1; }
    cat "${MOCK_STATE}/body"
    ;;
  "release edit v9.9.9 --repo fullsend-ai/fullsend --notes-file "*)
    [[ -z "${MOCK_EDIT_FAIL:-}" ]] || { echo "gh: HTTP 500" >&2; exit 1; }
    edits=$(( $(cat "${MOCK_STATE}/edits" 2>/dev/null || echo 0) + 1 ))
    echo "${edits}" > "${MOCK_STATE}/edits"
    cp "${@: -1}" "${MOCK_STATE}/body"
    if [[ -n "${MOCK_MANGLE_FIRST:-}" && "${edits}" -eq 1 ]]; then
      printf 'mangled' >> "${MOCK_STATE}/body"
    fi
    [[ -z "${MOCK_EDIT_LATE_FAIL:-}" ]] || { echo "gh: timeout" >&2; exit 1; }
    ;;
  *)
    echo "mock gh: unexpected command: $*" >&2
    exit 1
    ;;
esac
MOCKEOF
chmod +x "${MOCK_BIN}/gh"

# Release body as GoReleaser publishes it, with bytes that must survive:
# CRLF, trailing spaces, backticks, `$(` and no final newline.
OLD_BODY=$'## Changelog\n### Features\n* abc: feat: add `$(x)` support (@a)  \r\n* def: fix: y (@b)'

# run_case sets up state, runs the script, and leaves STATE/OUTPUT/EXIT.
#   $1 — highlights content (written with printf '%s', no expansion)
#   $2 — stored release body
#   remaining args — extra environment for the mock (e.g. MOCK_EDIT_FAIL=1)
run_case() {
  local highlights="$1" body="$2"
  shift 2
  STATE="$(mktemp -d "${TMPDIR}/state.XXXXXX")"
  printf '%s' "${body}" > "${STATE}/body"
  printf '%s' "${highlights}" > "${STATE}/highlights.md"
  : > "${STATE}/calls.log"
  EXIT=0
  OUTPUT=$(
    env PATH="${MOCK_BIN}:${PATH}" MOCK_STATE="${STATE}" "$@" \
      bash "${SCRIPT}" v9.9.9 < "${STATE}/highlights.md" 2>&1
  ) || EXIT=$?
}

# writes prints how many `gh release edit` calls the script made.
writes() {
  local n
  n=$(grep -c '^release edit' "${STATE}/calls.log") || true
  echo "${n:-0}"
}

# body_is succeeds when the stored body equals $1 byte for byte.
body_is() {
  [[ "$(cat "${STATE}/body"; printf x)" == "$1x" ]]
}

fail() {
  echo "FAIL: $1"
  echo "  exit: ${EXIT}  output: ${OUTPUT}"
  FAILURES=$((FAILURES + 1))
}

# expect_reject asserts a non-zero exit with a message and no edit call.
expect_reject() {
  local name="$1" message="$2"
  if [[ "${EXIT}" -eq 0 || "${OUTPUT}" != *"${message}"* ]]; then
    fail "${name}"
  elif grep -q 'release edit' "${STATE}/calls.log"; then
    fail "${name} — release was edited"
  else
    echo "PASS: ${name}"
  fi
}

# expect_prepended asserts success with highlights, rule, then the old body,
# written in exactly one edit.
expect_prepended() {
  local name="$1" highlights="$2"
  if [[ "${EXIT}" -eq 0 && "$(writes)" -eq 1 ]] \
    && body_is "${highlights}"$'\n\n---\n\n'"${OLD_BODY}"; then
    echo "PASS: ${name}"
  else
    fail "${name}"
  fi
}

echo "=== prepend-release-highlights tests ==="

HL=$'**Feature** lands.\n'
run_case "${HL}" "${OLD_BODY}"
expect_prepended "prepends highlights, old body kept byte for byte" "${HL}"

# v0.33.0 regression (#8366): a pasted placeholder must never be published.
run_case $'**Feature** lands.\n\n---\n\n$(cat /tmp/release-body-existing.txt)\n' "${OLD_BODY}"
expect_reject "rejects standalone \$(...) line" "unexpanded shell expression"

run_case $'**Feature** lands.\n  ${EXISTING_BODY}\n' "${OLD_BODY}"
expect_reject "rejects standalone \${...} line" "unexpanded shell expression"

# A migration snippet with an inline $(cat ...) is legitimate (#8649 review).
HL=$'**Token** handling changed:\n\n```bash\nGH_TOKEN=$(cat ~/.token) fullsend run\n```\n'
run_case "${HL}" "${OLD_BODY}"
expect_prepended "allows inline \$(cat file) in a code block" "${HL}"

run_case $'**Feature** lands.\n\n<existing body>\n' "${OLD_BODY}"
expect_reject "rejects old template placeholder" "template placeholder"

# v0.38.0 shape: an expansion inside prose is legitimate.
HL=$'Harnesses no longer get `${OPENAI_API_KEY}` injection.\n'
run_case "${HL}" "${OLD_BODY}"
expect_prepended "allows inline \${VAR} in prose" "${HL}"

HL=$'Use `$(fullsend version)` and $HOME.\nEOF\n'
run_case "${HL}" "${OLD_BODY}"
expect_prepended "keeps shell-special highlights verbatim" "${HL}"

# Highlights may start with a heading that also appears in the changelog.
HL=$'### Breaking Changes\n\n**Scaffold** no longer ships agent files.\n'
run_case "${HL}" $'## Changelog\n### Breaking Changes\n* abc: feat!: x (@a)\n'
if [[ "${EXIT}" -eq 0 ]]; then echo "PASS: allows heading shared with changelog"; else fail "allows heading shared with changelog"; fi

# No final newline: the last line must not become a setext heading.
run_case 'No trailing newline' "${OLD_BODY}"
expect_prepended "blank line before rule without final newline" 'No trailing newline'

run_case $'Some highlights\n' $'Only prose, no changelog\n'
expect_reject "refuses body without changelog" "have no '## Changelog' line"

run_case $'Some highlights\n' ''
expect_reject "refuses empty release body" "have no '## Changelog' line"

run_case $'Some highlights\n' $'## Changelog\r\n* abc: fix: x (@a)\n'
expect_reject "refuses CRLF first line" "stray characters"

run_case $'Some highlights\n' $'\xef\xbb\xbf## Changelog\n* abc: fix: x (@a)\n'
expect_reject "refuses byte-order mark first line" "stray characters"

run_case $'Intro\n\n## Changelog\n* x\n' "${OLD_BODY}"
expect_reject "refuses highlights containing changelog" "must not contain the changelog"

# Running twice must not prepend again, even with different highlights.
run_case $'Other words\n' $'**Feature** lands.\n\n\n---\n\n'"${OLD_BODY}"
expect_reject "refuses second prepend" "highlights already added"

run_case '' "${OLD_BODY}"
expect_reject "refuses empty highlights" "empty or blank"

run_case $'\n  \n\t\n' "${OLD_BODY}"
expect_reject "refuses whitespace-only highlights" "empty or blank"

# Another editor changes the notes after the first read: no write at all.
OTHER=$'## Changelog\n* zzz: fix: someone else (@c)\n'
run_case $'Highlights\n' "${OLD_BODY}" MOCK_CHANGE_ON_VIEW=2 MOCK_OTHER_BODY="${OTHER}"
if [[ "${EXIT}" -ne 0 && "${OUTPUT}" == *"nothing edited"* && "$(writes)" -eq 0 ]] \
  && body_is "${OTHER}"; then
  echo "PASS: change before the edit stops with no write"
else
  fail "change before the edit stops with no write"
fi

# Stored body differs after the edit: report, never overwrite, keep copies.
run_case $'Highlights\n' "${OLD_BODY}" MOCK_MANGLE_FIRST=1
kept=$(sed -n 's/^Copies kept in \([^:]*\):.*/\1/p' <<< "${OUTPUT}")
if [[ "${EXIT}" -ne 0 && "${OUTPUT}" == *"nothing overwritten"* && "$(writes)" -eq 1 ]] \
  && [[ "$(cat "${STATE}/body")" == *mangled ]] \
  && [[ -n "${kept}" && -f "${kept}/old.md" && -f "${kept}/new.md" && -f "${kept}/after.md" ]]; then
  echo "PASS: mismatch after the edit keeps the other text and the copies"
else
  fail "mismatch after the edit keeps the other text and the copies"
fi
[[ -z "${kept}" ]] || rm -rf "${kept}"

# Edit fails without applying: reported, body unchanged, no second write.
run_case $'Highlights\n' "${OLD_BODY}" MOCK_EDIT_FAIL=1
if [[ "${EXIT}" -ne 0 && "${OUTPUT}" == *"did not apply"* && "$(writes)" -eq 1 ]] \
  && body_is "${OLD_BODY}"; then
  echo "PASS: failed edit leaves body unchanged"
else
  fail "failed edit leaves body unchanged"
fi

# Re-read after the edit fails: no further write, copies kept.
HL=$'Highlights\n'
run_case "${HL}" "${OLD_BODY}" MOCK_VIEW_FAIL_ON=3
kept=$(sed -n 's/^Copies kept in \([^:]*\):.*/\1/p' <<< "${OUTPUT}")
if [[ "${EXIT}" -ne 0 && "${OUTPUT}" == *"could not re-read"* && "$(writes)" -eq 1 && -n "${kept}" ]] \
  && body_is "${HL}"$'\n\n---\n\n'"${OLD_BODY}"; then
  echo "PASS: failed re-read stops with no further write"
else
  fail "failed re-read stops with no further write"
fi
[[ -z "${kept}" ]] || rm -rf "${kept}"

# Edit applied but gh reported an error: the result is still verified.
HL=$'Highlights\n'
run_case "${HL}" "${OLD_BODY}" MOCK_EDIT_LATE_FAIL=1
if [[ "${OUTPUT}" == *"checking what the release now holds"* ]]; then
  expect_prepended "verifies an edit that reported failure" "${HL}"
else
  fail "verifies an edit that reported failure"
fi

# The exact command form from SKILL.md step 11.5: a quoted heredoc passes
# shell-special text through untouched.
STATE="$(mktemp -d "${TMPDIR}/state.XXXXXX")"
printf '%s' "${OLD_BODY}" > "${STATE}/body"
: > "${STATE}/calls.log"
EXIT=0
OUTPUT=$(
  env PATH="${MOCK_BIN}:${PATH}" MOCK_STATE="${STATE}" \
    bash "${SCRIPT}" v9.9.9 <<'HIGHLIGHTS_EOF' 2>&1
Run `$(fullsend version)` with $HOME set.
EOF
HIGHLIGHTS_EOF
) || EXIT=$?
expect_prepended "skill heredoc form keeps text verbatim" \
  $'Run `$(fullsend version)` with $HOME set.\nEOF\n'

# The old file-argument form must not be mistaken for a tag plus highlights.
STATE="$(mktemp -d "${TMPDIR}/state.XXXXXX")"
printf '%s' "${OLD_BODY}" > "${STATE}/body"
printf 'Highlights\n' > "${STATE}/highlights.md"
: > "${STATE}/calls.log"
EXIT=0
OUTPUT=$(
  env PATH="${MOCK_BIN}:${PATH}" MOCK_STATE="${STATE}" \
    bash "${SCRIPT}" v9.9.9 "${STATE}/highlights.md" < /dev/null 2>&1
) || EXIT=$?
expect_reject "rejects old highlights-file argument" "Usage:"

EXIT=0
OUTPUT=$(bash "${SCRIPT}" 9.9.9 < /dev/null 2>&1) || EXIT=$?
if [[ "${EXIT}" -ne 0 && "${OUTPUT}" == *"must start with 'v'"* ]]; then
  echo "PASS: rejects malformed tag"
else
  fail "rejects malformed tag"
fi

echo
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
