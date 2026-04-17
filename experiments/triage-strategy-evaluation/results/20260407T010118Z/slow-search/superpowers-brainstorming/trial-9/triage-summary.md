# Triage Summary

**Title:** Full-text search regression in v2.3: 10-15s response time on ~5k task workspaces

## Problem
Full-text search across task descriptions and comments takes 10-15 seconds to return results, up from sub-second prior to v2.3. Title-only search remains fast. The issue affects workspaces with ~5,000 tasks, many with lengthy descriptions (e.g., pasted meeting notes).

## Root Cause Hypothesis
v2.3 likely introduced a change to the full-text search indexing or query path — possibly a missing or dropped index on task descriptions/comments, a change from indexed search to sequential scan, or a regression in query construction. The fact that title search is unaffected confirms the issue is isolated to the full-text code path.

## Reproduction Steps
  1. Set up or use a workspace with ~5,000 tasks, including tasks with long descriptions
  2. Upgrade to v2.3
  3. Perform a full-text search (across task descriptions/comments) and measure response time
  4. Compare with a title-only search on the same workspace to confirm the discrepancy

## Environment
TaskFlow v2.3, ~5,000 tasks with lengthy descriptions, work laptop (OS unspecified)

## Severity: high

## Impact
Full-text search is effectively unusable for power users with large workspaces. These are likely the most engaged users who rely on search to navigate years of accumulated tasks.

## Recommended Fix
Diff the full-text search query path between v2.2 and v2.3. Check for: dropped or rebuilt full-text indexes, changes to search query construction (e.g., switching from indexed lookup to LIKE/sequential scan), new joins or subqueries on the comments table, or removed query result caching. Run EXPLAIN/ANALYZE on the full-text search query against a ~5k task dataset to identify the bottleneck.

## Proposed Test Case
Performance test: execute a full-text search on a workspace with 5,000+ tasks (including tasks with 1,000+ character descriptions) and assert response time is under 2 seconds. Run this test against both v2.2 (baseline) and v2.3 (regression) to confirm the fix restores prior performance.

## Information Gaps
- Exact database backend (SQLite, PostgreSQL, etc.) — though the developer will know this from the codebase
- Whether the issue reproduces on a fresh v2.3 install or only on upgrades (migration-related index issue vs. code regression)
