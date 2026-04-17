# Triage Summary

**Title:** Search filter for active/archived tasks is inverted — active filter returns archived tasks and vice versa

## Problem
The search task-status filter is applying the opposite predicate. When a user selects the default 'active tasks' filter, search returns archived tasks instead. When they switch the filter to 'archived tasks', they see their active tasks. The individual task detail view correctly displays each task's actual status, confirming the underlying data is correct — the inversion is isolated to the search/filter layer.

## Root Cause Hypothesis
The search query or filter predicate for the active/archived status flag is inverted. Most likely a boolean negation error or swapped enum values in the search filter logic — e.g., the filter passes `archived = true` when the user selects 'active', or the mapping between the UI filter option and the query parameter is reversed.

## Reproduction Steps
  1. Create or identify a task that is in 'active' status
  2. Go to the search/task list view with the default filter set to 'Active tasks'
  3. Search for the task by name — it will not appear in results
  4. Switch the filter to 'Archived tasks'
  5. The active task now appears in the archived filter results
  6. Click into the task — its detail view correctly shows it as 'active'

## Environment
Affects all users (confirmed by reporter and at least one teammate). Not browser- or account-specific. The default 'active tasks' filter is the affected path.

## Severity: high

## Impact
All users searching for tasks see inverted results by default. Active tasks are effectively hidden from normal search, and archived tasks pollute results. This undermines core usability of the task search feature for everyone.

## Recommended Fix
Investigate the search filter logic where the active/archived status predicate is constructed. Look for an inverted boolean or swapped enum mapping between the UI filter selection and the database/search query. Likely a one-line fix — either a negation that shouldn't be there, or two enum values that are swapped in a mapping. Check recent changes to the search filter or task status model for a regression.

## Proposed Test Case
Create one active task and one archived task with the same keyword in their titles. Search with the 'active' filter — assert only the active task is returned. Search with the 'archived' filter — assert only the archived task is returned. Verify the fix with both filter states.

## Information Gaps
- When this behavior started (recent deploy regression vs. longstanding bug) — useful for git bisect but not required for the fix
- Whether the inversion also affects other filter types (e.g., by priority, by assignee) or is isolated to the active/archived filter
