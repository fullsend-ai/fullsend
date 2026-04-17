# Triage Summary

**Title:** Description search regression in v2.3: full-text search on task descriptions ~100x slower than expected (~5,000 tasks)

## Problem
After upgrading from TaskFlow v2.2 to v2.3, searching for terms that appear in task descriptions takes 10-15 seconds. Title-based search remains fast. The slowdown is uniform regardless of how many tasks match the query, and is accompanied by a CPU spike. The user has ~5,000 tasks, some with very long descriptions (pasted meeting notes).

## Root Cause Hypothesis
The v2.3 update likely broke or removed the full-text index on the task description field, causing description searches to perform a sequential scan across all 5,000 task descriptions on every query. Title search is unaffected because its index was preserved. The CPU spike and uniform latency regardless of result count are consistent with a full-scan rather than an index lookup.

## Reproduction Steps
  1. Have a workspace with ~5,000 tasks, some with lengthy descriptions
  2. Upgrade from TaskFlow v2.2 to v2.3
  3. Use the search bar to search for a word that appears in a task description (not just the title)
  4. Observe 10-15 second delay and CPU spike
  5. Compare with a title-only search term, which returns quickly

## Environment
TaskFlow v2.3 (upgraded from v2.2), ~5,000 tasks with some long descriptions, Lenovo ThinkPad T14 with 32GB RAM, work laptop

## Severity: high

## Impact
Any user with a non-trivial number of tasks experiences severely degraded description search after upgrading to v2.3. Title search still works as a partial workaround, but description search is a core feature. Users who store detailed notes in task descriptions (a common workflow) are most affected.

## Recommended Fix
1. Diff the search/query implementation between v2.2 and v2.3 — look for changes to how description fields are queried (removed index, changed query strategy, new ORM behavior, etc.). 2. Check the database schema migration in v2.3 for any dropped or altered indexes on the description column. 3. If the full-text index was removed, restore it. If the query was changed (e.g., from indexed FTS to LIKE/ILIKE scan), revert to the indexed approach. 4. Benchmark with ≥5,000 tasks with realistic description lengths to verify the fix.

## Proposed Test Case
Create a test database with 5,000+ tasks, including tasks with descriptions of 500+ words. Run a description search for a term appearing in exactly one task. Assert that the query completes in under 1 second (matching v2.2 baseline performance). Also verify that the query plan uses an index scan rather than a sequential scan.

## Information Gaps
- Exact OS version (not expected to be relevant for a query-layer regression)
- Whether other v2.3 users report the same issue (would confirm it's not data-specific)
- The specific database backend in use (SQLite vs PostgreSQL) — developer can check from the codebase
