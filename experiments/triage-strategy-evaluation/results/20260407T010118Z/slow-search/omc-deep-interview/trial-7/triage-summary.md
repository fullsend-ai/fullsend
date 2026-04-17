# Triage Summary

**Title:** Full-text description search regression in v2.3: 10-15s latency on ~5,000 tasks (SQLite/desktop)

## Problem
After upgrading from v2.2 to v2.3 approximately two weeks ago, searching across task descriptions takes 10-15 seconds, whereas it previously completed in under a second. Title-only search remains fast. The user has approximately 5,000 tasks, many with long descriptions.

## Root Cause Hypothesis
The v2.3 release likely changed the full-text search implementation — possible causes include: (1) a dropped or misconfigured FTS index on the descriptions column in SQLite, forcing a sequential scan; (2) a query change that bypasses the FTS index (e.g., switching from FTS5 MATCH to LIKE); or (3) a new search feature (e.g., ranking, highlighting) that adds per-row processing cost that scales poorly with description length and row count.

## Reproduction Steps
  1. Install TaskFlow v2.3 desktop app on Linux (Ubuntu) with default SQLite storage
  2. Populate the database with ~5,000 tasks, including tasks with long multi-paragraph descriptions
  3. Perform a full-text search (searching descriptions, not just titles) with any search term
  4. Observe response time — expect 10-15 seconds
  5. Compare: repeat the same search with title-only search and observe it returns quickly
  6. Optionally downgrade to v2.2 and repeat to confirm the search returns in under 1 second

## Environment
TaskFlow v2.3 desktop app, self-hosted on a ThinkPad T14 running Ubuntu, default SQLite storage, ~5,000 tasks with long descriptions

## Severity: high

## Impact
Any user with a large task database who relies on full-text description search experiences severe latency after upgrading to v2.3. Title-only search still works as a partial workaround, but users who need to search description content are effectively blocked.

## Recommended Fix
1. Diff the v2.2 and v2.3 search query paths — check for changes to SQLite FTS index usage on the descriptions column. 2. Run EXPLAIN QUERY PLAN on the v2.3 description search query against a 5K-row database to confirm whether the FTS index is being used. 3. If the index was dropped or the query changed to bypass it, restore FTS5 indexing on descriptions. 4. If new per-row processing was added (ranking, snippet extraction), benchmark it and optimize or make it opt-in. 5. Consider adding a migration step that rebuilds the FTS index on upgrade.

## Proposed Test Case
Performance regression test: populate a test database with 5,000 tasks (descriptions averaging 500+ words). Execute a full-text description search and assert the query completes in under 2 seconds. Run this test against both SQLite FTS-indexed and non-indexed configurations to catch index regressions.

## Information Gaps
- Exact v2.3 sub-version or build number
- Whether the SQLite FTS index exists in the reporter's database (could be confirmed by developer inspection of the schema)
- Whether the v2.3 upgrade migration ran successfully or logged any warnings
- RAM and disk I/O characteristics of the reporter's ThinkPad (unlikely to be the bottleneck but not confirmed)
