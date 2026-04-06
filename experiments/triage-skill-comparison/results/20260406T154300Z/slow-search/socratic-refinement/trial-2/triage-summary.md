# Triage Summary

**Title:** Search regression in v2.3: description search takes 10-15 seconds while title search remains fast

## Problem
After upgrading from v2.2 to v2.3, full-text search over task descriptions takes 10-15 seconds consistently, regardless of query length or frequency. Title-only search remains fast. The user has ~5,000 tasks with lengthy descriptions (including pasted meeting notes).

## Root Cause Hypothesis
The v2.3 update likely introduced a regression in how description fields are searched — most probably a dropped or unused database index on the description column, a change from indexed full-text search to a naive sequential scan, or a query planner change that bypasses the description index. The fact that title search is unaffected suggests the issue is isolated to the description search path.

## Reproduction Steps
  1. Set up a TaskFlow instance with v2.3 installed
  2. Populate the database with ~5,000 tasks, including tasks with long descriptions (multiple paragraphs, pasted meeting notes)
  3. Perform a search that targets task descriptions
  4. Observe response time of 10-15 seconds
  5. Repeat the same search targeting only task titles and observe fast response
  6. Optionally: downgrade to v2.2 and confirm description search is fast again

## Environment
TaskFlow v2.3 (upgraded from v2.2), work laptop (OS unspecified), ~5,000 tasks with long descriptions

## Severity: high

## Impact
Users who search task descriptions — especially power users with large task counts and long descriptions — experience unusable search performance. This is a core workflow regression from a stable release.

## Recommended Fix
Diff the search implementation and database migrations between v2.2 and v2.3. Specifically investigate: (1) whether a full-text index on the description column was dropped or altered, (2) whether the search query for descriptions changed (e.g., switched from indexed FTS to LIKE/ILIKE scan), (3) whether a new ORM or query builder is generating a different query plan. Run EXPLAIN ANALYZE on the description search query against a dataset of ~5,000 tasks to confirm the scan type.

## Proposed Test Case
Performance regression test: populate a test database with 5,000 tasks having realistic description lengths (500-2,000 words each). Assert that a description search completes in under 2 seconds. Run this test as part of the search module's CI suite to prevent future regressions.

## Information Gaps
- Exact OS and hardware specs (unlikely to be relevant given the version correlation)
- Whether the user has any custom plugins or integrations that might interact with search
- Database engine in use (SQLite vs PostgreSQL vs other) — may affect index behavior
