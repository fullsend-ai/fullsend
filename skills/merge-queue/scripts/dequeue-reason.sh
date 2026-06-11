#!/usr/bin/env bash
# Shows why a PR was removed from the merge queue.
# Usage: dequeue-reason.sh <PR_NUMBER_OR_URL>
#
# Queries the PR timeline for RemovedFromMergeQueueEvent entries and
# prints the reason, timestamp, and commit SHA for each removal.
# Requires: gh CLI authenticated, jq.

set -euo pipefail

pr="${1:?Usage: dequeue-reason.sh <PR_NUMBER_OR_URL>}"

# Resolve to owner/repo and PR number
if [[ "$pr" =~ ^https://github.com/([^/]+/[^/]+)/pull/([0-9]+) ]]; then
  repo="${BASH_REMATCH[1]}"
  number="${BASH_REMATCH[2]}"
elif [[ "$pr" =~ ^[0-9]+$ ]]; then
  repo="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
  number="$pr"
else
  echo "Error: provide a PR number or URL" >&2
  exit 1
fi

owner="${repo%%/*}"
name="${repo##*/}"

result="$(gh api graphql -f query='
  query($owner: String!, $name: String!, $number: Int!) {
    repository(owner: $owner, name: $name) {
      pullRequest(number: $number) {
        title
        url
        timelineItems(last: 20, itemTypes: [REMOVED_FROM_MERGE_QUEUE_EVENT]) {
          nodes {
            ... on RemovedFromMergeQueueEvent {
              createdAt
              reason
              beforeCommit { abbreviatedOid }
            }
          }
        }
      }
    }
  }
' -f owner="$owner" -f name="$name" -F number="$number")"

title="$(echo "$result" | jq -r '.data.repository.pullRequest.title')"
url="$(echo "$result" | jq -r '.data.repository.pullRequest.url')"
count="$(echo "$result" | jq '.data.repository.pullRequest.timelineItems.nodes | length')"

if [[ "$count" -eq 0 ]]; then
  echo "${url}  ${title}"
  echo "  No merge queue removals found."
  exit 0
fi

echo "${url}  ${title}"
echo "${count} removal(s):"
echo ""
echo "$result" | jq -r '
  .data.repository.pullRequest.timelineItems.nodes[] |
  "  \(.createdAt)  reason: \(.reason)  commit: \(.beforeCommit.abbreviatedOid // "unknown")"
'
