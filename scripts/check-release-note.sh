#!/usr/bin/env bash
# check-release-note.sh — Validate the `release-note` block in a PR body.
#
# Release notes are gathered from fenced ```release-note blocks in PR
# bodies (see COMMITS.md, "Release notes"). This script enforces the
# contract on every PR, whether a human or an agent wrote it:
#
#   1. The body must contain a fenced ```release-note block.
#   2. The block must not be empty.
#   3. If the PR title or any commit subject carries the Conventional
#      Commits `!` breaking-change marker, the block must start with
#      "action required". A NONE block (or any of the variants the
#      release-notes gatherer treats as "no note") is rejected instead.
#
# On success the script prints "release-note block OK", plus the line
# "release-note: action-required" when the PR is breaking and valid. The
# release-note-check workflow uses that marker to label the PR
# `release-note-action-required`, which is what makes the gatherer list
# the note under "Urgent Upgrade Notes".
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

# Strip carriage returns so CRLF bodies (common after web-UI edits)
# normalize the same way as LF bodies.
PR_BODY="${PR_BODY//$'\r'/}"

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

# The whole trimmed block must be the sentinel (case-insensitive,
# optionally quoted) for the gatherer to treat it as "no note". This
# mirrors the gatherer's exclusion regex for `none`, `n/a` and `na`.
is_none_block() {
  grep -Eiq "^['\"]?(none|n/a|na)['\"]?$" <<<"${NOTE_TRIMMED}"
}

# The gatherer drops a PR entirely when its body matches either exclusion
# filter, regardless of which block comes first. Mirror both so a breaking
# PR cannot hide its warning behind a second NONE block or the
# /release-note-none tag. Newlines are folded to spaces first because the
# fenced-block regex spans lines and grep is line-based.
matches_exclusion_filter() {
  local oneline
  oneline=$(tr '\n' ' ' <<<"${PR_BODY}")
  grep -Eiq '```release-notes?[[:space:]]*['"'"'"]?(none|n/a|na)['"'"'"]?[[:space:]]*```' <<<"${oneline}" ||
    grep -q '/release-note-none' <<<"${PR_BODY}"
}

FIRST_LINE=$(head -n1 <<<"${NOTE_TRIMMED}")

if has_breaking_marker "${PR_TITLE}" || has_breaking_marker "${COMMIT_SUBJECTS}"; then
  if is_none_block || matches_exclusion_filter; then
    echo "::error::release-note block says NONE (or the body carries a release-note-none marker), but the PR title or a commit subject carries the '!' breaking-change marker. Start the block with 'action required' and describe what breaks."
    exit 1
  fi
  if ! grep -Eiq '^action required' <<<"${FIRST_LINE}"; then
    echo "::error::PR title or a commit subject carries the '!' breaking-change marker, but the release-note block does not start with 'action required'. Start the block with 'action required' and describe what breaks."
    exit 1
  fi
  echo "release-note block OK"
  echo "release-note: action-required"
else
  echo "release-note block OK"
fi
