#!/usr/bin/env bash
# check-release-note-test.sh — Tests for check-release-note.sh
#
# Run from the repo root:
#   bash scripts/check-release-note-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/check-release-note.sh"
FAILURES=0

run_case() {
  local desc="$1" expected="$2"
  shift 2
  local actual
  if "$@" bash "${SCRIPT}" >/dev/null 2>&1; then
    actual="pass"
  else
    actual="fail"
  fi
  if [[ "${actual}" == "${expected}" ]]; then
    echo "ok   — ${desc}"
  else
    echo "FAIL — ${desc} (expected ${expected}, got ${actual})"
    FAILURES=$((FAILURES + 1))
  fi
}

BODY_WITH_NONE="## Summary

Does a thing.

\`\`\`release-note
NONE
\`\`\`
"

BODY_WITH_NOTE="## Summary

Does a thing.

\`\`\`release-note
The fullsend CLI now frobs the widget.
\`\`\`
"

BODY_WITH_ACTION_REQUIRED="## Summary

Breaks a thing.

\`\`\`release-note
action required: the widget flag is now required.
\`\`\`
"

BODY_EMPTY_BLOCK="## Summary

Does a thing.

\`\`\`release-note
\`\`\`
"

BODY_NO_BLOCK="## Summary

Does a thing.
"

BODY_INDENTED_NONE="## Summary

\`\`\`release-note
  NONE
\`\`\`
"

BODY_MULTI_LINE="## Summary

\`\`\`release-note
The CLI now supports frobbing.

Users must pass --frob.
\`\`\`
"

# Valid blocks pass.
run_case "NONE block passes" pass env PR_BODY="$BODY_WITH_NONE"
run_case "prose block passes" pass env PR_BODY="$BODY_WITH_NOTE"
run_case "action required block passes" pass env PR_BODY="$BODY_WITH_ACTION_REQUIRED"
run_case "indented NONE passes" pass env PR_BODY="$BODY_INDENTED_NONE"
run_case "multi-line block passes" pass env PR_BODY="$BODY_MULTI_LINE"

# Missing or empty blocks fail.
run_case "no block fails" fail env PR_BODY="$BODY_NO_BLOCK"
run_case "empty block fails" fail env PR_BODY="$BODY_EMPTY_BLOCK"
run_case "empty body fails" fail env PR_BODY=""

# NONE with a breaking marker fails.
run_case "NONE with breaking PR title fails" fail \
  env PR_BODY="$BODY_WITH_NONE" PR_TITLE="feat(harness)!: require role field"
run_case "NONE with breaking commit subject fails" fail \
  env PR_BODY="$BODY_WITH_NONE" PR_TITLE="feat(harness): add thing" \
  COMMIT_SUBJECTS=$'feat(harness): add thing\nfix(mint)!: drop old flag'
run_case "NONE with scoped breaking title fails" fail \
  env PR_BODY="$BODY_WITH_NONE" PR_TITLE="fix!: remove flag"

# NONE without a breaking marker passes even when title/commits are set.
run_case "NONE with non-breaking title and commits passes" pass \
  env PR_BODY="$BODY_WITH_NONE" PR_TITLE="feat(harness): add thing" \
  COMMIT_SUBJECTS=$'feat(harness): add thing\nfix(mint): correct flag'

# A breaking marker with a real note (not NONE) passes; the note content
# is the author's responsibility, only NONE is machine-checked.
run_case "breaking title with prose note passes" pass \
  env PR_BODY="$BODY_WITH_NOTE" PR_TITLE="feat(harness)!: require role field"

# A `!` in prose (not a Conventional Commits marker) must not trip the check.
run_case "NONE with exclamation in title prose passes" pass \
  env PR_BODY="$BODY_WITH_NONE" PR_TITLE="feat(harness): wow!"

if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "all tests passed"
