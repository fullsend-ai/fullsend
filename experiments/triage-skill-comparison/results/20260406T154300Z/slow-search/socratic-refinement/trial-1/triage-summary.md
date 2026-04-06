# Triage Summary

**Title:** Search across task descriptions regressed to ~10-15s after v2.3 upgrade (title-only search unaffected)

## Problem
Since upgrading to TaskFlow v2.3, plain-text search that includes task descriptions takes 10-15 seconds to return results. Searching by title only remains fast. The user has approximately 5,000 tasks accumulated over 2 years. The slowdown is consistent regardless of query length or complexity.

## Root Cause Hypothesis
The v2.3 release likely introduced a regression in description search — most probably a dropped or missing database index on the task descriptions column, a switch from indexed full-text search to a sequential scan (e.g., LIKE/ILIKE instead of FTS), or a query change that bypasses existing indexes. The fact that title search is unaffected suggests the title field still has proper indexing while the description field does not.

## Reproduction Steps
  1. Set up a TaskFlow instance running v2.3 with a dataset of ~5,000 tasks that have populated description fields
  2. Perform a search using the default search mode (searching across all fields including descriptions)
  3. Observe response time — expect 10-15 seconds
  4. Perform the same search restricted to title only
  5. Observe response time — expect sub-second results
  6. Optionally repeat on v2.2 with the same dataset to confirm the regression

## Environment
TaskFlow v2.3, work laptop (OS unspecified), ~5,000 tasks

## Severity: high

## Impact
Any user with a moderate-to-large task count who uses description search will experience significant delays. This is a core workflow — search is a primary navigation mechanism in a task management app. The 10-15 second wait degrades usability substantially.

## Recommended Fix
1. Diff the search-related code and database migrations between v2.2 and v2.3 to identify changes to how description search is executed. 2. Check the query plan (EXPLAIN ANALYZE or equivalent) for description search queries to confirm whether an index is being used. 3. If an index was dropped or a migration failed to create one, add the appropriate full-text or trigram index on the descriptions column. 4. If the query itself changed (e.g., from FTS to LIKE), revert to an indexed search strategy.

## Proposed Test Case
Create a performance regression test that populates a database with 5,000+ tasks with description text, executes a description search, and asserts the query completes within an acceptable threshold (e.g., under 2 seconds). Run this test as part of the CI pipeline for any changes to search functionality.

## Information Gaps
- Exact database backend in use (SQLite, PostgreSQL, etc.) — may affect index strategy
- Whether other v2.3 users report the same issue or if it is data-shape dependent
- Full v2.3 changelog to identify specific search-related changes
