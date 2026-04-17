# Triage Summary

**Title:** Full-text search over task descriptions is slow (~10-15s) at ~5,000 tasks

## Problem
Searching within task descriptions takes 10-15 seconds to return results for a user with ~5,000 tasks. Title-based search remains fast. The regression suggests the description search path lacks proper indexing or is performing unoptimized full-table scans.

## Root Cause Hypothesis
The task descriptions column is missing a full-text search index (or the existing index has not been updated/rebuilt as the dataset grew). Title search likely hits an indexed column, while description search falls back to a sequential scan or LIKE query over unindexed text.

## Reproduction Steps
  1. Create or use a TaskFlow instance with ~5,000 tasks that have non-trivial descriptions
  2. Perform a full-text search (search within descriptions) for a common term
  3. Observe query latency of ~10-15 seconds
  4. Compare with a title-only search for the same term — should return quickly

## Environment
Work laptop, long-running TaskFlow instance (~2 years, ~5,000 tasks). Specific OS and database engine not confirmed but not needed to begin investigation.

## Severity: medium

## Impact
Users with large task counts experience significant delays on description search, degrading daily usability. Title search is unaffected.

## Recommended Fix
1. Check the database schema for the tasks table — verify whether a full-text index exists on the description column. 2. If missing, add an appropriate full-text index (e.g., GIN index with tsvector for PostgreSQL, FULLTEXT index for MySQL). 3. Review the search query itself — ensure it uses the index rather than LIKE '%term%'. 4. Consider adding pagination or result-count limits if not already present.

## Proposed Test Case
With a dataset of 5,000+ tasks with substantive descriptions, assert that a full-text description search completes in under 2 seconds. Benchmark before and after the index change.

## Information Gaps
- Exact database engine in use (PostgreSQL, MySQL, SQLite) — affects index syntax but not the diagnosis
- Whether the slowness appeared suddenly (possible regression) or grew gradually with data volume
- TaskFlow version
