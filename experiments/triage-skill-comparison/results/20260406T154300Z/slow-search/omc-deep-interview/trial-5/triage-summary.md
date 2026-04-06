# Triage Summary

**Title:** Search across task descriptions regressed to 10-15s in v2.3 (title search unaffected)

## Problem
After upgrading from v2.2 to v2.3, searching across task descriptions takes 10-15 seconds regardless of query term. Searching by task title remains fast (~sub-second). The reporter has approximately 5,000 tasks, many with lengthy descriptions (e.g., pasted meeting notes). Search was consistently fast prior to the upgrade.

## Root Cause Hypothesis
The v2.3 release likely changed how description search is performed — most probably a missing or dropped full-text index on the task descriptions column, a switch from indexed full-text search to unindexed LIKE/ILIKE scanning, or a new query path that loads full description text into memory before filtering. The fact that title search is unaffected suggests the title field retained its index or query strategy while the description path changed.

## Reproduction Steps
  1. Install TaskFlow v2.3 (or upgrade from v2.2)
  2. Populate the database with ~5,000 tasks, including tasks with lengthy descriptions (multi-paragraph text)
  3. Perform a search across all tasks using the description search mode with any search term
  4. Observe search latency of 10-15 seconds
  5. Compare with a title-only search on the same dataset to confirm title search is still fast

## Environment
TaskFlow v2.3 (upgraded from v2.2), work laptop (OS and specs not specified but not likely relevant given the regression is version-correlated), ~5,000 tasks with some having very long descriptions

## Severity: high

## Impact
Any TaskFlow user with a nontrivial number of tasks who searches by description is affected. This is a core workflow regression — search is a primary feature and 10-15s latency makes description search effectively unusable. Title-only search serves as a partial workaround but does not cover the same use cases.

## Recommended Fix
Diff the search query path between v2.2 and v2.3, focusing on how task descriptions are queried. Likely fixes: (1) restore or add a full-text index on the descriptions column, (2) revert any change from indexed search to sequential scan/LIKE pattern matching, (3) check if descriptions are now being fully loaded before filtering rather than filtered at the database level. Run EXPLAIN ANALYZE on the description search query against a 5,000-task dataset to confirm the query plan.

## Proposed Test Case
Performance regression test: seed database with 5,000 tasks (including tasks with 1KB+ descriptions), execute a description search, and assert results return in under 2 seconds. Run this test against both the v2.2 and v2.3 query paths to verify the regression and confirm the fix.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to matter given version-correlated regression)
- Whether the reporter is using a local database or a hosted/cloud backend
- Specific database engine in use (SQLite, PostgreSQL, etc.) — relevant for index implementation details
- v2.3 changelog or migration scripts that may reveal the specific change
