#!/usr/bin/env bash
# after_each hook: capture fixture state for judges.
#
# Snapshots the GitHub issue/PR state into output/fixture-state.json
# so judges can evaluate the agent's work.
#
# Required env (forward-propagated from setup-fixture.sh):
#   EPHEMERAL_REPO  — org/name of the ephemeral repo
#   FIXTURE_NUMBER  — issue or PR number
#   FIXTURE_TYPE    — "issue" or "pull_request"
#   FIXTURE_URL     — full URL of the fixture
#   FORGE           — "github"
#
# Required env (set by harness):
#   CASE_WORKSPACE  — path to the case workspace
set -euo pipefail

CASE_WORKSPACE="${CASE_WORKSPACE:?CASE_WORKSPACE is required}"
EPHEMERAL_REPO="${EPHEMERAL_REPO:?EPHEMERAL_REPO is required}"
FIXTURE_NUMBER="${FIXTURE_NUMBER:?FIXTURE_NUMBER is required}"
FIXTURE_TYPE="${FIXTURE_TYPE:?FIXTURE_TYPE is required}"
FIXTURE_URL="${FIXTURE_URL:?FIXTURE_URL is required}"

OUTPUT_DIR="${CASE_WORKSPACE}/output"
mkdir -p "$OUTPUT_DIR"
STATE_FILE="${OUTPUT_DIR}/fixture-state.json"

case "${FIXTURE_TYPE}" in
  issue)
    issue_json=$(gh issue view "$FIXTURE_NUMBER" --repo "$EPHEMERAL_REPO" \
      --json state,labels,assignees,milestone,title)
    comments_json=$(gh issue view "$FIXTURE_NUMBER" --repo "$EPHEMERAL_REPO" --json comments \
      | jq '[.comments[] | {author: .author.login, body: .body, created_at: .createdAt}]')

    jq -n \
      --arg fixture_type "issue" \
      --arg fixture_url "$FIXTURE_URL" \
      --argjson issue "$issue_json" \
      --argjson comments "$comments_json" \
      '{
        fixture_type: $fixture_type,
        fixture_url: $fixture_url,
        state: $issue.state,
        title: $issue.title,
        labels: [($issue.labels // [])[] | .name],
        assignees: [($issue.assignees // [])[] | .login],
        milestone: ($issue.milestone.title // null),
        comments: $comments
      }' > "$STATE_FILE"
    ;;

  pull_request)
    pr_json=$(gh pr view "$FIXTURE_NUMBER" --repo "$EPHEMERAL_REPO" \
      --json state,labels,assignees,milestone,title,mergeable,reviewDecision)
    comments_json=$(gh pr view "$FIXTURE_NUMBER" --repo "$EPHEMERAL_REPO" --json comments \
      | jq '[.comments[] | {author: .author.login, body: .body, created_at: .createdAt}]')
    reviews_json=$(gh pr view "$FIXTURE_NUMBER" --repo "$EPHEMERAL_REPO" --json reviews \
      | jq '[.reviews[] | {author: .author.login, state: .state, body: .body}]')

    jq -n \
      --arg fixture_type "pull_request" \
      --arg fixture_url "$FIXTURE_URL" \
      --argjson pr "$pr_json" \
      --argjson comments "$comments_json" \
      --argjson reviews "$reviews_json" \
      '{
        fixture_type: $fixture_type,
        fixture_url: $fixture_url,
        state: $pr.state,
        title: $pr.title,
        labels: [($pr.labels // [])[] | .name],
        assignees: [($pr.assignees // [])[] | .login],
        milestone: ($pr.milestone.title // null),
        mergeable: $pr.mergeable,
        review_decision: $pr.reviewDecision,
        comments: $comments,
        reviews: $reviews
      }' > "$STATE_FILE"
    ;;

  *)
    echo "ERROR: unsupported fixture_type: ${FIXTURE_TYPE}" >&2
    exit 1
    ;;
esac

echo "Captured ${FIXTURE_TYPE} state -> ${STATE_FILE}"
