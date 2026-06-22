#!/usr/bin/env bash
# Adds a pull request to a GitHub merge queue using the GraphQL API.
# Usage: enqueue-pr.sh [PR_NUMBER_OR_URL]
#
# If no argument is given, uses the current branch's PR.
# Requires: gh CLI authenticated with sufficient permissions, and jq.

set -euo pipefail

pr="${1:-}"

# Resolve PR to its URL and node ID in a single API call
if [[ -z "$pr" ]]; then
  pr_json="$(gh pr view --json url,id)"
elif [[ "$pr" =~ ^[0-9]+$ ]]; then
  pr_json="$(gh pr view "$pr" --json url,id)"
else
  pr_json="$(gh pr view "$pr" --json url,id)"
fi

pr_url="$(echo "$pr_json" | jq -r .url)"
pr_node_id="$(echo "$pr_json" | jq -r .id)"

echo "Enqueuing: $pr_url"

# Enqueue the PR
result="$(gh api graphql -f query='
  mutation($prId: ID!) {
    enqueuePullRequest(input: {pullRequestId: $prId}) {
      mergeQueueEntry {
        position
        estimatedTimeToMerge
      }
    }
  }
' -f prId="$pr_node_id")"

# Check for GraphQL errors
if echo "$result" | jq -e '.errors' >/dev/null 2>&1; then
  echo "GraphQL errors:" >&2
  echo "$result" | jq '.errors' >&2
  exit 1
fi

position="$(echo "$result" | jq -r '.data.enqueuePullRequest.mergeQueueEntry.position')"
eta="$(echo "$result" | jq -r '.data.enqueuePullRequest.mergeQueueEntry.estimatedTimeToMerge // "unknown"')"

echo "PR enqueued at position $position (ETA: $eta)"
echo "Confirming PR stays in queue..."

# Poll briefly to verify the PR wasn't immediately dequeued (e.g. for failed
# required checks).  GitHub can take up to ~60s to remove a PR after the
# mutation returns, so we check a few times before declaring success.
VERIFY_ATTEMPTS="${VERIFY_ATTEMPTS:-4}"
VERIFY_INTERVAL="${VERIFY_INTERVAL:-10}"

# Resolve owner/repo and number from the URL for GraphQL queries
if [[ "$pr_url" =~ ^https://github.com/([^/]+)/([^/]+)/pull/([0-9]+) ]]; then
  _owner="${BASH_REMATCH[1]}"
  _name="${BASH_REMATCH[2]}"
  _number="${BASH_REMATCH[3]}"
else
  echo "ERROR: could not parse PR URL: $pr_url" >&2
  exit 1
fi

for ((i = 1; i <= VERIFY_ATTEMPTS; i++)); do
  sleep "$VERIFY_INTERVAL"

  verify="$(gh api graphql -f query='
    query($owner: String!, $name: String!, $number: Int!) {
      repository(owner: $owner, name: $name) {
        pullRequest(number: $number) {
          mergeQueueEntry {
            position
          }
        }
      }
    }
  ' -f owner="$_owner" -f name="$_name" -F number="$_number" --jq '.data.repository.pullRequest.mergeQueueEntry')"

  if [[ -z "$verify" || "$verify" == "null" ]]; then
    echo "" >&2
    echo "ERROR: PR was removed from the merge queue shortly after being added." >&2

    dq_result="$(gh api graphql -f query='
      query($owner: String!, $name: String!, $number: Int!) {
        repository(owner: $owner, name: $name) {
          pullRequest(number: $number) {
            timelineItems(last: 1, itemTypes: [REMOVED_FROM_MERGE_QUEUE_EVENT]) {
              nodes {
                ... on RemovedFromMergeQueueEvent {
                  createdAt
                  reason
                }
              }
            }
          }
        }
      }
    ' -f owner="$_owner" -f name="$_name" -F number="$_number" 2>/dev/null)" || true

    reason="$(echo "$dq_result" | jq -r '.data.repository.pullRequest.timelineItems.nodes[0].reason // empty' 2>/dev/null)" || true
    if [[ -n "$reason" ]]; then
      echo "Reason: $reason" >&2
    fi

    exit 1
  fi

  echo "  check $i/$VERIFY_ATTEMPTS: still queued at position $(echo "$verify" | jq -r '.position')"
done

echo "Confirmed: PR is in the merge queue."
