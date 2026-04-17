# Triage Summary

**Title:** Search archive filter is inverted since v2.3.1 — 'active only' returns archived tasks and vice versa

## Problem
Since the v2.3.1 update (~3 days ago), the search functionality's archive status filter is inverted. Searching with the default 'active tasks only' filter returns archived tasks and omits active ones. Switching to the 'archived' filter shows active tasks instead. The main task list view and individual task detail views are unaffected — the inversion is isolated to search.

## Root Cause Hypothesis
The v2.3.1 release likely introduced a boolean inversion or enum swap in the search-specific filtering logic. Since the main task list uses a different code path for filtering and works correctly, the bug is in the search module's filter application — most likely a negated boolean (e.g., `!isArchived` where `isArchived` was intended), swapped enum/parameter values, or an inverted query predicate in the search index query builder. A diff of search filter logic between v2.3.0 and v2.3.1 should reveal the regression immediately.

## Reproduction Steps
  1. Log in to the web UI as any user with both active and archived tasks
  2. Use the search box to search for a term matching a known active task (e.g., a task created recently)
  3. Observe that with the default 'active tasks only' filter, the active task does not appear but archived tasks matching the query do
  4. Switch the filter to 'archived'
  5. Observe that the active task now appears in results
  6. Confirm that browsing the main task list (without search) shows correct active/archived separation

## Environment
Web UI, v2.3.1. Reproduced by at least two users (reporter and teammate). Reporter has ~150 active and ~300 archived tasks. Archived tasks were manually archived over several months.

## Severity: high

## Impact
All users searching with the archive filter are affected. Search is functionally broken for its primary use case (finding active tasks). Users can work around it by manually inverting the filter, but this is confusing and error-prone. The main task list view is unaffected, so basic task browsing still works.

## Recommended Fix
Diff the search filter/query logic between v2.3.0 and v2.3.1. Look for changes to how the archive status filter parameter is applied in the search query builder — likely a boolean negation, inverted conditional, or swapped enum mapping. Fix the inversion and verify against both the 'active only' and 'archived' filter states.

## Proposed Test Case
Create a test with a mix of active and archived tasks. Execute a search with the 'active only' filter and assert that only active tasks are returned. Execute the same search with the 'archived' filter and assert that only archived tasks are returned. Add this as a regression test gated on the filter parameter to prevent future inversions.

## Information Gaps
- Whether the teammate's experience is identical or subtly different (e.g., different task counts or filter combinations)
- Whether the API endpoint backing search also exhibits the inversion (would confirm it's backend, not frontend)
- Whether any other filters (e.g., by project, priority, assignee) are similarly affected in search
