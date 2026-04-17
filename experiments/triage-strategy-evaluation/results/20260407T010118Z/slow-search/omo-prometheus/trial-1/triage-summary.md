# Triage Summary

**Title:** Description search performs full table scan instead of using FTS index after v2.3 upgrade (~10-15s for 5,000 tasks)

## Problem
After upgrading to TaskFlow v2.3, searching by task description takes 10-15 seconds on a dataset of ~5,000 tasks. Title search remains fast (<1 second). The slowdown is consistent across all description searches within a session, not just the first query.

## Root Cause Hypothesis
The v2.3 upgrade likely removed, broke, or bypassed the SQLite full-text search (FTS) index on the task descriptions column. Description searches now appear to perform a sequential scan — loading each description into memory and pattern-matching one by one — rather than using an FTS index lookup. Evidence: single CPU core pegs at 100% during search (characteristic of a single-threaded sequential scan), title search is unaffected (its index is intact), and the timing (~10-15s for 5,000 rows) is consistent with row-by-row LIKE or in-application string matching rather than indexed retrieval.

## Reproduction Steps
  1. Install TaskFlow v2.3 with a local SQLite database
  2. Populate the database with ~5,000 tasks that have non-trivial descriptions
  3. Open the main search bar and search for a term that appears in task descriptions (not just titles)
  4. Observe that the search takes 10-15 seconds and CPU usage spikes on one core
  5. Compare by searching for a term that appears only in task titles — this should return quickly

## Environment
TaskFlow v2.3, local SQLite database (.db file in TaskFlow data folder), laptop with 32GB RAM, running locally with no cloud/server backend

## Severity: medium

## Impact
Users with large task databases (thousands of tasks) experience severe search degradation when searching by description content. Title-only search is unaffected. No data loss, but the feature is effectively unusable for description searches at scale. Likely affects all local SQLite users who upgraded to v2.3.

## Recommended Fix
1. Diff the search/query code between v2.2 and v2.3 — look for changes to how description search queries are constructed (e.g., switched from FTS MATCH to LIKE '%term%', or moved to in-application filtering). 2. Check whether the SQLite FTS virtual table for descriptions still exists and is being populated on v2.3 databases. 3. If the FTS index was dropped or is no longer used, restore it and ensure the description search query uses FTS MATCH syntax. 4. If the FTS table exists but a migration failed to populate it, add a repair migration. 5. Consider adding a query execution plan check (EXPLAIN QUERY PLAN) to integration tests to catch index regression.

## Proposed Test Case
Create a test database with 5,000+ tasks with varied descriptions. Run a description search query and assert it completes in under 2 seconds. Additionally, verify via EXPLAIN QUERY PLAN that the description search uses the FTS index rather than a sequential scan.

## Information Gaps
- Exact v2.3 changelog or migration scripts (developer-side investigation, not reporter-facing)
- Whether this affects other database backends or only SQLite
- Exact search query syntax used in v2.3 vs v2.2 (code review needed)
