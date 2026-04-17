# Triage Summary

**Title:** Search results inverted after v2.3.1: archived tasks shown, active tasks hidden

## Problem
Since the v2.3.1 update (approximately 3 days ago), search returns archived tasks instead of active ones. Active tasks that exist and are accessible via direct browsing do not appear in search results, while archived tasks (correctly displayed as archived/grayed out) appear instead. The issue affects all searches for all users — at least two users have confirmed the behavior.

## Root Cause Hypothesis
The v2.3.1 update likely introduced an inverted filter condition in the search query logic. The archived/active status filter appears to be negated — including archived tasks and excluding active ones, when it should do the opposite. Since archived tasks render correctly with their archived styling, the data model and display layer are unaffected; only the search index query or filter predicate is wrong.

## Reproduction Steps
  1. Log in to TaskFlow on a version running v2.3.1
  2. Create or confirm an active task exists with a known keyword (e.g., 'Q2 planning')
  3. Confirm an archived task also exists with the same or similar keyword
  4. Use the search function to search for that keyword
  5. Observe that the archived task appears in results (grayed out, tagged as Archived) while the active task is absent
  6. Navigate to the task list directly and confirm the active task exists and is accessible

## Environment
TaskFlow v2.3.1 (post-patch update). Multiple users affected across at least one team. No user-side configuration or workflow changes preceded the issue.

## Severity: high

## Impact
Search is effectively broken for all users — the primary discovery mechanism returns wrong results. Users cannot find active tasks via search and must resort to manual browsing. This degrades productivity for any team relying on search for task management.

## Recommended Fix
Diff the search query/filter logic between v2.3.0 and v2.3.1. Look for an inverted boolean condition, negated predicate, or flipped enum comparison on the archived/active status field in the search index query. Likely candidates: a `!is_archived` that became `is_archived`, a filter direction reversal, or a migration that flipped the meaning of a status flag in the search index without updating the query layer.

## Proposed Test Case
Given a dataset with both active and archived tasks sharing a common keyword: when searching for that keyword with default filters, assert that only active tasks appear in results and no archived tasks are included. Additionally, regression-test that explicitly filtering for archived tasks still works correctly.

## Information Gaps
- Exact search interface used (web, mobile, API) — unlikely to matter if the bug is in a shared backend query layer
- Whether any specific search filter combinations produce correct results (e.g., explicitly selecting 'all statuses')
- Whether the search index was rebuilt during the v2.3.1 update or if this is purely a query-side issue
