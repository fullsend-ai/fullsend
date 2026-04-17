# Triage Summary

**Title:** Full-text search on task descriptions regressed to ~10-15s after v2.3 upgrade

## Problem
Searching across task descriptions takes 10-15 seconds, while title search remains fast. The reporter has a large number of tasks and noticed the slowdown approximately 2 weeks ago, coinciding with an upgrade to v2.3.

## Root Cause Hypothesis
The v2.3 release likely introduced a regression in the description search path — either a full-text index on the description field was dropped/not migrated, or the search query was changed to do an unindexed scan (e.g., switching from indexed full-text search to a LIKE/ILIKE pattern match). The fact that title search is fast while description search is slow points to an index or query difference between these two code paths.

## Reproduction Steps
  1. Set up a TaskFlow instance on v2.3 with a large task set (reporter has 'a lot of tasks')
  2. Perform a search by task title keyword — observe fast response
  3. Perform a search by task description keyword — observe 10-15 second response time
  4. Compare query plans (EXPLAIN ANALYZE or equivalent) for title search vs description search to confirm index usage

## Environment
TaskFlow v2.3, running on a work laptop (local/desktop deployment), large task count

## Severity: medium

## Impact
Users with large task sets who rely on description search experience 10-15 second delays, making the feature effectively unusable for interactive workflows. Title-only search is unaffected.

## Recommended Fix
1. Diff the search implementation between v2.2 and v2.3, focusing on the description search query and any migration scripts. 2. Check whether a full-text index exists on the task description column. 3. If missing, add/restore the index. If present, check whether the v2.3 query actually uses it (e.g., look for LIKE '%term%' patterns that bypass indexes). 4. Verify the fix restores sub-second search times on a representative dataset.

## Proposed Test Case
Seed the database with 10,000+ tasks with varied descriptions. Search for a keyword that appears in ~50 task descriptions. Assert that results return in under 2 seconds. Run this test against both title and description search paths.

## Information Gaps
- Exact number of tasks in the reporter's instance (not critical — reproduce with any large dataset)
- Whether the reporter is using the default database engine or a custom configuration
