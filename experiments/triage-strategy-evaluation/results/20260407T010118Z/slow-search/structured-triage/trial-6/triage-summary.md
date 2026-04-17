# Triage Summary

**Title:** Description search extremely slow (~10-15s) with large task count since v2.3 upgrade

## Problem
Searching by task description content takes 10-15 seconds to return results, while title-based search remains fast. The user has approximately 5,000 tasks accumulated over two years. The slowdown appears to have started around the time of upgrading from TaskFlow 2.2 to 2.3.

## Root Cause Hypothesis
The v2.3 upgrade likely introduced a regression in description search — possibly a missing or dropped database index on the task description/body column, a change from indexed full-text search to unindexed LIKE/ILIKE queries, or a new search implementation that performs full table scans on description content. The fact that title search remains fast suggests title indexing is intact while description search is not benefiting from equivalent indexing.

## Reproduction Steps
  1. Set up a TaskFlow 2.3 instance with a large dataset (~5,000 tasks with populated descriptions)
  2. Open the search bar in the desktop app
  3. Type a keyword known to exist in task descriptions (not just titles)
  4. Observe that results take 10-15 seconds to return
  5. Compare by searching for a keyword that appears in task titles — this should return quickly

## Environment
Ubuntu 22.04, ThinkPad T14 (32GB RAM), TaskFlow 2.3 desktop app (upgraded from 2.2 approximately 2-3 weeks ago)

## Severity: high

## Impact
Users with large task histories (~thousands of tasks) experience unusable description search performance. This degrades a core workflow — finding tasks by content. Likely affects all users who upgraded to 2.3 with significant task volumes.

## Recommended Fix
Compare the search query paths for title vs. description search between v2.2 and v2.3. Check whether a database migration in 2.3 dropped or failed to create an index on the description column. Inspect query plans (EXPLAIN ANALYZE) for description search queries against large datasets. If full-text search was replaced or modified in 2.3, verify the new implementation uses proper indexing.

## Proposed Test Case
Create a performance test that seeds a database with 5,000+ tasks with populated descriptions, then asserts that a description keyword search completes within an acceptable threshold (e.g., under 2 seconds). Run this test against both v2.2 and v2.3 to confirm the regression and validate the fix.

## Information Gaps
- Exact timing of when the slowdown started relative to the 2.3 upgrade (reporter is unsure if they coincided)
- Whether the desktop app logs show slow query warnings or other diagnostics during description search
- Whether other v2.3 users with large task counts experience the same issue
