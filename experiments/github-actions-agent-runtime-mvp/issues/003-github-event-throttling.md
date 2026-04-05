# 003: GitHub Event Throttling After ~20 Reviews

## Problem

After ~20 `CHANGES_REQUESTED` reviews from the same bot on a single PR, the `pull_request_review` event stops triggering the fix agent workflow. The review is posted successfully but no workflow run is created.

## Root Cause

GitHub's secondary rate limits or anti-abuse detection suppresses event delivery. Content-generating requests cost 5 points toward a 900 points/min limit. Combined with other API calls per review cycle, the bot may exhaust the budget or trigger anomaly detection.

## Observed

- Reviews 1-18 on [PR #3](https://github.com/nonflux/integration-service/pull/3): reliably triggered fix agent
- Reviews 19-22: event not delivered, no fix agent run created

## Proposed Fix

Replace event-driven trigger with explicit `workflow_dispatch` API call from review agent after submitting the review. `workflow_dispatch` bypasses event delivery entirely and can target any branch.

Keep `pull_request_review` trigger as fallback for manual reviews.
