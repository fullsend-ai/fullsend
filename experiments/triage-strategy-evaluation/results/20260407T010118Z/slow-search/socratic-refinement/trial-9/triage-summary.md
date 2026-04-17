# Triage Summary

**Title:** Full-text description search regression in v2.3: 10-15s response time with large task sets

## Problem
After upgrading from TaskFlow 2.2 to 2.3, searching by task description content takes 10-15 seconds consistently. Title-based search remains fast (sub-second). The reporter has approximately 5,000 tasks, some with lengthy descriptions (1000+ words from pasted meeting notes). Prior to the upgrade, all searches were near-instant.

## Root Cause Hypothesis
The v2.3 update likely introduced a regression in full-text description search — most probably a dropped or unused full-text index on the task description field, a switch from indexed queries to sequential scanning, or removal of a query result cache. The consistent 10-15 second timing across all description searches (rather than variable latency) suggests the system is performing a full table scan or unindexed text search on every query.

## Reproduction Steps
  1. Set up a TaskFlow 2.3 instance with a large dataset (~5,000 tasks), including some tasks with long descriptions (1000+ words)
  2. Perform a search by task title keyword — confirm it returns quickly (sub-second)
  3. Perform a search by a keyword that appears only in task descriptions — observe 10-15 second response time
  4. Repeat the description search to confirm the slowness is consistent (not a cold-cache effect)
  5. Optionally: repeat on TaskFlow 2.2 with the same dataset to confirm the regression

## Environment
TaskFlow v2.3 (upgraded from v2.2), running on a work laptop (OS not specified), ~5,000 tasks accumulated over 2 years, some descriptions with 1000+ words

## Severity: high

## Impact
Any user with a moderately large task set who relies on full-text description search is affected. The feature is effectively unusable at 10-15 seconds per query. Title search users are unaffected.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3, focusing on how description/full-text search queries are constructed and executed. Check for: (1) missing or unused full-text index on the description column, (2) query planner choosing a sequential scan instead of an index scan, (3) removed or broken query caching. If an index was dropped or a migration missed rebuilding it, restoring it should resolve the issue. Profile the actual query execution plan for a description search against a large dataset.

## Proposed Test Case
Performance test: with a dataset of 5,000+ tasks (including 100+ tasks with descriptions over 1,000 words), assert that a full-text description search returns results in under 2 seconds. Run this test against both title search and description search to ensure parity.

## Information Gaps
- Exact operating system and hardware specs of the work laptop (unlikely to matter given this appears to be a query-level regression)
- Whether the search results themselves are correct (just slow) or also missing expected matches
- Which database backend TaskFlow is using (SQLite, PostgreSQL, etc.) — relevant for fix implementation but not for identifying the regression
