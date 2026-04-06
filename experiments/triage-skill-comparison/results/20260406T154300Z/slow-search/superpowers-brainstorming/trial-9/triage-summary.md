# Triage Summary

**Title:** Search across task descriptions is slow (~10-15s) with ~5,000 tasks; title search unaffected

## Problem
Searching by task description takes 10-15 seconds to return results, while searching by task title remains fast. The user has approximately 5,000 tasks accumulated over two years. The slowness is consistent for all description searches, not specific to certain query terms.

## Root Cause Hypothesis
The task description column likely lacks a database index or full-text search index. Title search is fast because it hits an indexed column, while description search performs a full table scan or unoptimized pattern match (e.g., SQL LIKE '%term%') against 5,000 rows of potentially large text content.

## Reproduction Steps
  1. Create or use a TaskFlow instance with ~5,000 tasks that have populated description fields
  2. Perform a search using a task title keyword — observe fast results
  3. Perform a search using a keyword that appears in task descriptions but not titles — observe 10-15 second delay

## Environment
Work laptop, ~5,000 tasks. Specific OS/DB/TaskFlow version not confirmed but unlikely to affect the fix approach.

## Severity: medium

## Impact
Users with large task counts experience unacceptable search latency when searching descriptions, degrading the core search feature. Title-only search still works, so search is not fully broken.

## Recommended Fix
1. Check the query plan for description search queries (EXPLAIN/EXPLAIN ANALYZE). 2. Add a full-text search index on the task description column (e.g., GIN index with tsvector in PostgreSQL, or FULLTEXT index in MySQL). 3. Update the search query to use the full-text search capability instead of pattern matching. 4. If the application already uses an ORM, verify it generates indexed queries for description search. 5. Consider whether a combined title+description full-text index would be more appropriate.

## Proposed Test Case
With a dataset of 5,000+ tasks with populated descriptions, assert that a description search query returns results in under 1 second. Include a performance regression test that fails if description search exceeds an acceptable threshold (e.g., 2 seconds).

## Information Gaps
- Exact database engine and version (affects index syntax but not the overall approach)
- Whether search is implemented at the application layer or database layer
- Whether this is a regression (reporter says it 'used to be fast') — could indicate a dropped index or a migration that changed the search implementation
