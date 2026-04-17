# Triage Summary

**Title:** Search performance regression after v2.3 upgrade (~10-15s latency with ~5,000 tasks)

## Problem
Search results take 10-15 seconds to return, up from near-instant in v2.2. The user has approximately 5,000 tasks accumulated over 2 years of usage. The slowdown correlates with upgrading from v2.2 to v2.3 roughly 2 weeks ago.

## Root Cause Hypothesis
The v2.3 release likely introduced a search regression — possible causes include a missing or dropped database index, a change in query logic (e.g., switching from indexed lookup to full-table scan), addition of new searchable fields without optimization, or a change in how search results are ranked/sorted that scales poorly with large datasets.

## Reproduction Steps
  1. Set up a TaskFlow instance with ~5,000 tasks (or import a representative dataset)
  2. Run v2.2 and execute several search queries, noting response times
  3. Upgrade to v2.3 and repeat the same queries
  4. Compare response times — expect significant degradation on v2.3

## Environment
TaskFlow v2.3, work laptop (OS and specs unknown), ~5,000 tasks accumulated over 2 years

## Severity: high

## Impact
Search is a core workflow feature. A 10-15 second delay severely impacts productivity for any user with a large task history. All long-term users with substantial task counts are likely affected after upgrading to v2.3.

## Recommended Fix
Diff the search-related code and database migrations between v2.2 and v2.3. Profile search queries against a dataset of ~5,000 tasks to identify slow operations. Check for dropped or missing indexes, new unoptimized joins, or changes to query structure. Review any new full-text search or sorting logic introduced in v2.3.

## Proposed Test Case
Performance test: execute a representative search query against a database with 5,000+ tasks and assert that results return within an acceptable threshold (e.g., under 1 second). Run this test against both v2.2 and v2.3 search code paths to confirm the regression and validate the fix.

## Information Gaps
- Whether all search queries are slow or only certain types (e.g., broad vs. specific terms)
- Exact laptop specs and OS (could rule out or confirm hardware/platform factors)
- Whether other v2.3 features (beyond search) also feel slower
- Database backend in use (SQLite, PostgreSQL, etc.) and whether it was migrated during upgrade
