# Triage Summary

**Title:** Search filter logic inverted since v2.3.1 — 'Active tasks only' returns archived tasks and vice versa

## Problem
The main search bar's task-status filter is returning the opposite of what the label indicates. Selecting 'Active tasks only' surfaces archived tasks, while selecting 'Archived tasks' shows active tasks. This affects all search queries, not just specific terms.

## Root Cause Hypothesis
The v2.3.1 update likely introduced a logic inversion in the search filter predicate — either a boolean flag was flipped, filter enum values were swapped, or a query condition was negated. The filter UI labels are correct, but the underlying query they map to is reversed.

## Reproduction Steps
  1. Log into TaskFlow on version 2.3.1
  2. Navigate to the main search bar at the top of the page
  3. Ensure the filter is set to 'Active tasks only' (the default)
  4. Search for a term that matches both an active task and an archived task (e.g., 'Q2 planning')
  5. Observe that archived tasks appear in results while known active tasks are missing
  6. Switch the filter to 'Archived tasks'
  7. Observe that active tasks now appear in the results

## Environment
TaskFlow v2.3.1, web interface, main search bar. Confirmed across at least two users.

## Severity: high

## Impact
All users relying on the default search experience see incorrect results. Active tasks are effectively hidden and archived tasks are surfaced, degrading core search usability for every search query. Workaround exists (manually select the opposite filter), but users must know about it.

## Recommended Fix
Diff the search filter logic between v2.3.0 and v2.3.1. Look for an inverted boolean, swapped enum mapping, or negated condition in the query that translates the 'Active'/'Archived' filter selection into a database or index query. The fix is likely a one-line inversion.

## Proposed Test Case
Create one active task and one archived task with the same keyword. Search with 'Active tasks only' filter and assert only the active task is returned. Search with 'Archived tasks' filter and assert only the archived task is returned. Run this test against both v2.3.0 (should pass) and v2.3.1 (should fail pre-fix, pass post-fix).

## Information Gaps
- Whether the issue affects API-based search or only the web UI
- Whether the inversion applies to other filter types (e.g., status, assignee) or only the active/archived toggle
- Exact changelog or commit diff for v2.3.1 that touched search or filter logic
