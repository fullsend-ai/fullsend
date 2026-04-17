# Triage Summary

**Title:** Search performance regression in v2.3: description search takes 10-15s with large task corpus

## Problem
Full-text search over task descriptions became extremely slow (~10-15 seconds) after upgrading to v2.3. Title-only searches remain fast. The user has approximately 5,000 tasks, many with lengthy descriptions (pasted meeting notes). Previously, all searches returned in under a second.

## Root Cause Hypothesis
The v2.3 upgrade likely changed or broke the indexing strategy for task description fields. Possible causes: (1) a missing or dropped full-text index on the description column, (2) a switch from indexed search to naive LIKE/substring scanning, or (3) a new search codepath that doesn't use the index. The fact that title search remains fast while description search is slow strongly suggests the description field is being scanned without an index.

## Reproduction Steps
  1. Create or use an account with a large number of tasks (~5,000) where many tasks have lengthy descriptions
  2. Perform a search using a keyword or short phrase (e.g., 'budget review' or 'quarterly planning')
  3. Observe that the search takes 10-15 seconds to return results
  4. Compare by searching with title-only search (if available) — this should return quickly, confirming the issue is isolated to description search

## Environment
TaskFlow v2.3, work laptop (OS and browser not specified but likely irrelevant given this is a search/indexing issue)

## Severity: high

## Impact
Any user with a significant number of tasks is affected when searching by description content. This degrades a core workflow — finding tasks by keyword — from sub-second to 10-15 seconds, making the feature effectively unusable for power users with large task histories.

## Recommended Fix
1. Check the v2.3 migration scripts for changes to the search index on the task descriptions table. 2. Verify that a full-text index exists on the description column in the current schema. 3. Run EXPLAIN/ANALYZE on the description search query to confirm whether it's doing a sequential scan. 4. If the index was dropped or altered, restore it. If the search codepath changed, ensure it uses the indexed query. 5. Consider adding pagination or query-time limits for large result sets.

## Proposed Test Case
Create a test fixture with 5,000+ tasks where at least 500 have descriptions exceeding 1,000 characters. Execute a description search for a keyword present in ~10 tasks. Assert that results return in under 2 seconds. Run this test against both v2.2 and v2.3 schemas to confirm the regression and validate the fix.

## Information Gaps
- Exact OS and browser (unlikely to be relevant)
- Whether the user performed a clean upgrade or migration to v2.3
- Server-side logs or query execution plans
- Whether other users on v2.3 with large task counts experience the same issue
