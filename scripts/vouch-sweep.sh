#!/usr/bin/env bash
# vouch-sweep.sh — Close recent PRs from unvouched first-time contributors.
#
# Scheduled replacement for the pull_request_target trigger of vouch-check.yml
# (#9245). Fork pull_request runs get a read-only token and wait for
# first-time-contributor approval, so the gate runs on a schedule instead and
# reads PR metadata through the API. It never checks out PR code.
#
# Considers open PRs opened or reopened inside the lookback window (default 60
# minutes, so missed or late cron runs overlap). Older open PRs are left alone.
# For each candidate:
#   - skip authors with user.type Bot;
#   - skip collaborators whose role_name is admin, maintain or write;
#   - skip authors listed in .github/VOUCHED.td on the `vouched` branch
#     (never the PR branch: the author could add themselves there);
#   - otherwise close the PR, then comment. Closing comes first so the gate
#     holds even if the comment API fails. A PR that already carries the
#     sweep's comment (marker <!-- vouch-check -->) is not commented on again,
#     so overlapping runs and repeat reopens post one comment.
#
# API errors never close a PR: a failed VOUCHED.td read aborts the sweep, and a
# failed permission lookup skips that PR. The next run retries it while the PR
# is still inside the window.
#
# Usage: vouch-sweep.sh [--dry-run] [--lookback-minutes N] OWNER/REPO
#
# Environment:
#   GH_TOKEN         — token with pull-requests: write (read-only in --dry-run)
#   VOUCH_BOT_LOGIN  — login that posts the comment (default github-actions[bot])
#   VOUCH_SWEEP_NOW  — current time as epoch seconds (tests)
#
# --dry-run prints what would be closed and commented on and changes nothing.
# Exits 1 when any PR could not be checked or closed.

set -euo pipefail

MARKER='<!-- vouch-check -->'
BOT_LOGIN="${VOUCH_BOT_LOGIN:-github-actions[bot]}"

usage() {
  echo "Usage: $0 [--dry-run] [--lookback-minutes N] OWNER/REPO" >&2
  exit 2
}

dry_run=false
lookback=60
repository=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) dry_run=true; shift ;;
    --lookback-minutes)
      [[ $# -ge 2 && "$2" =~ ^[1-9][0-9]*$ ]] || usage
      lookback="$2"; shift 2 ;;
    -h|--help) usage ;;
    -*) usage ;;
    *)
      [[ -z "${repository}" ]] || usage
      repository="$1"; shift ;;
  esac
