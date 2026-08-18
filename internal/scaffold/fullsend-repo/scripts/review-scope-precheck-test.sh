#!/usr/bin/env bash
# review-scope-precheck-test.sh — Self-check for review-scope-precheck.sh
# using a throwaway git repo (BASE/HEAD path, no gh needed).
#
# Run from the repo root:
#   bash internal/scaffold/fullsend-repo/scripts/review-scope-precheck-test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/review-scope-precheck.sh"
FAILURES=0

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

REPO="${TMP}/repo"
git init -q "${REPO}"
git -C "${REPO}" -c user.name=t -c user.email=t@t commit -q --allow-empty -m base
BASE_SHA="$(git -C "${REPO}" rev-parse HEAD)"

# commit_files <name> <file>... — commit files on top of BASE, echo the sha
commit_files() {
  local name="$1"; shift
  git -C "${REPO}" checkout -q "${BASE_SHA}"
  for f in "$@"; do
    mkdir -p "${REPO}/$(dirname "${f}")"
    echo x > "${REPO}/${f}"
    git -C "${REPO}" add "${f}"
  done
  git -C "${REPO}" -c user.name=t -c user.email=t@t commit -q -m "${name}"
  git -C "${REPO}" rev-parse HEAD
}

# expect <label> <expected-exit> <file>...
expect() {
  local label="$1" want="$2"; shift 2
  local head rc=0
  head="$(commit_files "${label}" "$@")"
  BASE="${BASE_SHA}" HEAD="${head}" REVIEW_TARGET_REPO_DIR="${REPO}" \
    bash "${SCRIPT}" >/dev/null 2>&1 || rc=$?
  if [[ "${rc}" -eq "${want}" ]]; then
    echo "PASS: ${label} (exit ${rc})"
  else
    echo "FAIL: ${label} — want exit ${want}, got ${rc}"
    FAILURES=$((FAILURES + 1))
  fi
}

expect "docs-only"          78 README.md docs/guide.md
expect "test-only"          78 internal/x/x_test.go tests/fixture.txt
expect "mixed"              0  README.md internal/x/x.go
expect "code-only"          0  cmd/main.go
expect "md-in-code-path"    78 internal/x/notes.md

# Reason must be on stdout as the last line (protocol fallback reason).
head="$(commit_files reason README.md)"
out="$(BASE="${BASE_SHA}" HEAD="${head}" REVIEW_TARGET_REPO_DIR="${REPO}" bash "${SCRIPT}" 2>/dev/null || true)"
if [[ "$(tail -n1 <<< "${out}")" == "docs/test-only change, review skipped" ]]; then
  echo "PASS: reason on stdout"
else
  echo "FAIL: reason on stdout — got: ${out}"
  FAILURES=$((FAILURES + 1))
fi

# No inputs → inconclusive → proceed.
rc=0; env -u BASE -u HEAD -u PR_NUMBER -u REPO_FULL_NAME bash "${SCRIPT}" >/dev/null 2>&1 || rc=$?
if [[ "${rc}" -eq 0 ]]; then echo "PASS: no inputs proceeds"; else echo "FAIL: no inputs exit ${rc}"; FAILURES=$((FAILURES + 1)); fi

# Custom patterns via env.
head="$(commit_files custom foo.rst)"
rc=0; BASE="${BASE_SHA}" HEAD="${head}" REVIEW_TARGET_REPO_DIR="${REPO}" REVIEW_SCOPE_SKIP_PATTERNS='\.rst$' \
  bash "${SCRIPT}" >/dev/null 2>&1 || rc=$?
if [[ "${rc}" -eq 78 ]]; then echo "PASS: custom patterns"; else echo "FAIL: custom patterns exit ${rc}"; FAILURES=$((FAILURES + 1)); fi

if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} failure(s)"
  exit 1
fi
echo "All review-scope-precheck tests passed"
