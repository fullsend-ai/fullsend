# Triage Summary

**Title:** Search performance regression in v2.3 (~10-15s latency with ~5,000 tasks)

## Problem
After upgrading from v2.2 to v2.3, search latency increased from sub-second to 10-15 seconds. The reporter has approximately 5,000 tasks accumulated over 2 years of use. Search was consistently fast on v2.2.

## Root Cause Hypothesis
A change introduced in v2.3 degraded search performance — likely a modified query that lost an index, a switch from indexed database search to in-memory/full-scan filtering, or a newly added search feature (e.g., full-text search across more fields) that wasn't optimized for larger datasets.

## Reproduction Steps
  1. Set up a TaskFlow instance with ~5,000 tasks (or use a seed/fixture script to generate them)
  2. Run search on v2.2 and record latency
  3. Upgrade to v2.3 and run the same search query
  4. Observe 10-15 second response time on v2.3

## Environment
TaskFlow v2.3 (upgraded from v2.2), running on a work laptop, ~5,000 tasks

## Severity: high

## Impact
Search is a core workflow feature. A 10-15 second delay on every search makes the application feel broken for any user with a non-trivial number of tasks. Likely affects all v2.3 users at scale, not just this reporter.

## Recommended Fix
Diff all search-related code between v2.2 and v2.3 (query logic, ORM calls, database migrations). Check for missing or dropped indexes, new unindexed columns added to search scope, or removal of pagination/query limits. Profile the search query with EXPLAIN/ANALYZE on a 5,000-task dataset to identify the bottleneck.

## Proposed Test Case
Add a performance/benchmark test that runs a search query against a dataset of 5,000+ tasks and asserts that results are returned within an acceptable threshold (e.g., under 2 seconds). This test should run against the search path to catch future regressions.

## Information Gaps
- Whether the slowness affects all search queries equally or only certain search terms/filters
- Whether the database backend or storage engine was also changed during the upgrade
- Exact laptop specs (CPU, RAM, disk type) — unlikely to be the root cause given the version correlation but could be a contributing factor
