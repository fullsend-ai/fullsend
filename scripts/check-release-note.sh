#!/usr/bin/env bash
# check-release-note.sh — Validate the `release-note` block in a PR body.
#
# Release notes are gathered from fenced ```release-note blocks in PR
# bodies (see COMMITS.md, "Release notes"). This script enforces the
# contract on every PR, whether a human or an agent wrote it:
#
#   1. The body must contain a fenced ```release-note block.
#   2. The block must not be empty.
#   3. If the block says NONE, neither the PR title nor any commit subject
#      may carry the Conventional Commits `!` breaking-change marker. A
#      breaking PR must start its block with "action required".
#
# Exits 0 if the block is valid, 1 otherwise.
#
# Inputs (env vars):
#   PR_BODY          — full PR body text
#   PR_TITLE         — PR title (optional; skips the title check if unset)
#   COMMIT_SUBJECTS  — newline-separated commit subjects (optional; skips
#                      the commit check if unset)
#
# Requires: bash, awk, grep. No network access.

set -euo pipefail

PR_BODY="${PR_BODY:-}"
PR_TITLE="${PR_TITLE:-}"
COMMIT_SUBJECTS="${COMMIT_SUBJECTS:-}"

# Extract the first fenced ```release-note block: lines between the
# opening ```release-note fence and the next closing fence.
NOTE=$(awk '
  /^```release-note/ { inblock = 1; next }
  inblock && /^```/ { exit }
  inblock { print }
' <<<"${PR_BODY}")

# Trim each line and drop leading/trailing blank lines, so indented or
# padded blocks still validate.
NOTE_TRIMMED=$(
  awk '
    { sub(/^[ \t]+/, ""); sub(/[ \t]+$/, ""); print }
  ' <<<"${NOTE}" |
  awk 'NF { started = 1 } started { print }' |
  awk '{ lines[NR] = $0; last = NR } END { for (i = 1; i <= last; i++) print lines[i] }'
)

if [[ -z "${NOTE_TRIMMED}" ]]; then
  echo "::error::PR body has no release-note block, or the block is empty. Add a fenced \`\`\`release-note block; write NONE if users see no change."
  exit 1
fi

# A Conventional Commits breaking-change marker: type, optional scope,
# then `!` before the colon, e.g. "feat(harness)!:" or "fix!:".
BREAKING_RE='^[[:alpha:]]+(\([^)]*\))?!:'

has_breaking_marker() {
  local text="$1"
  [[ -z "${text}" ]] && return 1
  grep -Eq "${BREAKING_RE}" <<<"${text}"
}

# The first line decides NONE-ness; the k8s release-notes tool treats a
# block whose text is exactly "NONE" as "no note".
FIRST_LINE=$(head -n1 <<<"${NOTE_TRIMMED}")

if [[ "${FIRST_LINE}" == "NONE" ]]; then
  if has_breaking_marker "${PR_TITLE}" || has_breaking_marker "${COMMIT_SUBJECTS}"; then
    echo "::error::release-note block says NONE, but the PR title or a commit subject carries the '!' breaking-change marker. Start the block with 'action required' and describe what breaks."
    exit 1
  fi
fi

echo "release-note block OK"
