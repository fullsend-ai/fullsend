# Triage Summary

**Title:** Search performance regression in v2.3: 10-15s response time (was <1s in v2.2)

## Problem
After upgrading from v2.2 to v2.3, search queries that previously returned results in under a second now take 10-15 seconds. The reporter has approximately 5,000 tasks accumulated over 2 years of use.

## Root Cause Hypothesis
A change in the v2.3 search implementation likely introduced a performance regression — possible causes include a dropped or changed database index, a switch from indexed search to full-table scan, removal of query result caching, or a new search feature (e.g., full-text or fuzzy matching) that scales poorly with large task counts.

## Reproduction Steps
  1. Set up a TaskFlow instance with ~5,000 tasks (or use a seeded test database at that scale)
  2. Run search queries on v2.2 and record response times
  3. Upgrade to v2.3 and run the same search queries
  4. Compare response times — expect sub-second on v2.2 and 10-15 seconds on v2.3

## Environment
TaskFlow v2.3 (upgraded from v2.2), work laptop (OS and specs unspecified), ~5,000 tasks

## Severity: high

## Impact
Search is a core workflow feature. A 10-15x slowdown affects any user with a substantial task history, making the feature effectively unusable for power users. Likely affects all v2.3 users at scale, not just this reporter.

## Recommended Fix
Diff the search-related code and database migrations between v2.2 and v2.3. Check for: (1) dropped or altered indexes on task tables, (2) changes to the search query (e.g., switching from indexed lookup to LIKE/full-scan), (3) removal of search result caching, (4) new search features that lack optimization for large datasets. Profile the search query on a 5,000-task dataset to confirm the bottleneck.

## Proposed Test Case
Performance regression test: seed a database with 5,000 tasks, execute a representative search query, and assert that results return in under 2 seconds. Run this test as part of CI to prevent future regressions.

## Information Gaps
- Whether all search queries are equally slow or only certain query patterns
- Exact OS and hardware specs of the reporter's laptop (unlikely to be the root cause given the version correlation)
- Whether other v2.3 users have reported the same issue
