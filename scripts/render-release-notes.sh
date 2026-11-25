#!/usr/bin/env bash
# render-release-notes.sh — Render release notes from the Kubernetes
# release-notes tool's JSON output.
#
# The gatherer can credit a commit to any PR cited as "(#N)" in its
# message, so its JSON may contain PRs from earlier releases. This script
# keeps only PRs that were actually merged in the tag range (the caller
# passes their numbers) and groups the notes by PR title type, mirroring
# the layout the old GoReleaser changelog used.
#
# Usage:
#   render-release-notes.sh NOTES_JSON MERGED_PRS TITLES_TSV
#
#   NOTES_JSON  --output file of `release-notes generate --format json`
#                 (an object keyed by PR number).
#   MERGED_PRS  file with one PR number per line: the PRs merged in the
#                 tag range (first-parent commits between the tags).
#   TITLES_TSV  file with `NUMBER<TAB>TITLE` lines (PR titles; used to
#                 pick the section for each note).
#
# Prints the markdown body (without the `## Changelog` header, which the
# release workflow prepends) to stdout.

set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 NOTES_JSON MERGED_PRS TITLES_TSV" >&2
  exit 2
fi

NOTES_JSON=$1
MERGED_PRS=$2
TITLES_TSV=$3

for f in "${NOTES_JSON}" "${MERGED_PRS}" "${TITLES_TSV}"; do
  if [[ ! -f "${f}" ]]; then
    echo "error: file not found: ${f}" >&2
    exit 2
  fi
done

# jq arguments: the allowed PR numbers and the PR titles.
ALLOWED=$(jq -Rn '[inputs | tonumber]' "${MERGED_PRS}")
TITLES=$(jq -Rn '
  reduce inputs as $line ({};
    ($line | split("\t")) as $parts
    | .[$parts[0]] = ($parts[1] // "")
  )' "${TITLES_TSV}")

# One JSON object per line for each published note: number, title,
# text, PR URL, author, author URL, action-required flag. Notes for PRs
# outside the merged set, notes marked do-not-publish, and empty notes
# are dropped here.
ROWS=$(jq -c --argjson allowed "${ALLOWED}" --argjson titles "${TITLES}" '
  [ to_entries[]
    | select(.value.pr_number as $n | $allowed | index($n))
    | select(.value.do_not_publish != true)
    | .value
    | select((.text | length) > 0)
    | {number: .pr_number, title: ($titles[.pr_number | tostring] // ""),
       text: .text, url: .pr_url,
       author: .author, author_url: .author_url,
       action_required: (.action_required == true)}
  ] | sort_by(.number) | .[]' "${NOTES_JSON}")

# Section for a note, in priority order. Action-required notes always go
# under Urgent Upgrade Notes; otherwise the PR title type decides, with
# `!` (breaking) pulled out of the type groups first — same order the old
# GoReleaser changelog used.
# Regexes in variables: bash cannot parse nested parentheses in an
# inline `[[ =~ ]]` pattern.
RE_BREAKING='^[a-z]+(\([^)]*\))?!:'
RE_FEAT='^feat(\([^)]*\))?!?:'
RE_FIX='^fix(\([^)]*\))?!?:'
RE_REFACTOR='^refactor(\([^)]*\))?!?:'

section_for() {
  local action_required=$1 title=$2
  if [[ "${action_required}" == "true" ]]; then
    echo "urgent"
  elif [[ "${title}" =~ ${RE_BREAKING} ]]; then
    echo "breaking"
  elif [[ "${title}" =~ ${RE_FEAT} ]]; then
    echo "features"
  elif [[ "${title}" =~ ${RE_FIX} ]]; then
    echo "fixes"
  elif [[ "${title}" =~ ${RE_REFACTOR} ]]; then
    echo "refactoring"
  else
    echo "other"
  fi
}

declare -a SECTIONS=(urgent breaking features fixes refactoring other)
declare -A HEADINGS=(
  [urgent]="## Urgent Upgrade Notes"
  [breaking]="### Breaking Changes"
  [features]="### Features"
  [fixes]="### Bug Fixes"
  [refactoring]="### Refactoring"
  [other]="### Other Changes"
)
declare -A BULLETS

while IFS= read -r line; do
  [[ -z "${line}" ]] && continue
  number=$(jq -r .number <<<"${line}")
  title=$(jq -r .title <<<"${line}")
  text=$(jq -r .text <<<"${line}")
  url=$(jq -r .url <<<"${line}")
  author=$(jq -r .author <<<"${line}")
  author_url=$(jq -r .author_url <<<"${line}")
  action_required=$(jq -r '.action_required' <<<"${line}")
  section=$(section_for "${action_required}" "${title}")
  # Indent continuation lines of multi-line notes so they stay inside the
  # bullet.
  formatted=$(printf '%s\n' "${text}" | sed '2,$s/^/  /')
  BULLETS[${section}]+="- ${formatted} ([#${number}](${url}), [@${author}](${author_url}))"$'\n'
done <<<"${ROWS}"

for section in "${SECTIONS[@]}"; do
  if [[ -n "${BULLETS[${section}]:-}" ]]; then
    echo "${HEADINGS[${section}]}"
    echo
    printf '%s' "${BULLETS[${section}]}"
    echo
  fi
done
