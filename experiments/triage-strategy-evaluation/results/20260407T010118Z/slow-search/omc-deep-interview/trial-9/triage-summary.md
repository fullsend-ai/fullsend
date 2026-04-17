# Triage Summary

**Title:** Full-text description search regression in v2.3: 10-15s latency with ~5,000 tasks

## Problem
After upgrading from v2.2 to v2.3, searching via the main search bar takes 10-15 seconds when the search term matches task description/body content. Title-only matches return in under 1 second. The CPU spikes heavily during slow searches, indicating a compute-bound operation rather than I/O or memory pressure.

## Root Cause Hypothesis
The v2.3 update likely introduced a regression in how description/body fields are searched — most probably a missing or dropped full-text index on the task description column, a switch from indexed search to naive string scanning, or a new search feature (e.g., expanded search scope or ranking) that performs unoptimized full-text comparison across all 5,000 task bodies. The CPU spike pattern supports an unindexed sequential scan hypothesis.

## Reproduction Steps
  1. Install TaskFlow v2.3 (desktop app)
  2. Populate the instance with ~5,000 tasks that have non-trivial description/body content
  3. Open the main search bar at the top of the app
  4. Search for a term known to exist only in task titles — confirm it returns in under 1 second
  5. Search for a term known to exist in task description bodies — observe 10-15 second delay and high CPU usage
  6. Optionally compare against v2.2 with the same dataset to confirm the regression

## Environment
Desktop app (local install), ThinkPad T14 with 32GB RAM, TaskFlow v2.3, ~5,000 tasks accumulated over 2 years

## Severity: medium

## Impact
Users with large task counts (~5,000+) experience severely degraded search performance after upgrading to v2.3. Core search functionality becomes effectively unusable for description-level searches. Likely affects all users with significant task history. Title-only search still works as a partial workaround.

## Recommended Fix
1. Diff the search implementation between v2.2 and v2.3 — look for changes to query logic, index definitions, or search scope on the description/body field. 2. Check the local database schema for a missing or dropped full-text index on the task description column. 3. Profile the search query with EXPLAIN/ANALYZE (or equivalent) on a 5,000-task dataset to confirm whether it's doing a sequential scan. 4. If v2.3 added full-text search over descriptions as a new feature, ensure it uses proper indexing (FTS5 for SQLite, GIN/GiST for PostgreSQL, etc.).

## Proposed Test Case
Performance test: seed a TaskFlow instance with 5,000+ tasks with varied description content. Assert that a description-level search completes in under 2 seconds. Run this test against both v2.2 (baseline) and v2.3 (regression) to validate the fix restores prior performance.

## Information Gaps
- Exact v2.3 changelog — whether search implementation was intentionally modified
- Whether the issue worsens linearly with task count or has a threshold
- Operating system version (likely not a factor given the clear version correlation)
