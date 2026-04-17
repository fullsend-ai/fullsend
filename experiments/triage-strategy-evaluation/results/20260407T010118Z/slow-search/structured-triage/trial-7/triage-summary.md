# Triage Summary

**Title:** Full-text search performance regression in v2.3 (~10-15s for keyword queries on large task sets)

## Problem
Full-text search (searching task descriptions, not just titles) takes 10-15 seconds to return results for users with large task counts (~5,000). Title-only search remains fast. The reporter believes the slowness began around the upgrade from v2.2 to v2.3 approximately two weeks ago.

## Root Cause Hypothesis
The v2.3 upgrade likely introduced a change to the full-text search implementation — possibly a missing or dropped database index on task description fields, a change in query strategy (e.g., switching from indexed search to sequential scan), or a new search feature that scans additional fields without optimization. The fact that title search is fast while full-text search is slow points to the description-search query path specifically.

## Reproduction Steps
  1. Create or use an account with approximately 5,000 tasks with populated descriptions
  2. Perform a full-text search (not title-only) for a common keyword like 'budget' or 'quarterly review'
  3. Observe that results take 10-15 seconds to return
  4. Compare with a title-only search for the same keyword — this should return quickly

## Environment
Ubuntu 22.04, Lenovo ThinkPad T14, TaskFlow v2.3 (upgraded from v2.2 ~2 weeks ago)

## Severity: medium

## Impact
Users with large task histories experience unacceptable search latency on full-text queries. This degrades daily usability for power users and long-term customers who have accumulated thousands of tasks.

## Recommended Fix
1. Diff the search query path between v2.2 and v2.3 for full-text/description search. 2. Check database indexes on task description columns — verify they weren't dropped or altered in v2.3 migration. 3. Run EXPLAIN ANALYZE on the full-text search query with ~5,000 tasks to identify whether it's doing a sequential scan. 4. If an index is missing, add it; if the query changed, optimize or revert the query strategy.

## Proposed Test Case
Performance test: seed a database with 5,000 tasks with realistic descriptions. Run a full-text keyword search and assert results return within an acceptable threshold (e.g., under 2 seconds). Run this test against both v2.2 and v2.3 to confirm the regression and validate the fix.

## Information Gaps
- Exact browser and version (reporter indicated the issue feels server-side, not client-side)
- Whether server-side logs show slow query warnings during the slow searches
- Whether other users with large task counts also experience the issue or if it's account-specific
