# Triage Summary

**Title:** Keyword search over task descriptions regressed to 10-15s in v2.3 (was <1s)

## Problem
Since upgrading to TaskFlow v2.3, keyword searches across task descriptions consistently take 10-15 seconds. Title-only search, tag filtering, and status filtering remain fast. The user has ~5,000 tasks, many with lengthy descriptions (pasted meeting notes), but this dataset performed fine prior to v2.3.

## Root Cause Hypothesis
v2.3 likely introduced a regression in how description-field keyword search is executed. Possible causes: (1) a full-text search index on the description field was dropped or not migrated during the v2.3 upgrade, (2) the search query was changed from indexed lookup to sequential scan/LIKE query, or (3) a new search implementation (e.g., switching search libraries) handles large text fields poorly. The fact that title search is unaffected suggests the regression is specific to the description field path.

## Reproduction Steps
  1. Install or upgrade to TaskFlow v2.3
  2. Create or import a workspace with ~5,000 tasks, many with long descriptions (1,000+ words)
  3. Perform a keyword search scoped to task descriptions
  4. Observe search takes 10-15 seconds consistently
  5. Compare with the same search scoped to task titles only (should be fast)

## Environment
TaskFlow v2.3, work laptop (OS/specs unspecified), ~5,000 tasks with lengthy descriptions

## Severity: high

## Impact
Any user with a large task corpus who relies on description search is affected. Keyword search on descriptions is a core workflow for power users. Workaround exists (search by title, filter by tags/status), but this significantly degrades the usefulness of search for users who store detailed information in descriptions.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3, focusing on how description-field queries are constructed. Check whether a full-text index on the description column was dropped or altered in the v2.3 migration. If the search backend was changed, benchmark the new implementation against large text fields. Restore or add proper indexing for description content.

## Proposed Test Case
Performance test: seed a database with 5,000 tasks where descriptions average 500+ words. Assert that keyword search across descriptions returns results in under 2 seconds. Run this test against both v2.2 (baseline) and v2.3 to confirm the regression and validate the fix.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to be root cause given the version correlation)
- Whether other v2.3 users with large datasets experience the same issue
- Whether the v2.3 changelog or migration scripts mention search-related changes
