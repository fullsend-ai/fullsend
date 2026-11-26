#!/usr/bin/env bash
# render-release-notes-test.sh — Tests for render-release-notes.sh
#
# Run from the repo root:
#   bash scripts/render-release-notes-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/render-release-notes.sh"
TMP=$(mktemp -d)
trap 'rm -rf "${TMP}"' EXIT

FAILURES=0

fail() {
  echo "FAIL — $1"
  echo "--- expected ---"
  printf '%s\n' "$2"
  echo "--- actual ---"
  printf '%s\n' "$3"
  FAILURES=$((FAILURES + 1))
}

check() {
  local desc=$1 expected=$2 actual
  actual=$("${SCRIPT}" "${TMP}/notes.json" "${TMP}/merged.txt" "${TMP}/titles.tsv")
  if [[ "${actual}" == "${expected}" ]]; then
    echo "ok   — ${desc}"
  else
    echo "FAIL — ${desc}"
    echo "--- expected ---"
    printf '%s\n' "${expected}"
    echo "--- actual ---"
    printf '%s\n' "${actual}"
    FAILURES=$((FAILURES + 1))
  fi
}

write_fixtures() {
  # $1: titles TSV content; stdin: notes JSON. The merged-PR list is
  # written separately after each call.
  cat >"${TMP}/notes.json"
  printf '%b' "$1" >"${TMP}/titles.tsv"
}

# --- Grouping by PR title type -------------------------------------------

write_fixtures '101\tfeat(cli): new thing
102\tfix: a bug
103\trefactor: internals
104\tdocs: some docs
105\tfeat(api)!: breaking change
' <<'EOF'
{
  "101": {"pr_number": 101, "text": "Adds a new CLI flag.",
          "markdown": "Adds a new CLI flag. ([#101](https://example.com/pull/101), [@alice](https://example.com/alice))",
          "pr_url": "https://example.com/pull/101", "author": "alice",
          "author_url": "https://example.com/alice"},
  "102": {"pr_number": 102, "text": "Fixes a crash.",
          "markdown": "Fixes a crash. ([#102](https://example.com/pull/102), [@bob](https://example.com/bob))",
          "pr_url": "https://example.com/pull/102", "author": "bob",
          "author_url": "https://example.com/bob"},
  "103": {"pr_number": 103, "text": "Renames a package.",
          "markdown": "Renames a package. ([#103](https://example.com/pull/103), [@carol](https://example.com/carol))",
          "pr_url": "https://example.com/pull/103", "author": "carol",
          "author_url": "https://example.com/carol"},
  "104": {"pr_number": 104, "text": "Documents a flag.",
          "markdown": "Documents a flag. ([#104](https://example.com/pull/104), [@dave](https://example.com/dave))",
          "pr_url": "https://example.com/pull/104", "author": "dave",
          "author_url": "https://example.com/dave"},
  "105": {"pr_number": 105, "text": "Removes a flag.",
          "markdown": "Removes a flag. ([#105](https://example.com/pull/105), [@erin](https://example.com/erin))",
          "pr_url": "https://example.com/pull/105", "author": "erin",
          "author_url": "https://example.com/erin"}
}
EOF
cat >"${TMP}/merged.txt" <<'EOF'
101
102
103
104
105
EOF

check "notes grouped by title type" '### Breaking Changes

