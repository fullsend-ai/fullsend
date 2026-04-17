# Triage Summary

**Title:** Search active/archived filter is inverted — 'active only' returns archived tasks and vice versa

## Problem
The search filter for active vs. archived tasks is producing inverted results. When set to 'active tasks only' (the default), search returns archived tasks and omits active ones. When toggled to show archived tasks, it instead shows active tasks. This regression started approximately one week ago and affects all users.

## Root Cause Hypothesis
A recent code change (within the last week) inverted the boolean logic or enum mapping for the active/archived filter in the search query. The filter predicate is likely negated or the filter values are swapped, causing the query to select the opposite task state from what the UI requests.

## Reproduction Steps
  1. Create or identify an active task with a known title (e.g., 'Q2 planning')
  2. Ensure an archived task also exists with a similar or matching title
  3. Open the search interface with the default filter ('active tasks only')
  4. Search for the task title
  5. Observe that archived tasks appear in results and the active task does not
  6. Toggle the filter to 'show archived tasks'
  7. Observe that active tasks now appear instead of archived ones

## Environment
Affects all users; not environment-specific. Regression introduced approximately one week ago.

## Severity: high

## Impact
All users searching for tasks see incorrect results by default. Active tasks are effectively hidden, and archived tasks surface unexpectedly. This undermines core search functionality and could cause users to miss important active work.

## Recommended Fix
Review commits from the past week that touched the search filter logic, particularly any changes to how the active/archived filter value is passed to the search query or how the query predicate is constructed. Look for an inverted boolean, a swapped enum/constant, or a negated condition. Fix the filter so 'active only' correctly excludes archived tasks.

## Proposed Test Case
Given a dataset with both active and archived tasks sharing a search term: (1) verify that searching with the default 'active only' filter returns only active tasks, (2) verify that toggling to 'show archived' returns archived tasks, and (3) verify that the filter state in the UI matches the actual query parameter sent to the backend.

## Information Gaps
- Exact date or deploy that introduced the regression (reporter said 'last week')
- Whether the issue affects all search entry points (global search, project-scoped search, API) or only the main UI
