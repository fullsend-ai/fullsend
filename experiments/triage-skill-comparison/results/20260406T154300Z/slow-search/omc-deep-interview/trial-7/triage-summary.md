# Triage Summary

**Title:** Keyword search across task descriptions regressed to 10-15s in v2.3 (title search unaffected)

## Problem
After upgrading from v2.2 to v2.3, keyword searches across task descriptions take 10-15 seconds for a user with ~5,000 tasks. Previously this was near-instant. Searching by title remains fast, isolating the regression to description-field search.

## Root Cause Hypothesis
The v2.3 upgrade likely changed how description search is executed — most probably a dropped or missing full-text index on the task descriptions column, a query planner regression, or a switch from indexed search to unoptimized LIKE/ILIKE scanning. The fact that title search is unaffected suggests title search still uses an index while description search does not.

## Reproduction Steps
  1. Set up TaskFlow v2.3
  2. Populate the database with ~5,000 tasks with non-trivial descriptions
  3. Perform a keyword search targeting task descriptions
  4. Observe query latency (expected: 10-15 seconds)
  5. Compare against v2.2 with the same dataset (expected: near-instant)

## Environment
TaskFlow v2.3 (upgraded from v2.2), ~5,000 tasks, work laptop (specific OS/hardware unknown but not likely relevant given the regression is version-correlated)

## Severity: medium

## Impact
Users with large task counts experience significant delays on description search, degrading daily workflow. Title search still works as a partial workaround. Likely affects all users with non-trivial task volumes on v2.3.

## Recommended Fix
Diff the database migration and search query logic between v2.2 and v2.3. Check whether a full-text or B-tree index on the task descriptions column was dropped, altered, or never created in the v2.3 migration. Run EXPLAIN ANALYZE on the description search query to confirm a sequential scan. Restore or add the appropriate index. If the search implementation changed (e.g., from indexed full-text search to application-level filtering), revert or optimize the new approach.

## Proposed Test Case
Performance test: with 5,000+ tasks, a keyword search across descriptions must return results in under 1 second. Run this test against both v2.2 and v2.3 schemas to catch regressions.

## Information Gaps
- Exact database engine and version in use
- Whether other v2.3 users report the same issue or this is dataset-specific
- v2.3 changelog or migration diff (not yet inspected)
