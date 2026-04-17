# Triage Summary

**Title:** Full-text search across task descriptions regressed to 10-15s after upgrade to v2.3

## Problem
After upgrading from TaskFlow 2.2 to 2.3, searching by keyword across task descriptions takes 10-15 seconds per query. Title-based search remains fast. The issue affects all description search queries regardless of keyword, on an account with approximately 5,000 tasks.

## Root Cause Hypothesis
The v2.3 upgrade likely introduced a regression in the full-text search implementation for task descriptions — possibly a dropped or changed database index on the description field, a change in the search query path (e.g., switching from indexed search to unindexed LIKE/ILIKE scan), or a new search backend that lacks proper indexing for descriptions while titles retained their existing index.

## Reproduction Steps
  1. Create or use an account with approximately 5,000 tasks that have text in their descriptions
  2. Open the search feature in the TaskFlow desktop app (v2.3)
  3. Search for a keyword known to appear in task descriptions (e.g., a project name or person's name)
  4. Observe that results take 10-15 seconds to return
  5. Search for the same keyword but scoped to task titles only
  6. Observe that title search returns results quickly

## Environment
Ubuntu 22.04, Lenovo ThinkPad T14, 32GB RAM, TaskFlow v2.3 (desktop app), upgraded from v2.2 approximately two weeks ago

## Severity: high

## Impact
Any user with a significant number of tasks experiences unusable full-text search performance. This is a core workflow for finding tasks by content and represents a regression from the prior version.

## Recommended Fix
Diff the search query path and database schema between v2.2 and v2.3. Check whether a full-text index on the task descriptions column was dropped, altered, or is no longer being used by the query planner. Run EXPLAIN/ANALYZE on the description search query against a dataset of 5,000+ tasks. If an index was removed, restore it; if the query path changed, revert or optimize it.

## Proposed Test Case
Performance test: execute a full-text description search on a dataset of 5,000 tasks and assert that results return within an acceptable threshold (e.g., under 2 seconds). Run this test on both v2.2 and v2.3 schemas to confirm the regression and validate the fix.

## Information Gaps
- No application logs or query timing data from the desktop app to confirm whether the bottleneck is client-side or database-side
- Exact database backend in use (SQLite, PostgreSQL, etc.) is unknown
- Whether the issue also affects the web/browser version of TaskFlow is untested
