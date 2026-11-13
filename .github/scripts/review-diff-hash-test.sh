#!/usr/bin/env bash
# review-diff-hash-test.sh — canonicalizer and workflow-contract tests.
#
# The workflow-side guard is intentionally fail-open: only a complete diff,
# an approved review at the recorded head, and a stable PR snapshot may skip.
# Keep these cases close to the workflow so changes to the routing contract
# are visible in `make script-test`.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/review-diff-hash.sh"
WORKFLOW="${SCRIPT_DIR}/../workflows/reusable-dispatch.yml"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
contains() { grep -Fq -- "$2" "$1" || fail "${1} does not contain ${2}"; }

contains "${WORKFLOW}" 'name: Check unchanged review diff'
contains "${WORKFLOW}" 'if [[ "${prior_state}" != "APPROVED"'
contains "${WORKFLOW}" '"${base_before}" != "${base_after}"'
contains "${WORKFLOW}" '"${head_before}" != "${head_after}"'
contains "${WORKFLOW}" 'No app-verified previous review found; running review'
contains "${WORKFLOW}" 'defaults_root: .fullsend/.defaults'
contains "${WORKFLOW}" 'ln -s .fullsend/.defaults .defaults'
contains "${WORKFLOW}" 'Fullsend'"'"'s earlier unchanged-diff skip note was superseded'

# This is the workflow guard's decision contract. Keep the cases explicit so
# a refactor cannot accidentally turn a missing or stale approval into a skip.
dedup_decision() {
  local current_hash="$1" prior_hash="$2" state="$3" reviewed_head="$4"
  local current_head="$5" before_head="$6" after_head="$7"
  if [[ -z "${current_hash}" || "${current_hash}" != "${prior_hash}" ]]; then
    echo run
  elif [[ "${state}" != APPROVED || "${reviewed_head}" != "${before_head}" ]]; then
    echo run
  elif [[ "${before_head}" != "${after_head}" || "${after_head}" != "${current_head}" ]]; then
    echo run
  else
    echo skip
  fi
}

[[ "$(dedup_decision hash hash APPROVED head head head head)" == skip ]] || fail "approved unchanged diff must skip"
[[ "$(dedup_decision '' hash APPROVED head head head head)" == run ]] || fail "missing hash must run"
[[ "$(dedup_decision hash hash COMMENTED head head head head)" == run ]] || fail "non-approving review must run"
[[ "$(dedup_decision hash hash DISMISSED head head head head)" == run ]] || fail "dismissed approval must run"
[[ "$(dedup_decision hash hash APPROVED head new-head head head)" == run ]] || fail "changed head must run"
[[ "$(dedup_decision hash hash APPROVED head head head new-head)" == run ]] || fail "head changed during fetch must run"

cat >"${TMPDIR}/base.json" <<'EOF'
{"file.txt":"100644"}
EOF
cat >"${TMPDIR}/head.json" <<'EOF'
{"file.txt":"100644"}
EOF
cat >"${TMPDIR}/same.json" <<'EOF'
{"filename":"file.txt","previous_filename":"","status":"modified","patch":"@@ -1 +1 @@\n-a\n+b"}
EOF

same_hash="$("${SCRIPT}" "${TMPDIR}/base.json" "${TMPDIR}/head.json" <"${TMPDIR}/same.json")"
[[ "${same_hash}" =~ ^[0-9a-f]{64}$ ]] || fail "complete diff did not produce a SHA-256 hash"

empty_hash="$(printf '' | "${SCRIPT}" "${TMPDIR}/base.json" "${TMPDIR}/head.json")"
[[ -z "${empty_hash}" ]] || fail "empty diff must fail open"

missing_mode_hash="$(printf '%s\n' '{"filename":"missing.txt","previous_filename":"","status":"modified","patch":"@@ -1 +1 @@"}' | "${SCRIPT}" "${TMPDIR}/base.json" "${TMPDIR}/head.json")"
[[ -z "${missing_mode_hash}" ]] || fail "unverified mode metadata must fail open"

echo "review diff hash and workflow-contract tests passed"
