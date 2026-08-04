#!/usr/bin/env bash
# Calculate rework rate for agent-authored PRs.
#
# Usage: ./scripts/rework-rate.sh [REPO] [DAYS] [FOLLOWUP_DAYS] [BOT_LOGIN]
#
#   REPO           - GitHub repo (default: fullsend-ai/fullsend)
#   DAYS           - Look back window for merged PRs (default: 30)
#   FOLLOWUP_DAYS  - Window after merge to check for human follow-ups (default: 7)
#   BOT_LOGIN      - Bot author to search for (default: fullsend-ai-coder[bot])
#
# Requires: gh CLI authenticated with repo access, jq

set -euo pipefail

REPO="${1:-fullsend-ai/fullsend}"
DAYS="${2:-30}"
FOLLOWUP_DAYS="${3:-7}"
BOT_LOGIN="${4:-fullsend-ai-coder[bot]}"

SINCE=$(date -u -d "-${DAYS} days" +%Y-%m-%dT00:00:00Z 2>/dev/null \
  || date -u -v-"${DAYS}"d +%Y-%m-%dT00:00:00Z 2>/dev/null \
  || echo "")
if [ -z "$SINCE" ]; then
  echo "ERROR: could not compute start date. DAYS='${DAYS}' may be invalid."
  exit 1
fi

NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)

echo "Rework Rate Report"
echo "Repository: ${REPO}"
echo "Window: last ${DAYS} days (since ${SINCE})"
echo "Follow-up window: ${FOLLOWUP_DAYS} days after merge"
echo ""

# Fetch merged PRs by bot authors (paginated)
BOT_PRS_ERR=$(mktemp)
if ! BOT_PRS=$(gh api "search/issues?q=repo:${REPO}+is:pr+is:merged+author:${BOT_LOGIN}+merged:>=${SINCE}&per_page=100&sort=created&order=desc" \
  --paginate --jq '.items[] | {number: .number, title: .title, merged_at: .pull_request.merged_at}' 2>"$BOT_PRS_ERR"); then
  echo "ERROR: could not fetch bot PRs: $(cat "$BOT_PRS_ERR")"
  rm -f "$BOT_PRS_ERR"
  exit 1
fi
rm -f "$BOT_PRS_ERR"

if [ -z "$BOT_PRS" ]; then
  echo "No agent PRs found in the last ${DAYS} days."
  exit 0
fi

PR_COUNT=$(echo "$BOT_PRS" | jq -s 'length')
if [ "$PR_COUNT" -ge 1000 ]; then
  echo "WARNING: GitHub Search API caps results at 1000. Actual count may be higher; consider narrowing the DAYS window."
fi
echo "Found ${PR_COUNT} agent PRs to check."
echo ""

TOTAL=0
CHECKED=0
REWORKED=0
SKIPPED_WINDOW=0
SKIPPED_ERROR=0
SKIPPED_NULL_AUTHOR=0
REWORKED_LINES=()

