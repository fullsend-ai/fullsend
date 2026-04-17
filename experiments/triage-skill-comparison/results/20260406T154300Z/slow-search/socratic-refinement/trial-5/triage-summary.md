# Triage Summary

**Title:** Description search regression in v2.3: 10-15s query times on description field while title search remains fast

## Problem
After upgrading from v2.2 to v2.3, searching across task descriptions takes 10-15 seconds per query. Title-based search remains fast. The slowdown is consistent regardless of search terms, query frequency, or session duration. The user has approximately 5,000 tasks, some with very long descriptions (pasted meeting notes).

## Root Cause Hypothesis
The v2.3 release likely introduced a regression in description search — most probably a dropped or ineffective full-text index on the task description column, a change from indexed search to unoptimized full-text scanning (e.g., LIKE '%term%' instead of a full-text index query), or a new code path that loads full description text into memory before filtering. The fact that title search is unaffected suggests the title field's search path was not changed.

## Reproduction Steps
  1. Install TaskFlow v2.3
  2. Have or import a dataset of ~5,000 tasks, with some tasks containing long descriptions (several paragraphs)
  3. Perform a search using the description search mode with any search term
  4. Observe query time of 10-15 seconds
  5. Switch to title search mode with the same term and observe fast results for comparison

## Environment
TaskFlow v2.3, upgraded from v2.2. Work laptop (OS and specs not specified). ~5,000 tasks, some with lengthy descriptions containing pasted meeting notes.

## Severity: high

## Impact
Any user with a non-trivial number of tasks experiences unusable search performance on description searches — a core workflow. This is a regression from v2.2 where search was fast.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3, focusing on how description search queries are constructed and executed. Specifically check: (1) whether a full-text index on the description column was dropped or altered in a v2.3 migration, (2) whether the query plan for description search changed (run EXPLAIN/ANALYZE on the search query), (3) whether v2.3 introduced a new code path that performs in-memory filtering instead of database-level search. Restore or add proper full-text indexing on the description field and ensure the search query uses it.

## Proposed Test Case
Create a performance test that populates a database with 5,000+ tasks (some with descriptions over 2,000 characters), runs a description search, and asserts the query completes in under 1 second. Run this test against both v2.2 and v2.3 to confirm the regression and validate the fix.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to be root cause given it's a regression)
- Whether the database backend is SQLite, PostgreSQL, or another engine (affects index implementation details)
- Whether other v2.3 users with large task counts experience the same issue