- Removes a flag. ([#105](https://example.com/pull/105), [@erin](https://example.com/erin))

### Features

- Adds a new CLI flag. ([#101](https://example.com/pull/101), [@alice](https://example.com/alice))

### Bug Fixes

- Fixes a crash. ([#102](https://example.com/pull/102), [@bob](https://example.com/bob))

### Refactoring

- Renames a package. ([#103](https://example.com/pull/103), [@carol](https://example.com/carol))

### Other Changes

- Documents a flag. ([#104](https://example.com/pull/104), [@dave](https://example.com/dave))'

# --- PRs outside the merged set are dropped ------------------------------

write_fixtures '101\tfeat(cli): new thing
' <<'EOF'
{
  "101": {"pr_number": 101, "text": "Adds a new CLI flag.",
          "pr_url": "https://example.com/pull/101", "author": "alice",
          "author_url": "https://example.com/alice"},
  "999": {"pr_number": 999, "text": "Old release note.",
          "pr_url": "https://example.com/pull/999", "author": "mallory",
          "author_url": "https://example.com/mallory"}
}
EOF
cat >"${TMP}/merged.txt" <<'EOF'
101
EOF

check "PR cited in a commit body but not merged in range is dropped" '### Features

- Adds a new CLI flag. ([#101](https://example.com/pull/101), [@alice](https://example.com/alice))'

# --- Action-required notes and do-not-publish ----------------------------

write_fixtures '201\tfix: urgent thing
202\tfeat: quiet thing
' <<'EOF'
{
  "201": {"pr_number": 201, "text": "The role field is now required.",
          "pr_url": "https://example.com/pull/201", "author": "alice",
          "author_url": "https://example.com/alice", "action_required": true},
  "202": {"pr_number": 202, "text": "Should not appear.",
          "pr_url": "https://example.com/pull/202", "author": "bob",
          "author_url": "https://example.com/bob", "do_not_publish": true}
}
EOF
cat >"${TMP}/merged.txt" <<'EOF'
201
202
EOF

check "action-required note lands under Urgent Upgrade Notes; do-not-publish dropped" '## Urgent Upgrade Notes

- The role field is now required. ([#201](https://example.com/pull/201), [@alice](https://example.com/alice))'

# --- Multi-line notes stay inside the bullet -----------------------------

write_fixtures '301\tfeat: multi-line
' <<'EOF'
{
  "301": {"pr_number": 301, "text": "First line.\nSecond line.",
          "markdown": "First line.\n  Second line. ([#301](https://example.com/pull/301), [@alice](https://example.com/alice))",
          "pr_url": "https://example.com/pull/301", "author": "alice",
          "author_url": "https://example.com/alice"}
}
EOF
cat >"${TMP}/merged.txt" <<'EOF'
301
EOF

check "multi-line note continuation lines are indented" '### Features

- First line.
  Second line. ([#301](https://example.com/pull/301), [@alice](https://example.com/alice))'

# --- Titles TSV produced the way release.yml produces it ---------------

# release.yml builds the TSV with `gh pr view --json number,title
# --jq '"\(.number)\t\(.title)"'`. gh's --jq is jq, so simulate it with
# jq on the same JSON shape and feed the result to the renderer, so the
# producer and consumer cannot drift apart again.
printf '{"number": 501, "title": "feat: tsv producer"}\n' \
  | jq -r '"\(.number)\t\(.title)"' > "${TMP}/titles.tsv"
cat >"${TMP}/notes.json" <<'EOF'
{
  "501": {"pr_number": 501, "text": "Note from the TSV producer test.",
          "pr_url": "https://example.com/pull/501", "author": "alice",
          "author_url": "https://example.com/alice"}
}
EOF
printf '501\n' >"${TMP}/merged.txt"

check "titles TSV built with the workflow's jq expression drives section choice" '### Features

- Note from the TSV producer test. ([#501](https://example.com/pull/501), [@alice](https://example.com/alice))'

# --- Empty input ----------------------------------------------------------

write_fixtures '401\tfeat: nothing
' <<'EOF'
{}
EOF
: >"${TMP}/merged.txt"

check "empty notes render an empty body" ''

# --- PRs without a release note -------------------------------------------

# A merged PR absent from the notes JSON (no release-note block) must be
# listed under "PRs Without Release Notes" instead of vanishing, and a
# warning must go to stderr.
printf '601\tfix: has note\n602\tchore: no block\n' > "${TMP}/titles.tsv"
cat >"${TMP}/notes.json" <<'EOF'
{
  "601": {"pr_number": 601, "text": "Has a note.",
          "pr_url": "https://example.com/pull/601", "author": "alice",
          "author_url": "https://example.com/alice"}
}
EOF
printf '601\n602\n' > "${TMP}/merged.txt"

actual=$("${SCRIPT}" "${TMP}/notes.json" "${TMP}/merged.txt" "${TMP}/titles.tsv" 2>"${TMP}/stderr")
expected='### Bug Fixes

- Has a note. ([#601](https://example.com/pull/601), [@alice](https://example.com/alice))

### PRs Without Release Notes

- #602 chore: no block'
if [[ "${actual}" == "${expected}" ]]; then
  echo "ok   — merged PR without a note is listed, not dropped"
else
  fail "merged PR without a note is listed, not dropped" "${expected}" "${actual}"
fi
if grep -q "PRs merged in the range have no release note" "${TMP}/stderr"; then
  echo "ok   — missing-note warning goes to stderr"
else
  echo "FAIL — missing-note warning goes to stderr"
  cat "${TMP}/stderr"
  FAILURES=$((FAILURES + 1))
fi

# A non-empty range that gathered zero notes must warn.
printf '701\tfeat: nothing\n' > "${TMP}/titles.tsv"
printf '{}' > "${TMP}/notes.json"
printf '701\n' > "${TMP}/merged.txt"

"${SCRIPT}" "${TMP}/notes.json" "${TMP}/merged.txt" "${TMP}/titles.tsv" \
  >"${TMP}/stdout" 2>"${TMP}/stderr"
if grep -q "Zero release notes gathered" "${TMP}/stderr"; then
  echo "ok   — zero notes from a non-empty range warns"
else
  echo "FAIL — zero notes from a non-empty range warns"
  cat "${TMP}/stderr"
  FAILURES=$((FAILURES + 1))
fi

# --- Missing files --------------------------------------------------------

if "${SCRIPT}" "${TMP}/missing.json" "${TMP}/merged.txt" "${TMP}/titles.tsv" 2>/dev/null; then
  echo "FAIL — missing notes file should be an error"
  FAILURES=$((FAILURES + 1))
else
  echo "ok   — missing notes file is an error"
fi

if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "all tests passed"
