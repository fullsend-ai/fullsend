# Triage Summary

**Title:** Search by task description regressed to 10-15s in v2.3 (title search unaffected)

## Problem
After upgrading from v2.2 to v2.3, searching tasks by description takes 10-15 seconds consistently, regardless of query. Searching by title remains fast. The user has approximately 5,000 tasks accumulated over 2 years of use.

## Root Cause Hypothesis
The v2.3 release likely introduced a regression in description search — most probably a dropped or missing database index on the task descriptions field, a change from indexed full-text search to an unoptimized pattern match (e.g., unindexed LIKE/ILIKE), or a query planner change causing full table scans on descriptions. The fact that title search is unaffected suggests titles are still indexed while descriptions lost their optimization.

## Reproduction Steps
  1. Create or use an account with a large number of tasks (~5,000)
  2. Run TaskFlow v2.3
  3. Perform a search using a term known to exist in a task description
  4. Observe response time of 10-15 seconds
  5. Perform a search using a term known to exist in a task title
  6. Observe that title search returns quickly

## Environment
TaskFlow v2.3 (upgraded from v2.2), work laptop, ~5,000 tasks

## Severity: high

## Impact
Any user with a non-trivial number of tasks who searches by description is affected. Description search is likely a core workflow for power users. Title-only search is a partial workaround but inadequate for users who need to find tasks by content.

## Recommended Fix
1. Diff the search implementation between v2.2 and v2.3 — look for changes to the description search query, ORM calls, or database migrations affecting the tasks table. 2. Check for dropped or missing indexes on the description column. 3. Run EXPLAIN/ANALYZE on the description search query against a dataset of ~5,000 tasks to confirm whether it's doing a sequential scan. 4. Restore or add the appropriate index (full-text index if using FTS, or a GIN/trigram index for pattern matching).

## Proposed Test Case
Performance regression test: seed a database with 5,000+ tasks with varied descriptions, execute a description search query, and assert response time is under 1 second. Run this test against both v2.2 and v2.3 schemas to confirm the regression and validate the fix.

## Information Gaps
- Exact database engine in use (SQLite, PostgreSQL, etc.) — though the fix approach is similar regardless
- Whether any v2.3 database migration explicitly altered the tasks table schema or indexes
- Server-side vs. client-side timing breakdown (is the delay in the query, the API, or rendering)
