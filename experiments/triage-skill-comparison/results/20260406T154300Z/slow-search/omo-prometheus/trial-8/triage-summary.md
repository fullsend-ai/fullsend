# Triage Summary

**Title:** Search across task descriptions regressed to 10-15s after v2.3 upgrade (title search unaffected)

## Problem
After upgrading from v2.2 to v2.3 approximately two weeks ago, searching via the quick-search bar takes 10-15 seconds when the search hits task descriptions. Searching by task title remains fast. The user has ~5,000 tasks accumulated over 2 years.

## Root Cause Hypothesis
The v2.3 release likely changed how description search is executed — either a full-text index on the descriptions column was dropped/not migrated, the query was changed from an indexed lookup to an unindexed LIKE/ILIKE scan, or a new full-text search feature was introduced without proper indexing. The fact that title search is unaffected confirms the regression is isolated to the description search path.

## Reproduction Steps
  1. Have a TaskFlow instance with ~5,000 tasks (descriptions populated)
  2. Upgrade from v2.2 to v2.3
  3. Use the quick-search bar at the top of the UI
  4. Enter a term that would match task descriptions but not titles
  5. Observe 10-15 second response time
  6. Compare: search for a term that matches a task title — this returns quickly

## Environment
TaskFlow v2.3 (upgraded from v2.2), work laptop (OS/specs unknown but not likely relevant given this is a query-side regression), ~5,000 tasks

## Severity: high

## Impact
Any user with a non-trivial number of tasks who searches by description content will experience severe slowdowns after upgrading to v2.3. This degrades a core workflow — search is a primary navigation mechanism in a task management app.

## Recommended Fix
1. Diff the search query path between v2.2 and v2.3, focusing on how description search is executed. 2. Check the database migration scripts in v2.3 for any dropped or altered indexes on the task descriptions column. 3. Run EXPLAIN/EXPLAIN ANALYZE on the description search query against a dataset of ~5,000 tasks to confirm whether it's doing a sequential scan. 4. Restore or add the appropriate index (likely a full-text/GIN index on descriptions). 5. If v2.3 intentionally changed the search approach (e.g., added fuzzy matching), ensure proper indexing supports the new query pattern.

## Proposed Test Case
Create a test dataset with 5,000+ tasks with populated descriptions. Benchmark the quick-search bar query for a term matching only descriptions (not titles). Assert response time is under 1 second. Run this test against both v2.2 and v2.3 schemas to confirm the regression and validate the fix.

## Information Gaps
- Exact database engine and version (PostgreSQL, SQLite, etc.)
- Whether the v2.3 changelog mentions any search-related changes
- Server-side vs. client-side — whether the 10-15s is network/API response time or UI rendering time (likely server-side given the title vs. description split)
