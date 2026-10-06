#!/usr/bin/env bash
# Shows why a PR was removed from the merge queue.
# Usage: dequeue-reason.sh <PR_NUMBER> [-R owner/repo]
#        dequeue-reason.sh <PR_URL>
#
# PR_NUMBER defaults to the current repo; pass -R/--repo owner/repo for a
# PR in another repo. Prefer PR_NUMBER (+ -R) over PR_URL: an agent invoking
# this script through the sandboxed Bash tool must never put a raw
# github.com URL on the command line, or the SSRF PreToolUse hook
# fail-closes before this script even runs (see SKILL.md).
#
# Queries the PR timeline for RemovedFromMergeQueueEvent entries and
# prints the reason, timestamp, and commit SHA for each removal.
# Requires: gh CLI authenticated, jq.

set -euo pipefail

# Reject anything but a plain two-segment owner/repo nwo: no host-qualified
# ("host/owner/repo") or "."/".." path segments.
validate_repo_nwo() {
  local value="$1"
  if [[ ! "$value" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]]; then
    echo "Error: -R/--repo must be a plain owner/repo value, not: $value" >&2
    exit 1
  fi
  local owner_part="${value%%/*}" repo_part="${value#*/}"
  if [[ "$owner_part" == "." || "$owner_part" == ".." || "$repo_part" == "." || "$repo_part" == ".." ]]; then
    echo "Error: -R/--repo must be a plain owner/repo value, not: $value" >&2
    exit 1
  fi
}

pr=""
repo=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    -R|--repo)
      if [[ $# -lt 2 ]]; then
        echo "Error: -R/--repo requires an owner/repo value" >&2
        exit 1
      fi
      repo="$2"
      validate_repo_nwo "$repo"
      shift 2
      ;;
    *)
      if [[ -n "$pr" ]]; then
        echo "Error: unexpected extra argument: $1" >&2
        exit 1
      fi
      pr="$1"
      shift
      ;;
  esac
done

if [[ -n "$repo" && "$pr" =~ ^https://github.com/ ]]; then
  echo "Error: provide either a PR URL or -R/--repo, not both" >&2
  exit 1
fi

# Resolve to owner/repo and PR number
if [[ -n "$repo" ]]; then
  if [[ -z "$pr" ]]; then
    echo "Error: -R/--repo requires a PR number" >&2
    exit 1
  elif [[ "$pr" =~ ^[0-9]+$ ]]; then
    number="$pr"
  else
    echo "Error: -R/--repo requires a PR number" >&2
    exit 1
  fi
elif [[ -z "$pr" ]]; then
  echo "Error: provide a PR number or URL" >&2
  exit 1
elif [[ "$pr" =~ ^https://github.com/([^/]+/[^/]+)/pull/([0-9]+) ]]; then
  repo="${BASH_REMATCH[1]}"
  number="${BASH_REMATCH[2]}"
  validate_repo_nwo "$repo"
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
