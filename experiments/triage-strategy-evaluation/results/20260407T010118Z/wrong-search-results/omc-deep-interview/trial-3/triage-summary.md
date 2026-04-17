# Triage Summary

**Title:** Search filter predicate is inverted — 'Active tasks only' returns archived tasks and vice versa

## Problem
The search filter on the main dashboard search bar is applying the opposite filter logic. When set to 'Active tasks only' (the default), results contain archived tasks and exclude active ones. When switched to 'Archived tasks', active tasks appear instead. This affects all searches, not just specific queries.

## Root Cause Hypothesis
The filter predicate for task status is inverted — most likely a boolean negation error or swapped enum/flag values in the search query builder. The 'active' filter value is being mapped to the 'archived' status (or vice versa), causing the WHERE clause or equivalent filter to select the wrong set of tasks.

## Reproduction Steps
  1. Log into TaskFlow and navigate to the main dashboard
  2. Ensure the search filter is set to 'Active tasks only' (the default)
  3. Create a new task with a distinctive name (e.g., 'filter-test-active')
  4. Archive an older task or confirm one exists in archived state
  5. Search for the distinctive name in the main search bar
  6. Observe that the active task does NOT appear in results
  7. Switch the filter to 'Archived tasks'
  8. Observe that the active task now appears in the archived filter results

## Environment
Main dashboard search bar with default settings. No custom sort or filter modifications. Affects multiple users (reporter and at least one teammate).

## Severity: high

## Impact
All users relying on the default search experience see incorrect results. Active tasks are effectively hidden from search unless users manually switch to the 'Archived' filter, which is counterintuitive. This undermines core search functionality and could cause users to miss important active tasks or act on stale archived ones.

## Recommended Fix
Inspect the search query builder or filter mapping layer where the 'Active tasks only' and 'Archived tasks' filter values are translated into the underlying query predicate. Look for an inverted boolean (e.g., `is_archived` used where `!is_archived` was intended), swapped enum mappings, or a recent commit that flipped the filter logic. Check the filter-to-query mapping for all status filter options.

## Proposed Test Case
Create both an active and an archived task with the same keyword. Search with 'Active tasks only' filter and assert only the active task is returned. Search with 'Archived tasks' filter and assert only the archived task is returned. Add this as a regression test for the search filter pipeline.

## Information Gaps
- Exact date or deployment when the behavior started (useful for git bisect but not required to find the bug)
- Whether the issue affects API-based search or only the dashboard UI
- Whether other filter options beyond Active/Archived are also inverted
