#!/usr/bin/env bash
# pre-fetch-resolved-threads.sh — Fetch human-resolved review threads before the review agent runs
#
# Queries the PR's review threads via the GitHub GraphQL API and writes
# a JSON file listing threads that a human explicitly resolved. The
# review agent uses this to avoid re-raising findings that a person
# already dismissed.
#
# Threads resolved by the bot itself (auto-resolution of outdated
# comments) are excluded — only human resolutions count.
#
# If a thread's inline comment contains a finding id marker
# (<!-- finding:f_abc -->), the id is included in the output so the
# agent can match by exact id instead of fuzzy file+line.
#
# Best-effort: if the GraphQL query fails or any step errors, the
# script writes an empty file and exits 0 so the review proceeds
# without resolution data (current behavior preserved).
#
# Required environment variables (set by the workflow):
#
#   - GH_TOKEN          — token with read access to the PR
#   - SOURCE_REPO       — owner/repo (e.g., "fullsend-ai/fullsend")
#   - PR_NUM            — PR number
#   - ORG_NAME          — org name for bot identity filtering
#
# Outputs (via GITHUB_OUTPUT):
#   - human_resolved_file — path to the JSON file
set -euo pipefail

OUTPUT_FILE="${GITHUB_WORKSPACE:-/tmp}/human-resolved-threads.json"
REVIEW_BOT="${ORG_NAME}-review[bot]"
SHARED_REVIEW_BOT="fullsend-ai-review[bot]"

OWNER="${SOURCE_REPO%%/*}"
NAME="${SOURCE_REPO##*/}"

# --- GraphQL query ---
# Fetches review threads with resolution state, path, line, and comments.
# Paginated at 100 threads per page with a 20-page cap (matching the
# pattern in forge_resolve_outdated_review_threads from agents PR #1415).
QUERY='query($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          isResolved
          path
          line
          originalLine
          resolvedBy { login }
          comments(first: 100) {
            pageInfo { hasNextPage }
            nodes {
              author { login __typename }
              body
              createdAt
            }
          }
        }
      }
    }
  }
}'

# --- Pagination ---
cursor=""
has_next="true"
page=0
nodes_json="[]"
truncated="false"

while [[ "${has_next}" == "true" ]]; do
  page=$((page + 1))
  if [[ "${page}" -gt 20 ]]; then
    echo "::warning::Resolved-threads pagination hit page cap — remaining threads skipped"
    truncated="true"
    break
  fi

  gh_args=(api graphql
    -f owner="${OWNER}"
    -f name="${NAME}"
    -F number="${PR_NUM}"
    -f query="${QUERY}")
  if [[ -n "${cursor}" ]]; then
    gh_args+=(-f cursor="${cursor}")
  fi

  if ! response=$(gh "${gh_args[@]}" 2>/dev/null); then
    echo "::warning::Failed to fetch review threads — writing empty resolved-threads file"
    echo '{"resolved_threads":[],"metadata":{"error":"graphql_fetch_failed"}}' > "${OUTPUT_FILE}"
    echo "human_resolved_file=${OUTPUT_FILE}" >> "${GITHUB_OUTPUT:-/dev/null}"
    exit 0
  fi

  if echo "${response}" | jq -e '.errors | type == "array" and length > 0' >/dev/null 2>&1; then
    echo "::warning::Review threads query returned errors — writing empty resolved-threads file"
    echo '{"resolved_threads":[],"metadata":{"error":"graphql_errors"}}' > "${OUTPUT_FILE}"
    echo "human_resolved_file=${OUTPUT_FILE}" >> "${GITHUB_OUTPUT:-/dev/null}"
    exit 0
  fi

  page_nodes=$(echo "${response}" | jq -c '.data.repository.pullRequest.reviewThreads.nodes // []' 2>/dev/null) || {
    echo "::warning::Failed to parse review threads — writing empty resolved-threads file"
    echo '{"resolved_threads":[],"metadata":{"error":"parse_failed"}}' > "${OUTPUT_FILE}"
    echo "human_resolved_file=${OUTPUT_FILE}" >> "${GITHUB_OUTPUT:-/dev/null}"
    exit 0
  }

  nodes_json=$(jq -c --argjson page "${page_nodes}" '. + $page' <<< "${nodes_json}" 2>/dev/null) || {
    echo "::warning::Failed to merge review thread pages — writing empty resolved-threads file"
    echo '{"resolved_threads":[],"metadata":{"error":"merge_failed"}}' > "${OUTPUT_FILE}"
    echo "human_resolved_file=${OUTPUT_FILE}" >> "${GITHUB_OUTPUT:-/dev/null}"
    exit 0
  }

  has_next=$(echo "${response}" | jq -r '.data.repository.pullRequest.reviewThreads.pageInfo.hasNextPage // false' 2>/dev/null) || has_next="false"
  cursor=$(echo "${response}" | jq -r '.data.repository.pullRequest.reviewThreads.pageInfo.endCursor // empty' 2>/dev/null) || cursor=""
  if [[ "${has_next}" == "true" && -z "${cursor}" ]]; then
    echo "::warning::Review threads page missing cursor — stopping pagination"
    break
  fi
