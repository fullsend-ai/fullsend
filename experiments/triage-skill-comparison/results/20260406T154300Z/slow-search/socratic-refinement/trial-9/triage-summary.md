# Triage Summary

**Title:** Search on task descriptions is extremely slow after v2.3 update (title search unaffected)

## Problem
After updating to TaskFlow v2.3, searching by keywords that appear in task descriptions/body text takes 10-15 seconds to return results. Searching by task title remains instant. The user has approximately 5,000 tasks, many with lengthy descriptions containing pasted meeting notes. Results are accurate when they return — the issue is purely performance.

## Root Cause Hypothesis
The v2.3 update likely introduced a regression in how task description text is searched. Probable causes: (1) a full-text search index on the description/body field was dropped or not migrated during the v2.3 upgrade, (2) v2.3 changed the search query to perform unindexed full-table scans on description content, or (3) v2.3 introduced a new search code path for body text that bypasses the existing index. The fact that title search remains fast suggests the title index is intact and the issue is isolated to the description field's query or index.

## Reproduction Steps
  1. Set up a TaskFlow instance with a substantial number of tasks (ideally ~5,000) where many tasks have lengthy description text
  2. Update to v2.3
  3. Search for a keyword that appears only in task descriptions/body text — observe 10-15 second response time
  4. Search for a keyword that appears in a task title — observe near-instant response
  5. Compare query plans or database logs between the two searches to confirm indexing difference

## Environment
TaskFlow v2.3, work laptop (specific OS/specs unknown), ~5,000 tasks with lengthy descriptions

## Severity: high

## Impact
Any user with a non-trivial number of tasks who searches by description content will experience severe slowdowns after upgrading to v2.3. This degrades a core workflow — search — from instant to 10-15 seconds, making it functionally unusable for body-text searches.

## Recommended Fix
1. Diff the v2.3 database migration scripts against v2.2 to check whether the full-text index on the task description/body column was dropped or altered. 2. Inspect the v2.3 search query code path for description searches — check whether it still uses the index or falls back to LIKE/unindexed scan. 3. If the index was dropped, add a migration to restore it. If the query changed, restore the indexed query path. 4. Run EXPLAIN/ANALYZE on the slow description search query to confirm the fix uses the index.

## Proposed Test Case
Create a performance regression test: seed a database with 5,000+ tasks with multi-paragraph descriptions, execute a description-text search, and assert the query completes within an acceptable threshold (e.g., under 1 second). Run this test against both v2.2 (baseline) and v2.3 to confirm the regression and validate the fix.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to be the root cause given the title-vs-description split)
- Whether the database backend is SQLite, PostgreSQL, or another engine (affects index investigation approach)
- Whether other v2.3 users with smaller datasets also experience the slowdown (would help confirm it's index-related vs. dataset-size-related)
