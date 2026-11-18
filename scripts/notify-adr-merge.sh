#!/usr/bin/env bash
# notify-adr-merge.sh — Post a Slack message for each merged PR that changed ADRs.
#
# Push-triggered replacement for the pull_request_target trigger of
# notify-adr-slack.yml (#9245). The workflow runs on push to main with
# paths: docs/ADRs/** and passes the pushed commit shas. One merge-queue push
# can carry several PRs, so this script finds the merged PRs for every commit
# (GET /repos/{owner}/{repo}/commits/{sha}/pulls), dedupes them by number, and
# sends one message per PR whose files include docs/ADRs/. The message format
# matches the former workflow. Pushes that came from no merged PR send nothing.
#
# Usage: notify-adr-merge.sh [--dry-run] OWNER/REPO SHA [SHA...]
#
# Environment:
#   GH_TOKEN          — token with contents: read and pull-requests: read
#   SLACK_WEBHOOK_URL — incoming webhook (required unless --dry-run)
#
# --dry-run prints each Slack payload (one JSON object per line) instead of
# sending it. Exits 1 when the webhook is missing or a message fails to send.

set -euo pipefail

usage() {
  echo "Usage: $0 [--dry-run] OWNER/REPO SHA [SHA...]" >&2
  exit 2
}

dry_run=false
if [[ "${1:-}" == "--dry-run" ]]; then
  dry_run=true
  shift
fi
[[ $# -ge 2 && "$1" == */* ]] || usage
repository="$1"
shift

pr_numbers=""
for sha in "$@"; do
  [[ "${sha}" =~ ^[0-9a-fA-F]{7,40}$ ]] || { echo "invalid commit sha: ${sha}" >&2; exit 2; }
  merged="$(gh api "repos/${repository}/commits/${sha}/pulls" |
    jq -r '.[] | select(.merged_at != null) | .number')"
  pr_numbers+="${merged}"$'\n'
done
pr_numbers="$(grep -E '^[0-9]+$' <<<"${pr_numbers}" | sort -un || true)"

if [[ -z "${pr_numbers}" ]]; then
  echo "notify-adr-merge: no merged pull request found for the pushed commits" >&2
  exit 0
fi

failures=0
while IFS= read -r number; do
  adr_files="$(gh api "repos/${repository}/pulls/${number}/files" --paginate |
    jq -s -r 'add // [] | .[].filename | select(startswith("docs/ADRs/"))')"
  if [[ -z "${adr_files}" ]]; then
    echo "#${number}: no ADR files changed; skipping" >&2
    continue
  fi

  summary=""
  while IFS= read -r f; do
    summary+="• $(basename "${f}" .md)"$'\n'
  done <<<"${adr_files}"

  pr_json="$(gh api "repos/${repository}/pulls/${number}")"
  payload="$(jq -c --arg files "${summary}" '
    ("📋 *ADR updated* — <" + .html_url + "|#" + (.number | tostring) + " " + .title + ">\nMerged by *" + (.merged_by.login // "") + "*") as $head
    | {
        blocks: [
          {type: "section", text: {type: "mrkdwn", text: $head}},
          {type: "section", text: {type: "mrkdwn", text: ("*Affected ADRs:*\n" + $files)}}
        ]
      }
  ' <<<"${pr_json}")"

  if [[ "${dry_run}" == "true" ]]; then
    printf '%s\n' "${payload}"
    continue
  fi

  if [[ -z "${SLACK_WEBHOOK_URL:-}" ]]; then
    echo "::error::SLACK_WEBHOOK_URL secret is not configured"
    exit 1
  fi
  if curl -sf -X POST -H 'Content-Type: application/json' \
    --data-binary @- "${SLACK_WEBHOOK_URL}" <<<"${payload}" >/dev/null; then
    echo "#${number}: Slack notification sent" >&2
  else
    echo "::error::#${number}: Slack notification failed"
    failures=$((failures + 1))
  fi
done <<<"${pr_numbers}"

if [[ "${failures}" -gt 0 ]]; then
  exit 1
fi
