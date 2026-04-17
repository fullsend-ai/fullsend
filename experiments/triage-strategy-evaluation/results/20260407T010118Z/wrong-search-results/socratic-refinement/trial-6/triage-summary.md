# Triage Summary

**Title:** Search filter logic inverted in v2.3.1 — archived tasks shown as active and vice versa

## Problem
Since the v2.3.1 update (~3 days ago), the search results filter is inverted: the default/active view returns archived tasks, and the 'show archived only' filter returns active tasks. Active tasks are completely absent from default search results, not merely deprioritized.

## Root Cause Hypothesis
The v2.3.1 update introduced an inversion in the search filter predicate — likely a flipped boolean condition, negated flag, or swapped enum values in the query that partitions active vs. archived tasks. The underlying task data and status fields are correct (tasks display normally outside search), so this is purely a query/filter-layer bug.

## Reproduction Steps
  1. Create a new task with a distinctive title (e.g., 'filter test active') and leave it in active status
  2. Archive a different task with a related title (e.g., 'filter test archived')
  3. Open search and query for 'filter test' with the default (active) filter
  4. Observe that only the archived task appears in results
  5. Switch the search filter to 'show archived only'
  6. Observe that the active task appears there instead

## Environment
TaskFlow v2.3.1. Reproduced by at least two users (reporter and teammate). Reporter has ~300 archived tasks and ~150 active tasks. Not browser- or OS-specific based on multi-user reproduction.

## Severity: high

## Impact
All users are affected. Search is functionally broken — users cannot find active tasks through search at all, which is a core workflow. Users with large archives (like the reporter) are disproportionately impacted since results are dominated by irrelevant archived items.

## Recommended Fix
Diff the search/filter logic between v2.3.0 and v2.3.1. Look for an inverted boolean, negated condition, or swapped enum/constant in the query that filters tasks by archived status. The fix is almost certainly a one-line predicate flip. Verify that both the default filter and the 'show archived only' filter use the correct status values.

## Proposed Test Case
Unit test the search filter: given a set of tasks with known active/archived statuses, assert that querying with the 'active' filter returns only active tasks and querying with the 'archived' filter returns only archived tasks. Add a regression test that specifically checks a newly created active task appears in default search results.

## Information Gaps
- Whether the API/backend returns the wrong results or whether a frontend filter is inverting correct backend results (not critical for initial investigation — the developer can determine this from the v2.3.1 diff)
- Whether other filter types (e.g., by priority, assignee) are also affected or only the archived/active partition
