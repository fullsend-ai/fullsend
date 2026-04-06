# Triage Summary

**Title:** Search across task descriptions regressed to ~10-15s in v2.3 (title search unaffected)

## Problem
After upgrading to TaskFlow v2.3, searching across task descriptions takes 10-15 seconds with high CPU usage. Searching by task title remains fast. The reporter has ~5,000 tasks, many with lengthy descriptions (pasted meeting notes). The slowdown is consistent regardless of search term specificity.

## Root Cause Hypothesis
v2.3 likely introduced a change to how description search is executed — most probably the description field lost its search index, the index is no longer being built/maintained, or the search path was changed from an indexed query to a full-text scan. The fact that title search remains fast while description search is uniformly slow (regardless of query term) strongly suggests the description field is being scanned sequentially rather than looked up via index. The CPU spike corroborates a brute-force scan over large text blobs.

## Reproduction Steps
  1. Set up a TaskFlow v2.3 workspace with a substantial number of tasks (~5,000) including tasks with long description fields
  2. Perform a search using the description search mode (not title-only) for any common term like 'meeting'
  3. Observe 10-15 second delay with CPU spike before results appear
  4. Compare by switching to title-only search and confirming it returns results quickly

## Environment
TaskFlow v2.3, work laptop (specific OS not confirmed), ~5,000 tasks accumulated over 2 years, many tasks containing lengthy pasted meeting notes in descriptions

## Severity: medium

## Impact
Affects any user with a non-trivial number of tasks who searches by description content. Workaround exists (search by title only), but users who rely on description search — especially those with rich task descriptions — are significantly impacted in daily workflow. Likely affects more users as task count grows.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3, focusing on: (1) whether description fields are still indexed for search, (2) whether the search query path for descriptions changed (e.g., from indexed lookup to sequential scan or regex match), (3) whether index rebuild/migration runs correctly on upgrade. Check if a search index migration was added in v2.3 that may not be executing properly, leaving description fields unindexed.

## Proposed Test Case
Performance test: seed a workspace with 5,000+ tasks (including tasks with descriptions >1KB), execute a description search, and assert results return within an acceptable threshold (e.g., <1 second). Run this test against both v2.2 (baseline) and v2.3 to confirm the regression and validate the fix.

## Information Gaps
- Exact OS and hardware specs of the reporter's work laptop (unlikely to change the fix approach)
- Whether other v2.3 users with large workspaces are experiencing the same issue
- Whether the issue scales linearly with task count or description length specifically
- Server-side vs client-side — whether TaskFlow search runs locally or involves a backend (CPU fan spin suggests local)
