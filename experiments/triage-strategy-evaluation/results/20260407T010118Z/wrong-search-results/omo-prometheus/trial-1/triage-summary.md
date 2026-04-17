# Triage Summary

**Title:** Search status filter is inverted since v2.3.1 — 'active only' returns archived tasks and vice versa

## Problem
After the v2.3.1 update, the search status filter produces inverted results. Setting the filter to 'active only' returns archived tasks while hiding active ones, and setting it to 'archived' returns active tasks. Unfiltered search returns all tasks correctly, and task records themselves have correct status values. Multiple users are affected.

## Root Cause Hypothesis
The v2.3.1 update introduced an inverted boolean or swapped enum mapping in the search filter logic. Since unfiltered results are correct and tasks have correct status in the database, the issue is specifically in how the filter predicate is applied to search queries — likely a negated condition, swapped enum values, or an inverted comparison in the search indexing or query layer.

## Reproduction Steps
  1. Update to v2.3.1
  2. Create or confirm an active task exists (e.g., 'Q2 planning')
  3. Open search and set filter to 'active tasks only'
  4. Search for the task — observe that archived tasks appear but the active task does not
  5. Switch filter to 'archived' — observe that the active task now appears
  6. Remove filter entirely — observe that all tasks appear correctly

## Environment
TaskFlow v2.3.1 (regression from v2.3.0). Affects multiple users in the same workspace. Default 'active only' filter is the most common search mode, maximizing blast radius.

## Severity: high

## Impact
All users searching with status filters get wrong results. Since the default filter is 'active only,' every standard search is broken — users see stale archived tasks and miss current active work. Workaround exists (remove filter or mentally invert it), but it is unintuitive and error-prone.

## Recommended Fix
Diff the search filter logic between v2.3.0 and v2.3.1. Look for inverted boolean conditions, swapped enum/constant values, or negated predicates in the code path that translates the UI filter selection into a search query. Likely candidates: a flipped `is_archived` flag, a swapped `status == 'active'` vs `status == 'archived'` comparison, or an inverted index mapping introduced during the update.

## Proposed Test Case
Create one active task and one archived task with the same keyword. Search with filter set to 'active only' and assert only the active task is returned. Search with filter set to 'archived' and assert only the archived task is returned. Search with no filter and assert both are returned. Run this test against both v2.3.0 (baseline) and v2.3.1 (regression).

## Information Gaps
- Whether other search filters (e.g., by priority, assignee, date) are also affected or only the status filter
- Whether the inversion occurs at the API/query layer or the UI layer (though either way the fix path starts with the v2.3.1 diff)
- Whether the search index itself has inverted status values or the query predicate is inverted at query time
