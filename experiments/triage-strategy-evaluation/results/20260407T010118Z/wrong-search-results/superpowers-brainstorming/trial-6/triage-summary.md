# Triage Summary

**Title:** Search archive filter is inverted — 'active' filter shows archived tasks and vice versa

## Problem
The search archive filter applies the opposite predicate. When set to show only active tasks (the default), search results return archived tasks instead. When manually toggled to show archived tasks, active tasks appear. This affects all searches for all users.

## Root Cause Hypothesis
The boolean condition on the archive filter is inverted. The filter predicate likely checks something like `task.archived === true` when it should check `task.archived === false` for the 'active only' setting (or a NOT is missing, or an enum comparison is swapped). This could be in the query layer, the API filter parameter, or the frontend filter construction.

## Reproduction Steps
  1. Log in as any user with both active and archived tasks
  2. Ensure the archive filter is set to the default ('active tasks only')
  3. Search for a term that matches both an active task and an archived task (e.g., 'Q2 planning')
  4. Observe that archived tasks appear and matching active tasks do not
  5. Toggle the archive filter to 'show archived'
  6. Observe that active tasks now appear in results instead

## Environment
Affects all users; reported by at least two accounts. Not environment-specific — this is a logic bug.

## Severity: high

## Impact
All users' default search experience is broken. Active tasks are effectively unsearchable without manually inverting the filter. Users with many archived tasks (reporter has 300 archived vs 150 active) are disproportionately affected as results are flooded with irrelevant archived items.

## Recommended Fix
Find the archive filter predicate in the search query path and invert the boolean condition. Check both the API/query layer and the frontend filter construction — the inversion could be in either place. Search the codebase for the archive filter parameter and trace how its value maps to the database/index query predicate.

## Proposed Test Case
Create a test with both active and archived tasks matching the same search term. Assert that with the default filter (active only), only the active task is returned. Assert that with the filter set to 'archived', only the archived task is returned. Assert that with 'all', both appear.

## Information Gaps
- Exact location of the inversion (frontend filter construction vs API query layer vs database query) — but this is for the developer to determine during investigation, not something the reporter would know
