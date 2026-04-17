# Triage Summary

**Title:** Search performance regression in v2.3: queries take 10-15s (previously near-instant)

## Problem
After upgrading from TaskFlow v2.2 to v2.3, search queries that previously returned near-instantly now take 10-15 seconds. The reporter has approximately 5,000 tasks accumulated over 2 years of use.

## Root Cause Hypothesis
A change to the search implementation in v2.3 introduced a performance regression — likely a missing or dropped index, a switch from indexed/cached search to full-table scan, removal of query result caching, or a new search feature (e.g., full-text search) that doesn't scale well at 5k+ tasks.

## Reproduction Steps
  1. Set up a TaskFlow instance with ~5,000 tasks (or use a seed/import script to generate them)
  2. Run search queries on v2.2 and note response times
  3. Upgrade to v2.3 and run the same search queries
  4. Observe significantly degraded response times (10-15 seconds vs. near-instant)

## Environment
TaskFlow v2.3 (upgraded from v2.2), work laptop (OS and specs unspecified), ~5,000 tasks

## Severity: high

## Impact
Search is a core workflow feature. A 10-15 second delay on every search makes TaskFlow feel broken for power users with large task databases. Likely affects all v2.3 users with non-trivial task counts.

## Recommended Fix
Diff the search-related code and database migrations between v2.2 and v2.3. Specifically investigate: (1) any changed or dropped database indexes on searchable fields, (2) changes to the query builder or ORM calls for search, (3) removal or invalidation of search result caching, (4) any new search features (full-text, fuzzy matching) that may execute expensive operations. Profile the search query at ~5k tasks to confirm where time is spent.

## Proposed Test Case
Performance test: seed a database with 5,000 tasks with varied titles/descriptions, execute a representative search query, and assert the response completes in under 1 second. Run this test as part of CI to prevent future regressions.

## Information Gaps
- Whether the slowness affects all search queries equally or only certain search terms/filters
- Exact laptop specs and OS (unlikely to be relevant given the version-correlated regression)
- Whether the v2.3 upgrade included a database migration that may have altered indexes