done
[[ "${repository}" == */* ]] || usage

now="${VOUCH_SWEEP_NOW:-$(date -u +%s)}"
cutoff="$(jq -nr --argjson now "${now}" --argjson m "${lookback}" '($now - $m * 60) | todate')"
echo "vouch-sweep: ${repository}, PRs opened or reopened since ${cutoff}" >&2

# A failed read aborts the sweep (set -e) rather than treating everyone as
# unvouched.
vouched_file="$(gh api "repos/${repository}/contents/.github/VOUCHED.td?ref=vouched" |
  jq -er '.content | gsub("\\s"; "") | @base64d')"
# Lowercased usernames: skip blanks, comments (#) and denouncements (-).
vouched="$(awk '{ gsub(/^[ \t\r]+|[ \t\r]+$/, ""); if ($0 == "" || /^#/ || /^-/) next; print tolower($0) }' <<<"${vouched_file}")"

prs="$(gh api "repos/${repository}/pulls?state=open&per_page=100" --paginate | jq -s 'add // []')"

# updated_at >= cutoff is a cheap prefilter: opening and reopening both bump it.
candidates="$(jq -c --arg cutoff "${cutoff}" '
  .[] | select((.updated_at // "") >= $cutoff)
      | {number, login: .user.login, type: .user.type, created_at}
' <<<"${prs}")"

comment_body() {
  local author="$1"
  printf '%s\n' \
    "${MARKER}" \
    "Thank you for your interest in contributing to fullsend, @${author}." \
    '' \
    'This project uses a **vouch system** for first-time contributors. Before submitting a pull request, you need to be vouched by a maintainer.' \
    '' \
    '**To get vouched:**' \
    '1. Open a [Vouch Request](https://github.com/fullsend-ai/fullsend/discussions/new?category=vouch-request) discussion.' \
    '2. Describe what you want to change and why.' \
    '3. Write in your own words — do not have an AI generate the request.' \
    '4. A maintainer will comment `/vouch` if approved.' \
    '5. Once vouched, open a new PR (preferred) or reopen this one.' \
    '' \
    'See [CONTRIBUTING.md](https://github.com/fullsend-ai/fullsend/blob/main/CONTRIBUTING.md#first-time-contributors) for details.'
}

# Prints the collaborator role_name, "none" for non-collaborators. Returns 1 on
# any other API error.
collaborator_role() {
  local login="$1" out err
  err="$(mktemp)"
  if ! out="$(gh api "repos/${repository}/collaborators/${login}/permission" 2>"${err}")"; then
    if grep -qE "HTTP 404|not a collaborator" "${err}"; then
      rm -f "${err}"
      echo "none"
      return 0
    fi
    cat "${err}" >&2
    rm -f "${err}"
    return 1
  fi
  rm -f "${err}"
  jq -er '.role_name // ""' <<<"${out}"
}

failures=0
while IFS= read -r pr; do
  [[ -n "${pr}" ]] || continue
  number="$(jq -r '.number' <<<"${pr}")"
  author="$(jq -r '.login' <<<"${pr}")"
  author_type="$(jq -r '.type // ""' <<<"${pr}")"
  created_at="$(jq -r '.created_at // ""' <<<"${pr}")"

  if [[ "${created_at}" < "${cutoff}" ]]; then
    if ! events="$(gh api "repos/${repository}/issues/${number}/events" --paginate | jq -s 'add // []')"; then
      echo "::warning::#${number}: could not read events; skipping" >&2
      failures=$((failures + 1))
      continue
    fi
    reopened="$(jq -r --arg cutoff "${cutoff}" \
      '[.[] | select(.event == "reopened" and (.created_at // "") >= $cutoff)] | length' <<<"${events}")"
    if [[ "${reopened}" == "0" ]]; then
      continue
    fi
  fi

  if [[ "${author_type}" == "Bot" ]]; then
    echo "#${number}: ${author} is a bot; skipping"
    continue
  fi

  if ! role="$(collaborator_role "${author}")"; then
    echo "::warning::#${number}: permission check for ${author} failed; skipping" >&2
    failures=$((failures + 1))
    continue
  fi
  case "${role}" in
    admin|maintain|write)
      echo "#${number}: ${author} has write access; skipping"
      continue
      ;;
  esac

  author_lower="$(tr '[:upper:]' '[:lower:]' <<<"${author}")"
  if [[ -n "${vouched}" ]] && grep -Fxq -- "${author_lower}" <<<"${vouched}"; then
    echo "#${number}: ${author} is vouched"
    continue
  fi

  if [[ "${dry_run}" == "true" ]]; then
    echo "#${number}: would close (${author} is not vouched)"
  else
    if ! gh api -X PATCH "repos/${repository}/pulls/${number}" -f state=closed >/dev/null; then
      echo "::error::#${number}: could not close PR" >&2
      failures=$((failures + 1))
      continue
    fi
    echo "#${number}: closed (${author} is not vouched)"
  fi

  if ! comments="$(gh api "repos/${repository}/issues/${number}/comments" --paginate | jq -s 'add // []')"; then
    echo "::warning::#${number}: could not read comments; not commenting" >&2
    continue
  fi
  already="$(jq -r --arg login "${BOT_LOGIN}" --arg marker "${MARKER}" \
    '[.[] | select(.user.login == $login and ((.body // "") | contains($marker)))] | length' <<<"${comments}")"
  if [[ "${already}" != "0" ]]; then
    echo "#${number}: vouch comment already posted"
    continue
  fi

  if [[ "${dry_run}" == "true" ]]; then
    echo "#${number}: would comment"
  elif ! gh api -X POST "repos/${repository}/issues/${number}/comments" \
    -f body="$(comment_body "${author}")" >/dev/null; then
    echo "::warning::#${number}: could not post explanation comment" >&2
  else
    echo "#${number}: commented"
  fi
done <<<"${candidates}"

if [[ "${failures}" -gt 0 ]]; then
  echo "vouch-sweep: ${failures} PR(s) could not be processed" >&2
  exit 1
fi
