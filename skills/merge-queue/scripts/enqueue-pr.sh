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

echo "PR enqueued at position $position (ETA: ${eta}s)"
echo "Waiting for merge queue outcome..."

POLL_INTERVAL="${POLL_INTERVAL:-30}"

# Resolve owner/repo and number from the URL for GraphQL queries
if [[ "$pr_url" =~ ^https://github.com/([^/]+)/([^/]+)/pull/([0-9]+) ]]; then
  _owner="${BASH_REMATCH[1]}"
  _name="${BASH_REMATCH[2]}"
  _number="${BASH_REMATCH[3]}"
else
  echo "ERROR: could not parse PR URL: $pr_url" >&2
  exit 1
fi

# Poll until the PR either merges or gets ejected from the queue.
while true; do
  sleep "$POLL_INTERVAL"

  state="$(gh api graphql -f query='
    query($owner: String!, $name: String!, $number: Int!) {
      repository(owner: $owner, name: $name) {
        pullRequest(number: $number) {
          state
          mergeQueueEntry {
            position
            state
          }
        }
      }
    }
  ' -f owner="$_owner" -f name="$_name" -F number="$_number" --jq '{
    pr_state: .data.repository.pullRequest.state,
    queue_entry: .data.repository.pullRequest.mergeQueueEntry
  }')"

  pr_state="$(echo "$state" | jq -r '.pr_state')"
  queue_entry="$(echo "$state" | jq -r '.queue_entry')"

  # PR was merged — success
  if [[ "$pr_state" == "MERGED" ]]; then
    echo "PR merged successfully."
    exit 0
  fi

  # PR is still in the queue — keep waiting
  if [[ -n "$queue_entry" && "$queue_entry" != "null" ]]; then
    pos="$(echo "$queue_entry" | jq -r '.position')"
    qstate="$(echo "$queue_entry" | jq -r '.state')"
    echo "  position $pos ($qstate)"
    continue
  fi

  # PR is no longer in the queue and not merged — it was ejected
  echo "" >&2
  echo "ERROR: PR was removed from the merge queue." >&2

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
done
