# Triage Summary

**Title:** Full-text description search regressed to ~10-15s after upgrading to TaskFlow 2.3

## Problem
After upgrading from TaskFlow 2.2 to 2.3 approximately two weeks ago, searching across task descriptions takes 10-15 seconds with no partial/incremental results. Title-only search remains fast. The user has ~5,000 tasks accumulated over 2 years, many containing lengthy descriptions (pasted meeting notes, some several thousand words).

## Root Cause Hypothesis
The 2.3 release likely introduced a regression in full-text description search — possibly a dropped or changed index on the description field, a switch from indexed search to naive full-table scan, or a change in the search query path that bypasses previously-used indexing. The fact that title search is unaffected and the slowdown was sudden (not gradual with data growth) points to a code/schema change rather than a data volume issue.

## Reproduction Steps
  1. Create or use a TaskFlow instance with a large number of tasks (~5,000) including many with long description fields (1,000-2,000+ words)
  2. Perform a search that targets task descriptions (full-text search, not title-only)
  3. Observe that the search hangs for 10-15 seconds before returning all results at once
  4. Compare with the same search on TaskFlow 2.2 to confirm the regression

## Environment
TaskFlow 2.3 (upgraded from 2.2), running on a work laptop. ~5,000 tasks with some containing multi-thousand-word descriptions.

## Severity: high

## Impact
Any user with a non-trivial number of tasks performing full-text description search experiences severe slowdown. This is a core search workflow, and the 10-15 second delay makes it effectively unusable for iterative searching.

## Recommended Fix
Diff the search implementation between 2.2 and 2.3, focusing on how description search is executed. Check for: (1) removed or altered database indexes on the description field, (2) changes to the search query (e.g., switching from indexed FTS to LIKE/ILIKE scans), (3) new serialization or processing of description content before search. Restore or add appropriate full-text indexing on the description column.

## Proposed Test Case
Performance test: seed a database with 5,000 tasks (500 of which have descriptions over 1,000 words), execute a full-text description search, and assert that results return within an acceptable threshold (e.g., under 2 seconds). This test should run against both title and description search paths to catch future regressions in either.

## Information Gaps
- Exact TaskFlow 2.3 build/patch version (reporter said 'I think' re: version numbers)
- Whether other users on 2.3 with large datasets experience the same issue
- Database backend in use (SQLite vs PostgreSQL vs other) — may affect indexing behavior
