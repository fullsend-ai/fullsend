# Triage Summary

**Title:** Search active/archived filter inverted after v2.3.1 database migration

## Problem
After the v2.3.1 update (which included a database migration), the search filter for task status is inverted: filtering for 'active' tasks returns archived tasks, and filtering for 'archived' tasks returns active ones. Individual task detail views show the correct status, indicating the underlying task data is intact but the search index or filter predicate is reversed.

## Root Cause Hypothesis
The v2.3.1 database migration likely inverted a boolean/enum value in the search index (e.g., flipping an is_archived flag, swapping status enum values, or inverting the filter predicate logic). Since individual task views read from the primary data store and show correct status, the problem is isolated to the search layer — either the search index was rebuilt with inverted status values, or the filter query logic was changed to negate the condition.

## Reproduction Steps
  1. Update to v2.3.1 (ensure the database migration has run)
  2. Navigate to the main Tasks view
  3. Ensure the default 'Active tasks only' filter is selected
  4. Search for a term that matches both active and archived tasks (e.g., 'Q2 planning')
  5. Observe that archived tasks appear in results while known active tasks are missing
  6. Switch the filter to 'Archived tasks' and observe that active tasks now appear instead

## Environment
TaskFlow v2.3.1, post-database-migration. Affects multiple users on the same team. Observed from the main Tasks view using default search filters.

## Severity: high

## Impact
All users on v2.3.1 are affected. Search — a core workflow feature — returns incorrect results, making it unreliable for finding active work. Users with large task counts (e.g., 150 active / 300 archived) see dramatically wrong result sets, undermining trust in the tool.

## Recommended Fix
Examine the v2.3.1 database migration for changes to the task status field or search index. Specifically check: (1) whether a boolean flag like is_archived or is_active was inverted during migration, (2) whether status enum values were remapped incorrectly, (3) whether the search index was rebuilt with inverted status logic. If the search index is separate from the primary data store, a reindex with corrected status mapping may suffice. Also review the search filter query to confirm the predicate matches the intended semantics (e.g., WHERE is_archived = false for active tasks).

## Proposed Test Case
Create tasks in both active and archived states. Perform a search with the 'Active tasks only' filter and assert that only active tasks appear. Repeat with the 'Archived tasks' filter and assert only archived tasks appear. Run this test both before and after the v2.3.1 migration to verify correctness.

## Information Gaps
- Exact details of what the v2.3.1 database migration changed (schema diff)
- Whether the search uses a separate index (e.g., Elasticsearch) or queries the primary database directly
- Whether the issue affects all teams/organizations or only this team's data partition
