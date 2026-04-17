# Triage Summary

**Title:** Search filter inverted after v2.3.1 update — 'Active tasks only' returns archived tasks and vice versa

## Problem
After the v2.3.1 update (approximately three days ago), the main search bar's status filter is inverted. Selecting 'Active tasks only' returns archived tasks, and selecting 'Archived tasks' returns active tasks. Task status labels on individual results are correct — only the filter/query logic is reversed.

## Root Cause Hypothesis
The database migration included in v2.3.1 likely inverted the boolean or enum value representing task archived/active status (e.g., flipped 0/1, swapped enum labels, or negated an `is_archived` flag). Alternatively, the migration may have changed the column semantics (e.g., from `is_archived` to `is_active`) without updating the search query predicate to match.

## Reproduction Steps
  1. Log into TaskFlow on a workspace that has received the v2.3.1 update
  2. Ensure there are both active and archived tasks (reporter has ~150 active, ~300 archived)
  3. Use the main search bar on the dashboard with the default 'Active tasks only' filter
  4. Search for a known term (e.g., 'Q2 planning') that matches both an active and an archived task
  5. Observe that archived tasks appear in results while the matching active task is missing
  6. Switch the filter to 'Archived tasks' and observe that active tasks now appear instead

## Environment
TaskFlow v2.3.1 (post-update, with database migration applied). Reproduced by at least two users on the same workspace. Main dashboard search bar with default filters.

## Severity: high

## Impact
All users on workspaces running v2.3.1 are affected. Search is functionally unusable for finding current work — users with large task histories (e.g., 300 archived vs 150 active) have results dominated by irrelevant archived items. Core workflow (finding and acting on active tasks) is broken.

## Recommended Fix
1. Inspect the v2.3.1 database migration script for any changes to the task status/archived column (boolean flip, enum remapping, or column rename). 2. Check the search query or ORM filter that interprets the 'Active tasks only' / 'Archived tasks' filter value — verify the predicate matches the post-migration column semantics. 3. If the migration flipped stored values, either write a corrective migration to flip them back, or update the query predicate to match the new semantics. 4. Verify fix against both filter directions and confirm counts match expectations.

## Proposed Test Case
Create a workspace with known active and archived tasks. Apply the v2.3.1 migration. Assert that searching with the 'Active tasks only' filter returns exactly the active tasks (and zero archived), and that searching with the 'Archived tasks' filter returns exactly the archived tasks (and zero active). Regression test should cover both filter directions.

## Information Gaps
- Whether the API search endpoint exhibits the same inversion (vs. only the UI)
- Exact migration script contents and which column/table was modified
- Whether all workspaces on v2.3.1 are affected or only those with pre-existing archived tasks at migration time
