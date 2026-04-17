# Triage Summary

**Title:** Search archive filter is inverted since v2.3.1 — 'active only' shows archived tasks and vice versa

## Problem
The search archive filter is applying the opposite logic: the default 'active only' setting returns archived tasks, while toggling to 'include archived' returns active tasks instead. This affects all users and all searches.

## Root Cause Hypothesis
The v2.3.1 update introduced a boolean inversion in the archive filter logic — likely a negation error or swapped enum/flag values in the search query builder that determines which tasks to include based on the filter state.

## Reproduction Steps
  1. Update to v2.3.1
  2. Open the search bar with default settings (active tasks only)
  3. Search for a term that matches both an archived task and an active task (e.g., 'Q2 planning')
  4. Observe that archived tasks appear and active tasks are missing
  5. Toggle the archive filter to 'include archived'
  6. Observe that now only active tasks appear

## Environment
TaskFlow v2.3.1, confirmed by multiple users (reporter and teammate)

## Severity: high

## Impact
All users are affected. Default search returns stale archived results instead of active tasks, making search effectively unusable without the workaround of manually inverting the toggle. Users with many archived tasks (reporter has ~300) see severe result pollution.

## Recommended Fix
Diff the search filter logic between v2.3.0 and v2.3.1. Look for an inverted boolean, negation error, or swapped condition in the code path that translates the archive filter UI state into the search query predicate. The fix is likely a one-line boolean flip.

## Proposed Test Case
Unit test: given a dataset with both active and archived tasks, assert that a search with the default 'active only' filter returns only active tasks and excludes archived ones, and that the 'include archived' filter returns both. Integration test: verify the filter parameter is correctly passed from the UI toggle through to the query layer.

## Information Gaps
- Exact code path changed in v2.3.1 that touched search or archive filter logic — discoverable from the v2.3.1 changelog or diff
