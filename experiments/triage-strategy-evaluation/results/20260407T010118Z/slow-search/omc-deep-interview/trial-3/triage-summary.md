# Triage Summary

**Title:** Description search regression in v2.3: 10-15s query time with large task sets

## Problem
After upgrading to v2.3, searching by task description takes 10-15 seconds and causes high CPU usage (fan spin-up). Searching by task title remains fast. The delay occurs during the search/query phase, not result rendering. The user has approximately 5,000 tasks, some with very long descriptions (pasted meeting notes).

## Root Cause Hypothesis
v2.3 likely introduced a change to how description searches are executed — most probably a switch to unindexed full-text scanning of description fields, removal of a description search index, or a new search code path that performs brute-force string matching across all description content. The CPU spike and consistent slowness regardless of query specificity both point to a sequential scan rather than indexed lookup. Title search remaining fast confirms titles are still indexed.

## Reproduction Steps
  1. Install or upgrade to TaskFlow v2.3
  2. Create or import ~5,000 tasks, ensuring many have long descriptions (multiple paragraphs, e.g., pasted meeting notes)
  3. Perform a search filtering by task description using any search term
  4. Observe 10-15 second delay before results appear and elevated CPU usage
  5. Compare with a title-only search on the same dataset to confirm title search is fast

## Environment
TaskFlow v2.3, ~5,000 tasks with long descriptions, running on a work laptop (specific OS/specs not collected but not needed to reproduce)

## Severity: high

## Impact
Any user with a moderately large task set searching by description will experience severe slowdowns after upgrading to v2.3. This makes description search effectively unusable for power users, forcing them to rely only on title search as a workaround.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3 to identify what changed in the description search path. Most likely fix: add or restore a full-text index on the task description field. If the search was moved to application-layer string matching, push it back to the database/search engine. Profile the description search query to confirm whether the bottleneck is a table scan.

## Proposed Test Case
Performance test: seed the database with 5,000 tasks (descriptions averaging 500+ words). Assert that a description search for an arbitrary term returns results in under 2 seconds. Run this test against both v2.2 (baseline) and v2.3 (regression) to confirm the fix restores prior performance.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to affect fix direction since this is a search algorithm/indexing issue)
- Whether other users on v2.3 with large datasets experience the same issue (likely yes, given root cause hypothesis)
- Whether the reporter has tried any workarounds such as title-only search
