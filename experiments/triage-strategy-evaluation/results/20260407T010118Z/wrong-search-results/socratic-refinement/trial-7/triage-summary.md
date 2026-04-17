# Triage Summary

**Title:** Search active/archived filter inverted since v2.3.1 — active filter returns archived tasks and vice versa

## Problem
Since the v2.3.1 update (~3 days ago), the search/filter functionality inverts the active/archived status filter. Selecting 'show active tasks' returns archived tasks, and selecting 'show archived tasks' returns active ones. All other filters (priority, assignee, due date) work correctly. Task data itself is intact — viewing a task directly shows the correct status.

## Root Cause Hypothesis
The v2.3.1 release most likely introduced a boolean inversion in the search query predicate for archive status. This could be a flipped condition (e.g., `is_archived` vs `!is_archived`), a swapped enum mapping, or an inverted filter parameter. The fact that the data is correct on direct access and only the filter is affected confirms this is a query/filter-layer bug, not a data corruption issue.

## Reproduction Steps
  1. Have at least one active task and one archived task in the account
  2. Use the search/filter interface with default filters (show active tasks)
  3. Observe that archived tasks appear in results instead of active ones
  4. Switch the filter to show archived tasks
  5. Observe that active tasks appear in results instead of archived ones
  6. Open any task directly and confirm its status label is correct

## Environment
TaskFlow v2.3.1. Reproduced by at least two users (reporter and teammate). Not browser- or OS-specific based on multi-user reproduction.

## Severity: high

## Impact
All users relying on search or filtering by archive status see inverted results. For users with many tasks (reporter has ~450), search is the primary navigation method, making the application effectively unusable for daily task management. Workaround exists (manually invert the filter) but is confusing and error-prone.

## Recommended Fix
Diff the search/filter query logic between v2.3.0 and v2.3.1, focusing on the archive status predicate. Look for an inverted boolean condition, swapped enum values, or a negation error in the query builder for the active/archived filter. The fix is almost certainly a one-line boolean flip.

## Proposed Test Case
Create tasks with both active and archived statuses. Assert that filtering for 'active' returns only active tasks and filtering for 'archived' returns only archived tasks. This should be a regression test gating future releases.

## Information Gaps
- Exact code change in v2.3.1 that introduced the inversion (requires codebase access, not reporter knowledge)
- Whether the bug manifests in API responses as well as the UI (would not change the fix direction)
