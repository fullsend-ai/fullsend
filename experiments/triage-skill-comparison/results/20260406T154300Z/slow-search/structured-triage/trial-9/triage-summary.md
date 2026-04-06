# Triage Summary

**Title:** Full-text search on task descriptions extremely slow (~10-15s) with large task count since v2.3 upgrade

## Problem
Searching by keyword across task descriptions takes 10-15 seconds for a user with ~5,000 tasks, many with long descriptions (pasted meeting notes). Title search remains fast. The slowness may have started after upgrading from TaskFlow 2.2 to 2.3 approximately two weeks ago.

## Root Cause Hypothesis
The v2.3 upgrade likely introduced a regression in description search — possibly a missing or dropped full-text index on the task descriptions column, a query plan change, or a switch from indexed search to unindexed LIKE/ILIKE scanning. The fact that title search is still fast suggests titles are indexed but descriptions are not (or no longer are).

## Reproduction Steps
  1. Create or use an account with ~5,000 tasks, some with lengthy descriptions (multi-paragraph text)
  2. Perform a keyword search across all tasks (not filtered to a specific project)
  3. Observe that the search takes 10-15 seconds to return results
  4. Perform the same keyword search but restricted to task titles
  5. Observe that title search returns results quickly

## Environment
Ubuntu 22.04, ThinkPad T14, Firefox (latest stable), TaskFlow v2.3 (upgraded from v2.2 approximately two weeks ago)

## Severity: high

## Impact
Users with large task histories experience unusable search performance on description searches — a core workflow. Likely affects any user with a significant number of tasks and is not isolated to one environment.

## Recommended Fix
Investigate changes to the search query or indexing between v2.2 and v2.3. Check whether a full-text index on the task descriptions column exists and is being used by the query planner. Run EXPLAIN/ANALYZE on the description search query with a large dataset. If the index was dropped or a migration failed to create it, restore it. If the query was changed (e.g., from full-text search to LIKE '%term%'), revert to the indexed approach.

## Proposed Test Case
Performance test: seed a database with 5,000 tasks with realistic-length descriptions. Execute a keyword search across descriptions and assert the query completes in under 2 seconds. Run this test against both v2.2 and v2.3 schemas to confirm the regression and verify the fix.

## Information Gaps
- Exact browser version (not blocking — issue is almost certainly server-side or database-related)
- Whether the slowness started exactly with the v2.3 upgrade or around that time coincidentally
- Server-side logs or query timing to confirm where the bottleneck is (database vs. application layer)