done

# --- Filter and transform ---
# Select threads that:
#   - are resolved
#   - were resolved by a human (not the review bot)
#   - have at least one comment
#   - comment pagination is complete (incomplete pages might hide context)
#
# For each matching thread, extract:
#   - file, line, original_line from the thread
#   - resolved_by from resolvedBy.login
#   - the first bot comment body (for snippet matching)
#   - the resolver's last comment (the resolution rationale) — only
#     comments from the resolvedBy user count, not any human
#   - finding_id if present in the bot comment (<!-- finding:f_xxx -->)
#   - resolution_context classification: explicit_dismissal when the
#     resolver left a comment, silent_resolution otherwise
#
# GitHub GraphQL has no Actor/User.type (that is REST). Bots are
# __typename Bot on comment authors, and login ending in "[bot]" on
# resolvedBy (typed User in the schema even for GitHub Apps).

RESOLVED_THREADS=$(echo "${nodes_json}" | jq -c \
  --arg bot "${REVIEW_BOT}" \
  --arg shared_bot "${SHARED_REVIEW_BOT}" \
  '[.[]
    | select(.isResolved == true)
    | select(.resolvedBy != null)
    | select(.resolvedBy.login != $bot and .resolvedBy.login != $shared_bot)
    | select((.resolvedBy.login | endswith("[bot]")) | not)
    | select((.comments.nodes // [] | length) > 0)
    | select((.comments.pageInfo.hasNextPage // false) == false)
    | .resolvedBy.login as $resolver
    | {
        file: .path,
        line: .line,
        original_line: .originalLine,
        resolved_by: $resolver,
        bot_finding_snippet: (
          [.comments.nodes[] | select(.author.login == $bot or .author.login == $shared_bot)]
          | first // null
          | if . then (.body | .[0:200]) else null end
        ),
        finding_id: (
          [.comments.nodes[] | select(.author.login == $bot or .author.login == $shared_bot)]
          | first // null
          | if . then (.body | capture("<!-- finding:(?<id>[a-zA-Z0-9_]+) -->") // null | .id // null) else null end
        ),
        human_response: (
          [.comments.nodes[] | select(.author.login == $resolver)]
          | last // null
          | if . then (.body | .[0:500]) else null end
        ),
        resolution_context: (
          if ([.comments.nodes[] | select(.author.login == $resolver)] | length) > 0
          then "explicit_dismissal"
          else "silent_resolution"
          end
        )
      }
  ]' 2>/dev/null) || RESOLVED_THREADS="[]"

THREAD_COUNT=$(echo "${nodes_json}" | jq 'length' 2>/dev/null) || THREAD_COUNT=0
RESOLVED_COUNT=$(echo "${RESOLVED_THREADS}" | jq 'length' 2>/dev/null) || RESOLVED_COUNT=0

# --- Write output ---
if ! jq -n \
  --argjson threads "${RESOLVED_THREADS}" \
  --argjson pr_num "${PR_NUM}" \
  --arg repo "${SOURCE_REPO}" \
  --argjson thread_count "${THREAD_COUNT}" \
  --argjson resolved_count "${RESOLVED_COUNT}" \
  --argjson truncated "${truncated}" \
  --arg fetched_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{
    resolved_threads: $threads,
    metadata: {
      pr_number: $pr_num,
      repo: $repo,
      thread_count: $thread_count,
      human_resolved_count: $resolved_count,
      truncated: $truncated,
      fetched_at: $fetched_at
    }
  }' > "${OUTPUT_FILE}"; then
  echo "::warning::Failed to write resolved-threads file — writing empty resolved-threads file"
  echo '{"resolved_threads":[],"metadata":{"error":"write_failed"}}' > "${OUTPUT_FILE}"
fi

echo "Resolved threads: ${RESOLVED_COUNT} human-resolved out of ${THREAD_COUNT} total"
echo "human_resolved_file=${OUTPUT_FILE}" >> "${GITHUB_OUTPUT:-/dev/null}"
