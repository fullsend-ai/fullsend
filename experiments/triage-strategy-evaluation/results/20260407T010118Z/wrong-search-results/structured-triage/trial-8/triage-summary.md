# Triage Summary

**Title:** Search filter inverted: 'Active tasks only' returns archived tasks and vice versa since v2.3.1

## Problem
The search status filter on the main dashboard search bar is inverted. Searching with the default 'Active tasks only' filter returns archived tasks, while switching to the 'Archived tasks' filter shows active tasks. Individual task detail pages still show the correct status — the inversion is limited to the search/filter layer.

## Root Cause Hypothesis
The v2.3.1 database patch (~3 days ago) likely swapped or inverted the status flag values or filter predicate used by the search index. Possible causes: (1) a migration that flipped the boolean/enum mapping for active vs. archived status, (2) a query predicate change that negated the filter condition (e.g., `= 'archived'` where it should be `= 'active'`), or (3) a search index rebuild that applied the wrong status labels.

## Reproduction Steps
  1. Log into TaskFlow v2.3.1
  2. Navigate to the main dashboard
  3. Use the search bar at the top, leaving the default 'Active tasks only' filter in place
  4. Type a query for a known active task (e.g., 'Q2 planning') and press Enter
  5. Observe that results show archived tasks instead of active ones
  6. Switch the filter to 'Archived tasks' and re-run the same search
  7. Observe that active tasks now appear under the archived filter

## Environment
TaskFlow v2.3.1 (updated ~3 days ago with a database patch). Reproduced on Chrome ~124-125/Windows 11 and Safari/macOS Sonoma. Cross-browser reproduction confirms server-side issue.

## Severity: high

## Impact
All users are affected. Search is effectively unusable under default settings — users cannot find their active tasks without manually switching to the 'Archived' filter, which is counterintuitive. Reporter has ~150 active and ~300 archived tasks; confirmed by at least two users on different platforms.

## Recommended Fix
Investigate the v2.3.1 database migration for changes to the task status field or search index. Check whether the filter predicate in the search query was inverted (e.g., a negated condition or swapped enum values). Look at the search service's filter-to-query mapping for the 'Active tasks only' and 'Archived tasks' options. Since task detail pages show correct status, the underlying data is likely correct and the fix is in the search/filter layer, not a data migration rollback.

## Proposed Test Case
Create one active task and one archived task with similar titles. Search with 'Active tasks only' filter and assert only the active task is returned. Search with 'Archived tasks' filter and assert only the archived task is returned. Run this test against both the search API endpoint and the UI.

## Information Gaps
- Exact v2.3.1 changelog / migration script details (available internally)
- Whether the issue affects API search responses or only the UI filter layer
- Whether tasks created after the v2.3.1 update exhibit the same inversion or only pre-existing tasks
