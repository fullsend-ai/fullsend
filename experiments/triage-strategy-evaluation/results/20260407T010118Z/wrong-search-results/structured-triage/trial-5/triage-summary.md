# Triage Summary

**Title:** Search filter logic inverted: active filter returns archived tasks and vice versa (since v2.3.1)

## Problem
After updating to TaskFlow 2.3.1, the search results filter is inverted. The default 'active tasks' filter returns archived tasks, and manually switching to the 'archived' filter returns active tasks. This affects all searches for all users.

## Root Cause Hypothesis
The boolean or enum value controlling the active/archived filter predicate was likely inverted in the v2.3.1 update — either a flipped condition in the query logic, a reversed mapping between UI filter labels and backend query parameters, or a migration that swapped the status flag values.

## Reproduction Steps
  1. Log in to TaskFlow v2.3.1
  2. Use the main search bar at the top of the page
  3. Type a search term for a task that exists in both active and archived states (e.g., 'Q2 planning')
  4. Hit Enter with default filters (active tasks only)
  5. Observe that archived tasks appear in results and known active tasks are missing
  6. Switch the filter to show archived tasks
  7. Observe that active tasks now appear in results instead

## Environment
TaskFlow v2.3.1 (issue began after this update). Reproduced on Chrome (latest)/Windows 11 and Safari/macOS. Likely platform-independent (backend issue).

## Severity: high

## Impact
All users are affected. Search is effectively unusable for finding active work — the core use case. Users must manually invert their filter selection as a workaround, which is confusing and error-prone.

## Recommended Fix
Investigate the search filter logic introduced or modified in v2.3.1. Look for an inverted boolean condition, swapped enum values, or a negation error in the query that maps the active/archived UI filter to the backend task status. Check the diff between v2.3.0 and v2.3.1 for changes to the search query builder or task status filtering. A likely one-line fix (flipping a condition or removing an erroneous negation).

## Proposed Test Case
Create one active task and one archived task with the same keyword. Search using default filters (active only) and assert the active task is returned and the archived task is not. Then switch to the archived filter and assert the archived task is returned and the active task is not.

## Information Gaps
- Exact commit or changeset in v2.3.1 that touched search or filter logic (requires codebase investigation)
- Whether the inversion also affects non-search filtered views (e.g., project board filters)
