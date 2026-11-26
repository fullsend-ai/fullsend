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
# Requires gh (authenticated) for PR title, label and edit-timestamp
# lookups.
#
# The merged-PR extraction lives in a function below so
# gather-release-notes-test.sh can exercise it offline (the full run
# needs the release-notes binary and an authenticated gh).

set -euo pipefail

# Install the pinned Kubernetes release-notes tool if absent (in main,
# after the args are parsed). Update the URL and digest together: Renovate
# bumps the version (custom manager in renovate.json) and
# scripts/renovate/update-release-notes-checksum.sh refreshes the digest in
# the same PR. v0.17.1 was replaced by v0.23.0 (current at the time)
# because the old pin was ~2.4 years stale and nothing could bump it.
install_release_notes() {
  local outdir=$1
  local RELEASE_NOTES_SHA256=086ef82292e97a1dcc3b4e096ea40627a78b0ef3a2564f2faf324acc11f75724
  curl -fsSLo "${outdir}/release-notes" \
    https://github.com/kubernetes/release/releases/download/v0.23.0/release-notes-amd64-linux
  echo "${RELEASE_NOTES_SHA256}  ${outdir}/release-notes" | sha256sum -c -
  chmod +x "${outdir}/release-notes"
  sudo mv "${outdir}/release-notes" /usr/local/bin/release-notes
}

# extract_merged_prs PREV TAG — print the PR numbers merged in the range,
# one per line, sorted and unique. Merge commits say "Merge pull request
# #N"; squash-merge subjects end with "(#N)".
extract_merged_prs() {
  local prev=$1 tag=$2
  git log --first-parent --format='%s' "${prev}..${tag}" \
    | sed -nE 's/^Merge pull request #([0-9]+).*/\1/p; s/.*\(#([0-9]+)\)$/\1/p' \
    | sort -u
}

# fetch_pr_meta PR — print a JSON object with the PR's title,
# lastEditedAt, mergedAt and label names (one GraphQL call).
# lastEditedAt is null when the body was never edited.
fetch_pr_meta() {
  local pr=$1
  gh api graphql \
    -f query='query($number: Int!) {
      repository(owner: "fullsend-ai", name: "fullsend") {
        pullRequest(number: $number) {
          title
          lastEditedAt
          mergedAt
          labels(first: 100) {
            nodes { name }
          }
        }
      }
    }' \
    -F number="${pr}" \
    --jq '.data.repository.pullRequest'
}

main() {
  if [[ $# -ne 3 ]]; then
    echo "usage: $0 PREV_TAG TAG OUTDIR" >&2
    exit 2
  fi

  local PREV=$1 TAG=$2 OUTDIR=$3
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

  if ! command -v release-notes >/dev/null 2>&1; then
    install_release_notes "${OUTDIR}"
  fi

  echo "Gathering notes from ${PREV} to ${TAG}"
  # v0.23.0 walks first parents from END_SHA and stops at the merge base
  # of END_SHA and START_SHA (exclusive), so START_SHA must be PREV
  # itself: every commit after PREV up to TAG is then included, and the
  # first PR merged after the previous tag keeps its note.
  local START_SHA END_SHA
  START_SHA=$(git rev-parse "${PREV}")
  END_SHA=$(git rev-list -n1 "${TAG}")
  # The tool reads its range from these env vars and walks a local clone
  # (v0.23.0 has no --required-author flag and no date window; it opens
  # the repo at --repo-path, which defaults to a tempdir clone that does
  # not exist here).
  export START_SHA END_SHA
  # Gather as JSON, then render with our own script. The tool credits a
  # commit to any PR cited as "(#N)" in its message, so PRs from earlier
  # releases can appear; the renderer keeps only PRs actually merged in
  # the range (first-parent commits between PREV and TAG) and groups notes
  # by PR title type, mirroring the old GoReleaser changelog layout.
  release-notes generate \
    --repo-path "$(git rev-parse --show-toplevel)" \
    --dependencies=false \
    --format json \
    --output "${OUTDIR}/release-notes.json" \
    --org fullsend-ai --repo fullsend
  # PRs merged in this release.
  extract_merged_prs "${PREV}" "${TAG}" > "${OUTDIR}/merged-prs.txt"
  # Titles and edit timestamps for every merged PR (one GraphQL call
  # each). Titles feed the renderer's section grouping and its
  # missing-note fallback; the timestamps feed the post-merge edit check
  # below.
  : > "${OUTDIR}/pr-titles.tsv"
  local -a edited_after_merge=()
  local pr meta title
  while read -r pr; do
    meta=$(fetch_pr_meta "${pr}")
    title=$(jq -r '.title // ""' <<<"${meta}")
    printf '%s\t%s\n' "${pr}" "${title}" >> "${OUTDIR}/pr-titles.tsv"
    # The release-note check validates the body only before merge. After
    # merge the author (including a fork contributor) can still edit the
    # body, replacing a reviewed note with arbitrary markdown. Refuse to
    # publish a note whose body was edited after the merge. Reverting the
    # edit does not clear lastEditedAt, so the recovery path is explicit:
    # a maintainer reviews the edited body and adds the
    # release-note-edit-approved label to the PR.
    if jq -e '
      .lastEditedAt != null and .mergedAt != null
      and .lastEditedAt > .mergedAt
    ' >/dev/null <<<"${meta}" \
      && ! jq -e '[.labels.nodes[].name]
                 | index("release-note-edit-approved")' >/dev/null <<<"${meta}"; then
      edited_after_merge+=("${pr}")
    fi
  done < "${OUTDIR}/merged-prs.txt"
  if [[ ${#edited_after_merge[@]} -gt 0 ]]; then
    # PR numbers are numeric, so interpolating them needs no
    # workflow-command escaping.
    echo "::error::PRs whose body was edited after merge (the published note would differ from the reviewed one): ${edited_after_merge[*]}. A maintainer must review each edit and add the release-note-edit-approved label to the PR before releasing."
    exit 1
  fi
  bash "${SCRIPT_DIR}/render-release-notes.sh" \
    "${OUTDIR}/release-notes.json" \
    "${OUTDIR}/merged-prs.txt" \
    "${OUTDIR}/pr-titles.tsv" \
    > "${OUTDIR}/release-notes-body.md"
  # Prepend the header prepend-release-highlights.sh expects, so the
  # release-highlights step keeps working with the gathered-notes body.
  printf '## Changelog\n\n' | cat - "${OUTDIR}/release-notes-body.md" \
    > "${OUTDIR}/release-notes.md"
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
