# Triage Summary

**Title:** Description search performs full table scan, taking 10-15s with large task count (regression in v2.3)

## Problem
Searching task descriptions via the main search bar takes 10-15 seconds consistently, while title-only search returns results in ~1 second. The user has ~5,000 tasks accumulated over two years. The slowdown appeared roughly 2 weeks ago, coinciding with an upgrade to v2.3. The laptop fan spins up during description searches, indicating high CPU usage.

## Root Cause Hypothesis
v2.3 likely introduced a regression in the description search path — either a full-text index on the description column was dropped/not migrated, or the search implementation was changed from an indexed query to a brute-force scan (e.g., LIKE '%term%' or application-level string matching). The CPU fan behavior confirms the search is CPU-bound rather than I/O-bound, consistent with scanning 5,000 description blobs in-process or via unindexed SQL.

## Reproduction Steps
  1. Set up a local TaskFlow v2.3 installation
  2. Import or create ~5,000 tasks with non-trivial description text
  3. Open the main search bar and ensure the search mode is set to include descriptions (not title-only)
  4. Search for a term like 'quarterly review' that appears in some task descriptions
  5. Observe search time (~10-15 seconds) and CPU usage
  6. Switch the toggle to title-only search and repeat the same query — should return in ~1 second

## Environment
TaskFlow v2.3, local single-user installation on a work laptop (OS and specs not specified), ~5,000 tasks

## Severity: medium

## Impact
Any user with a large task count (~5,000+) running v2.3 will experience 10-15 second description searches. Title-only search still works as a workaround. Single-user local installations are affected; shared/server deployments may also be affected but were not tested.

## Recommended Fix
1. Compare the v2.2 and v2.3 database schema and migration scripts for changes to the description column index or full-text search configuration. 2. Check if a full-text index on task descriptions was dropped or if the search query was changed to a non-indexed pattern (e.g., LIKE scan). 3. Profile the description search query with EXPLAIN/EXPLAIN ANALYZE on a 5,000-task dataset. 4. Restore or add a full-text index on the description column, or revert the search implementation to the v2.2 approach.

## Proposed Test Case
Performance test: seed the database with 5,000 tasks with realistic descriptions, run a description search query, and assert it completes in under 2 seconds. Run this test against both v2.2 (baseline) and v2.3 to confirm regression and verify fix.

## Information Gaps
- Exact database backend (SQLite, PostgreSQL, etc.) — developer can determine from codebase
- Whether v2.3 changelog mentions search-related changes
- Laptop OS and hardware specs — unlikely to change fix direction given the indexed-vs-unindexed diagnosis
