#!/usr/bin/env bash
# Calculate rework rate for agent-authored PRs.
#
# Usage: ./scripts/rework-rate.sh [REPO] [DAYS] [FOLLOWUP_DAYS]
#
#   REPO           - GitHub repo (default: fullsend-ai/fullsend)
#   DAYS           - Look back window for merged PRs (default: 30)
#   FOLLOWUP_DAYS  - Window after merge to check for human follow-ups (default: 7)
#
# Requires: gh CLI authenticated with repo access, jq

set -euo pipefail

REPO="${1:-fullsend-ai/fullsend}"
DAYS="${2:-30}"
FOLLOWUP_DAYS="${3:-7}"

SINCE=$(date -d "-${DAYS} days" +%Y-%m-%dT00:00:00Z 2>/dev/null || date -v-${DAYS}d +%Y-%m-%dT00:00:00Z)

echo "Rework Rate Report"
echo "Repository: ${REPO}"
echo "Window: last ${DAYS} days (since ${SINCE})"
echo "Follow-up window: ${FOLLOWUP_DAYS} days after merge"
echo ""

# Fetch merged PRs by bot authors (both app identity and [bot] login)
BOT_PRS=$(gh api "search/issues?q=repo:${REPO}+is:pr+is:merged+author:fullsend-ai-coder[bot]+merged:>=${SINCE}&per_page=100&sort=created&order=desc" \
  --jq '.items[] | {number: .number, title: .title, closed_at: .closed_at}' 2>/dev/null || echo "")

if [ -z "$BOT_PRS" ]; then
  # Fallback: try app/ prefix
  BOT_PRS=$(gh api "search/issues?q=repo:${REPO}+is:pr+is:merged+author:app/fullsend-ai-coder+merged:>=${SINCE}&per_page=100&sort=created&order=desc" \
    --jq '.items[] | {number: .number, title: .title, closed_at: .closed_at}' 2>/dev/null || echo "")
fi

if [ -z "$BOT_PRS" ]; then
  echo "No agent PRs found in the last ${DAYS} days."
  exit 0
fi

PR_COUNT=$(echo "$BOT_PRS" | jq -s 'length')
echo "Found ${PR_COUNT} agent PRs to check."
echo ""

TOTAL=0
REWORKED=0
SKIPPED=0
REWORKED_LIST=""

while IFS= read -r pr_json; do
  PR_NUM=$(echo "$pr_json" | jq -r '.number')
  PR_TITLE=$(echo "$pr_json" | jq -r '.title')
  MERGED_AT=$(echo "$pr_json" | jq -r '.closed_at')
  TOTAL=$((TOTAL + 1))

  echo "  Checking PR ${TOTAL}/${PR_COUNT} (#${PR_NUM})..."

  # Get files changed in this PR (paginated)
  PR_FILES=$(gh api "repos/${REPO}/pulls/${PR_NUM}/files" --paginate \
    --jq '.[].filename' 2>&1)
  if [ $? -ne 0 ] || [ -z "$PR_FILES" ]; then
    echo "    WARNING: could not fetch files for #${PR_NUM}, skipping"
    SKIPPED=$((SKIPPED + 1))
    continue
  fi

  # Check for human commits touching the same files after merge
  FOLLOWUP_UNTIL=$(date -d "${MERGED_AT} +${FOLLOWUP_DAYS} days" +%Y-%m-%dT23:59:59Z 2>/dev/null \
    || date -j -f "%Y-%m-%dT%H:%M:%SZ" "${MERGED_AT}" -v+${FOLLOWUP_DAYS}d +%Y-%m-%dT23:59:59Z 2>/dev/null \
    || echo "")

  if [ -z "$FOLLOWUP_UNTIL" ]; then
    SKIPPED=$((SKIPPED + 1))
    continue
  fi

  # Get commits after merge by non-bot authors
  FOLLOWUP_COMMITS=$(gh api "repos/${REPO}/commits?since=${MERGED_AT}&until=${FOLLOWUP_UNTIL}&per_page=100" \
    --jq '[.[] | select(.author.type != "Bot" and .author.login != "fullsend-ai-coder[bot]" and .author.login != "fullsend-ai-fullsend[bot]") | {sha: .sha, author: .author.login, message: .commit.message}]' 2>&1)
  if [ $? -ne 0 ]; then
    echo "    WARNING: could not fetch follow-up commits for #${PR_NUM}, skipping"
    SKIPPED=$((SKIPPED + 1))
    continue
  fi

  if [ "$FOLLOWUP_COMMITS" = "[]" ] || [ -z "$FOLLOWUP_COMMITS" ]; then
    continue
  fi

  # Check if any follow-up commit touches the same files
  FOUND_REWORK=""
  while IFS= read -r commit_json; do
    COMMIT_SHA=$(echo "$commit_json" | jq -r '.sha')
    COMMIT_AUTHOR=$(echo "$commit_json" | jq -r '.author')

    COMMIT_FILES=$(gh api "repos/${REPO}/commits/${COMMIT_SHA}" \
      --jq '.files[].filename' 2>&1)
    if [ $? -ne 0 ]; then
      continue
    fi

    OVERLAP=$(comm -12 <(echo "$PR_FILES" | sort) <(echo "$COMMIT_FILES" | sort) 2>/dev/null || echo "")

    if [ -n "$OVERLAP" ]; then
      FOUND_REWORK="yes"
      REWORKED_LIST="${REWORKED_LIST}\n  #${PR_NUM} - ${PR_TITLE}\n    Follow-up: ${COMMIT_SHA:0:7} by @${COMMIT_AUTHOR} (same files: $(echo "$OVERLAP" | head -3 | tr '\n' ', '))"
      break
    fi
  done < <(echo "$FOLLOWUP_COMMITS" | jq -c '.[]')

  if [ -n "$FOUND_REWORK" ]; then
    REWORKED=$((REWORKED + 1))
  fi
done < <(echo "$BOT_PRS" | jq -c '.')

if [ "$TOTAL" -eq 0 ]; then
  RATE="0.0"
else
  RATE=$(awk "BEGIN {printf \"%.1f\", ($REWORKED / $TOTAL) * 100}")
fi

echo ""
echo "Results"
echo "-------"
echo "Agent PRs merged (last ${DAYS} days): ${TOTAL}"
echo "Reworked by humans: ${REWORKED}"
echo "Rework rate: ${RATE}%"
if [ "$SKIPPED" -gt 0 ]; then
  echo "Skipped (API errors): ${SKIPPED}"
fi

if [ -n "$REWORKED_LIST" ]; then
  echo ""
  echo "Reworked PRs:"
  echo -e "$REWORKED_LIST"
fi
