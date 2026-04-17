# Triage Summary

**Title:** Search performance regression in v2.3 (~10-15s latency with ~5K tasks)

## Problem
After upgrading to TaskFlow v2.3 approximately two weeks ago, search queries that previously returned quickly now take 10-15 seconds. The user has approximately 5,000 tasks accumulated over two years of use. Search was performant on the prior version.

## Root Cause Hypothesis
A change introduced in v2.3 likely degraded search performance — possible causes include a missing or dropped database index, a change from indexed search to full table scan, removal or misconfiguration of search result caching, or a new search feature (e.g., full-text search across more fields) that scales poorly with dataset size.

## Reproduction Steps
  1. Set up a TaskFlow instance on v2.3
  2. Populate the database with ~5,000 tasks
  3. Execute a search query and measure response time
  4. Repeat on v2.2 with the same dataset to confirm the regression

## Environment
TaskFlow v2.3, ~5,000 tasks, work laptop (specific OS and hardware unknown but not relevant to a server/app-side regression)

## Severity: high

## Impact
Search is a core workflow feature. 10-15 second latency on a dataset of only 5K tasks makes it effectively unusable for active users. Likely affects all users with non-trivial task counts after upgrading to v2.3.

## Recommended Fix
Diff all search-related code and database migration changes between v2.2 and v2.3. Profile the search query execution plan against a ~5K task dataset to identify whether the bottleneck is query execution (missing index, full scan), application-layer processing, or network/serialization. Check for dropped indexes in v2.3 migrations. If a new search feature was added, verify it has appropriate indexing and pagination.

## Proposed Test Case
Performance regression test: seed database with 5,000 tasks, execute a representative search query, and assert that results return within an acceptable threshold (e.g., under 1 second). Run this test against both v2.2 and v2.3 to validate the fix closes the gap.

## Information Gaps
- Exact search queries that are slow (all searches vs. specific patterns)
- Whether the issue is present on TaskFlow's server-side or is a self-hosted instance
- Whether other v2.3 users have reported the same degradation
