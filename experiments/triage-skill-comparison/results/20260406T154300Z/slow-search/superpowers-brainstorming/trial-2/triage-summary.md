# Triage Summary

**Title:** Search performance regression in v2.3 (10-15s response times, was instant in v2.2)

## Problem
After upgrading from TaskFlow v2.2 to v2.3, the search feature takes 10-15 seconds to return results. Search was responsive ('snappy') in v2.2 with the same dataset on the same machine.

## Root Cause Hypothesis
A change to the search implementation in v2.3 introduced a performance regression — likely a missing index, removed query optimization, new unoptimized filtering/sorting step, or a switch from indexed search to full table scan. Since the dataset didn't change, this is a code-level regression, not a scaling issue.

## Reproduction Steps
  1. Install TaskFlow v2.2 and populate with a non-trivial number of tasks
  2. Run a search query and note response time (expected: sub-second)
  3. Upgrade to TaskFlow v2.3
  4. Run the same search query and observe 10-15 second response time

## Environment
Work laptop, TaskFlow v2.3 (upgraded from v2.2 approximately two weeks ago), large task dataset

## Severity: high

## Impact
All users with non-trivial task counts will experience unusable search latency after upgrading to v2.3. Search is a core workflow feature, so this significantly degrades daily usage.

## Recommended Fix
Diff all search-related code between v2.2 and v2.3 (query construction, indexing, ORM changes, new middleware). Profile the v2.3 search query path to identify where time is spent. Common culprits: removed or changed database index, N+1 query introduction, new full-text search implementation without proper indexing, added eager-loading of associations.

## Proposed Test Case
Add a performance/benchmark test that populates the database with N tasks (e.g. 1000+) and asserts that search queries complete within an acceptable threshold (e.g. under 1 second). Run this test against both v2.2 and v2.3 to confirm the regression and later verify the fix.

## Information Gaps
- Exact number of tasks in the reporter's dataset
- Whether all search queries are slow or only certain query patterns
- Database backend in use (SQLite vs PostgreSQL, etc.)
- Specific v2.3 changelog entries related to search
