# Triage Summary

**Title:** Description search regression in v2.3.0: full-text scan takes 10-15s on moderate dataset (~5k tasks)

## Problem
After upgrading to v2.3.0, searching via the main search bar is extremely slow (10-15 seconds) when the query matches content in task descriptions. Title-only searches remain fast. The slowness is consistent regardless of search term specificity or result set size, indicating the bottleneck is in scanning/indexing descriptions rather than assembling results.

## Root Cause Hypothesis
v2.3.0 likely introduced a change to how description fields are searched — possibly dropping a full-text index, switching from indexed lookup to sequential scan, or adding unindexed text processing (e.g., new rich-text parsing or sanitization applied at query time). The fact that title search is unaffected and the slowness is independent of result count strongly points to a per-row scanning cost on the description column.

## Reproduction Steps
  1. Set up a TaskFlow v2.3.0 instance with ~5,000 tasks, some containing lengthy descriptions (pasted meeting notes, paragraphs of text)
  2. Open the main search bar at the top of the application
  3. Search for a term known to appear in only one task description
  4. Observe that results take 10-15 seconds to return
  5. Search for the same term when it appears only in a task title — observe that this returns quickly
  6. Compare against v2.2.x with the same dataset to confirm the regression

## Environment
TaskFlow v2.3.0, ~5,000 tasks accumulated over 2 years, some tasks with long descriptions (pasted meeting notes), running on a work laptop (OS/specs not specified)

## Severity: high

## Impact
Any user with a moderate-to-large task database is likely affected. Description search is effectively unusable at ~5k tasks. Users who rely on searching task content rather than titles are significantly impacted.

## Recommended Fix
Diff the search/query layer between v2.2.x and v2.3.0. Look for: (1) dropped or altered full-text indexes on the description column, (2) new ORM query patterns that bypass indexes, (3) added per-row processing during search (e.g., stripping HTML/markdown at query time instead of at index time). Run EXPLAIN/ANALYZE on the description search query against a 5k-row dataset to confirm whether an index is being used.

## Proposed Test Case
Performance test: with 5,000 tasks (including tasks with descriptions >1,000 characters), assert that a description search for a unique term completes in under 1 second. Run this test on both v2.2.x (baseline) and v2.3.0 to catch regressions.

## Information Gaps
- Exact OS and hardware specs of reporter's laptop (unlikely to be the cause given the version correlation)
- Whether other v2.3.0 users with large datasets experience the same issue (would confirm it's not data-specific)
- Whether the database backend was migrated or altered as part of the v2.3.0 upgrade (e.g., schema migration that dropped an index)
