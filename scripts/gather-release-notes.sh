#!/usr/bin/env bash
# gather-release-notes.sh — Gather release notes from release-note blocks
# in PR bodies for a tag range.
#
# Shared by the release workflow (.github/workflows/release.yml) and its
# workflow_dispatch dry-run job, which measures the GITHUB_TOKEN
# rate-limit cost and runtime before a final release relies on it.
#
# Usage: gather-release-notes.sh PREV_TAG TAG OUTDIR
#
# Writes OUTDIR/release-notes.md (with the "## Changelog" header the
# release-highlights step expects) and OUTDIR/release-notes-body.md.
# Requires gh (authenticated) for PR title lookups.

set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 PREV_TAG TAG OUTDIR" >&2
  exit 2
fi

PREV="$1"
TAG="$2"
OUTDIR="$3"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Install the pinned Kubernetes release-notes tool if absent. Update the
# URL and digest together (see scripts/renovate/ for the checksum-pinning
# convention).
if ! command -v release-notes >/dev/null 2>&1; then
  RELEASE_NOTES_SHA256=16e0ada3e3602c229e97fa789ea97c7d128710d7fe1fee9f12c00f7eaa770d6a
  curl -fsSLo "${OUTDIR}/release-notes" \
    https://github.com/kubernetes/release/releases/download/v0.17.1/release-notes-amd64-linux
  echo "${RELEASE_NOTES_SHA256}  ${OUTDIR}/release-notes" | sha256sum -c -
  chmod +x "${OUTDIR}/release-notes"
  sudo mv "${OUTDIR}/release-notes" /usr/local/bin/release-notes
fi

echo "Gathering notes from ${PREV} to ${TAG}"
# The tool lists commits from START_SHA's commit date onward (inclusive).
# Use the first first-parent commit after PREV: picking the oldest-dated
# commit in the range would open the window before PREV (PR-branch
# commits can carry dates older than PREV) and re-list PRs from the
# previous release.
RANGE=$(git rev-list --first-parent --reverse "${PREV}..${TAG}")
if [[ -n "${RANGE}" ]]; then
  START_SHA=$(head -n1 <<<"${RANGE}")
else
  # PREV and TAG share a commit (a re-tag); fall back to PREV.
  START_SHA=$(git rev-list -n1 "${PREV}")
fi
END_SHA=$(git rev-list -n1 "${TAG}")
# The tool reads its range and branch from these env vars. Use the tag's
# SHA as the listing ref (the `sha` parameter accepts a SHA): listing
# from main would include PRs merged to main after the tag but dated
# inside the window. --required-author="" includes bot-authored PRs
# (agents open many of our PRs).
export START_SHA END_SHA
export BRANCH="${END_SHA}"
# Gather as JSON, then render with our own script. The tool credits a
# commit to any PR cited as "(#N)" in its message, so PRs from earlier
# releases can appear; the renderer keeps only PRs actually merged in
# the range (first-parent commits between PREV and TAG) and groups notes
# by PR title type, mirroring the old GoReleaser changelog layout.
release-notes generate \
  --required-author="" \
  --dependencies=false \
  --format json \
  --output "${OUTDIR}/release-notes.json" \
  --org fullsend-ai --repo fullsend
# PRs merged in this release: merge commits say "Merge pull request #N";
# squash-merge subjects end with "(#N)".
git log --first-parent --format='%s' "${PREV}..${TAG}" \
  | sed -nE 's/^Merge pull request #([0-9]+).*/\1/p; s/.*\(#([0-9]+)\)$/\1/p' \
  | sort -u > "${OUTDIR}/merged-prs.txt"
# Titles for the gathered PRs (one cheap API call each; only PRs with a
# release-note block reach this list).
: > "${OUTDIR}/pr-titles.tsv"
while read -r pr; do
  gh pr view "${pr}" --json number,title \
    --jq '"\(.number)\t\(.title)"' >> "${OUTDIR}/pr-titles.tsv"
done < <(jq -r 'keys[]' "${OUTDIR}/release-notes.json")
bash "${SCRIPT_DIR}/render-release-notes.sh" \
  "${OUTDIR}/release-notes.json" \
  "${OUTDIR}/merged-prs.txt" \
  "${OUTDIR}/pr-titles.tsv" \
  > "${OUTDIR}/release-notes-body.md"
# Prepend the header prepend-release-highlights.sh expects, so the
# release-highlights step keeps working with the gathered-notes body.
printf '## Changelog\n\n' | cat - "${OUTDIR}/release-notes-body.md" \
  > "${OUTDIR}/release-notes.md"
