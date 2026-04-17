# Triage Summary

**Title:** Search/filter returns inverted results after v2.3.1 — active filter shows archived tasks and vice versa

## Problem
Since the v2.3.1 patch (approximately 3 days ago), the search and filter system returns inverted results. When users filter for active tasks they see archived tasks, and when they filter for archived tasks they see active ones. The underlying task statuses are correct — the inversion is purely in the filter/query layer. This affects both the main search bar and filtered views within projects.

## Root Cause Hypothesis
The v2.3.1 patch most likely introduced a boolean logic inversion in the search filter predicate — e.g., a negated condition (NOT archived vs. archived), a flipped boolean flag, or a swapped enum comparison in the query that maps the 'active'/'archived' filter selection to the database query. Since task detail views show correct statuses, the data layer is intact and only the search/filter query logic is affected.

## Reproduction Steps
  1. Log into TaskFlow as any user who has both active and archived tasks
  2. Use the main search bar to search for a term that matches both an active and an archived task (e.g., 'Q2 planning')
  3. Observe that with the default 'Show active tasks only' filter, archived tasks appear in results
  4. Click into a returned task and confirm its detail view shows 'Archived' status
  5. Switch the filter to 'Show archived tasks' and observe that active tasks appear instead
  6. Click into one of those and confirm its detail view shows 'Active' status

## Environment
TaskFlow v2.3.1 (patch applied ~3 days ago). Affects main search bar and filtered/board views. Reproduced by at least two users (reporter and teammate).

## Severity: high

## Impact
All users performing searches or using filtered views see inverted results. Users with many archived tasks (reporter has 300 archived vs 150 active) are disproportionately affected as their results are flooded with irrelevant archived items. Core search functionality is effectively broken for day-to-day task management.

## Recommended Fix
Inspect the v2.3.1 diff for changes to the search/filter query logic. Look for an inverted boolean condition, flipped enum comparison, or negated WHERE clause in the code that translates the active/archived filter selection into a database or API query. The fix is likely a single-character or single-line logic inversion. Reverting the relevant part of v2.3.1 would also restore correct behavior as a stopgap.

## Proposed Test Case
Create a user with known active and archived tasks. Assert that filtering by 'active' returns only tasks with active status, and filtering by 'archived' returns only tasks with archived status. Verify both via the main search bar and within filtered project views. This test should be added as a regression test gated on the filter logic path.

## Information Gaps
- Exact scope of affected users (appears to be all users, but only two confirmed so far)
- Whether the API endpoint itself returns wrong results or the UI is misinterpreting correct API responses
- Whether other filter dimensions (e.g., priority, assignee) are also inverted or only the active/archived flag