while IFS= read -r pr_json; do
  PR_NUM=$(echo "$pr_json" | jq -r '.number')
  PR_TITLE=$(echo "$pr_json" | jq -r '.title')
  MERGED_AT=$(echo "$pr_json" | jq -r '.merged_at')
  TOTAL=$((TOTAL + 1))

  echo "  Checking PR ${TOTAL}/${PR_COUNT} (#${PR_NUM})..."

  # Skip PRs whose follow-up window hasn't fully elapsed yet
  FOLLOWUP_UNTIL=$(date -u -d "${MERGED_AT} +${FOLLOWUP_DAYS} days" +%Y-%m-%dT23:59:59Z 2>/dev/null \
    || date -u -j -f "%Y-%m-%dT%H:%M:%SZ" -v+"${FOLLOWUP_DAYS}"d "${MERGED_AT}" +%Y-%m-%dT23:59:59Z 2>/dev/null \
    || echo "")

  if [ -z "$FOLLOWUP_UNTIL" ]; then
    echo "    WARNING: could not compute follow-up window for #${PR_NUM} (merged_at=${MERGED_AT})"
    SKIPPED_ERROR=$((SKIPPED_ERROR + 1))
    continue
  fi

  if [[ "$FOLLOWUP_UNTIL" > "$NOW" ]]; then
    echo "    follow-up window not elapsed yet, skipping"
    SKIPPED_WINDOW=$((SKIPPED_WINDOW + 1))
    continue
  fi

  # Get files changed in this PR (paginated)
  PR_FILES_ERR=$(mktemp)
  if ! PR_FILES=$(gh api "repos/${REPO}/pulls/${PR_NUM}/files" --paginate \
    --jq '.[].filename' 2>"$PR_FILES_ERR"); then
    echo "    WARNING: could not fetch files for #${PR_NUM}: $(cat "$PR_FILES_ERR")"
    rm -f "$PR_FILES_ERR"
    SKIPPED_ERROR=$((SKIPPED_ERROR + 1))
    continue
  fi
  rm -f "$PR_FILES_ERR"

  if [ -z "$PR_FILES" ]; then
    CHECKED=$((CHECKED + 1))
    continue
  fi

  # Get the PR's own merge commit SHA to exclude it from follow-up detection
  MERGE_SHA_ERR=$(mktemp)
  if ! PR_MERGE_SHA=$(gh api "repos/${REPO}/pulls/${PR_NUM}" --jq '.merge_commit_sha' 2>"$MERGE_SHA_ERR"); then
    echo "    WARNING: could not fetch merge SHA for #${PR_NUM}: $(cat "$MERGE_SHA_ERR")"
    rm -f "$MERGE_SHA_ERR"
    SKIPPED_ERROR=$((SKIPPED_ERROR + 1))
    continue
  fi
  rm -f "$MERGE_SHA_ERR"

  # Get commits after merge by non-bot authors (paginated)
  COMMITS_ERR=$(mktemp)
  if ! FOLLOWUP_COMMITS=$(gh api "repos/${REPO}/commits?since=${MERGED_AT}&until=${FOLLOWUP_UNTIL}&per_page=100" \
    --paginate --jq '.[] | select(.author == null or .author.type != "Bot") | {sha: .sha, author_login: (if .author != null then (.author.login // "unknown") else null end), parents: (.parents | length)}' 2>"$COMMITS_ERR"); then
    echo "    WARNING: could not fetch follow-up commits for #${PR_NUM}: $(cat "$COMMITS_ERR")"
    rm -f "$COMMITS_ERR"
    SKIPPED_ERROR=$((SKIPPED_ERROR + 1))
    continue
  fi
  rm -f "$COMMITS_ERR"

  if [ -z "$FOLLOWUP_COMMITS" ]; then
    CHECKED=$((CHECKED + 1))
    continue
  fi

  # Check if any single-parent follow-up commit touches the same files
  FOUND_REWORK=""
  PR_HAD_ERROR=""
  while IFS= read -r commit_json; do
    COMMIT_SHA=$(echo "$commit_json" | jq -r '.sha')
    COMMIT_AUTHOR=$(echo "$commit_json" | jq -r '.author_login')
    PARENT_COUNT=$(echo "$commit_json" | jq -r '.parents')

    # Skip commits with no linked GitHub identity
    if [ "$COMMIT_AUTHOR" = "null" ]; then
      SKIPPED_NULL_AUTHOR=$((SKIPPED_NULL_AUTHOR + 1))
      continue
    fi

    # Skip merge commits (2+ parents); their files list reflects the full merge, not incremental work
    if [ "$PARENT_COUNT" -gt 1 ]; then
      continue
    fi

    # Skip the PR's own merge commit
    if [ -n "$PR_MERGE_SHA" ] && [ "$COMMIT_SHA" = "$PR_MERGE_SHA" ]; then
      continue
    fi

    COMMIT_FILES_ERR=$(mktemp)
    if ! COMMIT_FILES=$(gh api "repos/${REPO}/commits/${COMMIT_SHA}" --paginate \
      --jq '.files[].filename' 2>"$COMMIT_FILES_ERR"); then
      echo "    WARNING: could not fetch files for commit ${COMMIT_SHA:0:7}: $(cat "$COMMIT_FILES_ERR")"
      rm -f "$COMMIT_FILES_ERR"
      PR_HAD_ERROR="yes"
      continue
    fi
    rm -f "$COMMIT_FILES_ERR"

    OVERLAP=$(comm -12 <(echo "$PR_FILES" | sort) <(echo "$COMMIT_FILES" | sort) 2>/dev/null || echo "")

    if [ -n "$OVERLAP" ]; then
      FOUND_REWORK="yes"
      REWORKED_LINES+=("  #${PR_NUM} - ${PR_TITLE}")
      REWORKED_LINES+=("    Follow-up: ${COMMIT_SHA:0:7} by @${COMMIT_AUTHOR} (same files: $(echo "$OVERLAP" | head -3 | tr '\n' ', '))")
      break
    fi
  done <<< "$FOLLOWUP_COMMITS"

  if [ -n "$PR_HAD_ERROR" ] && [ -z "$FOUND_REWORK" ]; then
    SKIPPED_ERROR=$((SKIPPED_ERROR + 1))
    continue
  fi

  CHECKED=$((CHECKED + 1))
  if [ -n "$FOUND_REWORK" ]; then
    REWORKED=$((REWORKED + 1))
  fi
done < <(echo "$BOT_PRS" | jq -c '.')

echo ""
echo "Results"
echo "-------"
echo "Agent PRs found: ${TOTAL}"
echo "Agent PRs checked: ${CHECKED}"
echo "Reworked by humans: ${REWORKED}"
if [ "$CHECKED" -eq 0 ]; then
  echo "Rework rate: n/a (0 of ${TOTAL} PRs evaluated)"
else
  RATE=$(awk "BEGIN {printf \"%.1f\", ($REWORKED / $CHECKED) * 100}")
  echo "Rework rate: ${RATE}%"
fi
if [ "$SKIPPED_WINDOW" -gt 0 ]; then
  echo "Skipped (follow-up window not elapsed): ${SKIPPED_WINDOW}"
fi
if [ "$SKIPPED_ERROR" -gt 0 ]; then
  echo "Skipped (API errors): ${SKIPPED_ERROR}"
fi
if [ "$SKIPPED_NULL_AUTHOR" -gt 0 ]; then
  echo "Follow-up commits with no linked GitHub identity (excluded): ${SKIPPED_NULL_AUTHOR}"
fi

if [ ${#REWORKED_LINES[@]} -gt 0 ]; then
  echo ""
  echo "Reworked PRs:"
  printf '%s\n' "${REWORKED_LINES[@]}"
fi
