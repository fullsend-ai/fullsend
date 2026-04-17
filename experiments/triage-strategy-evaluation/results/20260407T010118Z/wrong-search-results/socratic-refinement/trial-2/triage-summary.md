# Triage Summary

**Title:** Search filter inverted since v2.3.1 — 'active tasks only' returns archived tasks and vice versa

## Problem
Since the v2.3.1 patch (approximately three days ago), the search filter for task status is inverted. When 'active tasks only' is selected (the default), archived tasks are returned instead. When the filter is switched to show archived tasks, active tasks appear. The underlying task status data is correct — individual tasks display their correct status — so the bug is isolated to the search/filter query logic.

## Root Cause Hypothesis
The v2.3.1 patch likely introduced a logic inversion in the search filter predicate — either a negated boolean condition, a swapped enum mapping, or an inverted comparison when translating the UI filter selection into the search query. A diff of the search query construction between v2.3.0 and v2.3.1 should reveal the change.

## Reproduction Steps
  1. Update to v2.3.1 (or use any current instance)
  2. Create or confirm the existence of an active task with a known title (e.g., 'Q2 planning')
  3. Open search with the default filter ('active tasks only')
  4. Search for the known active task's title
  5. Observe that the active task is missing and archived tasks with matching terms appear instead
  6. Switch the filter to 'archived tasks' and repeat the search
  7. Observe that the active task now appears in the archived results

## Environment
Affects all users on v2.3.1. Confirmed by reporter and at least one teammate. Not account-specific.

## Severity: high

## Impact
All users relying on search (which uses 'active tasks only' as the default filter) see incorrect results. Active tasks are effectively hidden by default, and archived tasks pollute results. This breaks a core workflow for anyone who uses search to find their current tasks.

## Recommended Fix
Diff the search filter/query logic between v2.3.0 and v2.3.1. Look for an inverted boolean, negated condition, or swapped enum value in the code that translates the UI's active/archived filter into the database or search index query. The fix is likely a one-line logic inversion correction.

## Proposed Test Case
Create both an active and an archived task with the same keyword. Search with the 'active tasks only' filter and assert only the active task is returned. Search with the 'archived tasks' filter and assert only the archived task is returned. Run this test against both v2.3.0 (should pass) and v2.3.1 (should fail pre-fix, pass post-fix).

## Information Gaps
- Exact code change in v2.3.1 that introduced the inversion (requires codebase inspection, not reporter input)
- Whether the bug also affects API-level search or only the UI (tangential to fixing the core issue)
