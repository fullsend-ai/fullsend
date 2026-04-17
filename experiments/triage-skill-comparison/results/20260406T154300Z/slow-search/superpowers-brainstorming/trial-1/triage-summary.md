# Triage Summary

**Title:** Search performance regression in v2.3 (~10-15s latency with ~5,000 tasks)

## Problem
After upgrading from TaskFlow v2.2 to v2.3, search queries that previously returned near-instantly now take 10-15 seconds. The reporter's workspace contains approximately 5,000 tasks accumulated over two years. Task count has not changed significantly — the slowdown correlates with the version upgrade, not data growth.

## Root Cause Hypothesis
A change to the search implementation in v2.3 introduced a performance regression at scale. Likely candidates: a query that lost an index, a switch from indexed/cached search to unoptimized full-scan, addition of new search features (e.g., full-text, fuzzy matching) without corresponding indexing, or a removed/broken caching layer.

## Reproduction Steps
  1. Provision a TaskFlow instance on v2.2 and seed it with ~5,000 tasks
  2. Run a representative search query and record response time
  3. Upgrade the instance to v2.3 (same dataset)
  4. Run the same search query and record response time
  5. Expect a significant latency increase (from sub-second to 10-15 seconds)

## Environment
TaskFlow v2.3 (upgraded from v2.2 approximately two weeks ago), ~5,000 tasks, running on a work laptop (OS and specs not specified but unlikely to be relevant given the version-correlated regression)

## Severity: high

## Impact
Any user with a large task history (thousands of tasks) on v2.3 will experience unusable search latency. Search is a core workflow feature, so this likely affects daily productivity for power users.

## Recommended Fix
Diff all search-related code between v2.2 and v2.3 (query logic, indexing, caching). Profile the v2.3 search path against a 5,000-task dataset to identify the hot spot. Check database query plans for missing or dropped indexes. If a new search feature was added (e.g., fuzzy matching, additional fields), ensure it is backed by appropriate indexes or can be toggled off.

## Proposed Test Case
Add a performance/benchmark test that runs search queries against a seeded database of 5,000+ tasks and asserts response time stays under an acceptable threshold (e.g., 1 second). This test should run in CI to catch future regressions.

## Information Gaps
- Exact search queries that are slow (all queries, or specific patterns/filters)
- Whether the TaskFlow instance uses a local database or a remote one
- Server-side logs or query profiling data from v2.3
