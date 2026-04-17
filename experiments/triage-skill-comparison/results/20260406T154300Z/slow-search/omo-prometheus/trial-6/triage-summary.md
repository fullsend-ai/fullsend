# Triage Summary

**Title:** Description search regression in v2.3: 10-15s query time with ~5k tasks

## Problem
After upgrading from v2.2 to v2.3, searching by task description takes 10-15 seconds consistently. Title-only search remains fast. The user has approximately 5,000 tasks accumulated over two years.

## Root Cause Hypothesis
The v2.3 release likely introduced a change to the description search path — most probably a dropped or missing database index on the task descriptions column, a switch from indexed full-text search to a naive LIKE/ILIKE scan, or a removed search optimization (e.g., search cache or pre-computed index). The fact that title search is unaffected and description search is uniformly slow (not intermittent) points to a query-plan regression rather than a resource contention issue.

## Reproduction Steps
  1. Create or use an account with a large number of tasks (~5,000)
  2. Upgrade from TaskFlow v2.2 to v2.3
  3. Perform a search using a term that matches task descriptions
  4. Observe 10-15 second response time
  5. Perform a search using a term that matches only task titles
  6. Observe that title search returns quickly

## Environment
TaskFlow v2.3, work laptop (OS/specs not specified), ~5,000 tasks

## Severity: high

## Impact
Any user with a moderate-to-large task count who searches by description is affected. Search is a core workflow feature, and 10-15s latency effectively breaks it. Workaround exists (search by title only) but significantly limits functionality.

## Recommended Fix
1. Diff the search-related code and database migrations between v2.2 and v2.3 — look for changes to description search queries, removed indexes, or altered full-text search configuration. 2. Run EXPLAIN/ANALYZE on the description search query against a dataset with ~5k tasks to confirm whether it's doing a sequential scan. 3. If an index was dropped or a migration missed, restore it. If the search implementation changed, benchmark the new approach against the old one at scale.

## Proposed Test Case
Performance test: seed a test database with 5,000+ tasks with varied descriptions. Assert that a description search query returns results in under 1 second (or whatever the pre-v2.3 baseline was). Include this as a regression test in CI.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to be the cause given the regression pattern)
- Whether the database is local (SQLite) or remote (PostgreSQL, etc.) — affects which index types to investigate
- Whether other users on v2.3 with large task counts experience the same issue
