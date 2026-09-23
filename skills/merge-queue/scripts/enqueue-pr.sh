#!/usr/bin/env bash
# Adds a pull request to a GitHub merge queue using the GraphQL API.
# Usage: enqueue-pr.sh [PR_NUMBER_OR_URL]
#
# If no argument is given, uses the current branch's PR.
# Requires: gh CLI authenticated with sufficient permissions, and jq.

set -euo pipefail

pr="${1:-}"

# Resolve PR to its URL and node ID in a single API call.
# Parse github.com URLs locally so `gh pr view` never receives a raw URL
# argument: the sandbox SSRF PreToolUse hook DNS-resolves https?://
# literals on non-inert commands, and github.com is not allowlisted.
if [[ -z "$pr" ]]; then
  pr_json="$(gh pr view --json url,id)"
elif [[ "$pr" =~ ^https://github.com/([^/]+/[^/]+)/pull/([0-9]+) ]]; then
  repo="${BASH_REMATCH[1]}"
  number="${BASH_REMATCH[2]}"
  pr_json="$(gh pr view "$number" -R "$repo" --json url,id)"
elif [[ "$pr" =~ ^[0-9]+$ ]]; then
  pr_json="$(gh pr view "$pr" --json url,id)"
else
  echo "Error: provide a PR number or URL" >&2
  exit 1
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

echo "PR added to merge queue at position $position (ETA: $eta)"
