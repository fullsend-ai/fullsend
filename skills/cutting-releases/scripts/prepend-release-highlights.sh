#!/usr/bin/env bash
# Prepend user-facing highlights to a published release's notes.
#
# Usage: prepend-release-highlights.sh <tag> <<'HIGHLIGHTS_EOF'
#        <highlights>
#        HIGHLIGHTS_EOF
#
# Arguments:
#   tag    Release tag (e.g. v0.5.0)
#   stdin  Markdown highlights only; pass them in a quoted heredoc so the
#          shell expands nothing
#
# The script fetches the existing notes itself, and they move between gh and
# disk through files only. v0.33.0 shipped a literal
# `$(cat /tmp/release-body-existing.txt)` in place of its changelog because
# the notes were pasted into a quoted heredoc by hand (#8366). The script
# writes at most once: it re-reads the notes just before the edit and after
# it, and on any mismatch it reports and stops instead of overwriting.
set -euo pipefail

usage="Usage: prepend-release-highlights.sh <tag> <<'HIGHLIGHTS_EOF' ... HIGHLIGHTS_EOF"
if [[ $# -ne 1 ]]; then
  echo "$usage" >&2
  exit 1
fi
TAG="$1"
if [[ ! "$TAG" =~ ^v[0-9] ]]; then
  echo "ERROR: Tag must start with 'v' followed by a digit (e.g. v0.5.0)" >&2
  exit 1
fi
if [[ -t 0 ]]; then
  echo "ERROR: pass the highlights on stdin; $usage" >&2
  exit 1
fi

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

HIGHLIGHTS="$WORKDIR/highlights.md"
cat > "$HIGHLIGHTS"
if ! grep -q '[^[:space:]]' "$HIGHLIGHTS"; then
  echo "ERROR: highlights on stdin are empty or blank" >&2
  exit 1
fi

# Placeholders that mean the highlights were templated instead of written:
# a line that is only a shell expansion (the v0.33.0 signature) or the old
# skill template's <existing body>/<highlights>. Inline expansions are fine,
# in prose (e.g. `${OPENAI_API_KEY}` in v0.38.0) or in a migration snippet
# like `GH_TOKEN=$(cat ~/.token)`: this script fetches the existing notes
# itself, so a stray inline one cannot drop the changelog.
# shellcheck disable=SC2016  # the pattern matches a literal, unexpanded $(
placeholder='^[[:space:]]*\$(\(.*\)|\{.*\})[[:space:]]*$|<(existing body|highlights)>'
if grep -Eq "$placeholder" "$HIGHLIGHTS"; then
  echo "ERROR: highlights contain an unexpanded shell expression or template placeholder" >&2
  exit 1
fi
if grep -q '^## Changelog' "$HIGHLIGHTS"; then
  echo "ERROR: highlights must not contain the changelog; pass highlights only" >&2
  exit 1
fi

# --template prints the stored body byte for byte; `-q .body` would append
# a newline, and $(...) would drop trailing ones.
fetch_body() {
  gh release view "$TAG" --repo fullsend-ai/fullsend --json body --template '{{.body}}' > "$1"
}

fetch_body "$WORKDIR/old.md"
# GoReleaser bodies start with exactly this line. Refuse anything else, and
# say which case it is so the releaser knows what to look at.
first_line=$(head -n 1 "$WORKDIR/old.md")
if [[ "$first_line" != "## Changelog" ]]; then
  if ! grep -q '## Changelog' "$WORKDIR/old.md"; then
    reason="have no '## Changelog' line (empty changelog, or not a GoReleaser body)"
  elif [[ "$first_line" == *"## Changelog"* ]]; then
    reason="start with '## Changelog' plus stray characters (carriage return or byte-order mark?)"
  else
    reason="have text above '## Changelog' (highlights already added?)"
  fi
  echo "ERROR: release $TAG notes $reason; refusing to edit" >&2
  exit 1
fi

{
  cat "$HIGHLIGHTS"
  # Blank line before --- even without a final newline, or the last
  # highlights line would render as a setext heading.
  printf '\n\n---\n\n'
  cat "$WORKDIR/old.md"
} > "$WORKDIR/new.md"

# The edit below is the script's only write. GitHub has no conditional
# release update, so re-read just before writing to keep the window in which
# another editor's change could be overwritten as small as possible.
fetch_body "$WORKDIR/pre.md"
if ! cmp -s "$WORKDIR/old.md" "$WORKDIR/pre.md"; then
  echo "ERROR: release $TAG notes changed while the highlights were prepared; nothing edited, re-run" >&2
  exit 1
fi

# keep_files leaves the copies on disk for manual review after a failed check.
keep_files() {
  trap - EXIT
  echo "Copies kept in $WORKDIR: old.md (before), new.md (sent), after.md (now)" >&2
}

# An edit can fail after GitHub applied it (e.g. a timeout), so a failure
# falls through to the check below instead of exiting unverified.
if ! gh release edit "$TAG" --repo fullsend-ai/fullsend --notes-file "$WORKDIR/new.md" > /dev/null; then
  echo "WARNING: gh release edit failed; checking what the release now holds" >&2
fi

# Never write again from here: a mismatch most likely means someone else
# edited the notes, and their change must not be overwritten.
if ! fetch_body "$WORKDIR/after.md"; then
  echo "ERROR: could not re-read release $TAG notes; check them by hand" >&2
  keep_files
  exit 1
fi
if cmp -s "$WORKDIR/new.md" "$WORKDIR/after.md"; then
  echo "Prepended highlights to release $TAG"
  exit 0
fi
if cmp -s "$WORKDIR/old.md" "$WORKDIR/after.md"; then
  echo "ERROR: the edit did not apply; release $TAG notes are unchanged" >&2
  exit 1
fi
echo "ERROR: release $TAG notes differ from what was sent (edited by someone else?); nothing overwritten, review them by hand" >&2
keep_files
exit 1
