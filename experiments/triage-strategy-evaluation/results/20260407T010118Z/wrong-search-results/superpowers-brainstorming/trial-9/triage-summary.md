# Triage Summary

**Title:** Search returns archived tasks and hides active tasks (regression in v2.3.1)

## Problem
After the v2.3.1 update (~3 days ago), the search function returns archived tasks that should be hidden and fails to return active tasks that exist and are visible via direct browsing. The behavior affects all searches for all users, suggesting the archive/active filter logic in search is inverted.

## Root Cause Hypothesis
The v2.3.1 release likely introduced an inversion in the search query's archive filter — the predicate that should exclude archived tasks is instead selecting only archived tasks (or equivalently, the active-status flag is being negated). This would explain both symptoms simultaneously: archived items appearing and active items disappearing.

## Reproduction Steps
  1. Log in to TaskFlow on v2.3.1
  2. Create or confirm an active task exists (e.g., 'Q2 planning')
  3. Verify the task is visible by browsing the task list directly
  4. Use the search function to search for that task's name
  5. Observe that the active task does not appear in search results
  6. Observe that archived tasks matching the query appear instead

## Environment
TaskFlow v2.3.1 (regression not present in prior version)

## Severity: high

## Impact
All users are affected. Search is effectively unusable — it returns the opposite of what users expect. Users must fall back to manual browsing to find tasks, which does not scale.

## Recommended Fix
Diff the search query/filter logic between v2.3.0 and v2.3.1. Look for an inverted boolean or negated predicate on the archived/active status field in the search index query. Likely a one-character fix (e.g., `!is_archived` vs `is_archived`, or a flipped enum comparison). Also check whether the search index itself needs to be rebuilt if the indexing pipeline was affected.

## Proposed Test Case
Create one active task and one archived task with the same keyword. Execute a search for that keyword. Assert that the active task appears in results and the archived task does not. Add this as a regression test gated on the archive filter logic.

## Information Gaps
- Whether the search index itself is corrupted or just the query filter is inverted (determines if a reindex is needed after the fix)
- Exact v2.3.1 changelog entry that may have touched search or archive logic
