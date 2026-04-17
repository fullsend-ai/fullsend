# Triage Summary

**Title:** Full-text search on task descriptions extremely slow (~10-15s) since v2.3 upgrade

## Problem
Full-text search across task descriptions takes 10-15 seconds to return results for a user with ~5,000 tasks, many with long descriptions (pasted meeting notes). Title-only search remains fast. The slowness is consistent across all full-text queries regardless of search term specificity. The issue appeared around the time of upgrading from v2.2 to v2.3.

## Root Cause Hypothesis
The v2.3 upgrade likely introduced a regression in the full-text search implementation for task descriptions — possibly a missing or dropped search index on the descriptions field, a change in the query strategy (e.g., switching from indexed search to sequential scan), or a new feature (like search ranking or snippet extraction) that scales poorly with description length and task count.

## Reproduction Steps
  1. Create or import a dataset with ~5,000 tasks, including tasks with long descriptions (multi-paragraph meeting notes)
  2. Open TaskFlow desktop app
  3. Use the search feature to perform a full-text search across task descriptions for any keyword
  4. Observe that results take 10-15 seconds to return
  5. Compare with a title-only search for the same keyword, which should return quickly

## Environment
Ubuntu 22.04, ThinkPad T14 with 32GB RAM, TaskFlow desktop app v2.3 (upgraded from v2.2 approximately two weeks ago)

## Severity: high

## Impact
Users with large task databases (~5,000+ tasks) who rely on full-text search across descriptions are experiencing severe performance degradation, making a core feature effectively unusable for daily workflows. Title search users are unaffected.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3 to identify what changed. Likely areas: (1) check whether a full-text index on the descriptions column/field was dropped or is no longer being used, (2) check for query plan changes (EXPLAIN/ANALYZE if SQL-backed), (3) look for new processing on descriptions during search (e.g., snippet generation, relevance scoring) that might not scale. Profile the full-text search query with a 5,000-task dataset to confirm the bottleneck.

## Proposed Test Case
Performance regression test: seed a database with 5,000 tasks including 500+ tasks with descriptions over 1,000 characters. Execute a full-text description search and assert that results are returned within 2 seconds. Run this test against both v2.2 and v2.3 to confirm the regression and validate the fix.

## Information Gaps
- No confirmation whether the slowness started exactly with the v2.3 upgrade or just around the same time
- No database/storage backend details (SQLite, PostgreSQL, etc.)
- No profiling data or logs showing where time is spent during the search
