# Triage Summary

**Title:** Search performance regression in v2.3: queries take 10-15s (previously <1s)

## Problem
After updating to TaskFlow v2.3, search queries that previously returned results in under one second now take 10-15 seconds. The user has approximately 5,000 tasks accumulated over two years.

## Root Cause Hypothesis
A change in v2.3 likely introduced a performance regression in the search path — possible causes include a missing or dropped database index, a switch from indexed search to full table scan, removal of query result caching, or a change in the search algorithm (e.g., adding unoptimized full-text matching across all fields).

## Reproduction Steps
  1. Set up a TaskFlow instance with ~5,000 tasks (or use a seeded test dataset of equivalent size)
  2. Run the same search query on v2.2.x and confirm sub-second response
  3. Upgrade to v2.3 and run the same search query
  4. Observe response time of 10-15 seconds

## Environment
TaskFlow v2.3, ~5,000 tasks, work laptop (specific OS and hardware not provided but unlikely to be relevant given the version correlation)

## Severity: high

## Impact
Any user with a non-trivial number of tasks experiences severely degraded search performance after upgrading to v2.3. Search is a core workflow feature, and 10-15s latency effectively makes it unusable for interactive use.

## Recommended Fix
Diff all search-related code between v2.2.x and v2.3. Profile the search query execution to identify where time is spent. Likely candidates: (1) check for dropped or missing database indexes on searchable fields, (2) review any query changes that may have removed LIMIT clauses or added unindexed WHERE conditions, (3) check if a caching layer was removed or bypassed, (4) look for N+1 query patterns introduced in the search results path.

## Proposed Test Case
Add a performance benchmark test that seeds 5,000+ tasks and asserts search queries complete within 2 seconds. Run this test as part of CI to prevent future search performance regressions.

## Information Gaps
- Which specific search queries are slowest (keyword search, filtered search, or all searches equally)
- Whether the backend shows slow query logs or if the latency is elsewhere in the stack
