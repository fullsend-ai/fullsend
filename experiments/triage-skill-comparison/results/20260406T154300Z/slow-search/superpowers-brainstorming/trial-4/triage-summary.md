# Triage Summary

**Title:** Search performance regression in v2.3: 10-15s latency with ~5,000 tasks (was <1s in v2.2)

## Problem
After upgrading from TaskFlow v2.2 to v2.3, search results that previously returned in under one second now take 10-15 seconds. The reporter has approximately 5,000 tasks accumulated over two years. No other changes were made; the regression correlates directly with the version upgrade roughly two weeks ago.

## Root Cause Hypothesis
The v2.3 release likely changed the search implementation in a way that degrades performance at scale — possible causes include removal or breakage of a search index, a switch from indexed/optimized queries to full table scans, introduction of unintended eager-loading of related data, or a new feature (e.g., full-text search across additional fields) that lacks proper indexing.

## Reproduction Steps
  1. Set up a TaskFlow instance with ~5,000 tasks (or import a representative dataset)
  2. Run a search query on v2.2 and record response time
  3. Upgrade to v2.3 and run the same search query
  4. Observe that response time increases from <1s to 10-15s

## Environment
TaskFlow v2.3 (upgraded from v2.2), running on a work laptop, ~5,000 tasks

## Severity: high

## Impact
Search is a core workflow feature. A 10-15x slowdown affects any user with a non-trivial number of tasks, making the feature effectively unusable for daily work. All v2.3 users with moderately large task collections are likely affected.

## Recommended Fix
Diff the search-related code and queries between v2.2 and v2.3. Profile the search query execution in v2.3 against a ~5,000 task dataset to identify the bottleneck (missing index, unoptimized query, excessive data loading). Check for new database migrations in v2.3 that may have dropped or failed to create indexes. Restore the performant query path or add appropriate indexing.

## Proposed Test Case
Performance test: with a seeded database of 5,000 tasks, assert that search queries return results in under 2 seconds. Run this test against both v2.2 and v2.3 code paths to confirm the regression and validate the fix.

## Information Gaps
- Whether the slowdown affects all search queries equally or only certain query patterns (e.g., broad vs. narrow searches)
- Whether other v2.3 users with large task counts are experiencing the same regression
- Exact database backend in use (SQLite, PostgreSQL, etc.)
