# 005: Review Verdict Entries Accumulate in PR Timeline

## Problem

Each `gh pr review --request-changes` or `--approve` creates a separate review entry in the PR timeline that cannot be edited or collapsed. Over multiple review/fix cycles, these entries clutter the timeline. The actual review content lives in a single in-place comment, but the verdict entries add noise.

## Root Cause

GitHub's review API always creates a new review entry. There is no way to update an existing review's state — only dismiss it.

## Proposed Fix

Dismiss previous reviews before posting a new one:

```bash
PREV_REVIEW_ID=$(gh api repos/$REPO/pulls/$PR/reviews \
  --jq '[.[] | select(.user.login == "fullsend-reviewer[bot]")] | last | .id')
if [ -n "$PREV_REVIEW_ID" ] && [ "$PREV_REVIEW_ID" != "null" ]; then
  gh api "repos/$REPO/pulls/$PR/reviews/$PREV_REVIEW_ID/dismissals" \
    -f message="Superseded by new review" || true
fi
```
