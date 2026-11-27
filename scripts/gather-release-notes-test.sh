#!/usr/bin/env bash
# gather-release-notes-test.sh — Tests for the pure functions of
# gather-release-notes.sh (merged-PR extraction).
#
# The full script needs the release-notes binary and an authenticated
# gh, so only the offline parts are exercised here: extract_merged_prs
# (merge commits and squash subjects). The START_SHA selection is a
# plain `git rev-parse PREV` (v0.23.0 stops its first-parent walk at the
# merge base of START_SHA and END_SHA, exclusive), so it needs no test.
#
# Run from the repo root:
#   bash scripts/gather-release-notes-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/gather-release-notes.sh
source "${SCRIPT_DIR}/gather-release-notes.sh"

TMP=$(mktemp -d)
trap 'rm -rf "${TMP}"' EXIT

FAILURES=0

ok() {
  echo "ok   — $1"
}

fail() {
  echo "FAIL — $1"
  echo "--- expected ---"
  printf '%s\n' "$2"
  echo "--- actual ---"
  printf '%s\n' "$3"
  FAILURES=$((FAILURES + 1))
}

assert_equals() {
  local desc=$1 expected=$2 actual=$3
  if [[ "${actual}" == "${expected}" ]]; then
    ok "${desc}"
  else
    fail "${desc}" "${expected}" "${actual}"
  fi
}

# Build a repo shaped like a real release range:
#
#   A --- B(merge #1) --- C(squash #2) --- D(merge #3)  <- TAG
#         \
#          E (old-dated PR-branch commit for #1)
build_repo() {
  git -C "${TMP}" init -q -b main
  git -C "${TMP}" -c user.email=t@t -c user.name=t commit -q --allow-empty -m "A"
  # PREV points at A.
  git -C "${TMP}" tag v0.1.0

  # PR #1: a branch with an old-dated commit, merged with a merge commit.
  git -C "${TMP}" checkout -q -b pr1
  git -C "${TMP}" -c user.email=t@t -c user.name=t commit -q --allow-empty -m "E"
  git -C "${TMP}" checkout -q main
  git -C "${TMP}" -c user.email=t@t -c user.name=t merge -q --no-ff -m "Merge pull request #1 from x/pr1" pr1

  # PR #2: squash merge (subject ends with "(#2)").
  git -C "${TMP}" -c user.email=t@t -c user.name=t commit -q --allow-empty -m "feat: thing (#2)"

  # PR #3: another merge commit.
  git -C "${TMP}" checkout -q -b pr3
  git -C "${TMP}" -c user.email=t@t -c user.name=t commit -q --allow-empty -m "F"
  git -C "${TMP}" checkout -q main
  git -C "${TMP}" -c user.email=t@t -c user.name=t merge -q --no-ff -m "Merge pull request #3 from x/pr3" pr3

  git -C "${TMP}" tag v0.2.0
}

build_repo

# The functions run git in the current directory; run them inside the
# temp repo.
cd "${TMP}"

# --- extract_merged_prs ---------------------------------------------------

assert_equals "extract_merged_prs finds merge commits and squash subjects" \
  "1
2
3" "$(extract_merged_prs v0.1.0 v0.2.0)"

# A range with no merges yields nothing.
git -C "${TMP}" -c user.email=t@t -c user.name=t commit -q --allow-empty -m "chore: no PR"
git -C "${TMP}" tag v0.3.0
assert_equals "extract_merged_prs returns nothing for a PR-less range" \
  "" "$(extract_merged_prs v0.2.0 v0.3.0)"

if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} failure(s)" >&2
  exit 1
fi
echo "all tests passed"
