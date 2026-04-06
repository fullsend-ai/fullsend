# Triage Summary

**Title:** Full-text search on task descriptions regressed to 10-15s after v2.3 upgrade (~5,000 tasks)

## Problem
Searching by keyword within task descriptions takes 10-15 seconds to return results, whereas searching by task title remains fast. The reporter has approximately 5,000 tasks. The slowness began around the time of upgrading from TaskFlow v2.2 to v2.3.

## Root Cause Hypothesis
The v2.3 upgrade likely introduced a regression in description search — possible causes include a dropped or changed full-text index on the task descriptions column, a query plan change (e.g., switching from indexed search to sequential scan), or a new ORM/query path that doesn't use the existing index. The fact that title search is unaffected suggests the title index is intact but the description search path diverged.

## Reproduction Steps
  1. Create or use a workspace with approximately 5,000 tasks that have text in their descriptions
  2. Open the search feature in the TaskFlow desktop app
  3. Search for a keyword that appears in a task's description (not just its title)
  4. Observe that results take 10-15 seconds to return
  5. Compare by searching for a keyword that appears in a task title — this should return quickly

## Environment
Ubuntu 22.04, ThinkPad T14, TaskFlow v2.3 desktop app (upgraded from v2.2 approximately two weeks ago)

## Severity: high

## Impact
Any user with a non-trivial number of tasks experiences severely degraded search performance when searching task descriptions, a core workflow. The feature is still functional but practically unusable for power users with large task histories.

## Recommended Fix
Compare the v2.2 and v2.3 description search query paths and database migrations. Check whether a full-text index on the descriptions column was dropped, altered, or is no longer being used by the query. Run EXPLAIN/ANALYZE on the description search query against a dataset of ~5,000 tasks to confirm whether it's doing a sequential scan. Restore or add the appropriate index and verify the query planner uses it.

## Proposed Test Case
Performance test: seed a database with 5,000+ tasks with varied descriptions, execute a keyword search against descriptions, and assert that results return within an acceptable threshold (e.g., under 1 second). This test should run against both title and description search to catch future regressions in either path.

## Information Gaps
- No error messages or logs were collected — query timing logs or database slow-query logs could confirm the exact bottleneck
- Unknown whether the issue reproduces on a fresh v2.3 install or only on upgraded instances (migration-specific issue vs. code-level regression)
